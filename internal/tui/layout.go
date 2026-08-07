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
	cols   int // 1 or 2
	chartH int // cell height of each util/mem chart (0 when no GPUs anywhere)
}

// solveLayout picks the densest tier whose charts still get at least
// chartFloorCells of height within the body budget, and caps chart height
// at chartCapCells. Column count comes from width alone.
func solveLayout(hrs []hostRender, width, height int) gridLayout {
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
