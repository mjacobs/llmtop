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

// mapServersToGPUs assigns each server to every GPU its process tree runs
// on: LLMServer.GPUs when the collector filled it (it covers children such as
// vLLM's engine process), else LLMServer.PID matched against
// Procs[].GPUIndex for snapshots from agents that predate the field. A
// tensor-split server appears under each GPU it touches. Servers with a
// router role, no PID, or no GPU either way are host-level. GPU indexes that
// are not in this snapshot (filtered/hidden GPUs) are dropped, so a server
// running only on hidden GPUs falls through to the host strip. Order within
// each bucket follows Snapshot.Servers order; no server is ever dropped.
func mapServersToGPUs(snap *model.Snapshot) (byGPU map[int][]*model.LLMServer, hostLevel []*model.LLMServer) {
	byGPU = map[int][]*model.LLMServer{}
	if snap == nil {
		return byGPU, nil
	}
	visible := map[int]bool{}
	for _, g := range snap.GPUs {
		visible[g.Index] = true
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
		gpus := srv.GPUs
		if len(gpus) == 0 {
			gpus = pidGPUs[srv.PID]
		}
		if len(visible) == 0 {
			gpus = nil
		} else {
			var kept []int
			for _, gi := range gpus {
				if visible[gi] {
					kept = append(kept, gi)
				}
			}
			gpus = kept
		}
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

// sectionRule renders a dim labeled rule that fills exactly width visual
// columns. Narrow output is clipped with the same ANSI-aware helper as the
// rest of the panel.
func sectionRule(label string, width int, th Theme) string {
	if width <= 0 {
		return ""
	}
	line := "── " + label + " "
	if remaining := width - lipgloss.Width(line); remaining > 0 {
		line += strings.Repeat("─", remaining)
	}
	return truncateLine(th.Dim.Render(line), width)
}

// utilLabel renders the util-chart caption: `util 72% (llm 65%)`. The GPU's
// identity is not repeated here — the panel title, two lines up, carries it.
func utilLabel(gpu model.GPU, th Theme) string {
	return th.Util.Render(fmt.Sprintf("util %d%%", gpu.UtilPct)) + " " +
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

// serverLine2 renders a server's detail line: generation stats, a status
// note (unreachable/loading/sleeping), a "metrics disabled" hint, or a
// router tag — indented two spaces under line 1. The status arms come
// before the Metrics arm: an unreachable or loading server has no Metrics
// either, and must not be told to restart with --metrics.
func serverLine2(srv *model.LLMServer, th Theme) string {
	switch {
	case srv.Role == "router":
		return "  " + th.Dim.Render("router — spawns model instances on demand")
	case srv.Status == model.StatusUnreachable:
		return "  " + th.Err.Render("unreachable — health check failed")
	case srv.Status == model.StatusLoading:
		return "  " + th.Dim.Render("loading — model not resident yet")
	case srv.Status == model.StatusSleeping:
		return "  " + th.Dim.Render("sleeping — model unloaded until next request")
	case srv.Metrics != nil:
		m := srv.Metrics
		gen := fmt.Sprintf("gen %.1f t/s", m.PredictedTPS)
		if m.PredictedTPSEst {
			gen = fmt.Sprintf("gen ~%.0f t/s", m.PredictedTPS)
		}
		parts := []string{
			fmt.Sprintf("prompt %.1f t/s", m.PromptTPS),
			gen,
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
		return "  " + th.Dim.Render(metricsOffHint(srv))
	}
}

// metricsOffHint is the detail-line text for a reachable, resident server
// that exposes no metrics, worded for the engine that runs it: llama.cpp
// serves /metrics only with --metrics; vLLM serves it unless stats logging
// is turned off; NInfer has no metrics endpoint at all, so its line carries
// the static facts we do know (context length, concurrency limit) instead of
// telling the user to restart with a flag that does not exist.
func metricsOffHint(srv *model.LLMServer) string {
	switch srv.Kind {
	case "vllm":
		return "metrics disabled — start vllm without --disable-log-stats"
	case "ninfer":
		var parts []string
		if srv.CtxSize > 0 {
			parts = append(parts, fmt.Sprintf("ctx %d", srv.CtxSize))
		}
		if srv.SlotsTotal > 0 {
			parts = append(parts, fmt.Sprintf("slots %d", srv.SlotsTotal))
		}
		parts = append(parts, "no throughput — ninfer serves no /metrics endpoint")
		return strings.Join(parts, " · ")
	default:
		return "metrics disabled — start llama-server with --metrics"
	}
}

// serverBlockLines renders one server's block: identity line, detail line,
// and — only for a non-router with Metrics — a third gen-t/s sparkline line
// drawn from h.GenTPSHist[srv.Endpoint] (empty/absent history still renders a
// blank sparkline so the line count stays deterministic). Used by both
// renderGPUPanel and renderHostStrip so the two rendering sites can't drift.
//
// IMPORTANT: the length of this function's return value must always equal
// serverBlockLineCount(srv) — the layout solver counts lines via that
// function without rendering, so the two must be kept in lockstep by
// construction whenever this renderer changes.
func serverBlockLines(h *HostState, srv *model.LLMServer, th Theme, width int) []string {
	lines := []string{
		truncateLine(serverLine1(srv, th), width),
		truncateLine(serverLine2(srv, th), width),
	}
	if srv.Role == "router" || srv.Metrics == nil {
		return lines
	}
	var series []float64
	if r := h.GenTPSHist[srv.Endpoint]; r != nil {
		series = r.Slice()
	}
	max := 1.0
	for _, v := range series {
		if v > max {
			max = v
		}
	}
	w := width - 10
	if w < 8 {
		w = 8
	}
	spark := "  " + Sparkline(series, max, w) + " gen t/s"
	lines = append(lines, truncateLine(th.LLM.Render(spark), width))
	return lines
}

// serverBlockLineCount is the line count serverBlockLines produces for srv:
// 3 for a non-router with Metrics (identity + detail + sparkline), else 2.
// The solver (hostStripContentLines, panelFixedContentLines) uses this
// instead of rendering, so it must always agree with serverBlockLines's
// actual output length.
func serverBlockLineCount(srv *model.LLMServer) int {
	if srv.Role != "router" && srv.Metrics != nil {
		return 3
	}
	return 2
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

// renderGPUPanel renders one bordered per-GPU detail panel: title (index and
// name at the left edge, temp and power at the right), USAGE HISTORY rule,
// util chart, its caption, mem chart, its caption — both captions are flush
// right, under the newest samples the charts draw there, and neither repeats
// the GPU name from the title — LLM SERVERS rule, the server blocks mapped to this GPU, and
// (showProcs) the GPU PROCESSES rule plus its process table, which absorbs the
// remaining contentH budget. The process rule follows showProcs just like the
// table. The panel is exactly contentH+2 terminal rows tall so grid rows stay
// rectangular; overflow is clipped defensively. Caller guarantees hr.h.Snap != nil
// and a valid gpuIdx.
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
	title := fmt.Sprintf("GPU%d %s", gpu.Index, gpu.Name)
	env := th.Warm.Render(fmt.Sprintf("%d°", gpu.TempC)) + " " +
		th.Power.Render(fmt.Sprintf("%dW", gpu.PowerW))
	lines = append(lines, splitAlign(title, env, inner))
	lines = append(lines, sectionRule("USAGE HISTORY", inner, th))

	utilChart := renderGraph(
		ringSlice(h.UtilHist, gpu.Index), ringSlice(h.LLMUtilHist, gpu.Index),
		inner, chartH, th.Util, th.LLM)
	lines = append(lines, strings.Split(utilChart, "\n")...)
	lines = append(lines, rightAlign(utilLabel(gpu, th), inner))

	memChart := renderGraph(
		ringSlice(h.MemHist, gpu.Index), ringSlice(h.LLMMemHist, gpu.Index),
		inner, chartH, th.Mem, th.LLM)
	lines = append(lines, strings.Split(memChart, "\n")...)
	lines = append(lines, rightAlign(memLabel(gpu, th), inner))
	lines = append(lines, sectionRule("LLM SERVERS", inner, th))

	for _, srv := range hr.byGPU[gpu.Index] {
		lines = append(lines, serverBlockLines(h, srv, th, inner)...)
	}

	if showProcs {
		lines = append(lines, sectionRule("GPU PROCESSES", inner, th))
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

// renderHostStrip renders a host's slim strip: title line, host-level
// server blocks (servers that could not be mapped to a GPU, e.g. the
// router), collection errors, and the stale/unreachable banner. contentH
// pads (or clips) the strip to a fixed content height so a row of strips
// stays rectangular; 0 means "as tall as its content". Selection never
// lands here, so it always uses the passive border.
func renderHostStrip(hr hostRender, width, contentH int, th Theme) string {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	h := hr.h
	var lines []string
	lines = append(lines, titleLine(h, th, inner))
	for _, srv := range hr.hostLevel {
		lines = append(lines, serverBlockLines(h, srv, th, inner)...)
	}
	if h.Snap != nil {
		for _, e := range h.Snap.Errors {
			lines = append(lines, truncateLine(th.Err.Render("! "+e), inner))
		}
	}
	if h.ErrMsg != "" {
		prefix := "unreachable: "
		if h.Snap != nil {
			prefix = "stale: "
		}
		lines = append(lines, truncateLine(th.Err.Render(prefix+h.ErrMsg), inner))
	}
	border := th.Border.Width(width-2).Padding(0, 1)
	if contentH > 0 {
		if len(lines) > contentH {
			lines = lines[:contentH]
		}
		border = border.Height(contentH)
	}
	return border.Render(strings.Join(lines, "\n"))
}

// hostStripContentLines counts the content lines renderHostStrip emits —
// the solver's line accounting must match the renderer exactly.
func hostStripContentLines(hr hostRender) int {
	n := 1
	for _, srv := range hr.hostLevel {
		n += serverBlockLineCount(srv)
	}
	if hr.h.Snap != nil {
		n += len(hr.h.Snap.Errors)
	}
	if hr.h.ErrMsg != "" {
		n++
	}
	return n
}
