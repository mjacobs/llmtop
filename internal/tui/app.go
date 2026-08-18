package tui

import (
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mjacobs/llmtop/internal/client"
)

const (
	minRefresh = 250 * time.Millisecond
	maxRefresh = 10 * time.Second
)

type updateMsg client.Update

// App is the bubbletea root model: it owns per-host state, the current
// GPU-panel selection, and the refresh/pause controls. It never talks to
// collectors or the network directly — run.go wires those up and feeds
// this App a read-only channel of client.Update values.
type App struct {
	hosts      []*HostState // config order
	hostByName map[string]*HostState
	updates    <-chan client.Update
	refresh    time.Duration
	onRefresh  func(time.Duration)
	version    string

	selected      int // flat GPU-panel index in render order
	paused        bool
	showHelp      bool
	width, height int
	theme         Theme
	buildLine     string // expanded help's build-detail line, computed once
}

func NewApp(view ViewConfig, updates <-chan client.Update, refresh time.Duration, onRefreshChange func(time.Duration), version string) *App {
	a := &App{updates: updates, refresh: refresh, onRefresh: onRefreshChange,
		version: version, theme: DefaultTheme(), hostByName: map[string]*HostState{},
		buildLine: buildDetail(version)}
	for _, h := range hostStates(view) {
		a.hosts = append(a.hosts, h)
		a.hostByName[h.Name] = h
	}
	return a
}

// readUpdate returns a tea.Cmd that blocks on the update channel for one
// value and wraps it as an updateMsg. It is re-issued after every message
// (including while paused) so the channel never backs up and blocks
// producers.
func (a *App) readUpdate() tea.Cmd {
	return func() tea.Msg { return updateMsg(<-a.updates) }
}

func (a *App) Init() tea.Cmd { return a.readUpdate() }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case updateMsg:
		if !a.paused {
			if h, ok := a.hostByName[msg.Host]; ok {
				h.Apply(client.Update(msg))
			}
		}
		return a, a.readUpdate()

	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(msg)
	}
	return a, nil
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return a, tea.Quit

	case "j", "down":
		if a.selected < a.panelCount()-1 {
			a.selected++
		}

	case "k", "up":
		if a.selected > 0 {
			a.selected--
		}

	case "p":
		a.paused = !a.paused

	case "+", "=":
		next := a.refresh / 2
		if next < minRefresh {
			next = minRefresh
		}
		if next != a.refresh {
			a.refresh = next
			a.onRefresh(a.refresh)
		}

	case "-":
		next := a.refresh * 2
		if next > maxRefresh {
			next = maxRefresh
		}
		if next != a.refresh {
			a.refresh = next
			a.onRefresh(a.refresh)
		}

	case "?":
		a.showHelp = !a.showHelp
	}
	return a, nil
}

// panelCount is the number of GPU panels across all hosts — the selection
// domain (hosts without a snapshot contribute none).
func (a *App) panelCount() int {
	n := 0
	for _, h := range a.hosts {
		if h.Snap != nil {
			n += len(h.Snap.GPUs)
		}
	}
	return n
}

const helpText = "q quit · j/k select · p pause · +/- refresh · ? help"

// buildDetail renders the expanded help's first line: the display version
// plus whatever build metadata Go stamped into the binary — toolchain,
// VCS commit (short), commit date, and a dirty marker. A binary without
// VCS info (e.g. `go install module@version`) just omits those parts.
func buildDetail(version string) string {
	parts := []string{"llmtop " + version}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return parts[0]
	}
	if bi.GoVersion != "" {
		parts = append(parts, bi.GoVersion)
	}
	var rev, date, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			date = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		commit := "commit " + rev
		if modified == "true" {
			commit += " (dirty)"
		}
		parts = append(parts, commit)
	}
	if len(date) >= len("2006-01-02") {
		parts = append(parts, "built "+date[:len("2006-01-02")])
	}
	return strings.Join(parts, " · ")
}

var fullHelp = []string{
	"q / ctrl+c   quit",
	"j / down     select next GPU panel",
	"k / up       select previous GPU panel",
	"p            pause/resume updates",
	"+ / =        halve refresh interval (floor 250ms)",
	"-            double refresh interval (cap 10s)",
	"?            toggle this help",
}

func (a *App) View() string {
	header := fmt.Sprintf("llmtop · refresh %s", fmtRefresh(a.refresh))
	if a.paused {
		header += " ⏸"
	}

	footer := a.theme.Dim.Render(helpText)
	footerLines := 1
	if a.showHelp {
		lines := append([]string{a.buildLine}, fullHelp...)
		footer = a.theme.Dim.Render(strings.Join(lines, "\n"))
		footerLines = len(lines)
	}

	body := RenderOverview(a.hosts, a.selected, a.width, a.height-1-footerLines, a.theme)

	// An empty body (no height budget left) must not cost a line of its
	// own, and the whole frame never exceeds the terminal — even when the
	// expanded help alone is taller than it.
	parts := []string{header}
	if body != "" {
		parts = append(parts, body)
	}
	parts = append(parts, footer)
	return clampHeight(strings.Join(parts, "\n"), a.height)
}

// fmtRefresh renders the refresh interval as seconds with one decimal place
// (e.g. "1.0s", "0.3s").
func fmtRefresh(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}
