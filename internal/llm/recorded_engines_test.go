package llm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mjacobs/llmtop/internal/model"
)

func TestRecordedEngineResponses(t *testing.T) {
	cases := []struct {
		folder    string
		adapter   Adapter
		files     map[string]string
		slots     int
		cacheUsed float64
	}{
		{
			folder: "llama.cpp-b11541-ce8c04021", adapter: NewLlamaCpp(), slots: 1, cacheUsed: -1,
			files: map[string]string{"/health": "health.json", "/props": "props.json", "/metrics": "metrics.prom"},
		},
		{
			folder: "vllm-0.18.0", adapter: NewVLLM(), cacheUsed: 0,
			files: map[string]string{"/health": "health.json", "/v1/models": "models.json", "/metrics": "metrics.prom"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.folder, func(t *testing.T) {
			bodies := make(map[string][]byte, len(tc.files))
			for path, file := range tc.files {
				body, err := os.ReadFile(filepath.Join("testdata/engines", tc.folder, file))
				if err != nil {
					t.Fatal(err)
				}
				bodies[path] = body
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, ok := bodies[req.URL.Path]
				if !ok || req.Method != http.MethodGet {
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
					http.NotFound(w, req)
					return
				}
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			sample := tc.adapter.Fetch(t.Context(), srv.URL, FetchHint{})
			if sample.Kind != tc.adapter.Kind() || sample.Status != model.StatusOK || sample.Model != "alpha" || sample.CtxSize != 512 || sample.SlotsTotal != tc.slots {
				t.Fatalf("unexpected engine sample: %+v", sample)
			}
			metrics := sample.Metrics
			if metrics == nil || metrics.PromptTokensTotal != 1 || metrics.PredictedTokensTotal != 2 ||
				metrics.RequestsProcessing != 0 || metrics.RequestsDeferred != 0 || metrics.KVCacheUsage != tc.cacheUsed {
				t.Fatalf("unexpected engine metrics: %+v", metrics)
			}
		})
	}
}
