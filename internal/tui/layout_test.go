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
