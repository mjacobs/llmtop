package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mjacobs/llmtop/internal/model"
)

const strataHealthJSON = `{"service":"strata","status":"ok","loaded":true,"model":"qwen-demo","max_context":32768}`

func strataMetricsJSON(prompt, output int, live string) string {
	return fmt.Sprintf(`{"engine":{"model":"qwen-demo","max_context":32768},"live":%s,"totals":{"prompt_tokens":%d,"output_tokens":%d}}`, live, prompt, output)
}

func TestStrataEndpointFromCmdline(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"release script", []string{"/opt/strata/.venv/bin/python", "/opt/strata/serve/server.py", "--engine", "strata", "--host", "127.0.0.1", "--port", "8003"}, "http://127.0.0.1:8003"},
		{"module defaults", []string{"python3.12", "-u", "-m", "serve.server", "--engine=strata"}, "http://127.0.0.1:8095"},
		{"wildcard", []string{"python", "serve/server.py", "--engine", "strata", "--host=0.0.0.0", "--port=9000"}, "http://127.0.0.1:9000"},
		{"IPv6", []string{"python", "serve/server.py", "--engine=strata", "--host=::1"}, "http://[::1]:8095"},
		{"direct script", []string{"./serve/server.py", "--engine=strata"}, "http://127.0.0.1:8095"},
		{"config needs explicit host", []string{"python", "serve/server.py", "--engine=strata", "--config=/models/strata.json"}, ""},
		{"empty host uses config", []string{"python", "serve/server.py", "--engine=strata", "--config=/models/strata.json", "--host="}, ""},
		{"config with CLI host", []string{"python", "serve/server.py", "--engine=strata", "--config=/models/strata.json", "--host=::1", "--port=8003"}, "http://[::1]:8003"},
		{"native pipe engine", []string{"/opt/strata/bin/strata", "--serve", "--port", "8003"}, ""},
		{"mock", []string{"python", "-m", "serve.server", "--engine=mock"}, ""},
		{"default mock", []string{"python", "-m", "serve.server"}, ""},
		{"unrelated script", []string{"python", "other/server.py", "--engine=strata"}, ""},
		{"argument is not entrypoint", []string{"python", "unrelated.py", "serve/server.py", "--engine=strata"}, ""},
		{"code is not entrypoint", []string{"python", "-c", "serve/server.py", "--engine=strata"}, ""},
		{"non-python", []string{"echo", "serve/server.py", "--engine=strata"}, ""},
		{"foreign host", []string{"python", "serve/server.py", "--engine=strata", "--host=192.0.2.99"}, ""},
		{"hostname rejected", []string{"python", "serve/server.py", "--engine=strata", "--host=example.com"}, ""},
		{"URL injection", []string{"python", "serve/server.py", "--engine=strata", "--host=localhost/path"}, ""},
		{"bad port", []string{"python", "serve/server.py", "--engine=strata", "--port=0"}, ""},
		{"empty", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep, ok := StrataEndpointFromCmdline(tc.argv)
			if ep != tc.want || ok != (tc.want != "") {
				t.Fatalf("got (%q, %v), want %q", ep, ok, tc.want)
			}
		})
	}
}

func TestStrataFetchRates(t *testing.T) {
	live := `{"state":"idle"}`
	prompt, output := 100, 200
	var metricsBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(strataHealthJSON))
		case "/metrics":
			_, _ = w.Write([]byte(metricsBody.Load().(string)))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a := NewStrata()
	now := time.Unix(1000, 0)
	a.Now = func() time.Time { return now }
	fetch := func() model.LLMServer {
		t.Helper()
		metricsBody.Store(strataMetricsJSON(prompt, output, live))
		s := a.Fetch(context.Background(), srv.URL, FetchHint{})
		if s.Kind != KindStrata || s.Status != model.StatusOK || s.Model != "qwen-demo" || s.CtxSize != 32768 || s.Metrics == nil || s.MetricsErr != "" {
			t.Fatalf("server = %+v", s)
		}
		if s.Metrics.KVCacheUsage != -1 {
			t.Fatal("context fill must not be shown as KV-cache occupancy")
		}
		return s
	}
	s := fetch()
	if s.SlotsTotal != 1 || s.Metrics.PredictedTPS != 0 || s.Metrics.PredictedTokensTotal != 200 {
		t.Fatalf("initial idle sample = %+v", s)
	}
	now = now.Add(time.Second)
	live = `{"state":"generating","tok_s":140.5,"queued":2}`
	s = fetch()
	if s.Metrics.PredictedTPS != 140.5 || !s.Metrics.PredictedTPSEst || s.Metrics.RequestsProcessing != 1 || s.Metrics.RequestsDeferred != 2 {
		t.Fatalf("live generation = %+v", s.Metrics)
	}
	now = now.Add(time.Second)
	live = `{"state":"idle","tok_s":140.5}`
	prompt, output = 140, 400
	s = fetch()
	if s.Metrics.PromptTPS != 20 || s.Metrics.PredictedTPS != 100 || s.Metrics.PredictedTPSEst {
		t.Fatalf("completion rates = %+v", s.Metrics)
	}
	now = now.Add(time.Second)
	s = fetch()
	if s.Metrics.PredictedTPS != 0 {
		t.Fatalf("idle rate retained last request: %+v", s.Metrics)
	}
	now = now.Add(time.Second)
	live = `{"state":"reading","tok_s":140.5}`
	if s = fetch(); s.Metrics.PredictedTPS != 0 || s.Metrics.PredictedTPSEst {
		t.Fatalf("prefill must not show stale decode rate: %+v", s.Metrics)
	}
	now = now.Add(time.Second)
	live = `{"state":"generating","tok_s":140.5,"parallel":2,"running":2,"queued":1,"waiting":3}`
	s = fetch()
	if s.SlotsTotal != 2 || s.Metrics.RequestsProcessing != 2 || s.Metrics.RequestsDeferred != 4 || s.Metrics.PredictedTPSEst || s.Metrics.PredictedTPS != 0 {
		t.Fatalf("parallel sample must not use newest-request rate as aggregate: %+v / %+v", s, s.Metrics)
	}
	now = now.Add(time.Second)
	prompt, output = 0, 0
	live = `{"state":"idle"}`
	if s = fetch(); s.Metrics.PredictedTPS != 0 || s.Metrics.PromptTPS != 0 {
		t.Fatalf("restart must re-anchor: %+v", s.Metrics)
	}
}

func TestStrataFetchFailuresAndSleep(t *testing.T) {
	for _, tc := range []struct {
		name, health, metrics, status, metricsErr string
		healthCode, metricsCode                   int
		metricsHits                               int32
	}{
		{"loading", "", "", model.StatusLoading, "", 503, 200, 0},
		{"bad health", "oops", "", model.StatusUnreachable, "", 200, 200, 0},
		{"wrong service", `{"status":"ok","service":"other"}`, "", model.StatusUnreachable, "", 200, 200, 0},
		{"unloaded", `{"service":"strata","status":"ok","loaded":false}`, "", model.StatusSleeping, "", 200, 200, 0},
		{"unauthorized metrics", strataHealthJSON, "", model.StatusOK, "http 401", 200, 401, 1},
		{"missing metrics", strataHealthJSON, "", model.StatusOK, "http 404", 200, 404, 1},
		{"broken metrics", strataHealthJSON, "oops", model.StatusOK, "invalid JSON metrics", 200, 200, 1},
		{"empty metrics", strataHealthJSON, `{}`, model.StatusOK, "invalid JSON metrics", 200, 200, 1},
		{"negative counters", strataHealthJSON, strataMetricsJSON(-1, 0, `{"state":"idle"}`), model.StatusOK, "invalid JSON metrics", 200, 200, 1},
		{"unloaded during fetch", strataHealthJSON, strataMetricsJSON(0, 0, `{"state":"unloaded"}`), model.StatusSleeping, "", 200, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(tc.healthCode)
					_, _ = w.Write([]byte(tc.health))
				} else {
					hits.Add(1)
					w.WriteHeader(tc.metricsCode)
					_, _ = w.Write([]byte(tc.metrics))
				}
			}))
			defer srv.Close()
			a := NewStrata()
			a.rates.TPS(srv.URL, time.Unix(1000, 0), 0, 0, 0, false)
			s := a.Fetch(context.Background(), srv.URL, FetchHint{})
			if s.Status != tc.status || s.MetricsErr != tc.metricsErr || s.Metrics != nil || hits.Load() != tc.metricsHits {
				t.Fatalf("server=%+v metrics hits=%d", s, hits.Load())
			}
			if tc.status != model.StatusOK {
				if _, ok := a.rates.TPS(srv.URL, time.Unix(2000, 0), 100, 100, 100, false); ok {
					t.Fatal("sleep/failure must reset rate anchors")
				}
			}
		})
	}
}

func TestStrataFetchAuthAndRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	var redirect atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("missing authentication or JSON content negotiation")
		}
		switch {
		case redirect.Load():
			http.Redirect(w, r, target.URL, http.StatusFound)
		case r.URL.Path == "/health":
			_, _ = w.Write([]byte(strataHealthJSON))
		default:
			_, _ = w.Write([]byte(strataMetricsJSON(0, 0, `{"state":"idle"}`)))
		}
	}))
	defer srv.Close()
	a := NewStrata()
	hint := FetchHint{APIKey: "test-key"}
	if s := a.Fetch(context.Background(), srv.URL+"/", hint); s.Metrics == nil {
		t.Fatalf("authenticated scrape failed: %+v", s)
	}
	redirect.Store(true)
	if s := a.Fetch(context.Background(), srv.URL, hint); s.Status != model.StatusUnreachable || targetHits.Load() != 0 {
		t.Fatalf("redirect followed: server=%+v hits=%d", s, targetHits.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s := a.Fetch(ctx, srv.URL, hint); s.Status != model.StatusUnreachable {
		t.Fatalf("cancelled request = %+v", s)
	}
}
