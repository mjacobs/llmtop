package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

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
		"util 72%", "(llm 65%)", // util label
		"mem 70%",               // mem label (24106127360/32GiB ≈ 70%)
		"qwen36-35b-mtp", "t/s", // GPU0's mapped server
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

func TestRenderGPUPanelChartsTierOmitsProcs(t *testing.T) {
	hrs := buildHostRenders(SampleHostStates())
	out := renderGPUPanel(hrs[0], 0, 70, 16, 4, false, false, DefaultTheme())
	if strings.Contains(out, "PID") || strings.Contains(out, "more") {
		t.Errorf("showProcs=false must omit the process table\n%s", out)
	}
}

func TestRenderHostStrip(t *testing.T) {
	hrs := buildHostRenders(SampleHostStates())
	th := DefaultTheme()

	// alpha: all servers map to GPUs → strip is just the title line.
	out := renderHostStrip(hrs[0], 100, th)
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "driver 580.65.06") {
		t.Errorf("strip missing title content:\n%s", out)
	}
	if strings.Contains(out, "qwen36-35b-mtp") {
		t.Errorf("GPU-mapped server must not appear in the host strip:\n%s", out)
	}

	// bravo: PID-less server → 2-line host-level block.
	out = renderHostStrip(hrs[1], 100, th)
	if !strings.Contains(out, "http://192.0.2.20:8080") || !strings.Contains(out, "loading") {
		t.Errorf("host-level server block missing:\n%s", out)
	}

	// charlie: unreachable → banner.
	out = renderHostStrip(hrs[2], 100, th)
	if !strings.Contains(out, "unreachable") {
		t.Errorf("dead host banner missing:\n%s", out)
	}

	// Width clamp.
	for _, line := range strings.Split(renderHostStrip(hrs[1], 40, th), "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("strip line width %d > 40: %q", w, line)
		}
	}
}

func TestHostStripContentLinesMatchesRender(t *testing.T) {
	for i, hr := range buildHostRenders(SampleHostStates()) {
		got := len(strings.Split(renderHostStrip(hr, 100, DefaultTheme()), "\n"))
		want := hostStripContentLines(hr) + 2 // border top+bottom
		if got != want {
			t.Errorf("host %d: rendered %d lines, accounting says %d", i, got, want)
		}
	}
}
