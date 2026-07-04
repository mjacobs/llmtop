package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mjacobs/llmtop/internal/model"
)

const llamaKind = "llama.cpp"

type LlamaCpp struct {
	Client *http.Client
	Now    func() time.Time // test hook
	rates  *RateTracker
}

func NewLlamaCpp() *LlamaCpp {
	return &LlamaCpp{
		Client: &http.Client{Timeout: 500 * time.Millisecond},
		Now:    time.Now,
		rates:  NewRateTracker(),
	}
}

func (a *LlamaCpp) Kind() string { return llamaKind }

func (a *LlamaCpp) Detect(cmdline []string) (string, bool) {
	return EndpointFromCmdline(cmdline)
}

type propsResponse struct {
	Role                      string `json:"role"`
	ModelAlias                string `json:"model_alias"`
	ModelPath                 string `json:"model_path"`
	TotalSlots                int    `json:"total_slots"`
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

func (a *LlamaCpp) get(ctx context.Context, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

func (a *LlamaCpp) Fetch(ctx context.Context, endpoint string) model.LLMServer {
	s := model.LLMServer{Kind: llamaKind, Endpoint: endpoint}

	code, _, err := a.get(ctx, endpoint+"/health")
	switch {
	case err != nil:
		s.Status = model.StatusUnreachable
		return s
	case code == http.StatusServiceUnavailable:
		s.Status = model.StatusLoading
	case code == http.StatusOK:
		s.Status = model.StatusOK
	default:
		s.Status = model.StatusUnreachable
		return s
	}

	if code, body, err := a.get(ctx, endpoint+"/props"); err == nil && code == http.StatusOK {
		var p propsResponse
		if json.Unmarshal(body, &p) == nil {
			s.Role = p.Role
			s.SlotsTotal = p.TotalSlots
			s.CtxSize = p.DefaultGenerationSettings.NCtx
			switch {
			case p.ModelAlias != "" && p.ModelAlias != "llama-server":
				s.Model = p.ModelAlias
			case p.ModelPath != "" && p.ModelPath != "none":
				s.Model = filepath.Base(p.ModelPath)
			}
		}
	}

	if code, body, err := a.get(ctx, endpoint+"/metrics"); err == nil && code == http.StatusOK {
		m := ParsePromText(body)
		met := &model.LLMMetrics{
			PromptTokensTotal:    m["llamacpp:prompt_tokens_total"],
			PredictedTokensTotal: m["llamacpp:tokens_predicted_total"],
			RequestsProcessing:   int(m["llamacpp:requests_processing"]),
			RequestsDeferred:     int(m["llamacpp:requests_deferred"]),
			KVCacheUsage:         -1,
		}
		if v, ok := m["llamacpp:kv_cache_usage_ratio"]; ok {
			met.KVCacheUsage = v
		}
		p, g, ok := a.rates.TPS(endpoint, a.Now(), met.PromptTokensTotal, met.PredictedTokensTotal)
		if ok {
			met.PromptTPS, met.PredictedTPS = p, g
		} else { // first sample: average-since-start gauges
			met.PromptTPS = m["llamacpp:prompt_tokens_seconds"]
			met.PredictedTPS = m["llamacpp:predicted_tokens_seconds"]
		}
		s.Metrics = met
	}
	return s
}
