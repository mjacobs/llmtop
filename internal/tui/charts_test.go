package tui

import "testing"

func TestSparklineShape(t *testing.T) {
	got := Sparkline([]float64{0, 25, 50, 75, 100}, 100, 5)
	want := "▁▂▄▆█"
	if got != want {
		t.Errorf("Sparkline = %q, want %q", got, want)
	}
}

func TestSparklinePadAndTrim(t *testing.T) {
	if got := Sparkline([]float64{100}, 100, 3); got != "  █" {
		t.Errorf("padded = %q, want '  █'", got)
	}
	// More values than width → keep newest (rightmost).
	if got := Sparkline([]float64{100, 0, 0, 0}, 100, 3); got != "▁▁▁" {
		t.Errorf("trimmed = %q, want '▁▁▁'", got)
	}
	if got := Sparkline(nil, 100, 4); got != "    " {
		t.Errorf("empty = %q", got)
	}
	// Clamp out-of-range and max<=0 default.
	if got := Sparkline([]float64{500, -10}, 0, 2); got != "█▁" {
		t.Errorf("clamped = %q", got)
	}
}

func TestBrailleGraphShapeAndClasses(t *testing.T) {
	// Ramp: total 0→100, llm always half of total.
	var total, llm []float64
	for i := 0; i <= 20; i++ {
		total = append(total, float64(i*5))
		llm = append(llm, float64(i*5)/2)
	}
	rows := BrailleGraph(total, llm, 100, 10, 4)
	if len(rows) != 4 || len(rows[0]) != 10 {
		t.Fatalf("dims = %dx%d, want 4x10", len(rows), len(rows[0]))
	}
	// Bottom-right cell is filled and top-left is empty for a rising ramp.
	if rows[3][9][0] == " " {
		t.Error("bottom-right must be filled")
	}
	if rows[0][0][0] != " " {
		t.Error("top-left must be empty")
	}
	// Classes: bottom row cells (fully under llm boundary in early columns)
	// include some "llm"; the chart top edge is "total".
	sawLLM, sawTotal := false, false
	for _, row := range rows {
		for _, cell := range row {
			switch cell[1] {
			case "llm":
				sawLLM = true
			case "total":
				sawTotal = true
			}
		}
	}
	if !sawLLM || !sawTotal {
		t.Errorf("classes: llm=%v total=%v, want both", sawLLM, sawTotal)
	}
	// Empty cells have empty class.
	if rows[0][0][1] != "" {
		t.Error("empty cell must have empty class")
	}
}
