package tui

import "strings"

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Sparkline renders vals (newest last) as one row of block-eighths,
// exactly width runes, right-aligned.
func Sparkline(vals []float64, max float64, width int) string {
	if max <= 0 {
		max = 100
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	var b strings.Builder
	for i := 0; i < width-len(vals); i++ {
		b.WriteByte(' ')
	}
	for _, v := range vals {
		frac := clamp01(v / max)
		idx := int(frac * float64(len(sparkRunes)-1))
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

// BrailleGraph renders a filled area chart on a braille canvas.
// Each cell is 2 dots wide × 4 dots tall. Cell classes let the caller
// color the llm share differently from the total.
func BrailleGraph(total, llm []float64, max float64, width, height int) [][][2]string {
	if max <= 0 {
		max = 100
	}
	cols := width * 2  // dot columns
	rows := height * 4 // dot rows
	if len(total) > cols {
		total = total[len(total)-cols:]
	}
	if len(llm) > cols {
		llm = llm[len(llm)-cols:]
	}
	// Right-align: column c corresponds to total[len(total)-(cols-c)].
	totalH := make([]int, cols) // filled dot count from bottom, 0..rows
	llmH := make([]int, cols)
	for c := 0; c < cols; c++ {
		ti := len(total) - (cols - c)
		if ti >= 0 {
			totalH[c] = int(clamp01(total[ti]/max)*float64(rows) + 0.5)
			li := len(llm) - (cols - c)
			if li >= 0 && li < len(llm) {
				h := int(clamp01(llm[li]/max)*float64(rows) + 0.5)
				if h > totalH[c] {
					h = totalH[c]
				}
				llmH[c] = h
			}
		}
	}
	// Braille dot bit positions: (dx, dy) → bit. Unicode braille base U+2800.
	dotBit := [2][4]int{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}
	out := make([][][2]string, height)
	for cy := 0; cy < height; cy++ {
		out[cy] = make([][2]string, width)
		for cx := 0; cx < width; cx++ {
			bits, llmDots, totalDots := 0, 0, 0
			for dx := 0; dx < 2; dx++ {
				for dy := 0; dy < 4; dy++ {
					c := cx*2 + dx
					// dotRow 0 is the TOP dot of this cell.
					fromBottom := (height-1-cy)*4 + (3 - dy)
					if fromBottom < totalH[c] {
						bits |= dotBit[dx][dy]
						if fromBottom < llmH[c] {
							llmDots++
						} else {
							totalDots++
						}
					}
				}
			}
			cell := [2]string{" ", ""}
			if bits != 0 {
				class := "total"
				if totalDots == 0 {
					class = "llm"
				}
				cell = [2]string{string(rune(0x2800 + bits)), class}
			}
			out[cy][cx] = cell
		}
	}
	return out
}
