package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

	s := a.Fetch(context.Background(), srv.URL, FetchHint{})
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
	if s.Metrics.PredictedTPSEst {
		t.Fatalf("first-sample PredictedTPSEst = true, want false (lifetime-gauge fallback)")
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
	s2 := a.Fetch(context.Background(), srv.URL, FetchHint{})
	if s2.Metrics.PromptTPS != 100 || s2.Metrics.PredictedTPS != 50 {
		t.Fatalf("delta TPS = %v/%v, want 100/50", s2.Metrics.PromptTPS, s2.Metrics.PredictedTPS)
	}
}

// TestFetchLiveEstimateThenMeasured drives three polls against a fixture
// where prompt_tokens_total and tokens_predicted_total are frozen while
// n_decode_total ticks and requests_processing stays 1, then a completion
// poll where tokens_predicted_total jumps. Verifies PredictedTPSEst flags
// and values through both the in-flight-estimate and amortized-measurement
// paths.
func TestFetchLiveEstimateThenMeasured(t *testing.T) {
	body := "llamacpp:prompt_tokens_total 1000\n" +
		"llamacpp:tokens_predicted_total 5000\n" +
		"llamacpp:n_decode_total 100\n" +
		"llamacpp:prompt_tokens_seconds 2754.78\n" +
		"llamacpp:predicted_tokens_seconds 367.57\n" +
		"llamacpp:requests_processing 1\n" +
		"llamacpp:requests_deferred 0\n"
	srv := newFakeServer(t, workerProps, 200, &body)

	a := NewLlamaCpp()
	base := time.Unix(3000, 0)
	a.Now = func() time.Time { return base }

	// Poll 1: primes the tracker (first-sample fallback).
	s1 := a.Fetch(context.Background(), srv.URL, FetchHint{})
	if s1.Metrics == nil {
		t.Fatal("poll 1: Metrics must be present")
	}

	// Poll 2, 1s later: token counters frozen, decode +30, still busy ->
	// in-flight estimate at the default ratio (1.0).
	body = "llamacpp:prompt_tokens_total 1000\n" +
		"llamacpp:tokens_predicted_total 5000\n" +
		"llamacpp:n_decode_total 130\n" +
		"llamacpp:prompt_tokens_seconds 2754.78\n" +
		"llamacpp:predicted_tokens_seconds 367.57\n" +
		"llamacpp:requests_processing 1\n" +
		"llamacpp:requests_deferred 0\n"
	a.Now = func() time.Time { return base.Add(1 * time.Second) }
	s2 := a.Fetch(context.Background(), srv.URL, FetchHint{})
	if s2.Metrics == nil {
		t.Fatal("poll 2: Metrics must be present")
	}
	if !s2.Metrics.PredictedTPSEst {
		t.Fatalf("poll 2: PredictedTPSEst = false, want true (in-flight estimate)")
	}
	if s2.Metrics.PredictedTPS != 30 {
		t.Fatalf("poll 2: PredictedTPS = %v, want 30 (30 decodes/1s * default ratio 1.0)", s2.Metrics.PredictedTPS)
	}

	// Poll 3, 1s later (2s since poll 1's anchor): tokens_predicted_total
	// jumps +300 -> measured, amortized over the full frozen window (2s),
	// not the last poll interval.
	body = "llamacpp:prompt_tokens_total 1000\n" +
		"llamacpp:tokens_predicted_total 5300\n" +
		"llamacpp:n_decode_total 160\n" +
		"llamacpp:prompt_tokens_seconds 2754.78\n" +
		"llamacpp:predicted_tokens_seconds 367.57\n" +
		"llamacpp:requests_processing 1\n" +
		"llamacpp:requests_deferred 0\n"
	a.Now = func() time.Time { return base.Add(2 * time.Second) }
	s3 := a.Fetch(context.Background(), srv.URL, FetchHint{})
	if s3.Metrics == nil {
		t.Fatal("poll 3: Metrics must be present")
	}
	if s3.Metrics.PredictedTPSEst {
		t.Fatalf("poll 3: PredictedTPSEst = true, want false (measured completion)")
	}
	if s3.Metrics.PredictedTPS != 150 {
		t.Fatalf("poll 3: PredictedTPS = %v, want 150 (300 tokens / 2s window)", s3.Metrics.PredictedTPS)
	}
}

func TestFetchNoMetrics501(t *testing.T) {
	srv := newFakeServer(t, workerProps, 501, nil)
	s := NewLlamaCpp().Fetch(context.Background(), srv.URL, FetchHint{})
	if s.Status != model.StatusOK || s.Metrics != nil {
		t.Fatalf("want ok + nil Metrics, got %q %v", s.Status, s.Metrics)
	}
}

func TestFetchRouter(t *testing.T) {
	srv := newFakeServer(t, routerProps, 400, nil)
	s := NewLlamaCpp().Fetch(context.Background(), srv.URL, FetchHint{})
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
	s := NewLlamaCpp().Fetch(context.Background(), srv.URL, FetchHint{})
	if s.Status != model.StatusLoading {
		t.Fatalf("status = %q, want loading", s.Status)
	}
}

func TestFetchUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	srv.Close() // dead endpoint
	s := NewLlamaCpp().Fetch(context.Background(), srv.URL, FetchHint{})
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

// --- sleep-idle awareness ---------------------------------------------------
//
// llama.cpp counts any /metrics scrape as activity: it resets the
// --sleep-idle-seconds timer and wakes a sleeping server. These tests pin the
// adapter's counter-measures: report sleeping state from /props (never
// touching /metrics), and stop polling /metrics on sleep-idle-enabled servers
// once they go quiet, resuming on GPU activity or a wake transition.

// wokenProps is what a sleep-idle-capable server reports once awake: such
// servers always include is_sleeping explicitly. workerProps (no field)
// models a build without sleep support, whose state is "unknown".
const wokenProps = `{"model_alias":"demo-model","model_path":"/models/demo-model.gguf","total_slots":1,"default_generation_settings":{"n_ctx":131072},"is_sleeping":false}`

const idleMetrics = "llamacpp:prompt_tokens_total 1000\n" +
	"llamacpp:tokens_predicted_total 5000\n" +
	"llamacpp:n_decode_total 100\n" +
	"llamacpp:prompt_tokens_seconds 10\n" +
	"llamacpp:predicted_tokens_seconds 10\n" +
	"llamacpp:requests_processing 0\n" +
	"llamacpp:requests_deferred 0\n"

// switchableServer serves mutable props/metrics and counts /metrics hits.
func switchableServer(t *testing.T, props *string, metrics *string, hits *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(*props))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		*hits++
		_, _ = w.Write([]byte(*metrics))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// registerSleepIdle tells the adapter (via Detect, as the process scanner
// would) that the server at srv.URL runs with --sleep-idle-seconds.
func registerSleepIdle(t *testing.T, a *LlamaCpp, srv *httptest.Server) {
	t.Helper()
	hostport := strings.TrimPrefix(srv.URL, "http://")
	host, port, ok := strings.Cut(hostport, ":")
	if !ok {
		t.Fatalf("unexpected test server URL %q", srv.URL)
	}
	ep, ok := a.Detect([]string{"llama-server", "--host", host, "--port", port, "--sleep-idle-seconds", "1800"})
	if !ok || ep != srv.URL {
		t.Fatalf("Detect = %q %v, want %q", ep, ok, srv.URL)
	}
}

func TestFetchSleepingServerSkipsMetrics(t *testing.T) {
	props := `{"model_alias":"demo-model","model_path":"/models/demo-model.gguf","total_slots":1,"default_generation_settings":{"n_ctx":131072},"is_sleeping":true}`
	metrics, hits := idleMetrics, 0
	srv := switchableServer(t, &props, &metrics, &hits)

	s := NewLlamaCpp().Fetch(context.Background(), srv.URL, FetchHint{})
	if s.Status != model.StatusSleeping {
		t.Fatalf("status = %q, want %q", s.Status, model.StatusSleeping)
	}
	if s.Model != "demo-model" {
		t.Fatalf("model = %q, want demo-model (props still mapped)", s.Model)
	}
	if s.Metrics != nil {
		t.Fatalf("Metrics = %+v, want nil while sleeping", s.Metrics)
	}
	if hits != 0 {
		t.Fatalf("/metrics hit %d times; polling a sleeping server wakes it", hits)
	}
}

func TestSleepIdleGateStopsPollingWhenIdle(t *testing.T) {
	props := workerProps
	metrics, hits := idleMetrics, 0
	srv := switchableServer(t, &props, &metrics, &hits)

	a := NewLlamaCpp()
	base := time.Unix(5000, 0)
	registerSleepIdle(t, a, srv)

	var last model.LLMServer
	for i := 0; i < 6; i++ {
		a.Now = func() time.Time { return base.Add(time.Duration(i) * 2 * time.Second) }
		last = a.Fetch(context.Background(), srv.URL, FetchHint{})
	}
	if hits != metricsIdleGraceTicks {
		t.Fatalf("/metrics hit %d times over 6 idle polls, want %d (grace) then gated", hits, metricsIdleGraceTicks)
	}
	if last.Status != model.StatusOK {
		t.Fatalf("status = %q, want ok (awake, just not polled)", last.Status)
	}
	if last.Metrics == nil {
		t.Fatal("Metrics must be retained (cached) while gated")
	}
	if last.Metrics.PromptTPS != 0 || last.Metrics.PredictedTPS != 0 {
		t.Fatalf("gated rates = %v/%v, want 0/0", last.Metrics.PromptTPS, last.Metrics.PredictedTPS)
	}
}

func TestSleepIdleGateResumesOnGPUActivity(t *testing.T) {
	props := workerProps
	metrics, hits := idleMetrics, 0
	srv := switchableServer(t, &props, &metrics, &hits)

	a := NewLlamaCpp()
	base := time.Unix(6000, 0)
	registerSleepIdle(t, a, srv)

	for i := 0; i < 6; i++ { // drain the grace period
		a.Now = func() time.Time { return base.Add(time.Duration(i) * 2 * time.Second) }
		a.Fetch(context.Background(), srv.URL, FetchHint{})
	}
	gated := hits

	a.Now = func() time.Time { return base.Add(20 * time.Second) }
	s := a.Fetch(context.Background(), srv.URL, FetchHint{GPUActive: true})
	if hits != gated+1 {
		t.Fatalf("/metrics hits = %d after GPU-active hint, want %d", hits, gated+1)
	}
	if s.Metrics == nil {
		t.Fatal("Metrics must be fresh after resumed poll")
	}
}

func TestSleepIdleGateResumesAfterWakeTransition(t *testing.T) {
	props := `{"model_alias":"demo-model","model_path":"/models/demo-model.gguf","total_slots":1,"default_generation_settings":{"n_ctx":131072},"is_sleeping":true}`
	metrics, hits := idleMetrics, 0
	srv := switchableServer(t, &props, &metrics, &hits)

	a := NewLlamaCpp()
	base := time.Unix(7000, 0)
	a.Now = func() time.Time { return base }
	registerSleepIdle(t, a, srv)

	if s := a.Fetch(context.Background(), srv.URL, FetchHint{}); s.Status != model.StatusSleeping || hits != 0 {
		t.Fatalf("sleeping poll: status=%q hits=%d", s.Status, hits)
	}

	// Some other client woke it: is_sleeping flips false. The adapter must
	// resume /metrics polling even with no GPU hint yet.
	props = wokenProps
	a.Now = func() time.Time { return base.Add(2 * time.Second) }
	s := a.Fetch(context.Background(), srv.URL, FetchHint{})
	if s.Status != model.StatusOK || hits != 1 {
		t.Fatalf("woken poll: status=%q hits=%d, want ok/1", s.Status, hits)
	}
}

func TestSleepIdleGateAgesOutWhenMetricsDisabled(t *testing.T) {
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(workerProps))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(501) // llama.cpp without --metrics
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	a := NewLlamaCpp()
	base := time.Unix(9000, 0)
	registerSleepIdle(t, a, srv)

	var last model.LLMServer
	for i := 0; i < 6; i++ {
		a.Now = func() time.Time { return base.Add(time.Duration(i) * 2 * time.Second) }
		last = a.Fetch(context.Background(), srv.URL, FetchHint{})
	}
	if hits != metricsIdleGraceTicks {
		t.Fatalf("/metrics hit %d times with metrics disabled, want %d (grace) then gated", hits, metricsIdleGraceTicks)
	}
	if last.Status != model.StatusOK || last.Metrics != nil {
		t.Fatalf("gated 501 server: status=%q metrics=%v, want ok/nil", last.Status, last.Metrics)
	}
}

func TestPropsFailurePreservesSleepingState(t *testing.T) {
	sleepingProps := `{"model_alias":"demo-model","model_path":"/models/demo-model.gguf","total_slots":1,"default_generation_settings":{"n_ctx":131072},"is_sleeping":true}`
	props, propsCode := sleepingProps, 200
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		if propsCode != 200 {
			w.WriteHeader(propsCode)
			return
		}
		_, _ = w.Write([]byte(props))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(idleMetrics))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	a := NewLlamaCpp()
	if s := a.Fetch(context.Background(), srv.URL, FetchHint{}); s.Status != model.StatusSleeping {
		t.Fatalf("initial poll: status = %q, want sleeping", s.Status)
	}

	// /props fails: the sleep state is unknown, not "awake". Clearing it
	// would scrape /metrics and wake the server.
	propsCode = 500
	if s := a.Fetch(context.Background(), srv.URL, FetchHint{}); s.Status != model.StatusSleeping {
		t.Fatalf("failed-/props poll: status = %q, want sleeping preserved", s.Status)
	}

	// Malformed /props body must behave the same way.
	propsCode, props = 200, `{"truncated`
	if s := a.Fetch(context.Background(), srv.URL, FetchHint{}); s.Status != model.StatusSleeping {
		t.Fatalf("malformed-/props poll: status = %q, want sleeping preserved", s.Status)
	}

	// Valid JSON without is_sleeping is just as unknown: the zero value
	// must not masquerade as an explicit false.
	props = workerProps
	if s := a.Fetch(context.Background(), srv.URL, FetchHint{}); s.Status != model.StatusSleeping {
		t.Fatalf("absent-is_sleeping poll: status = %q, want sleeping preserved", s.Status)
	}
	if hits != 0 {
		t.Fatalf("/metrics hit %d times while sleep state unknown; polling wakes the server", hits)
	}

	// Only an explicit is_sleeping=false is recognized as a wake.
	props = wokenProps
	if s := a.Fetch(context.Background(), srv.URL, FetchHint{}); s.Status != model.StatusOK || hits != 1 {
		t.Fatalf("woken poll: status=%q hits=%d, want ok/1", s.Status, hits)
	}
}

// TestSleepIdleGateDropsCacheWhenMetricsBecomeUnsupported pins the
// 200-to-501 transition: once /metrics definitively stops being served
// (server replaced or reconfigured without --metrics), the gate must not
// keep serving the sample cached from the 200 era.
func TestSleepIdleGateDropsCacheWhenMetricsBecomeUnsupported(t *testing.T) {
	metricsCode, payload, hits := 200, idleMetrics, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(workerProps))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		hits++
		if metricsCode != 200 {
			w.WriteHeader(metricsCode)
			return
		}
		_, _ = w.Write([]byte(payload))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	a := NewLlamaCpp()
	base := time.Unix(11000, 0)
	registerSleepIdle(t, a, srv)

	a.Now = func() time.Time { return base }
	if s := a.Fetch(context.Background(), srv.URL, FetchHint{}); s.Metrics == nil {
		t.Fatal("setup: first 200 poll must yield metrics")
	}

	metricsCode = 501
	var last model.LLMServer
	for i := 1; i < 6; i++ {
		a.Now = func() time.Time { return base.Add(time.Duration(i) * 2 * time.Second) }
		last = a.Fetch(context.Background(), srv.URL, FetchHint{})
	}
	if last.Metrics != nil {
		t.Fatalf("gated poll served stale cached metrics after 501: %+v", last.Metrics)
	}
	if hits != metricsIdleGraceTicks {
		t.Fatalf("/metrics hit %d times, want %d (one 200 + 501s until gated)", hits, metricsIdleGraceTicks)
	}

	// Metrics return, with counters higher than the pre-501 era (a busier
	// replacement server). The scrape must be treated as a fresh first
	// sample — lifetime-gauge fallbacks — not amortized against anchors
	// captured before the 501s, which would emit rates spanning two server
	// lifetimes.
	metricsCode = 200
	payload = "llamacpp:prompt_tokens_total 9000\n" +
		"llamacpp:tokens_predicted_total 90000\n" +
		"llamacpp:n_decode_total 900\n" +
		"llamacpp:prompt_tokens_seconds 42.5\n" +
		"llamacpp:predicted_tokens_seconds 33.25\n" +
		"llamacpp:requests_processing 0\n" +
		"llamacpp:requests_deferred 0\n"
	a.Now = func() time.Time { return base.Add(12 * time.Second) }
	s := a.Fetch(context.Background(), srv.URL, FetchHint{GPUActive: true}) // reopen the gate
	if s.Metrics == nil {
		t.Fatal("returning 200 poll must yield metrics")
	}
	if s.Metrics.PromptTPS != 42.5 || s.Metrics.PredictedTPS != 33.25 {
		t.Fatalf("returning metrics rates = %v/%v, want gauge fallbacks 42.5/33.25 (fresh sample)",
			s.Metrics.PromptTPS, s.Metrics.PredictedTPS)
	}
}

func TestDetectClearsStateWhenSleepIdleRemoved(t *testing.T) {
	props := workerProps
	metrics, hits := idleMetrics, 0
	srv := switchableServer(t, &props, &metrics, &hits)

	a := NewLlamaCpp()
	base := time.Unix(10000, 0)
	registerSleepIdle(t, a, srv)

	for i := 0; i < 6; i++ { // drain the grace period; gate closes
		a.Now = func() time.Time { return base.Add(time.Duration(i) * 2 * time.Second) }
		a.Fetch(context.Background(), srv.URL, FetchHint{})
	}
	gated := hits
	if gated != metricsIdleGraceTicks {
		t.Fatalf("setup: /metrics hits = %d, want %d", gated, metricsIdleGraceTicks)
	}

	// The process at this endpoint is replaced by one without
	// --sleep-idle-seconds; the scanner re-Detects it with the new cmdline.
	hostport := strings.TrimPrefix(srv.URL, "http://")
	host, port, ok := strings.Cut(hostport, ":")
	if !ok {
		t.Fatalf("unexpected test server URL %q", srv.URL)
	}
	if ep, ok := a.Detect([]string{"llama-server", "--host", host, "--port", port}); !ok || ep != srv.URL {
		t.Fatalf("Detect = %q %v, want %q", ep, ok, srv.URL)
	}

	for i := 6; i < 12; i++ {
		a.Now = func() time.Time { return base.Add(time.Duration(i) * 2 * time.Second) }
		a.Fetch(context.Background(), srv.URL, FetchHint{})
	}
	if hits != gated+6 {
		t.Fatalf("/metrics hits = %d after sleep-idle removed, want %d (unconditional polling)", hits, gated+6)
	}
}

func TestNoGateWithoutSleepIdleFlag(t *testing.T) {
	props := workerProps
	metrics, hits := idleMetrics, 0
	srv := switchableServer(t, &props, &metrics, &hits)

	a := NewLlamaCpp()
	base := time.Unix(8000, 0)
	for i := 0; i < 6; i++ {
		a.Now = func() time.Time { return base.Add(time.Duration(i) * 2 * time.Second) }
		a.Fetch(context.Background(), srv.URL, FetchHint{})
	}
	if hits != 6 {
		t.Fatalf("/metrics hit %d times, want 6 (no sleep-idle -> no gating)", hits)
	}
}
