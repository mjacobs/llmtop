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
	// cols=1: rows = alpha 2 + bravo 1 = 3.
	// alpha GPU0's server has Metrics != nil → serverBlockLineCount 3 (was
	// 2), so its FULL fixed is 3+3+4(procs) = 10 (was 9). GPU1's server is
	// metrics-off (still 2) → FULL fixed stays 3+2+4 = 9.
	// FULL fixed = strips 12 + rows (2+10)+(2+9)+(2+3) = 12+28 = 40.
	// H = (B-40)/(2*3).
	lay := solveLayout(hrs, 100, 63) // H = 23/6 = 3
	if lay.tier != tierFull || lay.chartH != 3 {
		t.Errorf("B=63: tier=%v chartH=%d, want FULL/3", lay.tier, lay.chartH)
	}
	// Huge B: H caps at chartCapCells.
	lay = solveLayout(hrs, 100, 300)
	if lay.tier != tierFull || lay.chartH != chartCapCells {
		t.Errorf("B=300: tier=%v chartH=%d, want FULL/%d", lay.tier, lay.chartH, chartCapCells)
	}
	// B=45: FULL gives H=0 < floor → CHARTS.
	// CHARTS fixed: GPU0 = 3+3(serverblock, no procs at CHARTS tier) = 6
	// (was 5); GPU1 stays 3+2 = 5.
	// CHARTS fixed = 12 + (2+6)+(2+5)+(2+3) = 12+20 = 32; H = 13/6 = 2.
	lay = solveLayout(hrs, 100, 45)
	if lay.tier != tierCharts || lay.chartH != 2 {
		t.Errorf("B=45: tier=%v chartH=%d, want CHARTS/2", lay.tier, lay.chartH)
	}
	// B=20: even CHARTS can't reach floor → COMPACT.
	if lay := solveLayout(hrs, 100, 20); lay.tier != tierCompact {
		t.Errorf("B=20: tier=%v, want COMPACT", lay.tier)
	}
}

func TestSolveLayoutTwoColumnRowMath(t *testing.T) {
	hrs := solveFixture()
	// cols=2: alpha's two panels share ONE row → rows = 1 + 1 = 2.
	// GPU0's fixed is now 10 (metrics-bearing server's 3-line block), so
	// the row max is max(10,9) = 10, not the old 9/9 tie.
	// FULL fixed = strips 12 + (2+max(10,9))+(2+3) = 12+17 = 29.
	// H = (B-29)/(2*2); B=40 → 11/4 = 2.
	lay := solveLayout(hrs, 140, 40)
	if lay.tier != tierFull || lay.chartH != 2 {
		t.Errorf("cols=2 B=40: tier=%v chartH=%d, want FULL/2", lay.tier, lay.chartH)
	}
	// Same B in one column would NOT fit FULL (needs 40+6H) — the row
	// sharing is what buys the space.
	if lay := solveLayout(hrs, 100, 40); lay.tier == tierFull {
		t.Error("cols=1 B=40 must not be FULL")
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

func TestGPUHasProcs(t *testing.T) {
	procs := []model.GPUProc{{PID: 1, GPUIndex: 1}}
	if gpuHasProcs(procs, 0) {
		t.Error("GPU0 has no procs")
	}
	if !gpuHasProcs(procs, 1) {
		t.Error("GPU1 has a proc")
	}
}
