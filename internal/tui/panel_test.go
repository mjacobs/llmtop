package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mjacobs/llmtop/internal/model"
)

// mappingSnap builds a snapshot with two GPUs and four servers covering
// every mapping rule: PID on one GPU, PID spanning both GPUs (tensor
// split), a router, and a PID that matches no proc.
func mappingSnap() *model.Snapshot {
	return &model.Snapshot{
		GPUs: []model.GPU{{Index: 0}, {Index: 1}},
		Procs: []model.GPUProc{
			{PID: 100, Name: "llama-server", GPUIndex: 0, Class: model.ProcLLM},
			{PID: 200, Name: "llama-server", GPUIndex: 0, Class: model.ProcLLM},
			{PID: 200, Name: "llama-server", GPUIndex: 1, Class: model.ProcLLM},
			{PID: 300, Name: "chrome", GPUIndex: 1, Class: model.ProcGraphics},
		},
		Servers: []model.LLMServer{
			{Endpoint: "http://h:8081", PID: 100},                 // → GPU0
			{Endpoint: "http://h:8082", PID: 200},                 // → GPU0 and GPU1
			{Endpoint: "http://h:8080", Role: "router", PID: 100}, // router → host
			{Endpoint: "http://h:8083", PID: 999},                 // no proc → host
			{Endpoint: "http://h:8084"},                           // no PID → host
		},
	}
}

func TestMapServersToGPUs(t *testing.T) {
	byGPU, hostLevel := mapServersToGPUs(mappingSnap())

	if got := len(byGPU[0]); got != 2 {
		t.Errorf("GPU0 servers = %d, want 2 (:8081 and :8082)", got)
	}
	if got := len(byGPU[1]); got != 1 {
		t.Errorf("GPU1 servers = %d, want 1 (:8082 spans both GPUs)", got)
	}
	if byGPU[0][0].Endpoint != "http://h:8081" || byGPU[0][1].Endpoint != "http://h:8082" {
		t.Errorf("GPU0 order/content wrong: %v, %v", byGPU[0][0].Endpoint, byGPU[0][1].Endpoint)
	}
	if got := len(hostLevel); got != 3 {
		t.Fatalf("host-level servers = %d, want 3 (router, unmatched PID, no PID)", got)
	}
	// Nothing dropped: every server appears at least once.
	total := len(hostLevel)
	for _, s := range byGPU {
		total += len(s)
	}
	if total != 6 { // 5 servers, one counted twice (spans 2 GPUs)
		t.Errorf("total placements = %d, want 6", total)
	}
}

func TestMapServersToGPUsNilSnap(t *testing.T) {
	byGPU, hostLevel := mapServersToGPUs(nil)
	if len(byGPU) != 0 || hostLevel != nil {
		t.Errorf("nil snap must map to nothing, got %v / %v", byGPU, hostLevel)
	}
}

func TestBuildHostRenders(t *testing.T) {
	hosts := SampleHostStates()
	hrs := buildHostRenders(hosts)
	if len(hrs) != len(hosts) {
		t.Fatalf("hostRenders = %d, want %d", len(hrs), len(hosts))
	}
	// alpha: after testdata gains server PIDs, each server maps to its GPU.
	alpha := hrs[0]
	if len(alpha.byGPU[0]) != 1 || len(alpha.byGPU[1]) != 1 {
		t.Errorf("alpha mapping = GPU0:%d GPU1:%d, want 1 and 1",
			len(alpha.byGPU[0]), len(alpha.byGPU[1]))
	}
	if len(alpha.hostLevel) != 0 {
		t.Errorf("alpha host-level = %d, want 0", len(alpha.hostLevel))
	}
	// bravo: its server has no PID → host-level.
	if len(hrs[1].hostLevel) != 1 {
		t.Errorf("bravo host-level = %d, want 1", len(hrs[1].hostLevel))
	}
	// charlie: unreachable, no Snap → empty mapping, no panic.
	if len(hrs[2].byGPU) != 0 || len(hrs[2].hostLevel) != 0 {
		t.Errorf("dead host must have empty mapping")
	}
}

func TestPanelProcLinesFiltersAndDropsGPUColumn(t *testing.T) {
	h := SampleHostStates()[0] // 3 procs on GPU0, 2 on GPU1
	lines := panelProcLines(h.Snap.Procs, 0, DefaultTheme(), 80, 10)

	if len(lines) != 4 { // header + 3 GPU0 rows
		t.Fatalf("lines = %d, want 4:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if strings.Contains(lines[0], "GPU") {
		t.Errorf("per-GPU table must not have a GPU column: %q", lines[0])
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "kwin_wayland") { // GPU1-only proc
		t.Errorf("GPU1 proc leaked into GPU0 table:\n%s", joined)
	}
	if !strings.Contains(joined, "llama-server") || !strings.Contains(joined, "Xorg") {
		t.Errorf("GPU0 procs missing:\n%s", joined)
	}
	// VRAM-descending: llama-server (21G) first.
	if !strings.Contains(lines[1], "llama-server") {
		t.Errorf("rows not VRAM-descending: %q", lines[1])
	}
}

func TestSectionRuleGeometry(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(previousProfile)

	th := DefaultTheme()

	wide := sectionRule("LLM SERVERS", 32, th)
	if !strings.Contains(wide, "\x1b[") {
		t.Fatalf("wide rule missing ANSI styling: %q", wide)
	}
	if !strings.Contains(wide, "── LLM SERVERS ") {
		t.Fatalf("wide rule missing label: %q", wide)
	}
	if got := lipgloss.Width(wide); got != 32 {
		t.Fatalf("wide rule width = %d, want 32: %q", got, wide)
	}

	for _, width := range []int{8, 1, 0} {
		got := sectionRule("GPU PROCESSES", width, th)
		if visual := lipgloss.Width(got); visual != width {
			t.Errorf("width %d: visual width = %d: %q", width, visual, got)
		}
	}
}

func TestPanelProcLinesBudget(t *testing.T) {
	h := SampleHostStates()[0]
	th := DefaultTheme()

	if got := panelProcLines(h.Snap.Procs, 0, th, 80, 0); got != nil {
		t.Errorf("budget 0 must render nothing, got %d lines", len(got))
	}
	// Budget 1: "+N more" replaces the header (3 procs on GPU0).
	lines := panelProcLines(h.Snap.Procs, 0, th, 80, 1)
	if len(lines) != 1 || !strings.Contains(lines[0], "+3 more") {
		t.Errorf("budget-1 = %v, want single \"+3 more\" line", lines)
	}
	// Budget 3: header + 1 row + "+2 more" footer.
	lines = panelProcLines(h.Snap.Procs, 0, th, 80, 3)
	if len(lines) != 3 || !strings.Contains(lines[2], "+2 more") {
		t.Errorf("budget-3 = %v, want header+row+footer", lines)
	}
	// A GPU with no procs renders nothing regardless of budget.
	if got := panelProcLines(h.Snap.Procs, 7, th, 80, 5); got != nil {
		t.Errorf("no-proc GPU must render nothing, got %v", got)
	}
}

func TestRenderGPUPanelContent(t *testing.T) {
	hrs := buildHostRenders(SampleHostStates())
	out := renderGPUPanel(hrs[0], 0, 70, 24, 4, true, false, DefaultTheme())

	for _, want := range []string{
		"GPU0", "NVIDIA RTX 4090", "64°", "412W", // title
		"USAGE HISTORY",
		"util 72%", "(llm 65%)", // util label
		"mem 70%", // mem label (24106127360/32GiB ≈ 70%)
		"LLM SERVERS",
		"qwen36-35b-mtp", "t/s", // GPU0's mapped server
		"GPU PROCESSES",
		"PID", "llama-server", // proc table
	} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q\n%s", want, out)
		}
	}
	// Charts render braille cells.
	if !strings.ContainsFunc(out, func(r rune) bool { return r >= 0x2800 && r <= 0x28FF }) {
		t.Error("no braille chart cells rendered")
	}
	// Only GPU0 content: GPU1's server and procs must not leak in.
	for _, leak := range []string{"embeddinggemma", "kwin_wayland"} {
		if strings.Contains(out, leak) {
			t.Errorf("GPU1 content %q leaked into GPU0 panel\n%s", leak, out)
		}
	}
}

// TestRenderGPUPanelChartCaptions pins the caption placement: each chart's
// label sits on the line after its last chart row, flush against the panel's
// right edge (where the newest samples are drawn).
func TestRenderGPUPanelChartCaptions(t *testing.T) {
	const width, chartH = 70, 4
	hrs := buildHostRenders(SampleHostStates())
	lines := strings.Split(renderGPUPanel(hrs[0], 0, width, 24, chartH, true, false, DefaultTheme()), "\n")

	rule, util, mem := -1, -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "USAGE HISTORY"):
			rule = i
		case strings.Contains(line, "util 72%"):
			util = i
		case strings.Contains(line, "mem 70%"):
			mem = i
		}
	}
	if rule < 0 || util < 0 || mem < 0 {
		t.Fatalf("missing rule/util/mem rows (%d/%d/%d):\n%s", rule, util, mem, strings.Join(lines, "\n"))
	}
	// Captions follow their charts: rule, chartH rows, util caption, chartH
	// rows, mem caption.
	if util != rule+chartH+1 || mem != util+chartH+1 {
		t.Errorf("captions not below their charts: rule=%d util=%d mem=%d (chartH=%d)",
			rule, util, mem, chartH)
	}
	// Right-aligned: each caption ends at the panel's inner right edge, so
	// the full bordered line is exactly as wide as the panel.
	for _, i := range []int{util, mem} {
		if w := lipgloss.Width(lines[i]); w != width {
			t.Errorf("caption line %d is %d cols, want %d (right-aligned): %q", i, w, width, lines[i])
		}
	}
}

// TestRenderGPUPanelNarrowCaptionsKeepValues pins the caption clipping
// direction: when the panel is too narrow for a whole caption, the live
// values at its right edge (under the newest samples) survive, flush right;
// only the leading GPU name (redundant with the panel title) is dropped.
func TestRenderGPUPanelNarrowCaptionsKeepValues(t *testing.T) {
	const width, chartH = 30, 2 // inner 26 < both caption widths (41 and 30)
	hrs := buildHostRenders(SampleHostStates())
	lines := strings.Split(renderGPUPanel(hrs[0], 0, width, 16, chartH, false, false, DefaultTheme()), "\n")

	var utilLine, memLine string
	for _, line := range lines {
		switch {
		case strings.Contains(line, "util 72%"):
			utilLine = line
		case strings.Contains(line, "(llm 21.0G)"):
			memLine = line
		}
	}
	if utilLine == "" {
		t.Fatalf("narrow panel dropped the util values:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(utilLine, "(llm 65%)") {
		t.Errorf("util caption lost its llm value: %q", utilLine)
	}
	if strings.Contains(utilLine, "NVIDIA") {
		t.Errorf("util caption kept the GPU name over the values: %q", utilLine)
	}
	if memLine == "" {
		t.Fatalf("narrow panel dropped the mem values:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(memLine, "22.5/32.0G") {
		t.Errorf("mem caption lost its byte pair: %q", memLine)
	}
	// Flush right: the full bordered caption lines span exactly the panel
	// width, so the values end at the inner right edge.
	for _, line := range []string{utilLine, memLine} {
		if w := lipgloss.Width(line); w != width {
			t.Errorf("caption line is %d cols, want %d (right-aligned): %q", w, width, line)
		}
	}
}

func TestRenderGPUPanelSectionOrderWhenRegionsAreEmpty(t *testing.T) {
	h := NewHostState("idle")
	h.Snap = &model.Snapshot{GPUs: []model.GPU{{
		Index: 0,
		Name:  "NVIDIA GPU",
	}}}
	hr := buildHostRenders([]*HostState{h})[0]

	out := renderGPUPanel(hr, 0, 70, 10, 2, true, false, DefaultTheme())
	last := -1
	for _, label := range []string{
		"GPU0 NVIDIA GPU",
		"USAGE HISTORY",
		"LLM SERVERS",
		"GPU PROCESSES",
	} {
		at := strings.Index(out, label)
		if at < 0 {
			t.Fatalf("panel missing %q:\n%s", label, out)
		}
		if at <= last {
			t.Fatalf("%q is out of order:\n%s", label, out)
		}
		last = at
	}
}

func TestRenderGPUPanelGeometry(t *testing.T) {
	hrs := buildHostRenders(SampleHostStates())
	out := renderGPUPanel(hrs[0], 0, 70, 24, 4, true, false, DefaultTheme())

	lines := strings.Split(out, "\n")
	if len(lines) != 26 { // contentH + 2 border rows
		t.Errorf("panel height = %d lines, want 26", len(lines))
	}
	for _, line := range lines {
		if w := lipgloss.Width(line); w != 70 {
			t.Errorf("panel line width = %d, want exactly 70: %q", w, line)
		}
	}
}

func TestRenderGPUPanelSparklineLine(t *testing.T) {
	hrs := buildHostRenders(SampleHostStates())
	// GPU0's server has Metrics: sparkline line must appear.
	out := renderGPUPanel(hrs[0], 0, 70, 25, 4, true, false, DefaultTheme())
	hasSparkLine := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "gen t/s") {
			hasSparkLine = true
			if !strings.ContainsFunc(line, func(r rune) bool { return r >= '▁' && r <= '█' }) {
				t.Errorf("gen t/s line has no spark rune: %q", line)
			}
		}
	}
	if !hasSparkLine {
		t.Errorf("GPU0 panel (metrics-bearing server) must render a gen t/s sparkline line:\n%s", out)
	}

	// GPU1's server has Metrics: nil ("--metrics off") — no sparkline line.
	out = renderGPUPanel(hrs[0], 1, 70, 24, 4, true, false, DefaultTheme())
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "gen t/s") {
			t.Errorf("GPU1 panel (metrics-off server) must not render a sparkline line: %q", line)
		}
	}
}

func TestRenderGPUPanelChartsTierOmitsProcessSection(t *testing.T) {
	hrs := buildHostRenders(SampleHostStates())
	out := renderGPUPanel(hrs[0], 0, 70, 16, 4, false, false, DefaultTheme())

	for _, want := range []string{"USAGE HISTORY", "LLM SERVERS"} {
		if !strings.Contains(out, want) {
			t.Errorf("CHARTS panel missing %q:\n%s", want, out)
		}
	}
	for _, omitted := range []string{"GPU PROCESSES", "PID", "more"} {
		if strings.Contains(out, omitted) {
			t.Errorf("CHARTS panel must omit %q:\n%s", omitted, out)
		}
	}
}

func TestRenderHostStrip(t *testing.T) {
	hrs := buildHostRenders(SampleHostStates())
	th := DefaultTheme()

	// alpha: all servers map to GPUs → strip is just the title line.
	out := renderHostStrip(hrs[0], 100, 0, th)
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "driver 580.65.06") {
		t.Errorf("strip missing title content:\n%s", out)
	}
	if strings.Contains(out, "qwen36-35b-mtp") {
		t.Errorf("GPU-mapped server must not appear in the host strip:\n%s", out)
	}

	// bravo: PID-less server → 2-line host-level block.
	out = renderHostStrip(hrs[1], 100, 0, th)
	if !strings.Contains(out, "http://192.0.2.20:8080") || !strings.Contains(out, "loading") {
		t.Errorf("host-level server block missing:\n%s", out)
	}

	// charlie: unreachable → banner.
	out = renderHostStrip(hrs[2], 100, 0, th)
	if !strings.Contains(out, "unreachable") {
		t.Errorf("dead host banner missing:\n%s", out)
	}

	// Width clamp.
	for _, line := range strings.Split(renderHostStrip(hrs[1], 40, 0, th), "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("strip line width %d > 40: %q", w, line)
		}
	}

	// contentH pads a short strip so a row of strips stays rectangular.
	got := len(strings.Split(renderHostStrip(hrs[0], 100, 4, th), "\n"))
	if got != 6 {
		t.Errorf("padded strip = %d lines, want 6 (4 content + border)", got)
	}
}

func TestServerLine2EstimatedMarker(t *testing.T) {
	th := DefaultTheme()
	measured := &model.LLMServer{
		Metrics: &model.LLMMetrics{PromptTPS: 10.5, PredictedTPS: 142.34, KVCacheUsage: -1},
	}
	if got := serverLine2(measured, th); !strings.Contains(got, "gen 142.3 t/s") {
		t.Errorf("measured rate: got %q, want it to contain %q", got, "gen 142.3 t/s")
	}
	estimated := &model.LLMServer{
		Metrics: &model.LLMMetrics{PromptTPS: 10.5, PredictedTPS: 142.34, KVCacheUsage: -1, PredictedTPSEst: true},
	}
	got := serverLine2(estimated, th)
	if !strings.Contains(got, "gen ~142 t/s") {
		t.Errorf("estimated rate: got %q, want it to contain %q", got, "gen ~142 t/s")
	}
	if strings.Contains(got, "142.3") || strings.Contains(got, "142.34") {
		t.Errorf("estimated rate must not render decimals: %q", got)
	}
}

// serverBlockMatrix covers the three server shapes serverBlockLines /
// serverBlockLineCount must agree on: metrics-bearing, metrics-off, router.
func serverBlockMatrix() []*model.LLMServer {
	return []*model.LLMServer{
		{
			Endpoint: "http://h:8080", Status: model.StatusOK, Model: "m",
			Metrics: &model.LLMMetrics{PromptTPS: 1, PredictedTPS: 2, KVCacheUsage: -1},
		},
		{
			Endpoint: "http://h:8081", Status: model.StatusOK, Model: "m2",
			Metrics: nil,
		},
		{
			Endpoint: "http://h:8082", Role: "router",
			Metrics: &model.LLMMetrics{PromptTPS: 1, PredictedTPS: 2, KVCacheUsage: -1},
		},
	}
}

func TestServerBlockLinesMatchesCount(t *testing.T) {
	h := NewHostState("x")
	th := DefaultTheme()
	for _, srv := range serverBlockMatrix() {
		got := len(serverBlockLines(h, srv, th, 70))
		want := serverBlockLineCount(srv)
		if got != want {
			t.Errorf("endpoint %s: serverBlockLines returned %d lines, serverBlockLineCount says %d",
				srv.Endpoint, got, want)
		}
	}
}

func TestServerBlockLinesSparkline(t *testing.T) {
	h := NewHostState("x")
	th := DefaultTheme()
	metricsSrv := serverBlockMatrix()[0]
	// Push a couple of samples so the sparkline has non-blank content.
	ringPushStr(h.GenTPSHist, metricsSrv.Endpoint, 10)
	ringPushStr(h.GenTPSHist, metricsSrv.Endpoint, 90)

	lines := serverBlockLines(h, metricsSrv, th, 70)
	if len(lines) != 3 {
		t.Fatalf("metrics-bearing server: got %d lines, want 3:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	sparkLine := lines[2]
	if !strings.Contains(sparkLine, "gen t/s") {
		t.Errorf("sparkline line missing suffix: %q", sparkLine)
	}
	hasSpark := strings.ContainsFunc(sparkLine, func(r rune) bool { return r >= '▁' && r <= '█' })
	if !hasSpark {
		t.Errorf("sparkline line has no spark rune: %q", sparkLine)
	}

	metricsOffSrv := serverBlockMatrix()[1]
	lines = serverBlockLines(h, metricsOffSrv, th, 70)
	if len(lines) != 2 {
		t.Errorf("metrics-off server: got %d lines, want 2", len(lines))
	}
	for _, l := range lines {
		if strings.Contains(l, "gen t/s") {
			t.Errorf("metrics-off server must not have a sparkline line: %q", l)
		}
	}

	routerSrv := serverBlockMatrix()[2]
	lines = serverBlockLines(h, routerSrv, th, 70)
	if len(lines) != 2 {
		t.Errorf("router server: got %d lines, want 2", len(lines))
	}
}

func TestHostStripContentLinesMatchesRender(t *testing.T) {
	for i, hr := range buildHostRenders(SampleHostStates()) {
		got := len(strings.Split(renderHostStrip(hr, 100, 0, DefaultTheme()), "\n"))
		want := hostStripContentLines(hr) + 2 // border top+bottom
		if got != want {
			t.Errorf("host %d: rendered %d lines, accounting says %d", i, got, want)
		}
	}
}

func TestServerLine2Sleeping(t *testing.T) {
	th := DefaultTheme()
	srv := &model.LLMServer{Kind: "llama.cpp", Model: "demo-model", Status: model.StatusSleeping}
	got := serverLine2(srv, th)
	if !strings.Contains(got, "sleeping") {
		t.Fatalf("serverLine2 = %q, want sleeping notice", got)
	}
	if strings.Contains(got, "metrics disabled") {
		t.Fatalf("serverLine2 = %q, must not claim metrics disabled while sleeping", got)
	}
}
