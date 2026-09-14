package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/mjacobs/llmtop/internal/model"
)

const vllmKind = "vllm"

// VLLM integrates vLLM's OpenAI-compatible server (`vllm serve`, or
// `python -m vllm.entrypoints.openai.api_server`).
//
// Unlike llama.cpp, vLLM's /metrics has no side effects — there is no idle
// timer that a scrape would reset — so every endpoint is polled
// unconditionally: /health for liveness, /v1/models for the served name and
// context length, /metrics for counters, queue depth, KV-cache usage and the
// engine's sleep state. /health and /metrics are exempt from vLLM's
// --api-key check; /v1/models is not, so a keyed server falls back to the
// model_name label on its metrics and reports no context length.
type VLLM struct {
	Client *http.Client
	Now    func() time.Time
	rates  *RateTracker

	mu   sync.Mutex
	meta map[string]vllmMeta // cmdline-derived fallbacks, keyed by endpoint
}

type vllmMeta struct {
	model string
	ctx   int
	slots int
}

func NewVLLM() *VLLM {
	return &VLLM{
		Client: &http.Client{
			Timeout: 500 * time.Millisecond,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Now:   time.Now,
		rates: NewRateTracker(),
		meta:  map[string]vllmMeta{},
	}
}

func (a *VLLM) Kind() string { return vllmKind }

func (a *VLLM) Detect(cmdline []string) (string, bool) {
	ep, ok := VLLMEndpointFromCmdline(cmdline)
	if !ok {
		return ep, ok
	}
	m := vllmMeta{model: vllmModelFromCmdline(cmdline)}
	if n, ok := argvIntFlag(cmdline, "--max-model-len"); ok && n > 0 {
		m.ctx = n
	}
	if n, ok := argvIntFlag(cmdline, "--max-num-seqs"); ok && n > 0 {
		m.slots = n
	}
	nep := normalizeEndpoint(ep)
	a.mu.Lock()
	a.meta[nep] = m
	a.mu.Unlock()
	return ep, true
}

type vllmModelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		MaxModelLen int    `json:"max_model_len"`
	} `json:"data"`
}

func (a *VLLM) get(ctx context.Context, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

func (a *VLLM) Fetch(ctx context.Context, endpoint string, _ FetchHint) model.LLMServer {
	s := model.LLMServer{Kind: vllmKind, Endpoint: endpoint}
	a.mu.Lock()
	meta := a.meta[normalizeEndpoint(endpoint)]
	a.mu.Unlock()
	// meta.model is a cmdline guess; anything the server tells us about
	// itself outranks it (see vllmModelFromCmdline).
	fromCmdline := meta.model != ""
	if fromCmdline {
		s.Model = meta.model
	}
	s.CtxSize = meta.ctx
	s.SlotsTotal = meta.slots

	// /health is 200 once the API process is up; 503 while the engine is
	// still starting (or has died). Treat 503 like llama.cpp's loading
	// state so a bringing-up server is not labelled unreachable.
	code, _, err := a.get(ctx, endpoint+"/health")
	switch {
	case err != nil:
		s.Status = model.StatusUnreachable
		return s
	case code == http.StatusServiceUnavailable:
		s.Status = model.StatusLoading
		return s
	case code == http.StatusOK:
		s.Status = model.StatusOK
	default:
		s.Status = model.StatusUnreachable
		return s
	}

	if code, body, err := a.get(ctx, endpoint+"/v1/models"); err == nil && code == http.StatusOK {
		var mods vllmModelsResponse
		if json.Unmarshal(body, &mods) == nil && len(mods.Data) > 0 {
			if mods.Data[0].ID != "" {
				s.Model = mods.Data[0].ID
				fromCmdline = false
			}
			if mods.Data[0].MaxModelLen > 0 {
				s.CtxSize = mods.Data[0].MaxModelLen
			}
		}
	}

	mcode, mbody, merr := a.get(ctx, endpoint+"/metrics")
	if merr != nil || mcode != http.StatusOK {
		return s
	}
	samples := ParsePromSamples(mbody)
	if s.Model == "" || fromCmdline {
		for _, smp := range samples {
			if name := smp.Labels["model_name"]; name != "" {
				s.Model = name
				break
			}
		}
	}
	if vllmSleeping(samples) {
		// A sleeping engine stops advancing its token counters, so the
		// anchors held here would otherwise spread the next awake delta
		// across the whole sleep interval. Drop them; the first awake
		// scrape re-anchors.
		a.rates.Reset(endpoint)
		s.Status = model.StatusSleeping
		return s
	}

	prompt, okPrompt := promSum(samples, "vllm:prompt_tokens_total")
	gen, okGen := promSum(samples, "vllm:generation_tokens_total")
	if !okPrompt && !okGen {
		// /metrics answers with process series only (--disable-log-stats):
		// leave Metrics nil so the TUI shows its "metrics disabled" hint.
		return s
	}
	running, _ := promSum(samples, "vllm:num_requests_running")
	waiting, _ := promSum(samples, "vllm:num_requests_waiting")
	met := &model.LLMMetrics{
		PromptTokensTotal:    prompt,
		PredictedTokensTotal: gen,
		RequestsProcessing:   int(running),
		RequestsDeferred:     int(waiting),
		KVCacheUsage:         -1,
	}
	// vLLM ≥ 0.9 (V1 engine) names the ratio kv_cache_usage_perc; older
	// releases used gpu_cache_usage_perc. 1 means 100 percent usage.
	if v, ok := promMean(samples, "vllm:kv_cache_usage_perc"); ok {
		met.KVCacheUsage = v
	} else if v, ok := promMean(samples, "vllm:gpu_cache_usage_perc"); ok {
		met.KVCacheUsage = v
	}
	// vLLM advances generation_tokens_total on every engine step, so the
	// amortised rate is live while a request decodes; the counter doubles
	// as the "decode" signal so the tracker's in-flight estimate degrades
	// to 0 rather than inventing a number while a prompt is being prefilled.
	if res, ok := a.rates.TPS(endpoint, a.Now(), prompt, gen, gen, running > 0); ok {
		met.PromptTPS, met.PredictedTPS = res.PromptTPS, res.PredictedTPS
		met.PredictedTPSEst = res.Estimated
	}
	s.Metrics = met
	return s
}

// vllmSleeping reports whether every engine that publishes a sleep state is
// asleep. One awake engine keeps the server "ok".
func vllmSleeping(samples []PromSample) bool {
	have := false
	awake := false
	for _, s := range samples {
		if s.Name != "vllm:engine_sleep_state" {
			continue
		}
		have = true
		if s.Labels["sleep_state"] == "awake" && s.Value == 1 {
			awake = true
		}
	}
	return have && !awake
}
