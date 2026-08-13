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
	if lay := solveLayout(hrs, 140, 60); lay.cols != 2 {
		t.Errorf("width 140: cols = %d, want 2", lay.cols)
	}
	if lay := solveLayout(hrs, 100, 60); lay.cols != 1 {
		t.Errorf("width 100: cols = %d, want 1", lay.cols)
	}
}

func TestSolveLayoutOneRow(t *testing.T) {
	hrs := solveFixture()
	// 3 panels need (width-2)/3 - 4 >= minPanelInner=60 → width >= 194.
	lay := solveLayout(hrs, 220, 55)
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

	if lay := solveLayout(hrs, 193, 55); lay.oneRow {
		t.Error("width 193 is one column too narrow for three panels")
	}
	// Too short for CHARTS-tier charts → fall back to the stacked grid.
	if lay := solveLayout(hrs, 220, 20); lay.oneRow {
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
	lay := solveLayout(hrs, 100, 69)
	if lay.tier != tierFull || lay.chartH != 3 {
		t.Errorf("B=69: tier=%v chartH=%d, want FULL/3", lay.tier, lay.chartH)
	}

	lay = solveLayout(hrs, 100, 300)
	if lay.tier != tierFull || lay.chartH != chartCapCells {
		t.Errorf("B=300: tier=%v chartH=%d, want FULL/%d", lay.tier, lay.chartH, chartCapCells)
	}

	// FULL cannot reach the 2-cell floor at B=55. CHARTS fixed content is
	// strips 12 + GPU rows (2+8)+(2+7)+(2+5) = 38, so H=17/6=2.
	lay = solveLayout(hrs, 100, 55)
	if lay.tier != tierCharts || lay.chartH != 2 {
		t.Errorf("B=55: tier=%v chartH=%d, want CHARTS/2", lay.tier, lay.chartH)
	}

	if lay := solveLayout(hrs, 100, 45); lay.tier != tierCompact {
		t.Errorf("B=45: tier=%v, want COMPACT", lay.tier)
	}
}

func TestSolveLayoutTwoColumnRowMath(t *testing.T) {
	hrs := solveFixture()
	// cols=2: alpha shares one row and bravo uses one row. FULL fixed
	// content is strips 12 + (2+max(13,12)) + (2+6) = 35.
	// At B=43, H=(43-35)/(2*2)=2.
	lay := solveLayout(hrs, 140, 43)
	if lay.tier != tierFull || lay.chartH != 2 {
		t.Errorf("cols=2 B=43: tier=%v chartH=%d, want FULL/2", lay.tier, lay.chartH)
	}

	if lay := solveLayout(hrs, 100, 43); lay.tier == tierFull {
		t.Error("cols=1 B=43 must not be FULL")
	}
}

func TestSolveLayoutNoGPUs(t *testing.T) {
	dead := NewHostState("dead")
	dead.ErrMsg = "unreachable"
	lay := solveLayout(buildHostRenders([]*HostState{dead}), 100, 40)
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
