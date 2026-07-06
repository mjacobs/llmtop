package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/mjacobs/llmtop/internal/model"
)

const workerProps = `{"default_generation_settings":{"params":{},"n_ctx":262144},"model_alias":"qwen36-35b-mtp","model_path":"/tmp/llama-router/models.d/qwen36-35b-mtp.gguf","total_slots":1,"build_info":"b9704-10786217e"}`
const routerProps = `{"role":"router","model_alias":"llama-server","model_path":"none","default_generation_settings":{"params":null,"n_ctx":0},"build_info":"b9704-10786217e"}`

func metricsBody(prompt, predicted int) string {
	return "llamacpp:prompt_tokens_total " + itoa(prompt) + "\n" +
		"llamacpp:tokens_predicted_total " + itoa(predicted) + "\n" +
		"llamacpp:prompt_tokens_seconds 2754.78\n" +
		"llamacpp:predicted_tokens_seconds 367.57\n" +
		"llamacpp:requests_processing 1\n" +
		"llamacpp:requests_deferred 0\n"
}

func itoa(n int) string { return strconv.Itoa(n) }

func newFakeServer(t *testing.T, props string, metricsStatus int, metrics *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(props))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if metrics == nil {
			w.WriteHeader(metricsStatus)
			return
		}
		_, _ = w.Write([]byte(*metrics))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchWorkerWithMetrics(t *testing.T) {
	body := metricsBody(16121, 247195)
	srv := newFakeServer(t, workerProps, 200, &body)

	a := NewLlamaCpp()
	base := time.Unix(2000, 0)
	a.Now = func() time.Time { return base }

	s := a.Fetch(context.Background(), srv.URL)
	if s.Kind != "llama.cpp" || s.Status != model.StatusOK {
		t.Fatalf("kind/status = %q/%q", s.Kind, s.Status)
	}
	if s.Model != "qwen36-35b-mtp" || s.CtxSize != 262144 || s.SlotsTotal != 1 {
		t.Fatalf("props not mapped: %+v", s)
	}
	if s.Metrics == nil {
		t.Fatal("Metrics must be present")
	}
	// First sample: falls back to avg gauges.
	if s.Metrics.PromptTPS != 2754.78 || s.Metrics.PredictedTPS != 367.57 {
		t.Fatalf("first-sample TPS = %v/%v, want avg gauges", s.Metrics.PromptTPS, s.Metrics.PredictedTPS)
	}
	if s.Metrics.KVCacheUsage != -1 {
		t.Fatalf("KVCacheUsage = %v, want -1 (absent)", s.Metrics.KVCacheUsage)
	}
	if s.Metrics.RequestsProcessing != 1 {
		t.Fatalf("RequestsProcessing = %d", s.Metrics.RequestsProcessing)
	}

	// Second sample 2s later with +200/+100 tokens → instantaneous rates.
	body = metricsBody(16321, 247295) // update via the shared pointer
	a.Now = func() time.Time { return base.Add(2 * time.Second) }
	s2 := a.Fetch(context.Background(), srv.URL)
	if s2.Metrics.PromptTPS != 100 || s2.Metrics.PredictedTPS != 50 {
		t.Fatalf("delta TPS = %v/%v, want 100/50", s2.Metrics.PromptTPS, s2.Metrics.PredictedTPS)
	}
}

func TestFetchNoMetrics501(t *testing.T) {
	srv := newFakeServer(t, workerProps, 501, nil)
	s := NewLlamaCpp().Fetch(context.Background(), srv.URL)
	if s.Status != model.StatusOK || s.Metrics != nil {
		t.Fatalf("want ok + nil Metrics, got %q %v", s.Status, s.Metrics)
	}
}

func TestFetchRouter(t *testing.T) {
	srv := newFakeServer(t, routerProps, 400, nil)
	s := NewLlamaCpp().Fetch(context.Background(), srv.URL)
	if s.Role != "router" || s.Model != "" {
		t.Fatalf("router: role=%q model=%q, want router/empty", s.Role, s.Model)
	}
}

func TestFetchLoading(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"status":"loading model"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s := NewLlamaCpp().Fetch(context.Background(), srv.URL)
	if s.Status != model.StatusLoading {
		t.Fatalf("status = %q, want loading", s.Status)
	}
}

func TestFetchUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	srv.Close() // dead endpoint
	s := NewLlamaCpp().Fetch(context.Background(), srv.URL)
	if s.Status != model.StatusUnreachable {
		t.Fatalf("status = %q, want unreachable", s.Status)
	}
}

func TestDetectDelegatesToCmdlineParser(t *testing.T) {
	a := NewLlamaCpp()
	if ep, ok := a.Detect([]string{"/usr/bin/llama-server", "--port", "9"}); !ok || ep != "http://127.0.0.1:9" {
		t.Fatalf("Detect = %q %v", ep, ok)
	}
	if _, ok := a.Detect([]string{"chrome"}); ok {
		t.Fatal("chrome must not detect")
	}
}
