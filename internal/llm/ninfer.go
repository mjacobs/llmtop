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

const ninferKind = "ninfer"

// NInfer integrates NInfer's OpenAI-compatible server (`ninfer-serve`).
//
// NInfer exposes no Prometheus endpoint: its HTTP surface is /health,
// /v1/models and the generation routes, and its throughput report goes to
// the process log (--log-stats-interval-ms), not over HTTP. So the adapter
// reports liveness, the served model id and context length, and the
// concurrency limit from the command line, and leaves Metrics nil; the TUI
// explains that on the detail line instead of showing rates. /health is
// exempt from --api-key; /v1/models is not, so a keyed server falls back to
// the command-line model id and --max-context.
type NInfer struct {
	Client *http.Client

	mu   sync.Mutex
	meta map[string]ninferMeta // cmdline-derived fallbacks, keyed by endpoint
}

type ninferMeta struct {
	model string
	ctx   int
	slots int
}

func NewNInfer() *NInfer {
	return &NInfer{
		Client: &http.Client{
			Timeout: 500 * time.Millisecond,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		meta: map[string]ninferMeta{},
	}
}

func (a *NInfer) Kind() string { return ninferKind }

func (a *NInfer) Detect(cmdline []string) (string, bool) {
	ep, ok := NInferEndpointFromCmdline(cmdline)
	if !ok {
		return ep, ok
	}
	m := ninferMeta{model: ninferModelFromCmdline(cmdline)}
	if n, ok := argvIntFlag(cmdline, "--max-context"); ok && n > 0 {
		m.ctx = n
	}
	if n, ok := argvIntFlag(cmdline, "--max-concurrency"); ok && n > 0 {
		m.slots = n
	}
	nep := normalizeEndpoint(ep)
	a.mu.Lock()
	a.meta[nep] = m
	a.mu.Unlock()
	return ep, true
}

func (a *NInfer) get(ctx context.Context, url string) (int, []byte, error) {
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

func (a *NInfer) Fetch(ctx context.Context, endpoint string, _ FetchHint) model.LLMServer {
	s := model.LLMServer{Kind: ninferKind, Endpoint: endpoint}
	a.mu.Lock()
	meta := a.meta[normalizeEndpoint(endpoint)]
	a.mu.Unlock()
	s.Model = meta.model
	s.CtxSize = meta.ctx
	s.SlotsTotal = meta.slots

	// ninfer-serve loads the model before it starts listening, so a refused
	// connection is the loading state as well as the dead state; there is
	// no 503 phase to tell them apart. Report unreachable and let the
	// process list show the loader.
	code, _, err := a.get(ctx, endpoint+"/health")
	if err != nil || code != http.StatusOK {
		s.Status = model.StatusUnreachable
		return s
	}
	s.Status = model.StatusOK

	// Same shape as vLLM's list: one entry, with max_model_len carrying the
	// effective --max-context.
	if code, body, err := a.get(ctx, endpoint+"/v1/models"); err == nil && code == http.StatusOK {
		var mods vllmModelsResponse
		if json.Unmarshal(body, &mods) == nil && len(mods.Data) > 0 {
			if mods.Data[0].ID != "" {
				s.Model = mods.Data[0].ID
			}
			if mods.Data[0].MaxModelLen > 0 {
				s.CtxSize = mods.Data[0].MaxModelLen
			}
		}
	}
	return s
}
