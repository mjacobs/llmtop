package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderDrilldownContent(t *testing.T) {
	h := SampleHostStates()[0] // healthy 2-GPU host
	out := RenderDrilldown(h, 0, 100, 40, DefaultTheme())
	for _, want := range []string{
		"alpha", "gpu 1/2", "util", "mem",
		"qwen36-35b-mtp", "t/s", "ctx",
		"PID", "NAME", "CLASS", "VRAM",
		"llama-server", // proc table row
	} {
		if !strings.Contains(out, want) {
			t.Errorf("drilldown missing %q\n%s", want, out)
		}
	}
	// Charts must contain braille runes.
	if !strings.ContainsFunc(out, func(r rune) bool { return r >= 0x2800 && r <= 0x28FF }) {
		t.Error("no braille chart cells rendered")
	}
}

func TestRenderDrilldownWidthAndClamp(t *testing.T) {
	h := SampleHostStates()[0]
	out := RenderDrilldown(h, 99, 80, 30, DefaultTheme()) // gpuSel clamped
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line width %d > 80: %q", w, line)
		}
	}
}

func TestRenderDrilldownDeadHost(t *testing.T) {
	h := SampleHostStates()[2] // unreachable host
	out := RenderDrilldown(h, 0, 80, 24, DefaultTheme())
	if !strings.Contains(out, "unreachable") {
		t.Errorf("dead host drilldown:\n%s", out)
	}
}
