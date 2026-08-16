package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/history"
	"github.com/mjacobs/llmtop/internal/model"
)

func TestRenderOverviewContent(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 100, 60, DefaultTheme())

	for _, want := range []string{
		"alpha",            // host name
		"GPU0",             // gpu panel title
		"qwen36-35b-mtp",   // server model
		"t/s",              // throughput
		"metrics disabled", // degraded server hint (grid tiers use panel.go's serverLine2 wording, not legacy serverLine's "(--metrics off)")
		"unreachable",      // dead host strip
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q\n%s", want, out)
		}
	}
}

func TestRenderOverviewWidth(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 80, 60, DefaultTheme())
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line width %d > 80: %q", w, line)
		}
	}
}

func TestRenderOverviewTwoColumns(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 140, 50, DefaultTheme())
	// alpha's two GPU panels share rows: some line carries both titles.
	sideBySide := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "GPU0") && strings.Contains(line, "GPU1") {
			sideBySide = true
		}
	}
	if !sideBySide {
		t.Errorf("width 140 must render GPU0 and GPU1 side by side\n%s", out)
	}
	// Odd trailing panel keeps half width: bravo's lone panel row
	// is panelW = (140-1)/2 = 69 columns, not full width.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "RTX 4080") && lipgloss.Width(line) > 69 {
			t.Errorf("lone panel must stay half width, line is %d cols: %q",
				lipgloss.Width(line), line)
		}
	}
}

// TestRenderOverviewOneRow covers the wide layout: every GPU of every host
// on one line, with each host's strip sitting above its own columns.
func TestRenderOverviewOneRow(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 220, 55, DefaultTheme())
	lines := strings.Split(out, "\n")

	// alpha's two GPUs and bravo's one all sit on the same panel row.
	sideBySide := false
	for _, line := range lines {
		if strings.Contains(line, "RTX 4090") && strings.Contains(line, "RTX 3090") &&
			strings.Contains(line, "RTX 4080") {
			sideBySide = true
		}
	}
	if !sideBySide {
		t.Errorf("width 220 must put all three GPUs on one row\n%s", out)
	}

	// Both hosts with GPUs name themselves on the same strip row, above the
	// panels; the GPU-less host stacks below them.
	stripRow, panelRow, deadRow := -1, -1, -1
	for i, line := range lines {
		if strings.Contains(line, "alpha") && strings.Contains(line, "bravo") {
			stripRow = i
		}
		if strings.Contains(line, "RTX 4090") && strings.Contains(line, "RTX 4080") {
			panelRow = i
		}
		if strings.Contains(line, "unreachable") {
			deadRow = i
		}
	}
	if stripRow < 0 || panelRow < 0 || deadRow < 0 {
		t.Fatalf("missing strip/panel/dead-host rows (%d/%d/%d)\n%s", stripRow, panelRow, deadRow, out)
	}
	if stripRow >= panelRow || panelRow >= deadRow {
		t.Errorf("want strip row above panels above the GPU-less host, got %d/%d/%d\n%s",
			stripRow, panelRow, deadRow, out)
	}

	for _, line := range lines {
		if w := lipgloss.Width(line); w > 220 {
			t.Errorf("line width %d > 220: %q", w, line)
		}
	}
}

func TestRenderOverviewSingleColumn(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 100, 63, DefaultTheme())
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "GPU0") && strings.Contains(line, "GPU1") {
			t.Errorf("width 100 must stack panels, found side-by-side line: %q", line)
		}
	}
}

func TestRenderOverviewFitsHeight(t *testing.T) {
	hosts := SampleHostStates()
	for _, size := range [][2]int{{100, 63}, {140, 40}, {100, 45}, {120, 200}, {220, 55}, {220, 200}, {400, 30}} {
		out := RenderOverview(hosts, 0, size[0], size[1], DefaultTheme())
		if got := len(strings.Split(out, "\n")); got > size[1] {
			t.Errorf("%dx%d: rendered %d lines > height", size[0], size[1], got)
		}
	}
}

func TestRenderOverviewCompactParity(t *testing.T) {
	hosts := SampleHostStates()
	// Height 20 forces COMPACT (see layout_test tier fixtures).
	got := RenderOverview(hosts, 0, 100, 20, DefaultTheme())
	want := renderCompact(hosts, 0, 100, DefaultTheme())
	if got != want {
		t.Errorf("COMPACT tier must match the legacy overview\ngot:\n%s\nwant:\n%s", got, want)
	}
	// Selecting alpha's second panel (flat index 1) still highlights alpha.
	got = RenderOverview(hosts, 1, 100, 20, DefaultTheme())
	if got != want {
		t.Errorf("panel 1 is still host 0 — compact highlight must not move")
	}
}

func TestRenderOverviewSelection(t *testing.T) {
	hosts := SampleHostStates()
	th := DefaultTheme()
	// Panel 2 is bravo's GPU (alpha owns 0 and 1).
	if hostOfPanel(buildHostRenders(hosts), 2) != 1 {
		t.Fatal("panel 2 must belong to host 1")
	}
	// Selected panel gets the lavender active border. The difference is
	// color-only, and `go test` runs with stdout not a tty, so lipgloss's
	// default renderer would silently strip it (Ascii profile) and make a
	// and b identical regardless of selection — force TrueColor for this
	// assertion so the comparison is meaningful.
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prevProfile)
	a := RenderOverview(hosts, 0, 100, 63, th)
	b := RenderOverview(hosts, 2, 100, 63, th)
	if a == b {
		t.Error("moving selection must change the rendered output")
	}
}

func TestRenderOverviewRobustness(t *testing.T) {
	th := DefaultTheme()
	empty := NewHostState("empty")
	empty.Apply(client.Update{Host: "empty", Snapshot: &model.Snapshot{
		Timestamp: time.Unix(1_700_000_001, 0)}}) // Snap with zero GPUs
	dead := NewHostState("dead")
	dead.ErrMsg = "boom"

	for _, hosts := range [][]*HostState{
		nil,
		{dead},
		{empty},
		SampleHostStates(),
	} {
		for _, size := range [][2]int{{20, 5}, {5, 3}, {200, 100}} {
			_ = RenderOverview(hosts, 99, size[0], size[1], th) // must not panic
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
