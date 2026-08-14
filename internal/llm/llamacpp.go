package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/mjacobs/llmtop/internal/model"
)

const llamaKind = "llama.cpp"

// metricsIdleGraceTicks is how many consecutive quiet polls (no requests,
// counters frozen) a sleep-idle-enabled server gets before the adapter stops
// scraping /metrics. llama.cpp treats every /metrics scrape as activity, so
// polling through the idle window would keep the model resident forever.
const metricsIdleGraceTicks = 3

type LlamaCpp struct {
	Client *http.Client
	Now    func() time.Time // test hook
	rates  *RateTracker

	mu    sync.Mutex
	state map[string]*epState // sleep-idle gate state, keyed by endpoint
}

// epState tracks what the adapter must remember between polls to avoid
// resetting a server's --sleep-idle-seconds timer (see metricsIdleGraceTicks).
type epState struct {
	sleepIdle bool // process runs with --sleep-idle-seconds > 0
	sleeping  bool // is_sleeping on the last poll
	grace     int  // idle polls left before /metrics scraping is gated off

	haveSample                       bool // counters below are valid
	lastPrompt, lastPred, lastDecode float64
	cached                           *model.LLMMetrics // last scrape, served while gated
}

func NewLlamaCpp() *LlamaCpp {
	return &LlamaCpp{
		Client: &http.Client{Timeout: 500 * time.Millisecond},
		Now:    time.Now,
		rates:  NewRateTracker(),
		state:  map[string]*epState{},
	}
}

func (a *LlamaCpp) Kind() string { return llamaKind }

func (a *LlamaCpp) Detect(cmdline []string) (string, bool) {
	ep, ok := EndpointFromCmdline(cmdline)
	if !ok {
		return ep, ok
	}
	sleepIdle := sleepIdleFromCmdline(cmdline)
	nep := normalizeEndpoint(ep)
	st := a.stateFor(nep)
	reset := false
	a.mu.Lock()
	switch {
	case sleepIdle && !st.sleepIdle:
		st.sleepIdle = true
		st.grace = metricsIdleGraceTicks
	case !sleepIdle && st.sleepIdle:
		// The process at this endpoint no longer runs with
		// --sleep-idle-seconds: it was restarted or replaced. Drop the
		// inherited gate and cached scrape so the new server gets normal
		// unconditional /metrics polling.
		*st = epState{}
		reset = true
	}
	a.mu.Unlock()
	if reset {
		// The rate anchors belong to the replaced process too.
		a.rates.Reset(nep)
	}
	return ep, ok
}

func (a *LlamaCpp) stateFor(endpoint string) *epState {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.state[endpoint]
	if !ok {
		st = &epState{}
		a.state[endpoint] = st
	}
	return st
}

type propsResponse struct {
	Role       string `json:"role"`
	ModelAlias string `json:"model_alias"`
	ModelPath  string `json:"model_path"`
	TotalSlots int    `json:"total_slots"`
	// Pointer: absent/null must stay distinguishable from an explicit
	// false — only the latter may clear a known sleeping state.
	IsSleeping                *bool `json:"is_sleeping"`
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
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

func (a *LlamaCpp) Fetch(ctx context.Context, endpoint string, hint FetchHint) model.LLMServer {
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

	var sleepingNow, sleepKnown bool
	if code, body, err := a.get(ctx, endpoint+"/props"); err == nil && code == http.StatusOK {
		var p propsResponse
		if json.Unmarshal(body, &p) == nil {
			s.Role = p.Role
			s.SlotsTotal = p.TotalSlots
			s.CtxSize = p.DefaultGenerationSettings.NCtx
			if p.IsSleeping != nil {
				sleepKnown = true
				sleepingNow = *p.IsSleeping
			}
			switch {
			case p.ModelAlias != "" && p.ModelAlias != "llama-server":
				s.Model = p.ModelAlias
			case p.ModelPath != "" && p.ModelPath != "none":
				s.Model = filepath.Base(p.ModelPath)
			}
		}
	}

	// /health and /props are safe to poll: llama.cpp serves them without
	// touching the task queue. /metrics is not — every scrape resets the
	// --sleep-idle-seconds timer and wakes a sleeping server, so it needs
	// the gate below (see metricsIdleGraceTicks).
	st := a.stateFor(endpoint)
	a.mu.Lock()
	if !sleepKnown && st.sleeping {
		// Failed or malformed /props, or a response without is_sleeping:
		// the sleep state is unknown this poll. Keep treating the server
		// as sleeping — guessing "awake" would scrape /metrics, which
		// wakes the server and reloads its model. Only an explicit
		// is_sleeping=false clears the state.
		a.mu.Unlock()
		s.Status = model.StatusSleeping
		return s
	}
	if sleepingNow {
		st.sleeping = true
		st.grace = 0
		a.mu.Unlock()
		s.Status = model.StatusSleeping
		return s
	}
	if st.sleeping { // decoded is_sleeping=false: woken by someone else, watch it again
		st.sleeping = false
		st.grace = metricsIdleGraceTicks
	}
	if st.sleepIdle && !hint.GPUActive && st.grace <= 0 {
		// Quiet server: skip /metrics so it can reach its idle deadline.
		// Serve the last scrape with rates zeroed so the panel keeps its
		// identity/kv/slot info instead of claiming "metrics off".
		if st.cached != nil {
			c := *st.cached
			c.PromptTPS, c.PredictedTPS, c.PredictedTPSEst = 0, 0, false
			s.Metrics = &c
		}
		a.mu.Unlock()
		return s
	}
	a.mu.Unlock()

	mcode, mbody, merr := a.get(ctx, endpoint+"/metrics")
	if merr == nil && (mcode == http.StatusNotImplemented || mcode == http.StatusNotFound) {
		// Metrics are definitively unsupported (llama.cpp answers 501 when
		// --metrics is off), so no activity signal will ever arrive on this
		// path. Count the poll as quiet; after the grace period the
		// sleep-idle gate stops these scrapes from resetting the server's
		// idle timer forever.
		a.mu.Lock()
		if st.grace > 0 {
			st.grace--
		}
		// Any cached scrape and rate anchors predate metrics becoming
		// unsupported (server replaced or reconfigured): drop them so the
		// gate never serves a stale sample and a later 200 is treated as
		// a fresh first sample rather than amortized against old anchors.
		st.cached = nil
		st.haveSample = false
		a.mu.Unlock()
		a.rates.Reset(endpoint)
		return s
	}
	if merr == nil && mcode == http.StatusOK {
		m := ParsePromText(mbody)
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
		res, ok := a.rates.TPS(endpoint, a.Now(),
			met.PromptTokensTotal, met.PredictedTokensTotal,
			m["llamacpp:n_decode_total"], met.RequestsProcessing > 0)
		if ok {
			met.PromptTPS, met.PredictedTPS = res.PromptTPS, res.PredictedTPS
			met.PredictedTPSEst = res.Estimated
		} else { // first sample: average-since-start gauges
			met.PromptTPS = m["llamacpp:prompt_tokens_seconds"]
			met.PredictedTPS = m["llamacpp:predicted_tokens_seconds"]
		}
		s.Metrics = met

		decode := m["llamacpp:n_decode_total"]
		a.mu.Lock()
		active := met.RequestsProcessing > 0 || met.RequestsDeferred > 0 ||
			(st.haveSample && (met.PromptTokensTotal != st.lastPrompt ||
				met.PredictedTokensTotal != st.lastPred || decode != st.lastDecode))
		if active {
			st.grace = metricsIdleGraceTicks
		} else if st.grace > 0 {
			st.grace--
		}
		st.haveSample = true
		st.lastPrompt, st.lastPred, st.lastDecode = met.PromptTokensTotal, met.PredictedTokensTotal, decode
		cached := *met
		st.cached = &cached
		a.mu.Unlock()
	}
	return s
}
