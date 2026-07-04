package tui

import "github.com/charmbracelet/lipgloss"

// Catppuccin mocha — the only place color constants live.
const (
	cBase     = "#1e1e2e"
	cSurface0 = "#313244"
	cSurface1 = "#45475a"
	cText     = "#cdd6f4"
	cSubtext0 = "#a6adc8"
	cOverlay0 = "#6c7086"
	cBlue     = "#89b4fa"
	cGreen    = "#a6e3a1"
	cMauve    = "#cba6f7"
	cRed      = "#f38ba8"
	cYellow   = "#f9e2af"
	cPeach    = "#fab387"
	cTeal     = "#94e2d5"
	cLavender = "#b4befe"
)

type Theme struct {
	Border       lipgloss.Style // rounded border, surface1 fg
	BorderActive lipgloss.Style // rounded border, lavender fg (selected host)
	Title        lipgloss.Style // host names — lavender, bold
	Text         lipgloss.Style // text
	Dim          lipgloss.Style // subtext0/overlay0 for hints & units
	Err          lipgloss.Style // red
	Util         lipgloss.Style // blue   — util series & percent
	Mem          lipgloss.Style // mauve  — mem series & bytes
	LLM          lipgloss.Style // green  — llm split/series & tok/s
	Warm         lipgloss.Style // peach  — temperature
	Power        lipgloss.Style // yellow — power draw
}

func DefaultTheme() Theme {
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(cSurface1))
	return Theme{
		Border:       border,
		BorderActive: border.BorderForeground(lipgloss.Color(cLavender)),
		Title:        lipgloss.NewStyle().Foreground(lipgloss.Color(cLavender)).Bold(true),
		Text:         lipgloss.NewStyle().Foreground(lipgloss.Color(cText)),
		Dim:          lipgloss.NewStyle().Foreground(lipgloss.Color(cOverlay0)),
		Err:          lipgloss.NewStyle().Foreground(lipgloss.Color(cRed)),
		Util:         lipgloss.NewStyle().Foreground(lipgloss.Color(cBlue)),
		Mem:          lipgloss.NewStyle().Foreground(lipgloss.Color(cMauve)),
		LLM:          lipgloss.NewStyle().Foreground(lipgloss.Color(cGreen)),
		Warm:         lipgloss.NewStyle().Foreground(lipgloss.Color(cPeach)),
		Power:        lipgloss.NewStyle().Foreground(lipgloss.Color(cYellow)),
	}
}
