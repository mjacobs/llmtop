package tui

import (
	"testing"

	"github.com/mjacobs/llmtop/internal/model"
)

// solveFixture returns the sample hosts' hostRenders: alpha (2 GPUs, procs,
// both servers GPU-mapped), bravo (1 GPU, no procs, 1 host-level
// server), charlie (unreachable).
func solveFixture() []hostRender {
	return buildHostRenders(SampleHostStates())
}

func TestSolveLayoutColumns(t *testing.T) {
	hrs := solveFixture()
	// Two-up needs (width-panelGap)/2 - 4 >= minPanelInner=60 → width >= 129.
	if lay := solveLayout(hrs, 140, 60, LayoutPref{}); lay.cols != 2 {
		t.Errorf("width 140: cols = %d, want 2", lay.cols)
	}
	if lay := solveLayout(hrs, 100, 60, LayoutPref{}); lay.cols != 1 {
		t.Errorf("width 100: cols = %d, want 1", lay.cols)
	}
}

func TestSolveLayoutOneRow(t *testing.T) {
	hrs := solveFixture()
	// 3 panels need (width-2)/3 - 4 >= minPanelInner=60 → width >= 194.
	lay := solveLayout(hrs, 220, 55, LayoutPref{})
	if !lay.oneRow {
		t.Fatalf("width 220: want one-row layout, got %+v", lay)
	}
	if got := len(lay.widths); got != 3 {
		t.Fatalf("one-row widths = %d, want one per panel (3)", got)
	}
	// Widths fill the terminal exactly: panels plus the gaps between them.
	sum := 2 * panelGap
	for _, w := range lay.widths {
		sum += w
	}
	if sum != 220 {
		t.Errorf("one-row widths sum to %d, want 220", sum)
	}

	if lay := solveLayout(hrs, 193, 55, LayoutPref{}); lay.oneRow {
		t.Error("width 193 is one column too narrow for three panels")
	}
	// Too short for CHARTS-tier charts → fall back to the stacked grid.
	if lay := solveLayout(hrs, 220, 20, LayoutPref{}); lay.oneRow {
		t.Errorf("height 20 must not use the one-row layout: %+v", lay)
	}
}

func TestOneRowHeightAccounting(t *testing.T) {
	hrs := solveFixture()
	// Strip row: bravo's 3 content lines are the tallest (alpha's strip
	// is a lone title line) → 5 rows with the border. Panel row: alpha GPU0's
	// FULL fixed content is 13 → 13 + 2*chartH + 2. The GPU-less charlie host
	// stacks its own 2+2 row underneath. At chartH=0: 5 + 15 + 4 = 24.
	if got := oneRowHeight(hrs, tierFull, 0); got != 24 {
		t.Errorf("oneRowHeight(FULL, 0) = %d, want 24", got)
	}
	if got := oneRowHeight(hrs, tierFull, 4); got != 32 {
		t.Errorf("oneRowHeight(FULL, 4) = %d, want 32", got)
	}
}

func TestSplitWidths(t *testing.T) {
	// Remainder goes to the leftmost columns: 201 - 2 gaps = 199 = 67+66+66.
	if got := splitWidths(201, 3); len(got) != 3 || got[0] != 67 || got[1] != 66 || got[2] != 66 {
		t.Errorf("splitWidths(201, 3) = %v, want [67 66 66]", got)
	}
	if got := splitWidths(100, 3); got != nil {
		t.Errorf("splitWidths(100, 3) = %v, want nil (too narrow)", got)
	}
}

func TestSolveLayoutTiersSingleColumn(t *testing.T) {
	hrs := solveFixture()
	// cols=1: three panel rows. FULL fixed content is:
	// strips 12 + GPU rows (2+13)+(2+12)+(2+6) = 49.
	// At B=69, H=(69-49)/(2*3)=3.
	lay := solveLayout(hrs, 100, 69, LayoutPref{})
	if lay.tier != tierFull || lay.chartH != 3 {
		t.Errorf("B=69: tier=%v chartH=%d, want FULL/3", lay.tier, lay.chartH)
	}

	lay = solveLayout(hrs, 100, 300, LayoutPref{})
	if lay.tier != tierFull || lay.chartH != chartCapCells {
		t.Errorf("B=300: tier=%v chartH=%d, want FULL/%d", lay.tier, lay.chartH, chartCapCells)
	}

	// FULL cannot reach the 2-cell floor at B=55. CHARTS fixed content is
	// strips 12 + GPU rows (2+8)+(2+7)+(2+5) = 38, so H=17/6=2.
	lay = solveLayout(hrs, 100, 55, LayoutPref{})
	if lay.tier != tierCharts || lay.chartH != 2 {
		t.Errorf("B=55: tier=%v chartH=%d, want CHARTS/2", lay.tier, lay.chartH)
	}

	if lay := solveLayout(hrs, 100, 45, LayoutPref{}); lay.tier != tierCompact {
		t.Errorf("B=45: tier=%v, want COMPACT", lay.tier)
	}
}

func TestSolveLayoutTwoColumnRowMath(t *testing.T) {
	hrs := solveFixture()
	// cols=2: alpha shares one row and bravo uses one row. FULL fixed
	// content is strips 12 + (2+max(13,12)) + (2+6) = 35.
	// At B=43, H=(43-35)/(2*2)=2.
	lay := solveLayout(hrs, 140, 43, LayoutPref{})
	if lay.tier != tierFull || lay.chartH != 2 {
		t.Errorf("cols=2 B=43: tier=%v chartH=%d, want FULL/2", lay.tier, lay.chartH)
	}

	if lay := solveLayout(hrs, 100, 43, LayoutPref{}); lay.tier == tierFull {
		t.Error("cols=1 B=43 must not be FULL")
	}
}

func TestSolveLayoutNoGPUs(t *testing.T) {
	dead := NewHostState("dead")
	dead.ErrMsg = "unreachable"
	lay := solveLayout(buildHostRenders([]*HostState{dead}), 100, 40, LayoutPref{})
	if lay.tier != tierFull || lay.chartH != 0 {
		t.Errorf("no GPUs: tier=%v chartH=%d, want FULL/0 (strips only)", lay.tier, lay.chartH)
	}
}

func TestPanelFixedContentLinesSectionRulesByTier(t *testing.T) {
	hr := solveFixture()[1] // one GPU, no mapped servers or processes

	for _, tc := range []struct {
		name string
		tier tier
		want int
	}{
		{name: "full", tier: tierFull, want: 6},
		{name: "charts", tier: tierCharts, want: 5},
		{name: "compact", tier: tierCompact, want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := panelFixedContentLines(hr, 0, tc.tier); got != tc.want {
				t.Fatalf("fixed content lines = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGPUHasProcs(t *testing.T) {
	procs := []model.GPUProc{{PID: 1, GPUIndex: 1}}
	if gpuHasProcs(procs, 0) {
		t.Error("GPU0 has no procs")
	}
	if !gpuHasProcs(procs, 1) {
		t.Error("GPU1 has a proc")
	}
}

// TestSolveLayoutModes pins what each configured mode asks for at a width
// wide enough for any of them (220 columns fits all three panels in one row
// and up to three stacked columns).
func TestSolveLayoutModes(t *testing.T) {
	hrs := solveFixture()
	for _, tc := range []struct {
		name   string
		pref   LayoutPref
		oneRow bool
		cols   int
	}{
		{name: "auto picks one row", pref: LayoutPref{}, oneRow: true, cols: 3},
		{name: "one-row", pref: LayoutPref{Mode: LayoutOneRow}, oneRow: true, cols: 3},
		{name: "stack-by-host", pref: LayoutPref{Mode: LayoutStackByHost}, cols: 2},
		{name: "columns 3", pref: LayoutPref{Mode: LayoutColumns, Columns: 3}, cols: 3},
		{name: "columns 1", pref: LayoutPref{Mode: LayoutColumns, Columns: 1}, cols: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lay := solveLayout(hrs, 220, 55, tc.pref)
			if lay.oneRow != tc.oneRow {
				t.Errorf("oneRow = %v, want %v", lay.oneRow, tc.oneRow)
			}
			if lay.cols != tc.cols {
				t.Errorf("cols = %d, want %d", lay.cols, tc.cols)
			}
		})
	}
}

// TestSolveLayoutPreferenceDegradesOnWidth: a preference is not a mandate —
// a column target the terminal cannot hold shrinks, and one-row falls back
// to the stacked grid rather than rendering unreadable panels.
func TestSolveLayoutPreferenceDegradesOnWidth(t *testing.T) {
	hrs := solveFixture()
	// gridPanelWidth(220, 4) = 54, which is under minPanelInner+4 → 3 columns.
	if lay := solveLayout(hrs, 220, 55, LayoutPref{Mode: LayoutColumns, Columns: 4}); lay.cols != 3 {
		t.Errorf("columns 4 at width 220: cols = %d, want 3", lay.cols)
	}
	// At 100 columns nothing but a single column fits.
	if lay := solveLayout(hrs, 100, 55, LayoutPref{Mode: LayoutColumns, Columns: 3}); lay.cols != 1 {
		t.Errorf("columns 3 at width 100: cols = %d, want 1", lay.cols)
	}
	lay := solveLayout(hrs, 100, 55, LayoutPref{Mode: LayoutOneRow})
	if lay.oneRow || lay.cols != 1 {
		t.Errorf("one-row at width 100 must stack: %+v", lay)
	}
}

// TestSolveLayoutPreferenceDegradesOnHeight: every mode still walks the
// FULL → CHARTS → COMPACT ladder when the body budget shrinks.
func TestSolveLayoutPreferenceDegradesOnHeight(t *testing.T) {
	hrs := solveFixture()
	for _, pref := range []LayoutPref{
		{},
		{Mode: LayoutOneRow},
		{Mode: LayoutStackByHost},
		{Mode: LayoutColumns, Columns: 3},
	} {
		if lay := solveLayout(hrs, 220, 55, pref); lay.tier != tierFull {
			t.Errorf("%s at height 55: tier = %v, want FULL", pref.Label(), lay.tier)
		}
		if lay := solveLayout(hrs, 220, 20, pref); lay.tier != tierCompact {
			t.Errorf("%s at height 20: tier = %v, want COMPACT", pref.Label(), lay.tier)
		}
	}
}

func TestStackedColsAndGridPanelWidth(t *testing.T) {
	// Two-up is the default target and needs width >= 129.
	if got := stackedCols(128, LayoutPref{}); got != 1 {
		t.Errorf("stackedCols(128, auto) = %d, want 1", got)
	}
	if got := stackedCols(129, LayoutPref{}); got != 2 {
		t.Errorf("stackedCols(129, auto) = %d, want 2", got)
	}
	// A target above the default is honoured when it fits.
	if got := stackedCols(400, LayoutPref{Mode: LayoutColumns, Columns: 5}); got != 5 {
		t.Errorf("stackedCols(400, columns 5) = %d, want 5", got)
	}
	// gridPanelWidth drops the remainder so every column matches.
	if got := gridPanelWidth(220, 3); got != 72 {
		t.Errorf("gridPanelWidth(220, 3) = %d, want 72", got)
	}
	if got := gridPanelWidth(220, 1); got != 220 {
		t.Errorf("gridPanelWidth(220, 1) = %d, want 220", got)
	}
	// A column target far beyond what the terminal could ever fit must not
	// turn the search into a walk over that value; it should behave exactly
	// like a target clamped to the terminal width.
	if got := stackedCols(400, LayoutPref{Mode: LayoutColumns, Columns: 1_000_000_000}); got != stackedCols(400, LayoutPref{Mode: LayoutColumns, Columns: 400}) {
		t.Errorf("stackedCols(400, columns 1e9) = %d, want same as columns 400 (%d)", got, stackedCols(400, LayoutPref{Mode: LayoutColumns, Columns: 400}))
	}
}

func TestApplySlots(t *testing.T) {
	hrs := applySlots(solveFixture(), []LayoutSlot{
		{Host: "bravo", GPU: 0},
		{Host: "alpha", GPU: 1},
		{Host: "alpha", GPU: 0},
	})
	var names []string
	for _, hr := range hrs {
		names = append(names, hr.h.Name)
	}
	if want := []string{"bravo", "alpha", "charlie"}; !equalStrings(names, want) {
		t.Fatalf("host order = %v, want %v (unnamed hosts trail)", names, want)
	}
	alpha := hrs[1]
	if got := []int{alpha.gpuAt(0).Index, alpha.gpuAt(1).Index}; got[0] != 1 || got[1] != 0 {
		t.Errorf("alpha GPU order = %v, want [1 0]", got)
	}
}

// TestApplySlotsIgnoresAbsentTargets: a config shared across machines names
// hosts and GPUs that may not be present today; those slots are skipped and
// everything else keeps its configured order.
func TestApplySlotsIgnoresAbsentTargets(t *testing.T) {
	base := solveFixture()
	hrs := applySlots(base, []LayoutSlot{{Host: "delta", GPU: 0}, {Host: "alpha", GPU: 7}})
	var names []string
	for _, hr := range hrs {
		names = append(names, hr.h.Name)
	}
	if want := []string{"alpha", "bravo", "charlie"}; !equalStrings(names, want) {
		t.Errorf("host order = %v, want unchanged %v", names, want)
	}
	if hrs[0].gpuAt(0).Index != 0 || hrs[0].gpuAt(1).Index != 1 {
		t.Errorf("alpha GPU order changed by an unmatched slot")
	}
	// applySlots must not mutate the hostRenders it was handed.
	if base[0].order[0] != 0 || base[0].order[1] != 1 {
		t.Errorf("applySlots mutated its input: %v", base[0].order)
	}
}

// TestApplySlotsAbsentGPUDoesNotRankHost: a slot naming a present host but an
// absent GPU (e.g. bravo/GPU99) must not move that host ahead of others —
// only a slot matching a currently-present GPU may rank its host.
func TestApplySlotsAbsentGPUDoesNotRankHost(t *testing.T) {
	hrs := applySlots(solveFixture(), []LayoutSlot{{Host: "bravo", GPU: 99}})
	var names []string
	for _, hr := range hrs {
		names = append(names, hr.h.Name)
	}
	if want := []string{"alpha", "bravo", "charlie"}; !equalStrings(names, want) {
		t.Errorf("host order = %v, want unchanged %v (bravo has no valid slot)", names, want)
	}
}

func TestLayoutPrefNext(t *testing.T) {
	// Without a column target the ring is the three named modes.
	p := LayoutPref{}
	for _, want := range []LayoutMode{LayoutOneRow, LayoutStackByHost, LayoutAuto, LayoutOneRow} {
		p = p.Next()
		if p.Mode != want {
			t.Fatalf("cycled to %v, want %v", p.Mode, want)
		}
	}
	// A configured column target stays in the ring so it can be cycled back to.
	p = LayoutPref{Mode: LayoutStackByHost, Columns: 3}
	if p = p.Next(); p.Mode != LayoutColumns || p.Columns != 3 {
		t.Fatalf("cycled to %+v, want columns 3", p)
	}
	if p = p.Next(); p.Mode != LayoutAuto || p.Columns != 3 {
		t.Fatalf("cycled to %+v, want auto with the column target kept", p)
	}
}

func TestLayoutPrefLabel(t *testing.T) {
	for _, tc := range []struct {
		pref LayoutPref
		want string
	}{
		{pref: LayoutPref{}, want: "auto"},
		{pref: LayoutPref{Mode: LayoutOneRow}, want: "one-row"},
		{pref: LayoutPref{Mode: LayoutStackByHost}, want: "stack-by-host"},
		{pref: LayoutPref{Mode: LayoutColumns, Columns: 3}, want: "columns 3"},
	} {
		if got := tc.pref.Label(); got != tc.want {
			t.Errorf("Label() = %q, want %q", got, tc.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
