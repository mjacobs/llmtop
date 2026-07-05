package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mjacobs/llmtop/internal/model"
)

// chartHeight is the row height (in cells, each 4 braille dots tall) of the
// util/mem drill-down charts.
const chartHeight = 6

// Process-table column widths (visual columns), derived from the header
// literal `PID      NAME                 CLASS     GPU  VRAM     SM%`.
const (
	colPID   = 9
	colName  = 21
	colClass = 10
	colGPU   = 5
	colVRAM  = 9
)

// RenderDrilldown renders a single bordered panel with a per-GPU util/mem
// chart pair (split into total vs. LLM share), one block per LLM server, and
// a process table sorted by VRAM descending. gpuSel is clamped into
// [0, len(GPUs)-1]; a host with no Snap renders the same error panel style
// RenderOverview uses for an unreachable/never-seen host.
func RenderDrilldown(h *HostState, gpuSel, width, height int, th Theme) string {
	inner := width - 4 // border (1) + padding (1) each side
	if inner < 1 {
		inner = 1
	}
	contentHeight := height - 2 // border top+bottom (no vertical padding)
	if contentHeight < 1 {
		contentHeight = 1
	}

	if h.Snap == nil {
		lines := []string{titleLine(h, th, inner)}
		if h.ErrMsg != "" {
			lines = append(lines, truncateLine(th.Err.Render("unreachable: "+h.ErrMsg), inner))
		}
		return th.BorderActive.Width(width-2).Height(contentHeight).Padding(0, 1).
			Render(strings.Join(lines, "\n"))
	}

	gpus := h.Snap.GPUs
	n := len(gpus)
	if gpuSel < 0 {
		gpuSel = 0
	} else if n > 0 && gpuSel > n-1 {
		gpuSel = n - 1
	}

	var lines []string
	lines = append(lines, truncateLine(drilldownTitle(h, gpuSel, n, th), inner))

	if n > 0 {
		gpu := gpus[gpuSel]
		lines = append(lines, truncateLine(utilLabel(gpu, th), inner))
		utilChart := renderGraph(
			ringSlice(h.UtilHist, gpu.Index), ringSlice(h.LLMUtilHist, gpu.Index),
			inner, chartHeight, th.Util, th.LLM)
		lines = append(lines, strings.Split(utilChart, "\n")...)

		lines = append(lines, truncateLine(memLabel(gpu, th), inner))
		memChart := renderGraph(
			ringSlice(h.MemHist, gpu.Index), ringSlice(h.LLMMemHist, gpu.Index),
			inner, chartHeight, th.Mem, th.LLM)
		lines = append(lines, strings.Split(memChart, "\n")...)
	}

	for si := range h.Snap.Servers {
		srv := &h.Snap.Servers[si]
		lines = append(lines, truncateLine(serverLine1(srv, th), inner))
		lines = append(lines, truncateLine(serverLine2(srv, th), inner))
	}

	lines = append(lines, procTableLines(h.Snap.Procs, th, inner, contentHeight-len(lines))...)

	return th.BorderActive.Width(width-2).Height(contentHeight).Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

// renderGraph renders a two-series braille area chart, coloring "llm"-class
// cells with accent and "total"-class cells with base.
func renderGraph(total, llm []float64, chartW, chartH int, base, accent lipgloss.Style) string {
	rows := BrailleGraph(total, llm, 100, chartW, chartH)
	var b strings.Builder
	for y, row := range rows {
		if y > 0 {
			b.WriteByte('\n')
		}
		for _, cell := range row {
			switch cell[1] {
			case "llm":
				b.WriteString(accent.Render(cell[0]))
			case "total":
				b.WriteString(base.Render(cell[0]))
			default:
				b.WriteString(cell[0])
			}
		}
	}
	return b.String()
}

// drilldownTitle renders the host name, hostname/driver detail, and a 1-based
// "gpu N/total" indicator.
func drilldownTitle(h *HostState, gpuSel, n int, th Theme) string {
	driver := "?"
	if h.Snap.Host.DriverVersion != "" {
		driver = h.Snap.Host.DriverVersion
	}
	hostname := h.Snap.Host.Hostname
	if hostname == "" {
		hostname = "?"
	}
	gpuIndicator := ""
	if n > 0 {
		gpuIndicator = fmt.Sprintf(" · gpu %d/%d", gpuSel+1, n)
	}
	dim := fmt.Sprintf(" · %s · driver %s%s", hostname, driver, gpuIndicator)
	return th.Title.Render(h.Name) + th.Dim.Render(dim)
}

// utilLabel renders the util-chart label: `GPU0 NVIDIA RTX 4090 — util 72% (llm 65%)`.
func utilLabel(gpu model.GPU, th Theme) string {
	return fmt.Sprintf("GPU%d %s — ", gpu.Index, gpu.Name) +
		th.Util.Render(fmt.Sprintf("util %d%%", gpu.UtilPct)) + " " +
		th.LLM.Render(fmt.Sprintf("(llm %d%%)", gpu.LLMUtilPct))
}

// memLabel renders the mem-chart label: `mem 61% 22.5/32.0G (llm 20.0G)`.
func memLabel(gpu model.GPU, th Theme) string {
	memPct := int(pct(gpu.MemUsed, gpu.MemTotal) + 0.5)
	return th.Mem.Render(fmt.Sprintf("mem %d%% %s", memPct, FmtMemPair(gpu.MemUsed, gpu.MemTotal))) + " " +
		th.LLM.Render(fmt.Sprintf("(llm %s)", FmtBytes(gpu.LLMMemBytes)))
}

// serverLine1 renders a server's identity line: bullet, model/endpoint,
// kind, endpoint, status.
func serverLine1(srv *model.LLMServer, th Theme) string {
	label := srv.Model
	if label == "" {
		label = srv.Endpoint
	}
	return th.LLM.Render("● ") + strings.Join([]string{label, srv.Kind, srv.Endpoint, srv.Status}, "  ")
}

// serverLine2 renders a server's detail line: generation stats, a "metrics
// disabled" hint, or a router tag — indented two spaces under line 1.
func serverLine2(srv *model.LLMServer, th Theme) string {
	switch {
	case srv.Role == "router":
		return "  " + th.Dim.Render("router — spawns model instances on demand")
	case srv.Metrics != nil:
		m := srv.Metrics
		parts := []string{
			fmt.Sprintf("prompt %.1f t/s", m.PromptTPS),
			fmt.Sprintf("gen %.1f t/s", m.PredictedTPS),
		}
		if m.KVCacheUsage >= 0 {
			parts = append(parts, fmt.Sprintf("kv %.0f%%", m.KVCacheUsage*100))
		}
		parts = append(parts, fmt.Sprintf("queue %d", m.RequestsDeferred))
		if srv.SlotsTotal > 0 {
			parts = append(parts, fmt.Sprintf("slots %d/%d", m.RequestsProcessing, srv.SlotsTotal))
		}
		parts = append(parts, fmt.Sprintf("ctx %d", srv.CtxSize))
		return "  " + th.LLM.Render(strings.Join(parts, " · "))
	default:
		return "  " + th.Dim.Render("metrics disabled — start llama-server with --metrics")
	}
}

// procTableLines renders the process-table header plus as many rows
// (sorted by MemBytes descending) as budget allows, with a "+N more" footer
// when rows are truncated. budget is the number of visual rows remaining in
// the panel; a non-positive budget renders nothing.
func procTableLines(procs []model.GPUProc, th Theme, width, budget int) []string {
	if budget <= 0 {
		return nil
	}
	header := fmt.Sprintf("%-*s%-*s%-*s%-*s%-*s%s",
		colPID, "PID", colName, "NAME", colClass, "CLASS", colGPU, "GPU", colVRAM, "VRAM", "SM%")
	lines := []string{truncateLine(header, width)}

	rowBudget := budget - 1
	if rowBudget <= 0 {
		return lines
	}

	sorted := append([]model.GPUProc(nil), procs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].MemBytes > sorted[j].MemBytes })

	shown := len(sorted)
	truncated := false
	if shown > rowBudget {
		truncated = true
		shown = rowBudget - 1 // reserve one row for the "+N more" footer
		if shown < 0 {
			shown = 0
		}
	}
	for _, p := range sorted[:shown] {
		lines = append(lines, truncateLine(procRow(p, th, width), width))
	}
	if truncated && rowBudget > shown {
		lines = append(lines, truncateLine(th.Dim.Render(fmt.Sprintf("+%d more", len(sorted)-shown)), width))
	}
	return lines
}

// procRow renders one process-table row, coloring the CLASS column and
// rendering an unknown (-1) SM% as "--".
func procRow(p model.GPUProc, th Theme, width int) string {
	sm := "--"
	if p.SMUtilPct >= 0 {
		sm = fmt.Sprintf("%d%%", p.SMUtilPct)
	}
	classStyle := th.Dim
	switch p.Class {
	case model.ProcLLM:
		classStyle = th.LLM
	case model.ProcGraphics:
		classStyle = th.Mem
	}
	return fmt.Sprintf("%-*d%-*.*s", colPID, p.PID, colName, colName, p.Name) +
		classStyle.Render(fmt.Sprintf("%-*s", colClass, string(p.Class))) +
		fmt.Sprintf("%-*d%-*s%s", colGPU, p.GPUIndex, colVRAM, FmtBytes(p.MemBytes), sm)
}
