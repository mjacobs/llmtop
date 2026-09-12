package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mjacobs/llmtop/internal/model"
)

const vllmModelsJSON = `{"object":"list","data":[{"id":"qwen-demo","object":"model","max_model_len":8192}]}`

func vllmMetrics(prompt, gen, running, waiting int, kv float64, sleepState string) string {
	l := `{engine="0",model_name="qwen-demo"}`
	var b strings.Builder
	fmt.Fprintf(&b, "process_cpu_seconds_total 1.0\n")
	fmt.Fprintf(&b, "vllm:num_requests_running%s %d.0\n", l, running)
	fmt.Fprintf(&b, "vllm:num_requests_waiting%s %d.0\n", l, waiting)
	for _, st := range []string{"awake", "weights_offloaded", "discard_all"} {
		v := 0
		if st == sleepState {
			v = 1
		}
		fmt.Fprintf(&b, "vllm:engine_sleep_state{engine=\"0\",model_name=\"qwen-demo\",sleep_state=%q} %d.0\n", st, v)
	}
	fmt.Fprintf(&b, "vllm:kv_cache_usage_perc%s %v\n", l, kv)
	fmt.Fprintf(&b, "vllm:prompt_tokens_total%s %d.0\n", l, prompt)
	fmt.Fprintf(&b, "vllm:generation_tokens_total%s %d.0\n", l, gen)
	return b.String()
}

type fakeVLLM struct {
	srv          *httptest.Server
	modelsStatus int
	models       string
	metrics      atomic.Pointer[string]
}

// newFakeVLLM serves a healthy vLLM; non-200 /health is covered with a bare
// handler in the tests that need it.
func newFakeVLLM(t *testing.T, modelsStatus int, models, metrics string) *fakeVLLM {
	t.Helper()
	f := &fakeVLLM{modelsStatus: modelsStatus, models: models}
	f.metrics.Store(&metrics)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(f.modelsStatus)
		if f.modelsStatus == http.StatusOK {
			_, _ = w.Write([]byte(f.models))
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if p := f.metrics.Load(); p != nil {
			_, _ = w.Write([]byte(*p))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestVLLMDetectStoresCmdlineFallbacks(t *testing.T) {
	a := NewVLLM()
	argv := []string{"/opt/venv/bin/python", "/opt/venv/bin/vllm", "serve",
		"org/Qwen2-7B",
		"--served-model-name", "qwen-demo",
		"--host", "0.0.0.0", "--port", "18000",
		"--max-model-len", "8192", "--max-num-seqs", "16"}
	ep, ok := a.Detect(argv)
	if !ok || ep != "http://127.0.0.1:18000" {
		t.Fatalf("Detect = (%q, %v)", ep, ok)
	}
	s := a.Fetch(context.Background(), ep, FetchHint{})
	if s.Model != "qwen-demo" || s.CtxSize != 8192 || s.SlotsTotal != 16 {
		t.Fatalf("cmdline meta not stored: %+v", s)
	}
	// Restarted without the flags: stale slots/ctx must not survive.
	a.Detect([]string{"vllm", "serve", "m", "--host", "0.0.0.0", "--port", "18000"})
	s = a.Fetch(context.Background(), ep, FetchHint{})
	if s.SlotsTotal != 0 || s.CtxSize != 0 {
		t.Fatalf("stale cmdline meta survived restart: %+v", s)
	}
}

func TestVLLMFetchOK(t *testing.T) {
	body := vllmMetrics(100, 500, 1, 2, 0.5, "awake")
	f := newFakeVLLM(t, http.StatusOK, vllmModelsJSON, body)

	a := NewVLLM()
	base := time.Unix(4000, 0)
	a.Now = func() time.Time { return base }
	a.meta[normalizeEndpoint(f.srv.URL)] = vllmMeta{slots: 16}

	s := a.Fetch(context.Background(), f.srv.URL, FetchHint{})
	if s.Kind != vllmKind || s.Status != model.StatusOK {
		t.Fatalf("kind/status = %q/%q", s.Kind, s.Status)
	}
	if s.Model != "qwen-demo" || s.CtxSize != 8192 || s.SlotsTotal != 16 {
		t.Fatalf("identity = %+v", s)
	}
	if s.Metrics == nil {
		t.Fatal("Metrics must be present")
	}
	if s.Metrics.PromptTokensTotal != 100 || s.Metrics.PredictedTokensTotal != 500 {
		t.Fatalf("token totals = %+v", s.Metrics)
	}
	if s.Metrics.RequestsProcessing != 1 || s.Metrics.RequestsDeferred != 2 {
		t.Fatalf("queue = %+v", s.Metrics)
	}
	if s.Metrics.KVCacheUsage != 0.5 {
		t.Fatalf("kv = %v", s.Metrics.KVCacheUsage)
	}
	if s.Metrics.PromptTPS != 0 || s.Metrics.PredictedTPS != 0 || s.Metrics.PredictedTPSEst {
		t.Fatalf("first-sample rates = %+v, want zero (no lifetime gauge)", s.Metrics)
	}

	next := vllmMetrics(120, 540, 1, 0, 0.5, "awake")
	f.metrics.Store(&next)
	a.Now = func() time.Time { return base.Add(2 * time.Second) }
	s2 := a.Fetch(context.Background(), f.srv.URL, FetchHint{})
	if s2.Metrics.PromptTPS != 10 || s2.Metrics.PredictedTPS != 20 || s2.Metrics.PredictedTPSEst {
		t.Fatalf("delta TPS = %v/%v est=%v, want 10/20 measured", s2.Metrics.PromptTPS, s2.Metrics.PredictedTPS, s2.Metrics.PredictedTPSEst)
	}
}

func TestVLLMFetchSleeping(t *testing.T) {
	body := vllmMetrics(0, 0, 0, 0, 0, "discard_all")
	f := newFakeVLLM(t, http.StatusOK, vllmModelsJSON, body)
	s := NewVLLM().Fetch(context.Background(), f.srv.URL, FetchHint{})
	if s.Status != model.StatusSleeping {
		t.Fatalf("status = %q, want sleeping", s.Status)
	}
	if s.Metrics != nil {
		t.Fatalf("sleeping must not attach metrics: %+v", s.Metrics)
	}
	if s.Model != "qwen-demo" {
		t.Fatalf("identity must survive sleep: %+v", s)
	}
}

func TestVLLMFetchAPIKeyFallsBackToMetricLabel(t *testing.T) {
	body := vllmMetrics(10, 20, 0, 0, 0.5, "awake")
	f := newFakeVLLM(t, http.StatusUnauthorized, "", body)
	s := NewVLLM().Fetch(context.Background(), f.srv.URL, FetchHint{})
	if s.Status != model.StatusOK || s.Model != "qwen-demo" || s.CtxSize != 0 {
		t.Fatalf("keyed server: %+v", s)
	}
	if s.Metrics == nil || s.Metrics.KVCacheUsage != 0.5 {
		t.Fatalf("metrics must still be read: %+v", s.Metrics)
	}
}

func TestVLLMFetchStatsLoggingOff(t *testing.T) {
	f := newFakeVLLM(t, http.StatusOK, vllmModelsJSON, "process_cpu_seconds_total 1.0\n")
	s := NewVLLM().Fetch(context.Background(), f.srv.URL, FetchHint{})
	if s.Status != model.StatusOK || s.Metrics != nil {
		t.Fatalf("stats off: %+v", s)
	}
	if s.Model != "qwen-demo" {
		t.Fatalf("model must come from /v1/models: %+v", s)
	}
}

func TestVLLMFetchLegacyKVMetric(t *testing.T) {
	body := `vllm:prompt_tokens_total{model_name="m"} 1.0
vllm:generation_tokens_total{model_name="m"} 2.0
vllm:gpu_cache_usage_perc{model_name="m"} 0.3
`
	f := newFakeVLLM(t, http.StatusNotFound, "", body)
	s := NewVLLM().Fetch(context.Background(), f.srv.URL, FetchHint{})
	if s.Metrics == nil || s.Metrics.KVCacheUsage != 0.3 {
		t.Fatalf("legacy gpu_cache_usage_perc must map: %+v", s.Metrics)
	}
	if s.Model != "m" {
		t.Fatalf("model = %q, want label fallback", s.Model)
	}
}

func TestVLLMFetchLoading(t *testing.T) {
	// vLLM answers 503 on /health while the engine is still coming up
	// (or is temporarily unavailable). Same display as llama.cpp loading.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	a := NewVLLM()
	a.meta[normalizeEndpoint(srv.URL)] = vllmMeta{model: "qwen-demo", ctx: 8192, slots: 16}
	s := a.Fetch(context.Background(), srv.URL, FetchHint{})
	if s.Status != model.StatusLoading {
		t.Fatalf("status = %q, want loading", s.Status)
	}
	if s.Model != "qwen-demo" || s.CtxSize != 8192 || s.SlotsTotal != 16 {
		t.Fatalf("cmdline identity must survive loading: %+v", s)
	}
	if s.Metrics != nil {
		t.Fatalf("loading must not attach metrics: %+v", s.Metrics)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1 (must not probe past /health)", hits.Load())
	}
}

func TestVLLMFetchUnreachable(t *testing.T) {
	s := NewVLLM().Fetch(context.Background(), "http://127.0.0.1:1", FetchHint{})
	if s.Status != model.StatusUnreachable {
		t.Fatalf("status = %q", s.Status)
	}
}

func TestVLLMFetchDoesNotFollowRedirects(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()

	s := NewVLLM().Fetch(context.Background(), redirector.URL, FetchHint{})
	if s.Status == model.StatusOK {
		t.Fatalf("a redirecting /health must not count as ok, got %q", s.Status)
	}
	if n := atomic.LoadInt32(&targetHits); n != 0 {
		t.Fatalf("redirect target was contacted %d time(s); redirects must not be followed", n)
	}
}
