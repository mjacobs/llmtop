package tui

import (
	"fmt"
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
}

func NewApp(hostNames []string, updates <-chan client.Update, refresh time.Duration, onRefreshChange func(time.Duration), version string) *App {
	a := &App{updates: updates, refresh: refresh, onRefresh: onRefreshChange,
		version: version, theme: DefaultTheme(), hostByName: map[string]*HostState{}}
	for _, n := range hostNames {
		h := NewHostState(n)
		a.hosts = append(a.hosts, h)
		a.hostByName[n] = h
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
	header := fmt.Sprintf("llmtop %s · refresh %s", a.version, fmtRefresh(a.refresh))
	if a.paused {
		header += " ⏸"
	}

	footer := a.theme.Dim.Render(helpText)
	footerLines := 1
	if a.showHelp {
		footer = a.theme.Dim.Render(strings.Join(fullHelp, "\n"))
		footerLines = len(fullHelp)
	}

	body := RenderOverview(a.hosts, a.selected, a.width, a.height-1-footerLines, a.theme)

	return strings.Join([]string{header, body, footer}, "\n")
}

// fmtRefresh renders the refresh interval as seconds with one decimal place
// (e.g. "1.0s", "0.3s").
func fmtRefresh(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}
