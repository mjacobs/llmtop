package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjacobs/llmtop/internal/history"
	"github.com/mjacobs/llmtop/internal/model"
)

const sparkWidth = 12

// RenderOverview renders the unified adaptive view: per host a slim strip
// (title, host-level servers, banners) and per-GPU detail panels (charts,
// mapped servers, process table). On a wide terminal every GPU across every
// host shares one row, each host's strip spanning its own columns; otherwise
// hosts stack, each wrapping its GPUs into one or two columns. A per-frame
// solver picks the densest tier that fits the body height, degrading to the
// low-density COMPACT fallback (one bordered panel per host with compact
// rows) when space is tight. selected is a flat GPU-panel index in render
// order; the selected panel gets the active border (in COMPACT, its host
// panel does).
func RenderOverview(hosts []*HostState, selected int, width, height int, th Theme) string {
	return clampHeight(renderOverview(hosts, selected, width, height, th), height)
}

// clampHeight drops any lines beyond height. The solver sizes charts and
// panels to the budget, but host strips and the compact tier render their
// content at natural height, so a host with many host-level servers or
// errors (a driverless machine listing its llama.cpp servers, say) could
// otherwise push the frame past the terminal and into the footer.
func clampHeight(s string, height int) string {
	if height <= 0 {
		return "" // no room for a body at all (terminal shorter than the chrome)
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= height {
		return s
	}
	return strings.Join(lines[:height], "\n")
}

func renderOverview(hosts []*HostState, selected int, width, height int, th Theme) string {
	hrs := buildHostRenders(hosts)
	lay := solveLayout(hrs, width, height)
	if lay.tier == tierCompact {
		return renderCompact(hosts, hostOfPanel(hrs, selected), width, th)
	}
	if lay.oneRow {
		return renderOneRow(hrs, lay, selected, width, height, th)
	}

	panelW := width
	if lay.cols == 2 {
		panelW = (width - panelGap) / 2
	}

	// Surplus below the solved layout: FULL spends it on extra process-table
	// rows per panel row (remainder becomes inter-host gap lines); CHARTS has
	// no tables, so it all becomes gaps. Never a single void at the bottom.
	extra, gapBudget := 0, 0
	if rows := totalRows(hrs, lay.cols); rows > 0 {
		if leftover := height - totalHeight(hrs, lay.tier, lay.cols, lay.chartH); leftover > 0 {
			if lay.tier == tierFull {
				extra = leftover / rows
				gapBudget = leftover % rows
			} else {
				gapBudget = leftover
			}
		}
	}
	gapPerHost := 0
	if len(hrs) > 1 {
		gapPerHost = gapBudget / (len(hrs) - 1)
	}

	var blocks []string
	panelIdx := 0
	for hi, hr := range hrs {
		if hi > 0 && gapPerHost > 0 {
			blocks = append(blocks, strings.Repeat("\n", gapPerHost-1))
		}
		blocks = append(blocks, renderHostStrip(hr, width, 0, th))
		if hr.h.Snap == nil {
			continue
		}
		n := len(hr.h.Snap.GPUs)
		for row := 0; row*lay.cols < n; row++ {
			contentH := rowMaxFixed(hr, row, lay.cols, lay.tier) + 2*lay.chartH + extra
			var cells []string
			for i := row * lay.cols; i < (row+1)*lay.cols && i < n; i++ {
				if len(cells) > 0 {
					cells = append(cells, strings.Repeat(" ", panelGap))
				}
				cells = append(cells, renderGPUPanel(hr, i, panelW, contentH, lay.chartH,
					lay.tier == tierFull, panelIdx == clampPanel(hrs, selected), th))
				panelIdx++
			}
			blocks = append(blocks, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
		}
	}
	// Plain-join rather than lipgloss.JoinVertical: JoinVertical pads every
	// line to the widest block in the whole document, which would stretch a
	// half-width lone trailing panel row out to the full document width.
	return strings.Join(blocks, "\n")
}

// renderOneRow renders the wide layout: a row of host strips, each spanning
// the columns of its own GPUs, above a single row holding every GPU panel.
// Hosts with no GPU panels (unreachable, or reporting zero GPUs) have no
// column to sit above, so their strips stack full width underneath. All
// surplus height goes to the panel row so the frame never ends in a void.
func renderOneRow(hrs []hostRender, lay gridLayout, selected, width, height int, th Theme) string {
	selPanel := clampPanel(hrs, selected)
	contentH := oneRowFixed(hrs, lay.tier) + 2*lay.chartH
	if leftover := height - oneRowHeight(hrs, lay.tier, lay.chartH); leftover > 0 {
		contentH += leftover
	}
	stripH := stripRowContentLines(hrs)

	var strips, panels, trailing []string
	panelIdx := 0
	for _, hr := range hrs {
		n := hostPanelCount(hr)
		if n == 0 {
			trailing = append(trailing, renderHostStrip(hr, width, 0, th))
			continue
		}
		if len(strips) > 0 { // gap between host column groups
			strips = append(strips, strings.Repeat(" ", panelGap))
			panels = append(panels, strings.Repeat(" ", panelGap))
		}
		stripW := (n - 1) * panelGap
		for i := panelIdx; i < panelIdx+n; i++ {
			stripW += lay.widths[i]
		}
		strips = append(strips, renderHostStrip(hr, stripW, stripH, th))
		for i := 0; i < n; i++ {
			if i > 0 {
				panels = append(panels, strings.Repeat(" ", panelGap))
			}
			panels = append(panels, renderGPUPanel(hr, i, lay.widths[panelIdx+i], contentH,
				lay.chartH, lay.tier == tierFull, panelIdx+i == selPanel, th))
		}
		panelIdx += n
	}

	blocks := []string{
		lipgloss.JoinHorizontal(lipgloss.Top, strips...),
		lipgloss.JoinHorizontal(lipgloss.Top, panels...),
	}
	return strings.Join(append(blocks, trailing...), "\n")
}

// clampPanel clamps a flat panel index into [0, panels-1] (-1 when there
// are no panels, so nothing matches it).
func clampPanel(hrs []hostRender, panel int) int {
	total := totalPanels(hrs)
	if total == 0 {
		return -1
	}
	if panel < 0 {
		return 0
	}
	if panel > total-1 {
		return total - 1
	}
	return panel
}

// hostOfPanel returns the host index owning a flat panel index, 0 when
// there are no panels (COMPACT highlight fallback).
func hostOfPanel(hrs []hostRender, panel int) int {
	panel = clampPanel(hrs, panel)
	seen := 0
	for hi, hr := range hrs {
		if hr.h.Snap == nil {
			continue
		}
		seen += len(hr.h.Snap.GPUs)
		if panel < seen {
			return hi
		}
	}
	return 0
}

// renderCompact is the COMPACT tier, the low-density fallback: one bordered
// panel per host with one line per GPU and server, no charts.
func renderCompact(hosts []*HostState, selectedHost, width int, th Theme) string {
	panels := make([]string, 0, len(hosts))
	for i, h := range hosts {
		border := th.Border
		if i == selectedHost {
			border = th.BorderActive
		}
		inner := width - 4 // border (1) + padding (1) each side
		if inner < 1 {
			inner = 1
		}
		var lines []string
		lines = append(lines, titleLine(h, th, inner))
		if h.Snap != nil {
			for gi := range h.Snap.GPUs {
				lines = append(lines, gpuLine(h, gi, th, inner))
			}
			for si := range h.Snap.Servers {
				lines = append(lines, serverLine(&h.Snap.Servers[si], th, inner))
			}
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
		panels = append(panels, border.Width(width-2).Padding(0, 1).Render(strings.Join(lines, "\n")))
	}
	return lipgloss.JoinVertical(lipgloss.Left, panels...)
}

// truncateLine clips a (possibly ANSI-styled) line to width visual columns,
// preserving escape codes rather than cutting through them.
func truncateLine(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "")
}

// rightAlign clips a line to width and pads it to the right edge, so chart
// captions sit under the newest samples rather than under empty history.
// Overflow is clipped from the LEFT: the live values sit at the caption's
// end, so a narrow panel drops the leading GPU name (redundant with the
// panel title), never the values.
func rightAlign(s string, width int) string {
	if w := lipgloss.Width(s); w > width {
		s = ansi.TruncateLeft(s, w-width+1, "…")
	}
	if pad := width - lipgloss.Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

// splitAlign renders left flush to the left edge and right flush to the
// right edge of a single width-column line. When the two cannot both fit,
// left gives way first — it repeats the panel title, while right carries the
// live values.
func splitAlign(left, right string, width int) string {
	room := width - lipgloss.Width(right) - 1 // at least one space between
	if room <= 0 {
		return rightAlign(right, width)
	}
	if lipgloss.Width(left) > room {
		left = ansi.Truncate(left, room, "…")
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	return left + strings.Repeat(" ", gap) + right
}

// titleLine renders the host name plus a dim status suffix as the panel's
// first content line (lipgloss border-embedded titles are fiddly to align).
func titleLine(h *HostState, th Theme, width int) string {
	driver := "?"
	agent := "ok"
	if h.Snap != nil {
		if h.Snap.Host.DriverVersion != "" {
			driver = h.Snap.Host.DriverVersion
		}
	}
	if h.ErrMsg != "" {
		agent = "err"
	}
	line := th.Title.Render(h.Display) + th.Dim.Render(fmt.Sprintf(" · driver %s · agent %s", driver, agent))
	return truncateLine(line, width)
}

// gpuLine renders one GPU row: a fixed-width label, a util sparkline+percent,
// a mem sparkline+percent+bytes pair, temperature, and power draw.
func gpuLine(h *HostState, idx int, th Theme, width int) string {
	gpu := h.Snap.GPUs[idx]
	label := fmt.Sprintf("GPU%d %-20.20s", gpu.Index, gpu.Name)

	utilSpark := th.Util.Render(Sparkline(ringSlice(h.UtilHist, gpu.Index), 100, sparkWidth))
	memSpark := th.Mem.Render(Sparkline(ringSlice(h.MemHist, gpu.Index), 100, sparkWidth))
	memPct := int(pct(gpu.MemUsed, gpu.MemTotal) + 0.5)

	line := label + " " + utilSpark + fmt.Sprintf(" %3d%%", gpu.UtilPct) +
		" " + memSpark + fmt.Sprintf(" %3d%% %s", memPct, FmtMemPair(gpu.MemUsed, gpu.MemTotal)) +
		th.Warm.Render(fmt.Sprintf(" %d°", gpu.TempC)) +
		th.Power.Render(fmt.Sprintf(" %dW", gpu.PowerW))
	return truncateLine(line, width)
}

// serverLine renders one LLM server row: a colored bullet, model (or
// endpoint when no alias is set), an error tag when unreachable/loading, and
// either generation stats, a "router" tag, a "metrics off" hint, or — when
// /metrics failed with something other than 404/501 (bad api_key, 5xx,
// proxy hiccup) — a "metrics error" tag naming why, instead of the
// "off" hint that would wrongly suggest --metrics is missing. An
// unreachable or loading server has no Metrics either, but its status tag
// already explains why, so it gets neither hint.
func serverLine(srv *model.LLMServer, th Theme, width int) string {
	label := srv.Model
	if label == "" {
		label = srv.Endpoint
	}
	line := th.LLM.Render("● ") + label

	switch srv.Status {
	case model.StatusUnreachable:
		line += " " + th.Err.Render("unreachable")
	case model.StatusLoading:
		line += " " + th.Err.Render("loading")
	}

	switch {
	case srv.Role == "router":
		line += " " + th.Dim.Render("router")
	case srv.Status == model.StatusUnreachable, srv.Status == model.StatusLoading:
		// status tag above says it all
	case srv.Status == model.StatusSleeping:
		line += " " + th.Dim.Render("sleeping")
	case srv.Metrics != nil:
		stats := fmt.Sprintf(" gen %.1f t/s", srv.Metrics.PredictedTPS)
		if srv.Metrics.KVCacheUsage >= 0 {
			stats += fmt.Sprintf(" kv %.0f%%", srv.Metrics.KVCacheUsage*100)
		}
		if srv.SlotsTotal > 0 {
			stats += fmt.Sprintf(" %d/%d slots", srv.Metrics.RequestsProcessing, srv.SlotsTotal)
		}
		line += th.LLM.Render(stats)
	case srv.MetricsErr != "":
		line += th.Err.Render(" (metrics: " + srv.MetricsErr + ")")
	default:
		line += th.Dim.Render(" " + compactMetricsHint(srv))
	}
	return truncateLine(line, width)
}

// compactMetricsHint is the compact tier's short form of panel.go's
// metricsOffHint, for a reachable, resident server with no metrics. It is
// worded per engine: only llama.cpp has a --metrics flag to turn on, vLLM
// serves metrics unless stats logging is disabled, and NInfer has no
// metrics endpoint at all — so its row carries the static facts we do know
// (context length, concurrency limit) instead of naming a flag that does
// not exist.
func compactMetricsHint(srv *model.LLMServer) string {
	switch srv.Kind {
	case "vllm":
		return "(stats off)"
	case "ninfer":
		var parts []string
		if srv.CtxSize > 0 {
			parts = append(parts, fmt.Sprintf("ctx %d", srv.CtxSize))
		}
		if srv.SlotsTotal > 0 {
			parts = append(parts, fmt.Sprintf("%d slots", srv.SlotsTotal))
		}
		if len(parts) == 0 {
			return "(no /metrics)"
		}
		return strings.Join(parts, " ")
	default:
		return "(--metrics off)"
	}
}

// ringSlice reads a GPU-indexed ring's samples, tolerating an absent entry
// (e.g. a GPU present in Snap but not yet folded into history by Apply).
func ringSlice(m map[int]*history.Ring[float64], idx int) []float64 {
	r, ok := m[idx]
	if !ok || r == nil {
		return nil
	}
	return r.Slice()
}
