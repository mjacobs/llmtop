package tui

import "github.com/mjacobs/llmtop/internal/model"

// tier is the per-frame render density: FULL shows charts+servers+process
// table per GPU panel; CHARTS drops the process tables; COMPACT falls back
// to the one-line-per-GPU host rows.
type tier int

const (
	tierFull tier = iota
	tierCharts
	tierCompact
)

const (
	// chartFloorCells: below this chart height, degrade to the next tier.
	chartFloorCells = 2
	// chartCapCells: taller charts add no readable resolution (each cell is
	// 4 braille dots ≈ 32 levels already).
	chartCapCells = 8
	// procMinLines: FULL's minimum process-table allocation (header + 3 rows).
	procMinLines = 4
	// minPanelInner: narrowest panel content that fits the process table.
	minPanelInner = 60
	// panelGap: blank columns between two panels in a row.
	panelGap = 1
)

// gridLayout is the solver's per-frame decision, applied globally so every
// host renders at the same density.
type gridLayout struct {
	tier   tier
	cols   int // 1 or 2, or the panel count in one-row mode
	chartH int // cell height of each util/mem chart (0 when no GPUs anywhere)
	// oneRow puts every GPU panel across every host in a single row, with
	// each host's strip spanning its own panels' columns. widths holds the
	// per-panel terminal width in flat panel order (nil unless oneRow).
	oneRow bool
	widths []int
}

// solveLayout prefers the one-row layout (all GPUs side by side) when the
// terminal is wide enough for it, otherwise falls back to the stacked
// per-host grid: the densest tier whose charts still get at least
// chartFloorCells of height within the body budget, with chart height capped
// at chartCapCells. In the stacked grid, column count comes from width alone.
func solveLayout(hrs []hostRender, width, height int) gridLayout {
	if lay, ok := solveOneRow(hrs, width, height); ok {
		return lay
	}
	cols := 1
	if (width-panelGap)/2-4 >= minPanelInner {
		cols = 2
	}
	rows := totalRows(hrs, cols)
	if rows == 0 {
		return gridLayout{tier: tierFull, cols: cols, chartH: 0}
	}
	for _, t := range []tier{tierFull, tierCharts} {
		h := (height - totalHeight(hrs, t, cols, 0)) / (2 * rows)
		if h >= chartFloorCells {
			if h > chartCapCells {
				h = chartCapCells
			}
			return gridLayout{tier: t, cols: cols, chartH: h}
		}
	}
	return gridLayout{tier: tierCompact, cols: cols}
}

// solveOneRow tries the wide layout: one row holding every GPU panel of
// every host, host strips sitting side by side above their own columns.
// It reports ok=false when the terminal is too narrow to give each panel a
// readable width, or too short for even CHARTS-tier charts.
//
// A single row is never denser than the stacked grid — it has the fewest
// possible panel rows and shares one strip row — so preferring it whenever
// it fits cannot cost tier or chart height.
func solveOneRow(hrs []hostRender, width, height int) (gridLayout, bool) {
	n := totalPanels(hrs)
	if n < 2 {
		return gridLayout{}, false // a lone panel is already full width
	}
	widths := splitWidths(width, n)
	if widths == nil {
		return gridLayout{}, false
	}
	for _, t := range []tier{tierFull, tierCharts} {
		h := (height - oneRowHeight(hrs, t, 0)) / 2
		if h >= chartFloorCells {
			if h > chartCapCells {
				h = chartCapCells
			}
			return gridLayout{tier: t, cols: n, chartH: h, oneRow: true, widths: widths}, true
		}
	}
	return gridLayout{}, false
}

// splitWidths divides width into n panel columns separated by panelGap,
// handing the remainder out one column at a time from the left so the row
// fills the terminal exactly. Returns nil when the columns would be
// narrower than minPanelInner.
func splitWidths(width, n int) []int {
	avail := width - (n-1)*panelGap
	if avail < n || avail/n-4 < minPanelInner {
		return nil
	}
	base, rem := avail/n, avail%n
	widths := make([]int, n)
	for i := range widths {
		widths[i] = base
		if i < rem {
			widths[i]++
		}
	}
	return widths
}

// oneRowHeight is the rendered height of the one-row layout: the shared
// strip row, the single panel row, and a stacked full-width strip per
// GPU-less host. Like totalHeight, it must match the renderer line for line.
func oneRowHeight(hrs []hostRender, t tier, chartH int) int {
	total := oneRowFixed(hrs, t) + 2*chartH + 2
	if s := stripRowContentLines(hrs); s > 0 {
		total += s + 2
	}
	for _, hr := range hrs {
		if hostPanelCount(hr) == 0 {
			total += hostStripContentLines(hr) + 2
		}
	}
	return total
}

// oneRowFixed is the tallest fixed content among all panels in the frame —
// the single row's shared content height before chart rows are added.
func oneRowFixed(hrs []hostRender, t tier) int {
	maxFixed := 0
	for _, hr := range hrs {
		for i := 0; i < hostPanelCount(hr); i++ {
			if f := panelFixedContentLines(hr, i, t); f > maxFixed {
				maxFixed = f
			}
		}
	}
	return maxFixed
}

// stripRowContentLines is the content height every side-by-side host strip
// is padded to (0 when no host has GPU panels).
func stripRowContentLines(hrs []hostRender) int {
	max := 0
	for _, hr := range hrs {
		if hostPanelCount(hr) == 0 {
			continue
		}
		if n := hostStripContentLines(hr); n > max {
			max = n
		}
	}
	return max
}

// hostPanelCount is the number of GPU panels a host contributes.
func hostPanelCount(hr hostRender) int {
	if hr.h.Snap == nil {
		return 0
	}
	return len(hr.h.Snap.GPUs)
}

// totalPanels counts GPU panels across all hosts.
func totalPanels(hrs []hostRender) int {
	n := 0
	for _, hr := range hrs {
		n += hostPanelCount(hr)
	}
	return n
}

// totalRows counts panel grid rows across all hosts at the given column
// count (each host's GPUs wrap independently under its strip).
func totalRows(hrs []hostRender, cols int) int {
	rows := 0
	for _, hr := range hrs {
		if hr.h.Snap == nil {
			continue
		}
		rows += (len(hr.h.Snap.GPUs) + cols - 1) / cols
	}
	return rows
}

// totalHeight sums the frame's rendered height at a tier and chart height:
// every strip (content + border) plus every panel row (tallest panel's
// fixed content + two charts + border). This accounting must match the
// renderers line-for-line — the solver and the view disagree at their
// peril, so both lean on hostStripContentLines/panelFixedContentLines.
func totalHeight(hrs []hostRender, t tier, cols, chartH int) int {
	total := 0
	for _, hr := range hrs {
		total += hostStripContentLines(hr) + 2
		if hr.h.Snap == nil {
			continue
		}
		n := len(hr.h.Snap.GPUs)
		for row := 0; row*cols < n; row++ {
			total += rowMaxFixed(hr, row, cols, t) + 2*chartH + 2
		}
	}
	return total
}

// rowMaxFixed is the tallest fixed content among the panels in one grid
// row — the row's shared content height before chart rows are added.
func rowMaxFixed(hr hostRender, row, cols int, t tier) int {
	n := len(hr.h.Snap.GPUs)
	maxFixed := 0
	for i := row * cols; i < (row+1)*cols && i < n; i++ {
		if f := panelFixedContentLines(hr, i, t); f > maxFixed {
			maxFixed = f
		}
	}
	return maxFixed
}

// panelFixedContentLines counts a panel's content lines excluding chart rows:
// title, two chart labels, tier-specific section rules, mapped server blocks,
// and FULL's minimum process-table allocation when processes exist.
func panelFixedContentLines(hr hostRender, gpuIdx int, t tier) int {
	gpu := hr.h.Snap.GPUs[gpuIdx]
	n := 3 // title + two chart labels
	if t == tierFull || t == tierCharts {
		n += 2 // usage and server rules
	}
	if t == tierFull {
		n++ // process rule renders even when the process list is empty
	}
	for _, srv := range hr.byGPU[gpu.Index] {
		n += serverBlockLineCount(srv)
	}
	if t == tierFull && gpuHasProcs(hr.h.Snap.Procs, gpu.Index) {
		n += procMinLines
	}
	return n
}

// gpuHasProcs reports whether any process runs on the given GPU index.
func gpuHasProcs(procs []model.GPUProc, gpuIdx int) bool {
	for _, p := range procs {
		if p.GPUIndex == gpuIdx {
			return true
		}
	}
	return false
}
