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
	out := RenderOverview(hosts, 0, 100, 60, LayoutPref{}, DefaultTheme())

	for _, want := range []string{
		"alpha",            // host name
		"GPU0",             // gpu panel title
		"qwen36-35b-mtp",   // server model
		"t/s",              // throughput
		"metrics disabled", // degraded server hint (grid tiers use panel.go's serverLine2 wording, not the compact tier's "(--metrics off)")
		"unreachable",      // dead host strip
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q\n%s", want, out)
		}
	}
}

// TestServerLineStatusHints pins the compact-tier row for each nil-Metrics
// status: only a reachable, resident server gets the "--metrics off" hint;
// an unreachable or loading one already carries a status tag that explains
// the missing stats.
func TestServerLineStatusHints(t *testing.T) {
	th := DefaultTheme()
	for _, tc := range []struct {
		name    string
		status  string
		want    string
		notWant string
	}{
		{name: "ok", status: model.StatusOK, want: "(--metrics off)"},
		{name: "unreachable", status: model.StatusUnreachable, want: "unreachable", notWant: "--metrics"},
		{name: "loading", status: model.StatusLoading, want: "loading", notWant: "--metrics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &model.LLMServer{Kind: "llama.cpp", Endpoint: "http://h:8080", Status: tc.status}
			got := serverLine(srv, th, 100)
			if !strings.Contains(got, tc.want) {
				t.Errorf("serverLine = %q, want it to contain %q", got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("serverLine = %q, must not contain %q", got, tc.notWant)
			}
		})
	}
}

// The compact row's no-metrics hint follows the engine: only llama.cpp has
// a --metrics flag, and NInfer serves no /metrics at all, so it shows what
// we do know instead.
func TestServerLineMetricsHintIsEngineAware(t *testing.T) {
	th := DefaultTheme()
	for _, tc := range []struct {
		name    string
		srv     model.LLMServer
		want    string
		notWant string
	}{
		{name: "llama.cpp", srv: model.LLMServer{Kind: "llama.cpp"}, want: "(--metrics off)"},
		{name: "vllm", srv: model.LLMServer{Kind: "vllm"}, want: "(stats off)", notWant: "--metrics"},
		{
			name:    "ninfer with facts",
			srv:     model.LLMServer{Kind: "ninfer", CtxSize: 8192, SlotsTotal: 4},
			want:    "ctx 8192 4 slots",
			notWant: "--metrics",
		},
		{name: "ninfer bare", srv: model.LLMServer{Kind: "ninfer"}, want: "(no /metrics)", notWant: "--metrics off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.srv
			srv.Endpoint = "http://h:8080"
			srv.Status = model.StatusOK
			got := serverLine(&srv, th, 100)
			if !strings.Contains(got, tc.want) {
				t.Errorf("serverLine = %q, want it to contain %q", got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("serverLine = %q, must not contain %q", got, tc.notWant)
			}
		})
	}
}

// TestServerLineMetricsErr pins the compact-tier equivalent of
// TestServerLine2MetricsErr: a non-empty MetricsErr gets its own "(metrics:
// ...)" tag instead of the "(--metrics off)" hint, since /metrics answering
// something other than 200/404/501 does not mean metrics are disabled.
func TestServerLineMetricsErr(t *testing.T) {
	th := DefaultTheme()
	srv := &model.LLMServer{Kind: "llama.cpp", Endpoint: "http://h:8080", Status: model.StatusOK, MetricsErr: "http 401"}
	got := serverLine(srv, th, 100)
	if !strings.Contains(got, "(metrics: http 401)") {
		t.Fatalf("serverLine = %q, want it to name the metrics failure", got)
	}
	if strings.Contains(got, "--metrics off") {
		t.Fatalf("serverLine = %q, must not say --metrics off when MetricsErr is set", got)
	}
}

func TestRenderOverviewWidth(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 80, 60, LayoutPref{}, DefaultTheme())
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line width %d > 80: %q", w, line)
		}
	}
}

func TestRenderOverviewTwoColumns(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 140, 50, LayoutPref{}, DefaultTheme())
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
	out := RenderOverview(hosts, 0, 220, 55, LayoutPref{}, DefaultTheme())
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
	out := RenderOverview(hosts, 0, 100, 63, LayoutPref{}, DefaultTheme())
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "GPU0") && strings.Contains(line, "GPU1") {
			t.Errorf("width 100 must stack panels, found side-by-side line: %q", line)
		}
	}
}

func TestRenderOverviewFitsHeight(t *testing.T) {
	hosts := SampleHostStates()
	for _, size := range [][2]int{{100, 63}, {140, 40}, {100, 45}, {120, 200}, {220, 55}, {220, 200}, {400, 30}} {
		out := RenderOverview(hosts, 0, size[0], size[1], LayoutPref{}, DefaultTheme())
		if got := len(strings.Split(out, "\n")); got > size[1] {
			t.Errorf("%dx%d: rendered %d lines > height", size[0], size[1], got)
		}
	}
}

func TestRenderOverviewCompactParity(t *testing.T) {
	hosts := SampleHostStates()
	// Height 20 forces COMPACT (see layout_test tier fixtures).
	got := RenderOverview(hosts, 0, 100, 20, LayoutPref{}, DefaultTheme())
	want := renderCompact(buildHostRenders(hosts), 0, 100, DefaultTheme())
	if got != want {
		t.Errorf("COMPACT tier must match renderCompact\ngot:\n%s\nwant:\n%s", got, want)
	}
	// Selecting alpha's second panel (flat index 1) still highlights alpha.
	got = RenderOverview(hosts, 1, 100, 20, LayoutPref{}, DefaultTheme())
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
	a := RenderOverview(hosts, 0, 100, 63, LayoutPref{}, th)
	b := RenderOverview(hosts, 2, 100, 63, LayoutPref{}, th)
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
			_ = RenderOverview(hosts, 99, size[0], size[1], LayoutPref{}, th) // must not panic
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
// families: UtilHist/MemHist/LLMUtilHist/LLMMemHist per GPU, and GenTPSHist
// per server endpoint (skipping nil Metrics).
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

// TestRenderOverviewZeroGPUHostFitsHeight pins that a host with no GPU
// panels but many host-level servers and errors — a driverless machine
// listing its llama.cpp servers — never renders past the height budget.
func TestRenderOverviewZeroGPUHostFitsHeight(t *testing.T) {
	h := NewHostState("local")
	var servers []model.LLMServer
	for i := 0; i < 6; i++ {
		servers = append(servers, model.LLMServer{
			Kind: "llama.cpp", Endpoint: "http://127.0.0.1:808" + string(rune('0'+i)),
			Status: model.StatusOK, Model: "m",
		})
	}
	h.Apply(client.Update{Host: "local", Snapshot: &model.Snapshot{
		Host: model.HostInfo{Name: "local"}, Timestamp: time.Unix(1_700_000_002, 0),
		Servers: servers,
		Errors:  []string{"gpu sample: nvml init: no NVIDIA driver found", "proc scan: boom"},
	}})
	for _, size := range [][2]int{{100, 8}, {100, 12}, {80, 5}, {200, 6}, {120, 40}} {
		out := RenderOverview([]*HostState{h}, 0, size[0], size[1], LayoutPref{}, DefaultTheme())
		if got := len(strings.Split(out, "\n")); got > size[1] {
			t.Errorf("%dx%d: rendered %d lines > height", size[0], size[1], got)
		}
	}
	// No body budget at all (terminal shorter than header+footer): no body.
	for _, height := range []int{0, -3} {
		if out := RenderOverview([]*HostState{h}, 0, 100, height, LayoutPref{}, DefaultTheme()); out != "" {
			t.Errorf("height %d: want empty body, got %d lines", height, len(strings.Split(out, "\n")))
		}
	}
}

// quadHost is a single host with four GPUs — enough panels for a column
// target above the built-in two-up maximum to show up in the render.
func quadHost() []*HostState {
	h := NewHostState("alpha")
	gpus := make([]model.GPU, 4)
	for i := range gpus {
		gpus[i] = model.GPU{
			Index: i, UUID: "GPU-" + string(rune('0'+i)), Name: "NVIDIA RTX 4090",
			UtilPct: 10 * i, MemUsed: uint64(i+1) << 30, MemTotal: 24 << 30,
			TempC: 40 + i, PowerW: 100 + i,
		}
	}
	h.Apply(client.Update{Host: "alpha", Snapshot: &model.Snapshot{
		Host:      model.HostInfo{Name: "alpha", Hostname: "alpha", DriverVersion: "580.65.06"},
		Timestamp: sampleTS,
		GPUs:      gpus,
	}})
	return []*HostState{h}
}

// TestRenderOverviewColumnsMode: a column target above two is honoured by
// the renderer, not just the solver.
func TestRenderOverviewColumnsMode(t *testing.T) {
	hosts := quadHost()
	pref := LayoutPref{Mode: LayoutColumns, Columns: 3}
	out := RenderOverview(hosts, 0, 220, 60, pref, DefaultTheme())

	threeUp := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "GPU0") && strings.Contains(line, "GPU1") && strings.Contains(line, "GPU2") {
			threeUp = true
		}
		if w := lipgloss.Width(line); w > 220 {
			t.Errorf("line width %d > 220: %q", w, line)
		}
	}
	if !threeUp {
		t.Errorf("columns 3 must put GPU0-2 on one row\n%s", out)
	}

	// The default heuristic at the same size stops at two columns.
	auto := RenderOverview(hosts, 0, 220, 60, LayoutPref{}, DefaultTheme())
	for _, line := range strings.Split(auto, "\n") {
		if strings.Contains(line, "GPU0") && strings.Contains(line, "GPU2") {
			t.Errorf("auto must not render three columns: %q", line)
		}
	}
}

// TestRenderOverviewStackByHostMode: stack-by-host refuses the one-row
// arrangement even at a width where the heuristic would take it.
func TestRenderOverviewStackByHostMode(t *testing.T) {
	hosts := SampleHostStates()
	out := RenderOverview(hosts, 0, 220, 55, LayoutPref{Mode: LayoutStackByHost}, DefaultTheme())
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "alpha") && strings.Contains(line, "bravo") {
			t.Errorf("stack-by-host must not share a strip row: %q\n%s", line, out)
		}
	}
	// Auto at the same size does take the one-row arrangement.
	auto := RenderOverview(hosts, 0, 220, 55, LayoutPref{}, DefaultTheme())
	if auto == out {
		t.Error("stack-by-host must differ from the auto layout at 220x55")
	}
}

// TestRenderOverviewSlotOrder: the slot list drives the order panels appear
// in, across hosts as well as within one.
func TestRenderOverviewSlotOrder(t *testing.T) {
	hosts := SampleHostStates()
	pref := LayoutPref{Slots: []LayoutSlot{
		{Host: "bravo", GPU: 0},
		{Host: "alpha", GPU: 1},
		{Host: "alpha", GPU: 0},
	}}
	out := RenderOverview(hosts, 0, 220, 55, pref, DefaultTheme())

	var panelRow string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "RTX 4080") && strings.Contains(line, "RTX 4090") {
			panelRow = line
		}
	}
	if panelRow == "" {
		t.Fatalf("no row holding every GPU panel\n%s", out)
	}
	bravo := strings.Index(panelRow, "RTX 4080")
	alpha1 := strings.Index(panelRow, "RTX 3090")
	alpha0 := strings.Index(panelRow, "RTX 4090")
	if bravo >= alpha1 || alpha1 >= alpha0 {
		t.Errorf("panel order = bravo %d, alpha GPU1 %d, alpha GPU0 %d; want that order\n%s",
			bravo, alpha1, alpha0, panelRow)
	}
}

// TestRenderOverviewModesFitTheTerminal: whatever the preference asks for,
// the frame never overruns the height budget or the terminal width, and no
// combination of mode, slot list, and size panics.
func TestRenderOverviewModesFitTheTerminal(t *testing.T) {
	th := DefaultTheme()
	slots := []LayoutSlot{{Host: "bravo", GPU: 0}, {Host: "alpha", GPU: 1}, {Host: "alpha", GPU: 0}}
	prefs := []LayoutPref{
		{},
		{Mode: LayoutOneRow},
		{Mode: LayoutStackByHost},
		{Mode: LayoutColumns, Columns: 1},
		{Mode: LayoutColumns, Columns: 4},
		{Mode: LayoutOneRow, Slots: slots},
		{Mode: LayoutColumns, Columns: 3, Slots: slots},
	}
	for _, hosts := range [][]*HostState{nil, SampleHostStates(), quadHost()} {
		for _, pref := range prefs {
			for _, size := range [][2]int{{5, 3}, {80, 24}, {100, 45}, {140, 50}, {220, 55}, {400, 200}} {
				out := RenderOverview(hosts, 1, size[0], size[1], pref, th)
				if out == "" {
					continue
				}
				lines := strings.Split(out, "\n")
				if len(lines) > size[1] {
					t.Errorf("%s at %dx%d: %d lines > height", pref.Label(), size[0], size[1], len(lines))
				}
				for _, line := range lines {
					if w := lipgloss.Width(line); w > size[0] {
						t.Errorf("%s at %dx%d: line width %d: %q", pref.Label(), size[0], size[1], w, line)
					}
				}
			}
		}
	}
}
