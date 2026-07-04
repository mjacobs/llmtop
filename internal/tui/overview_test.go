package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/history"
	"github.com/mjacobs/llmtop/internal/model"
)

func TestRenderOverviewContent(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 100, DefaultTheme())

	for _, want := range []string{
		"alpha",            // host name
		"GPU0",            // gpu row
		"qwen36-35b-mtp",  // server model
		"t/s",             // throughput
		"(--metrics off)", // degraded server hint
		"unreachable",     // dead host panel
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q\n%s", want, out)
		}
	}
}

func TestRenderOverviewWidth(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 80, DefaultTheme())
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line width %d > 80: %q", w, line)
		}
	}
}

func TestHostStateApplyMaintainsRings(t *testing.T) {
	hosts := SampleHostStates()
	h := hosts[0]
	if h.Snap == nil || len(h.UtilHist) == 0 {
		t.Fatalf("sample host state incomplete: %+v", h)
	}
	if h.UtilHist[0].Len() == 0 || h.MemHist[0].Len() == 0 {
		t.Fatal("rings must have samples after Apply")
	}
}

// TestHostStateApplyDedupesOnTimestamp guards against remote sources
// delivering duplicate Snapshots (same Timestamp) at poll cadence — Apply
// must skip ring-pushes for a repeat of the last-applied timestamp.
func TestHostStateApplyDedupesOnTimestamp(t *testing.T) {
	h := NewHostState("dup")
	snap := &model.Snapshot{
		Timestamp: time.Unix(1_700_000_000, 0),
		GPUs: []model.GPU{
			{Index: 0, UtilPct: 50, MemUsed: 10 << 30, MemTotal: 20 << 30},
		},
	}
	h.Apply(client.Update{Host: "dup", Snapshot: snap})
	h.Apply(client.Update{Host: "dup", Snapshot: snap})

	if got := h.UtilHist[0].Len(); got != 1 {
		t.Errorf("UtilHist len after duplicate apply = %d, want 1", got)
	}
	if got := h.MemHist[0].Len(); got != 1 {
		t.Errorf("MemHist len after duplicate apply = %d, want 1", got)
	}

	// A genuinely new sample (different timestamp) must still push.
	snap2 := &model.Snapshot{
		Timestamp: time.Unix(1_700_000_005, 0),
		GPUs: []model.GPU{
			{Index: 0, UtilPct: 55, MemUsed: 11 << 30, MemTotal: 20 << 30},
		},
	}
	h.Apply(client.Update{Host: "dup", Snapshot: snap2})
	if got := h.UtilHist[0].Len(); got != 2 {
		t.Errorf("UtilHist len after new sample = %d, want 2", got)
	}
}

// TestHostStateApplyRingFamilies checks that Apply pushes into all five ring
// families the brief specifies: UtilHist/MemHist/LLMUtilHist/LLMMemHist per
// GPU, and GenTPSHist per server endpoint (skipping nil Metrics).
func TestHostStateApplyRingFamilies(t *testing.T) {
	h := SampleHostStates()[0] // "alpha": 2 GPUs, 2 servers (one metrics-off)
	for _, idx := range []int{0, 1} {
		for name, m := range map[string]map[int]*history.Ring[float64]{
			"UtilHist": h.UtilHist, "MemHist": h.MemHist,
			"LLMUtilHist": h.LLMUtilHist, "LLMMemHist": h.LLMMemHist,
		} {
			if r := m[idx]; r == nil || r.Len() == 0 {
				t.Errorf("%s[%d] must have a sample after Apply", name, idx)
			}
		}
	}
	if r, ok := h.GenTPSHist["http://127.0.0.1:8080"]; !ok || r.Len() == 0 {
		t.Error("GenTPSHist for a server with Metrics must have a sample")
	}
	if _, ok := h.GenTPSHist["http://127.0.0.1:8090"]; ok {
		t.Error("GenTPSHist must not gain an entry for a nil-Metrics server")
	}
}

// TestHostStateApplyErrKeepsStaleSnap covers the u.Err != nil branch: the
// last Snap must be retained for stale display, and ErrMsg recorded.
func TestHostStateApplyErrKeepsStaleSnap(t *testing.T) {
	h := NewHostState("flaky")
	snap := &model.Snapshot{
		Timestamp: time.Unix(1_700_000_000, 0),
		GPUs:      []model.GPU{{Index: 0, UtilPct: 10, MemTotal: 1}},
	}
	h.Apply(client.Update{Host: "flaky", Snapshot: snap})
	h.Apply(client.Update{Host: "flaky", Err: errors.New("boom")})

	if h.Snap == nil {
		t.Fatal("stale Snap must be retained after an error update")
	}
	if h.ErrMsg != "boom" {
		t.Errorf("ErrMsg = %q, want %q", h.ErrMsg, "boom")
	}
}
