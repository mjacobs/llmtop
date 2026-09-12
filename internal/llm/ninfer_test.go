package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mjacobs/llmtop/internal/model"
)

const ninferModelsJSON = `{"data":[{"created":1789114908,"id":"qwen-demo","max_model_len":240000,"object":"model","owned_by":"ninfer"}],"object":"list"}`

// newFakeNInfer serves a healthy ninfer-serve; non-200 /health is covered
// with a bare handler in the tests that need it.
func newFakeNInfer(t *testing.T, modelsStatus int, models string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(modelsStatus)
		if modelsStatus == http.StatusOK {
			_, _ = w.Write([]byte(models))
		}
	})
	// No /metrics: the mux answers 404 like the real server.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestNInferDetectStoresCmdlineFallbacks(t *testing.T) {
	a := NewNInfer()
	argv := []string{"/opt/ninfer/build/apps/ninfer-serve", "/data/models/qwen3_8_27b_nvfp4.ninfer",
		"--model-id", "qwen-demo", "--host", "0.0.0.0", "--port", "18002",
		"--max-context", "240000", "--kv-capacity", "240000", "--max-concurrency", "2",
		"--spec", "mtp", "--draft-tokens", "3", "--lm-head-draft", "--preserve-thinking"}
	ep, ok := a.Detect(argv)
	if !ok || ep != "http://127.0.0.1:18002" {
		t.Fatalf("Detect = (%q, %v)", ep, ok)
	}
	s := a.Fetch(context.Background(), ep, FetchHint{})
	if s.Model != "qwen-demo" || s.CtxSize != 240000 || s.SlotsTotal != 2 {
		t.Fatalf("cmdline meta not stored: %+v", s)
	}
	// Restarted without the flags: stale slots/ctx must not survive, and the
	// model falls back to the container basename.
	a.Detect([]string{"ninfer-serve", "/data/models/qwen3_8_27b_nvfp4.ninfer", "--host", "0.0.0.0", "--port", "18002"})
	s = a.Fetch(context.Background(), ep, FetchHint{})
	if s.SlotsTotal != 0 || s.CtxSize != 0 || s.Model != "qwen3_8_27b_nvfp4" {
		t.Fatalf("stale cmdline meta survived restart: %+v", s)
	}
}

func TestNInferModelFromCmdline(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"ninfer-serve", "m.ninfer", "--model-id", "alias"}, "alias"},
		{[]string{"ninfer-serve", "--model-id=alias", "m.ninfer"}, "alias"},
		{[]string{"ninfer-serve", "/x/y/m.ninfer", "--port", "8002"}, "m"},
		{[]string{"ninfer-serve", "--port", "8002", "--vision", "/x/y/m.ninfer"}, "m"}, // boolean flag before the path
		{[]string{"ninfer-serve", "--port", "8002", "/x/y/container"}, "container"},    // no extension: first positional
		{[]string{"ninfer-serve", "--port=8002"}, ""},
	}
	for _, c := range cases {
		if got := ninferModelFromCmdline(c.argv); got != c.want {
			t.Errorf("ninferModelFromCmdline(%v) = %q, want %q", c.argv, got, c.want)
		}
	}
}

func TestNInferFetchOK(t *testing.T) {
	f := newFakeNInfer(t, http.StatusOK, ninferModelsJSON)
	a := NewNInfer()
	a.meta[normalizeEndpoint(f.URL)] = ninferMeta{model: "from-cmdline", ctx: 8192, slots: 2}

	s := a.Fetch(context.Background(), f.URL, FetchHint{})
	if s.Kind != ninferKind || s.Status != model.StatusOK {
		t.Fatalf("kind/status = %q/%q", s.Kind, s.Status)
	}
	// /v1/models is authoritative over the command line for id and context.
	if s.Model != "qwen-demo" || s.CtxSize != 240000 || s.SlotsTotal != 2 {
		t.Fatalf("identity = %+v", s)
	}
	if s.Metrics != nil {
		t.Fatalf("ninfer has no metrics endpoint; Metrics must stay nil: %+v", s.Metrics)
	}
}

func TestNInferFetchAPIKeyFallsBackToCmdline(t *testing.T) {
	f := newFakeNInfer(t, http.StatusUnauthorized, "")
	a := NewNInfer()
	a.meta[normalizeEndpoint(f.URL)] = ninferMeta{model: "qwen-demo", ctx: 240000, slots: 2}
	s := a.Fetch(context.Background(), f.URL, FetchHint{})
	if s.Status != model.StatusOK || s.Model != "qwen-demo" || s.CtxSize != 240000 {
		t.Fatalf("keyed server must keep cmdline identity: %+v", s)
	}
}

func TestNInferFetchUnreachable(t *testing.T) {
	a := NewNInfer()
	a.meta["http://127.0.0.1:1"] = ninferMeta{model: "qwen-demo", ctx: 240000, slots: 2}
	s := a.Fetch(context.Background(), "http://127.0.0.1:1", FetchHint{})
	if s.Status != model.StatusUnreachable {
		t.Fatalf("status = %q", s.Status)
	}
	if s.Model != "qwen-demo" || s.CtxSize != 240000 || s.SlotsTotal != 2 {
		t.Fatalf("cmdline identity must survive unreachable: %+v", s)
	}
}

func TestNInferFetchNon200HealthIsUnreachable(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	s := NewNInfer().Fetch(context.Background(), srv.URL, FetchHint{})
	if s.Status != model.StatusUnreachable {
		t.Fatalf("status = %q, want unreachable", s.Status)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1 (must not probe past /health)", hits.Load())
	}
}

func TestNInferFetchDoesNotFollowRedirects(t *testing.T) {
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

	s := NewNInfer().Fetch(context.Background(), redirector.URL, FetchHint{})
	if s.Status == model.StatusOK {
		t.Fatalf("a redirecting /health must not count as ok, got %q", s.Status)
	}
	if n := atomic.LoadInt32(&targetHits); n != 0 {
		t.Fatalf("redirect target was contacted %d time(s); redirects must not be followed", n)
	}
}
