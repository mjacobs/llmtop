package tui

import "fmt"

func FmtBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%dM", b>>20)
	case b >= 1<<10:
		return fmt.Sprintf("%dK", b>>10)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func FmtMemPair(used, total uint64) string {
	return fmt.Sprintf("%.1f/%.1fG", float64(used)/(1<<30), float64(total)/(1<<30))
}
