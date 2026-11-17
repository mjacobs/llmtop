package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/model"
)

func TestPoolingChartUsesInputHistory(t *testing.T) {
	h := NewHostState("alpha")
	for i, rate := range []float64{0, 100, 400} {
		snap := model.Snapshot{
			Timestamp: time.Unix(int64(i+1), 0),
			Servers: []model.LLMServer{{
				Kind: "vllm", Endpoint: "http://127.0.0.1:8101", Role: "pooling", Status: model.StatusOK,
				Metrics: &model.LLMMetrics{PromptTPS: rate, KVCacheUsage: -1},
			}},
		}
		// Exercise the same role and rate fields when they arrive from an agent.
		wire, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		var received model.Snapshot
		if err := json.Unmarshal(wire, &received); err != nil {
			t.Fatal(err)
		}
		h.Apply(client.Update{Snapshot: &received})
	}
	srv := &h.Snap.Servers[0]
	th := DefaultTheme()
	lines := serverBlockLines(h, srv, th, 80)
	if len(lines) != serverBlockLineCount(srv) || len(lines) != 3 {
		t.Fatalf("pooling block has %d lines", len(lines))
	}
	if !strings.Contains(lines[1], "input 400.0 t/s") || strings.Contains(lines[1], "gen ") {
		t.Fatalf("pooling detail = %q", lines[1])
	}
	if !strings.Contains(lines[2], "input t/s") || !strings.Contains(lines[2], "█") {
		t.Fatalf("pooling chart did not plot nonzero input history: %q", lines[2])
	}
	if compact := serverLine(srv, th, 80); !strings.Contains(compact, "input 400.0 t/s") {
		t.Fatalf("compact pooling detail = %q", compact)
	}
	// Normal text models still plot their generation series (all zero here).
	srv.Role = ""
	lines = serverBlockLines(h, srv, th, 80)
	if !strings.Contains(lines[2], "gen t/s") || strings.Contains(lines[2], "█") {
		t.Fatalf("generation chart used the input series: %q", lines[2])
	}
}
