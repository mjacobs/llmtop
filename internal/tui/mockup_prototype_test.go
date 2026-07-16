package tui

// THROWAWAY PROTOTYPE — delete after design sign-off on kata ec6y.
//
// Renders the proposed "larger charts" overview (CHARTS tier with adaptive,
// capped chart height + breathing room, falling back to the current COMPACT
// one-line rows when tight) at several terminal sizes, using the REAL chart
// primitives (renderGraph/BrailleGraph/Sparkline). What prints here is what
// the real view would look like — this is the TUI equivalent of a mockup.
//
// Run: go test ./internal/tui/ -run TestMockupOverview -v

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mjacobs/llmtop/internal/model"
)

const (
	protoCap    = 8 // max chart cell-height (≈32 levels)
	protoFloor  = 2 // below this, drop to COMPACT rows
	protoSparkW = 20
)

func protoClampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func protoHost(name string, gpus []model.GPU, servers []model.LLMServer) *HostState {
	h := NewHostState(name)
	h.Snap = &model.Snapshot{
		Host:      model.HostInfo{Name: name, Hostname: name, DriverVersion: "580.65"},
		GPUs:      gpus,
		Servers:   servers,
		Timestamp: time.Now(),
	}
	for n := 0; n < 140; n++ {
		t := float64(n)
		for _, g := range gpus {
			util := protoClampPct(55 + 32*math.Sin(t/9+float64(g.Index)))
			ringPush(h.UtilHist, g.Index, util)
			ringPush(h.LLMUtilHist, g.Index, util*0.82)
			mem := protoClampPct(58 + 22*math.Sin(t/17+float64(g.Index)*1.7))
			ringPush(h.MemHist, g.Index, mem)
			ringPush(h.LLMMemHist, g.Index, mem*0.88)
		}
		for _, s := range servers {
			ringPushStr(h.GenTPSHist, s.Endpoint, 40+18*math.Sin(t/7))
		}
	}
	return h
}

func protoGenMax(v []float64) float64 {
	m := 1.0
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m * 1.15
}

// protoChartsLines is the draft CHARTS-tier renderer.
func protoChartsLines(h *HostState, inner, cells int, th Theme) []string {
	lines := []string{titleLine(h, th, inner)}
	if h.Snap == nil {
		return lines
	}
	for gi := range h.Snap.GPUs {
		gpu := h.Snap.GPUs[gi]
		lines = append(lines, truncateLine(utilLabel(gpu, th), inner))
		uc := renderGraph(ringSlice(h.UtilHist, gpu.Index), ringSlice(h.LLMUtilHist, gpu.Index), inner, cells, th.Util, th.LLM)
		lines = append(lines, strings.Split(uc, "\n")...)
		lines = append(lines, truncateLine(memLabel(gpu, th), inner))
		mc := renderGraph(ringSlice(h.MemHist, gpu.Index), ringSlice(h.LLMMemHist, gpu.Index), inner, cells, th.Mem, th.LLM)
		lines = append(lines, strings.Split(mc, "\n")...)
	}
	for si := range h.Snap.Servers {
		srv := &h.Snap.Servers[si]
		lines = append(lines, truncateLine(serverLine(srv, th, inner), inner))
		var gts []float64
		if r := h.GenTPSHist[srv.Endpoint]; r != nil {
			gts = r.Slice()
		}
		spark := th.LLM.Render(Sparkline(gts, protoGenMax(gts), protoSparkW))
		lines = append(lines, truncateLine("  "+spark+th.Dim.Render(" gen t/s"), inner))
	}
	return lines
}

// protoCompactLines mirrors today's RenderOverview content (one row per GPU).
func protoCompactLines(h *HostState, inner int, th Theme) []string {
	lines := []string{titleLine(h, th, inner)}
	if h.Snap == nil {
		return lines
	}
	for gi := range h.Snap.GPUs {
		lines = append(lines, gpuLine(h, gi, th, inner))
	}
	for si := range h.Snap.Servers {
		lines = append(lines, serverLine(&h.Snap.Servers[si], th, inner))
	}
	return lines
}

func protoCounts(h *HostState) (nGPU, nSrv, nErr int) {
	if h.Snap != nil {
		return len(h.Snap.GPUs), len(h.Snap.Servers), len(h.Snap.Errors)
	}
	return 0, 0, 0
}

// protoSolve picks ONE shared chart cell-height across every GPU on every host
// (fair per-GPU, not per-host), or signals COMPACT when even floor won't fit.
func protoSolve(hosts []*HostState, body int) (cells int, compact bool) {
	totalFixed, totalCharts := 0, 0
	for _, h := range hosts {
		nGPU, nSrv, nErr := protoCounts(h)
		totalFixed += 2 + 1 + 2*nSrv + nErr // border + title + servers + errs
		totalCharts += 2 * nGPU             // util + mem chart per GPU
	}
	if totalCharts == 0 {
		return 0, false
	}
	cells = (body - totalFixed) / totalCharts
	if cells > protoCap {
		cells = protoCap
	}
	if cells < protoFloor {
		return 0, true
	}
	return cells, false
}

func protoHostPanel(h *HostState, width, cells int, compact bool, th Theme) string {
	inner := width - 4
	var lines []string
	if compact {
		lines = protoCompactLines(h, inner, th)
	} else {
		lines = protoChartsLines(h, inner, cells, th)
	}
	return th.Border.Width(width-2).Padding(0, 1).Render(strings.Join(lines, "\n"))
}

func protoOverview(hosts []*HostState, width, body int, th Theme) string {
	cells, compact := protoSolve(hosts, body)
	var panels []string
	for _, h := range hosts {
		panels = append(panels, protoHostPanel(h, width, cells, compact, th))
	}
	// Breathing room: spread leftover height as gaps between panels.
	gap := ""
	total := 0
	for _, p := range panels {
		total += lipgloss.Height(p)
	}
	if leftover := body - total; leftover > 0 && len(panels) > 1 {
		gap = strings.Repeat("\n", 1+leftover/(len(panels)-1))
	}
	out := strings.Join(panels, "\n"+gap)
	tier := fmt.Sprintf("CHARTS · h=%d cells (shared)", cells)
	if compact {
		tier = "COMPACT · 1-line rows"
	}
	return out + "\n" + th.Dim.Render("   ["+tier+"]")
}

func TestMockupOverview(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor) // force ANSI color even when piped
	th := DefaultTheme()
	gpus := []model.GPU{
		{Index: 0, Name: "NVIDIA GeForce RTX 5090", UtilPct: 72, LLMUtilPct: 65,
			MemUsed: 24 << 30, MemTotal: 32 << 30, LLMMemBytes: 20 << 30, TempC: 61, PowerW: 402},
		{Index: 1, Name: "NVIDIA RTX 4000 Ada", UtilPct: 18, LLMUtilPct: 12,
			MemUsed: 9 << 30, MemTotal: 20 << 30, LLMMemBytes: 7 << 30, TempC: 44, PowerW: 71},
	}
	servers := []model.LLMServer{
		{Kind: "llama.cpp", Endpoint: "http://localhost:8080", Status: model.StatusOK,
			Model: "llama-3.1-70b-instruct", CtxSize: 8192, SlotsTotal: 2,
			Metrics: &model.LLMMetrics{PromptTPS: 310, PredictedTPS: 42.3, KVCacheUsage: 0.38, RequestsProcessing: 1}},
		{Kind: "llama.cpp", Endpoint: "http://localhost:8081", Status: model.StatusOK,
			Model: "qwen2.5-coder-7b", CtxSize: 16384, SlotsTotal: 4,
			Metrics: &model.LLMMetrics{PromptTPS: 880, PredictedTPS: 110.5, KVCacheUsage: 0.12, RequestsProcessing: 0}},
	}
	host := protoHost("alpha", gpus, servers)
	host2 := protoHost("workstation", gpus[:1], servers[:1])

	width := 66
	scenes := []struct {
		name  string
		hosts []*HostState
		body  int
	}{
		{"NORMAL terminal (body=40) — adaptive charts, one host", []*HostState{host}, 40},
		{"TWO HOSTS (body=44) — height divided across panels", []*HostState{host, host2}, 44},
		{"SHORT terminal (body=12) — COMPACT one-line fallback", []*HostState{host}, 12},
	}
	for _, s := range scenes {
		fmt.Printf("\n========== %s  [width=%d] ==========\n", s.name, width)
		fmt.Println(protoOverview(s.hosts, width, s.body, th))
	}
}
