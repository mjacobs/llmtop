package tui

import (
	"fmt"
	"sort"

	"github.com/mjacobs/llmtop/internal/model"
)

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

// LayoutMode is the user's preferred overview arrangement. It is a
// preference, never a mandate: the solver shrinks the column count when the
// terminal is too narrow and still degrades through the FULL/CHARTS/COMPACT
// tiers when it is too short.
type LayoutMode int

const (
	// LayoutAuto is the built-in heuristic: one row when it fits, else the
	// stacked per-host grid at one or two columns.
	LayoutAuto LayoutMode = iota
	// LayoutOneRow asks for every GPU panel side by side in a single row,
	// falling back to the stacked grid when the row cannot fit.
	LayoutOneRow
	// LayoutStackByHost never uses the one-row arrangement: hosts stack,
	// each wrapping its own GPUs into the columns the width allows.
	LayoutStackByHost
	// LayoutColumns is the stacked grid at a target column count.
	LayoutColumns
)

func (m LayoutMode) String() string {
	switch m {
	case LayoutOneRow:
		return "one-row"
	case LayoutStackByHost:
		return "stack-by-host"
	case LayoutColumns:
		return "columns"
	default:
		return "auto"
	}
}

// LayoutSlot names one GPU panel by host (its config name) and GPU device
// index — the config's [[layout.slots]] entries.
type LayoutSlot struct {
	Host string
	GPU  int
}

// LayoutPref is the resolved [layout] section. The zero value is the
// built-in heuristic, so callers with no preference pass LayoutPref{}.
type LayoutPref struct {
	Mode    LayoutMode
	Columns int          // target column count; only read in LayoutColumns
	Slots   []LayoutSlot // explicit panel order; nil keeps configured order
}

// Label renders the mode for the header, spelling out the column target so
// "columns 3" is distinguishable from "columns 2".
func (p LayoutPref) Label() string {
	if p.Mode == LayoutColumns {
		return fmt.Sprintf("columns %d", p.Columns)
	}
	return p.Mode.String()
}

// Next returns the preference the layout key cycles to. The ring is
// auto → one-row → stack-by-host, plus the configured column target when
// there is one, so a user who wrote `columns = 3` can cycle back to it
// without editing the file. Columns and Slots carry across unchanged.
func (p LayoutPref) Next() LayoutPref {
	ring := []LayoutMode{LayoutAuto, LayoutOneRow, LayoutStackByHost}
	if p.Columns > 0 {
		ring = append(ring, LayoutColumns)
	}
	at := 0
	for i, m := range ring {
		if m == p.Mode {
			at = i
			break
		}
	}
	p.Mode = ring[(at+1)%len(ring)]
	return p
}

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

// solveLayout arranges the frame according to pref, degrading as the
// terminal demands. It tries the one-row layout (all GPUs side by side)
// when pref allows it and the terminal is wide enough, otherwise it falls
// back to the stacked per-host grid at the widest column count pref and the
// width agree on, then picks the densest tier whose charts still get at
// least chartFloorCells of height within the body budget, with chart height
// capped at chartCapCells.
func solveLayout(hrs []hostRender, width, height int, pref LayoutPref) gridLayout {
	if pref.Mode == LayoutAuto || pref.Mode == LayoutOneRow {
		if lay, ok := solveOneRow(hrs, width, height); ok {
			return lay
		}
	}
	cols := stackedCols(width, pref)
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

// stackedCols picks the stacked grid's column count: the preference's
// target when it set one, otherwise the two-up default. Either way the
// count shrinks until every column clears minPanelInner, so a column target
// wider than the terminal degrades instead of producing unreadable panels.
func stackedCols(width int, pref LayoutPref) int {
	want := 2
	if pref.Mode == LayoutColumns && pref.Columns > 0 {
		want = pref.Columns
	}
	// No column count wider than the terminal can ever clear minPanelInner,
	// so clamp here before counting down: an unbounded user-supplied target
	// (config only rejects negative values) must not turn every render into
	// a loop over that value.
	if want > width {
		want = width
	}
	for c := want; c > 1; c-- {
		if gridPanelWidth(width, c)-4 >= minPanelInner {
			return c
		}
	}
	return 1
}

// gridPanelWidth is the terminal width one panel gets in a cols-wide
// stacked row, gaps removed and the remainder dropped so every column in
// the row is the same width.
func gridPanelWidth(width, cols int) int {
	if cols <= 1 {
		return width
	}
	return (width - (cols-1)*panelGap) / cols
}

// applySlots reorders panels to an explicit slot list: hosts sort by their
// first mention and each host's named GPUs lead, in slot order. Hosts and
// GPUs the list does not name keep their configured order behind the named
// ones, and a slot naming a host or GPU that is not present is ignored — a
// config shared across machines still works when part of the rig is down.
func applySlots(hrs []hostRender, slots []LayoutSlot) []hostRender {
	if len(slots) == 0 {
		return hrs
	}
	// A slot naming a host or GPU that is not currently present must not
	// consume a rank: filter against the live snapshot before ranking, so
	// e.g. bravo/GPU99 (bravo present, GPU99 not) cannot move bravo ahead of
	// other hosts, and a host with no matching slots stays unranked.
	presentGPUs := make(map[string]map[int]bool, len(hrs))
	for _, hr := range hrs {
		if hr.h.Snap == nil {
			presentGPUs[hr.h.Name] = nil
			continue
		}
		gpus := make(map[int]bool, len(hr.h.Snap.GPUs))
		for _, g := range hr.h.Snap.GPUs {
			gpus[g.Index] = true
		}
		presentGPUs[hr.h.Name] = gpus
	}

	hostRank := map[string]int{}
	gpuRank := map[string]map[int]int{}
	for _, s := range slots {
		if !presentGPUs[s.Host][s.GPU] {
			continue
		}
		if _, ok := hostRank[s.Host]; !ok {
			hostRank[s.Host] = len(hostRank)
			gpuRank[s.Host] = map[int]int{}
		}
		if _, ok := gpuRank[s.Host][s.GPU]; !ok {
			gpuRank[s.Host][s.GPU] = len(gpuRank[s.Host])
		}
	}
	unranked := len(hostRank) // sorts every unnamed host after every named one

	out := make([]hostRender, len(hrs))
	copy(out, hrs)
	rankOf := func(hr hostRender) int {
		if r, ok := hostRank[hr.h.Name]; ok {
			return r
		}
		return unranked
	}
	sort.SliceStable(out, func(i, j int) bool { return rankOf(out[i]) < rankOf(out[j]) })

	for i := range out {
		ranks, ok := gpuRank[out[i].h.Name]
		if !ok || len(out[i].order) < 2 {
			continue
		}
		gpus := out[i].h.Snap.GPUs
		order := make([]int, len(out[i].order))
		copy(order, out[i].order)
		slotRank := func(pos int) int {
			if r, ok := ranks[gpus[pos].Index]; ok {
				return r
			}
			return len(ranks)
		}
		sort.SliceStable(order, func(a, b int) bool { return slotRank(order[a]) < slotRank(order[b]) })
		out[i].order = order
	}
	return out
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

// hostPanelCount is the number of GPU panels a host contributes, in render
// order (hr.order is empty for a host with no snapshot).
func hostPanelCount(hr hostRender) int { return len(hr.order) }

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
		rows += (hostPanelCount(hr) + cols - 1) / cols
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
		n := hostPanelCount(hr)
		for row := 0; row*cols < n; row++ {
			total += rowMaxFixed(hr, row, cols, t) + 2*chartH + 2
		}
	}
	return total
}

// rowMaxFixed is the tallest fixed content among the panels in one grid
// row — the row's shared content height before chart rows are added.
func rowMaxFixed(hr hostRender, row, cols int, t tier) int {
	n := hostPanelCount(hr)
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
func panelFixedContentLines(hr hostRender, pos int, t tier) int {
	gpu := hr.gpuAt(pos)
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
