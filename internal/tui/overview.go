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
// (title, host-level servers, banners) and a grid of per-GPU detail panels
// (charts, mapped servers, process table), one or two columns wide. A
// per-frame solver picks the densest tier that fits the body height,
// degrading to the legacy compact rows when space is tight. selected is a
// flat GPU-panel index in render order; the selected panel gets the active
// border (in COMPACT, its host panel does).
func RenderOverview(hosts []*HostState, selected int, width, height int, th Theme) string {
	hrs := buildHostRenders(hosts)
	lay := solveLayout(hrs, width, height)
	if lay.tier == tierCompact {
		return renderCompact(hosts, hostOfPanel(hrs, selected), width, th)
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
		blocks = append(blocks, renderHostStrip(hr, width, th))
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

// clampPanel clamps a flat panel index into [0, panels-1] (-1 when there
// are no panels, so nothing matches it).
func clampPanel(hrs []hostRender, panel int) int {
	total := 0
	for _, hr := range hrs {
		if hr.h.Snap != nil {
			total += len(hr.h.Snap.GPUs)
		}
	}
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

// renderCompact is the COMPACT tier: today's legacy overview — one bordered
// panel per host with one line per GPU and server.
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
	line := th.Title.Render(h.Name) + th.Dim.Render(fmt.Sprintf(" · driver %s · agent %s", driver, agent))
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
// either generation stats, a "router" tag, or a "metrics off" hint.
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
	default:
		line += th.Dim.Render(" (--metrics off)")
	}
	return truncateLine(line, width)
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
