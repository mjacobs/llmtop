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

// RenderOverview stacks one bordered panel per host: a title line, one line
// per GPU, one line per LLM server, then any collection errors or a
// stale/unreachable banner. Every content line is clamped to fit inside the
// panel — long values are elided from the right, ANSI-aware, rather than
// wrapping.
func RenderOverview(hosts []*HostState, selected int, width int, th Theme) string {
	panels := make([]string, 0, len(hosts))
	for i, h := range hosts {
		border := th.Border
		if i == selected {
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
