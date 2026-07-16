package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mjacobs/llmtop/internal/model"
)

// hostRender pairs a host with its per-frame server→GPU mapping so the
// solver and renderers agree on where every server block goes.
type hostRender struct {
	h         *HostState
	byGPU     map[int][]*model.LLMServer
	hostLevel []*model.LLMServer
}

// buildHostRenders computes the server→GPU mapping for every host once per
// frame.
func buildHostRenders(hosts []*HostState) []hostRender {
	hrs := make([]hostRender, 0, len(hosts))
	for _, h := range hosts {
		hr := hostRender{h: h}
		hr.byGPU, hr.hostLevel = mapServersToGPUs(h.Snap)
		hrs = append(hrs, hr)
	}
	return hrs
}

// mapServersToGPUs assigns each server to every GPU its process runs on,
// matching LLMServer.PID against Procs[].GPUIndex (a tensor-split server
// appears under each GPU it touches). Servers with a router role, no PID,
// or a PID absent from Procs are host-level. Order within each bucket
// follows Snapshot.Servers order; no server is ever dropped.
func mapServersToGPUs(snap *model.Snapshot) (byGPU map[int][]*model.LLMServer, hostLevel []*model.LLMServer) {
	byGPU = map[int][]*model.LLMServer{}
	if snap == nil {
		return byGPU, nil
	}
	pidGPUs := map[int32][]int{}
	seen := map[[2]int64]bool{} // dedupe (pid, gpu): Procs may repeat a pair
	for _, p := range snap.Procs {
		k := [2]int64{int64(p.PID), int64(p.GPUIndex)}
		if p.PID != 0 && !seen[k] {
			seen[k] = true
			pidGPUs[p.PID] = append(pidGPUs[p.PID], p.GPUIndex)
		}
	}
	for si := range snap.Servers {
		srv := &snap.Servers[si]
		gpus := pidGPUs[srv.PID]
		if srv.Role == "router" || srv.PID == 0 || len(gpus) == 0 {
			hostLevel = append(hostLevel, srv)
			continue
		}
		for _, gi := range gpus {
			byGPU[gi] = append(byGPU[gi], srv)
		}
	}
	return byGPU, hostLevel
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

// Panel proc-table column widths (no GPU column — the panel is per-GPU),
// derived from the header literal `PID      NAME                 CLASS     VRAM     SM%`.
const (
	pColPID   = 9
	pColName  = 21
	pColClass = 10
	pColVRAM  = 9
)

// panelProcLines renders the process table for one GPU: header plus rows
// filtered to gpuIdx, sorted by MemBytes descending, with a "+N more"
// footer when budget truncates rows. budget is the number of visual rows
// available: non-positive renders nothing, and a single-row budget with
// rows present renders "+N more" in place of the header so hidden rows are
// never silently dropped. A GPU with no processes renders nothing.
func panelProcLines(procs []model.GPUProc, gpuIdx int, th Theme, width, budget int) []string {
	var mine []model.GPUProc
	for _, p := range procs {
		if p.GPUIndex == gpuIdx {
			mine = append(mine, p)
		}
	}
	if budget <= 0 || len(mine) == 0 {
		return nil
	}
	if budget == 1 {
		return []string{truncateLine(th.Dim.Render(fmt.Sprintf("+%d more", len(mine))), width)}
	}
	header := fmt.Sprintf("%-*s%-*s%-*s%-*s%s",
		pColPID, "PID", pColName, "NAME", pColClass, "CLASS", pColVRAM, "VRAM", "SM%")
	lines := []string{truncateLine(header, width)}
	rowBudget := budget - 1

	// Stable sort so equal-VRAM processes keep a fixed order frame to frame.
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].MemBytes > mine[j].MemBytes })

	shown := len(mine)
	truncated := false
	if shown > rowBudget {
		truncated = true
		shown = rowBudget - 1 // reserve one row for the "+N more" footer
		if shown < 0 {
			shown = 0
		}
	}
	for _, p := range mine[:shown] {
		lines = append(lines, truncateLine(panelProcRow(p, th), width))
	}
	if truncated {
		lines = append(lines, truncateLine(th.Dim.Render(fmt.Sprintf("+%d more", len(mine)-shown)), width))
	}
	return lines
}

// panelProcRow renders one per-GPU process row, coloring the CLASS column
// and rendering an unknown (-1) SM% as "--".
func panelProcRow(p model.GPUProc, th Theme) string {
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
	return fmt.Sprintf("%-*d%-*.*s", pColPID, p.PID, pColName, pColName, p.Name) +
		classStyle.Render(fmt.Sprintf("%-*s", pColClass, string(p.Class))) +
		fmt.Sprintf("%-*s%s", pColVRAM, FmtBytes(p.MemBytes), sm)
}

// renderGPUPanel renders one bordered per-GPU detail panel: title
// (index, name, temp, power), util label+chart, mem label+chart, the
// server blocks mapped to this GPU, and (showProcs) its process table,
// which absorbs the remaining contentH budget. The panel is exactly
// contentH+2 terminal rows tall so grid rows stay rectangular; overflow
// is clipped defensively. Caller guarantees hr.h.Snap != nil and a valid
// gpuIdx.
func renderGPUPanel(hr hostRender, gpuIdx, width, contentH, chartH int, showProcs, active bool, th Theme) string {
	inner := width - 4 // border (1) + padding (1) each side
	if inner < 1 {
		inner = 1
	}
	if contentH < 1 {
		contentH = 1
	}
	h := hr.h
	gpu := h.Snap.GPUs[gpuIdx]

	var lines []string
	title := fmt.Sprintf("GPU%d %s", gpu.Index, gpu.Name) +
		th.Warm.Render(fmt.Sprintf(" %d°", gpu.TempC)) +
		th.Power.Render(fmt.Sprintf(" %dW", gpu.PowerW))
	lines = append(lines, truncateLine(title, inner))

	lines = append(lines, truncateLine(utilLabel(gpu, th), inner))
	utilChart := renderGraph(
		ringSlice(h.UtilHist, gpu.Index), ringSlice(h.LLMUtilHist, gpu.Index),
		inner, chartH, th.Util, th.LLM)
	lines = append(lines, strings.Split(utilChart, "\n")...)

	lines = append(lines, truncateLine(memLabel(gpu, th), inner))
	memChart := renderGraph(
		ringSlice(h.MemHist, gpu.Index), ringSlice(h.LLMMemHist, gpu.Index),
		inner, chartH, th.Mem, th.LLM)
	lines = append(lines, strings.Split(memChart, "\n")...)

	for _, srv := range hr.byGPU[gpu.Index] {
		lines = append(lines, truncateLine(serverLine1(srv, th), inner))
		lines = append(lines, truncateLine(serverLine2(srv, th), inner))
	}

	if showProcs {
		lines = append(lines, panelProcLines(h.Snap.Procs, gpu.Index, th, inner, contentH-len(lines))...)
	}
	if len(lines) > contentH {
		lines = lines[:contentH]
	}

	border := th.Border
	if active {
		border = th.BorderActive
	}
	return border.Width(width-2).Height(contentH).Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}
