package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mjacobs/llmtop/internal/model"
)

const strataKind = "strata"

// Strata reads the Python frontend's /health and JSON /metrics. These
// observe the engine without loading it or resetting its idle-unload timer.
// Token totals advance on completion; live.tok_s is a rolling decode
// estimate, not a cumulative counter. Strata's context-fill ratio is not
// KV-cache occupancy, so KVCacheUsage remains unknown.
type Strata struct {
	Client *http.Client
	Now    func() time.Time
	rates  *RateTracker
}

func NewStrata() *Strata {
	return &Strata{
		Client: &http.Client{
			Timeout: 500 * time.Millisecond,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Now: time.Now, rates: NewRateTracker(),
	}
}

func (a *Strata) Kind() string { return strataKind }

func (a *Strata) Detect(cmdline []string) (string, bool) {
	return StrataEndpointFromCmdline(cmdline)
}

func (a *Strata) get(ctx context.Context, url, apiKey string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

type strataHealth struct {
	Service    string `json:"service"`
	Status     string `json:"status"`
	Model      string `json:"model"`
	MaxContext int    `json:"max_context"`
	Loaded     *bool  `json:"loaded"`
}

type strataMetrics struct {
	Engine struct {
		Model      string `json:"model"`
		MaxContext int    `json:"max_context"`
	} `json:"engine"`
	Live struct {
		State    string  `json:"state"`
		TokS     float64 `json:"tok_s"`
		Parallel int     `json:"parallel"`
		Running  *int    `json:"running"`
		Queued   int     `json:"queued"`
		Waiting  int     `json:"waiting"`
	} `json:"live"`
	Totals struct {
		PromptTokens *float64 `json:"prompt_tokens"`
		OutputTokens *float64 `json:"output_tokens"`
	} `json:"totals"`
}

func (a *Strata) Fetch(ctx context.Context, endpoint string, hint FetchHint) model.LLMServer {
	endpoint = normalizeEndpoint(endpoint)
	s := model.LLMServer{Kind: strataKind, Endpoint: endpoint, Status: model.StatusUnreachable}
	code, body, err := a.get(ctx, endpoint+"/health", hint.APIKey)
	if err != nil || code != http.StatusOK {
		if err == nil && code == http.StatusServiceUnavailable {
			s.Status = model.StatusLoading
		}
		a.rates.Reset(endpoint)
		return s
	}
	var health strataHealth
	if json.Unmarshal(body, &health) != nil || health.Service != strataKind || health.Status != "ok" {
		a.rates.Reset(endpoint)
		return s
	}
	s.Model, s.CtxSize = health.Model, health.MaxContext
	if health.Loaded != nil && !*health.Loaded {
		s.Status = model.StatusSleeping
		a.rates.Reset(endpoint)
		return s
	}
	s.Status = model.StatusOK

	code, body, err = a.get(ctx, endpoint+"/metrics", hint.APIKey)
	switch {
	case err != nil:
		s.MetricsErr = "request failed"
	case code != http.StatusOK:
		s.MetricsErr = fmt.Sprintf("http %d", code)
	default:
		var m strataMetrics
		if json.Unmarshal(body, &m) != nil || m.Totals.PromptTokens == nil || m.Totals.OutputTokens == nil ||
			*m.Totals.PromptTokens < 0 || *m.Totals.OutputTokens < 0 {
			s.MetricsErr = "invalid JSON metrics"
			break
		}
		switch m.Live.State {
		case "idle", "reading", "generating", "unloaded":
		default:
			s.MetricsErr = "invalid JSON metrics"
		}
		if s.MetricsErr != "" {
			break
		}
		if m.Engine.Model != "" {
			s.Model = m.Engine.Model
		}
		if m.Engine.MaxContext > 0 {
			s.CtxSize = m.Engine.MaxContext
		}
		if m.Live.State == "unloaded" {
			s.Status = model.StatusSleeping
			a.rates.Reset(endpoint)
			return s
		}
		// The solo engine omits parallel/running; a batched engine reports
		// its effective slots, which can be fewer than --parallel requested.
		s.SlotsTotal = max(1, m.Live.Parallel)
		busy := m.Live.State == "reading" || m.Live.State == "generating"
		running := 0
		if m.Live.Running != nil {
			running = max(0, *m.Live.Running)
		} else if busy {
			running = 1
		}
		met := &model.LLMMetrics{
			PromptTokensTotal: *m.Totals.PromptTokens, PredictedTokensTotal: *m.Totals.OutputTokens,
			RequestsProcessing: running, RequestsDeferred: max(0, m.Live.Queued) + max(0, m.Live.Waiting),
			KVCacheUsage: -1,
		}
		// There is no live decode counter. Reusing the output counter
		// keeps RateTracker's measured completion rates, without inventing
		// an estimate while the totals are frozen.
		if res, ok := a.rates.TPS(endpoint, a.Now(), met.PromptTokensTotal, met.PredictedTokensTotal,
			met.PredictedTokensTotal, busy || running > 0); ok {
			met.PromptTPS, met.PredictedTPS = res.PromptTPS, res.PredictedTPS
		}
		// In parallel mode tok_s describes the newest request, not the
		// server's aggregate throughput. Keep completion rates there.
		if m.Live.Parallel <= 1 && running <= 1 && m.Live.State == "generating" && m.Live.TokS > 0 {
			met.PredictedTPS, met.PredictedTPSEst = m.Live.TokS, true
		}
		s.Metrics = met
	}
	if s.MetricsErr != "" {
		a.rates.Reset(endpoint)
	}
	return s
}
