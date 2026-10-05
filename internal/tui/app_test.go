package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/model"
)

func testApp() (*App, chan client.Update) {
	ch := make(chan client.Update, 8)
	a := NewApp(ViewConfig{
		Hosts:             []HostSpec{{Name: "h1"}, {Name: "h2"}},
		ShowAddresses:     true,
		ShowGraphicsProcs: true,
	}, ch, time.Second, func(time.Duration) {}, "test")
	return a, ch
}

// key builds the key-press message a single printable character produces.
func key(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func TestNavigationAcrossGPUPanels(t *testing.T) {
	a, ch := testApp()
	m, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a = m.(*App)

	// No snapshots yet → zero panels → j is a no-op.
	m, _ = a.Update(key("j"))
	a = m.(*App)
	if a.selected != 0 {
		t.Fatalf("selected with no panels = %d, want 0", a.selected)
	}

	// One 1-GPU snapshot per host → 2 panels.
	for _, hostName := range []string{"h1", "h2"} {
		ch <- client.Update{Host: hostName, Snapshot: &model.Snapshot{
			Timestamp: time.Unix(1_700_000_000, 0),
			Host:      model.HostInfo{Name: hostName},
			GPUs:      []model.GPU{{Index: 0, UtilPct: 50, MemUsed: 1, MemTotal: 2}}}}
		m, _ = a.Update(a.readUpdate()())
		a = m.(*App)
	}

	m, _ = a.Update(key("j"))
	a = m.(*App)
	if a.selected != 1 {
		t.Fatalf("selected = %d, want 1", a.selected)
	}
	m, _ = a.Update(key("j")) // clamp at last panel
	a = m.(*App)
	if a.selected != 1 {
		t.Fatalf("selected = %d, want 1 (clamped)", a.selected)
	}
	m, _ = a.Update(key("k"))
	a = m.(*App)
	if a.selected != 0 {
		t.Fatalf("selected = %d, want 0", a.selected)
	}

	// enter/esc/tab are unbound: they must not disturb state.
	m, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	a = m.(*App)
	m, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	a = m.(*App)
	m, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	a = m.(*App)
	if a.selected != 0 {
		t.Fatalf("removed keys must be no-ops, selected = %d", a.selected)
	}
}

func TestQuit(t *testing.T) {
	a, _ := testApp()
	_, cmd := a.Update(key("q"))
	if cmd == nil {
		t.Fatal("q must produce a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q must quit")
	}
}

func TestUpdateMsgAppliesAndPauseDiscards(t *testing.T) {
	a, ch := testApp()
	snap := &model.Snapshot{Host: model.HostInfo{Name: "h1"},
		GPUs: []model.GPU{{Index: 0, UtilPct: 50, MemTotal: 1}}}

	ch <- client.Update{Host: "h1", Snapshot: snap}
	m, cmd := a.Update(a.readUpdate()())
	a = m.(*App)
	if a.hostByName["h1"].Snap == nil {
		t.Fatal("update must apply")
	}
	if cmd == nil {
		t.Fatal("must keep reading channel")
	}

	m, _ = a.Update(key("p"))
	a = m.(*App)
	ch <- client.Update{Host: "h1", Err: errors.New("boom")}
	m, _ = a.Update(a.readUpdate()())
	a = m.(*App)
	if a.hostByName["h1"].ErrMsg != "" {
		t.Fatal("paused updates must be discarded")
	}
}

func TestRefreshKeysSkipNoOpCallback(t *testing.T) {
	ch := make(chan client.Update, 8)
	calls := 0
	var lastArg time.Duration
	a := NewApp(ViewConfig{
		Hosts:             []HostSpec{{Name: "h1"}},
		ShowAddresses:     true,
		ShowGraphicsProcs: true,
	}, ch, minRefresh, func(d time.Duration) {
		calls++
		lastArg = d
	}, "test")

	// Already at the floor: '+' would halve below minRefresh and clamp
	// right back to the same value, so the callback must not fire.
	m, _ := a.Update(key("+"))
	a = m.(*App)
	if calls != 0 {
		t.Fatalf("calls after no-op '+' = %d, want 0", calls)
	}
	if a.refresh != minRefresh {
		t.Fatalf("refresh after no-op '+' = %v, want %v", a.refresh, minRefresh)
	}

	// '-' doubles to 500ms, an actual change, so the callback must fire
	// exactly once with the new value.
	m, _ = a.Update(key("-"))
	a = m.(*App)
	if calls != 1 {
		t.Fatalf("calls after '-' = %d, want 1", calls)
	}
	if lastArg != 500*time.Millisecond {
		t.Fatalf("callback arg = %v, want 500ms", lastArg)
	}
	if a.refresh != 500*time.Millisecond {
		t.Fatalf("refresh after '-' = %v, want 500ms", a.refresh)
	}
}

func TestViewRenders(t *testing.T) {
	a, ch := testApp()
	m, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a = m.(*App)
	ch <- client.Update{Host: "h1", Snapshot: &model.Snapshot{
		Host: model.HostInfo{Name: "h1"},
		GPUs: []model.GPU{{Index: 0, UtilPct: 50, MemUsed: 1, MemTotal: 2}}}}
	m, _ = a.Update(a.readUpdate()())
	a = m.(*App)
	out := a.render()
	if out == "" || !strings.Contains(out, "llmtop") || !strings.Contains(out, "h1") {
		t.Fatalf("view:\n%s", out)
	}
	if header := strings.SplitN(out, "\n", 2)[0]; header != "llmtop · refresh 1.0s" {
		t.Fatalf("header = %q, want version-free header", header)
	}
	if strings.Contains(out, "drill-down") {
		t.Fatalf("footer must not advertise a drill-down mode (there is none):\n%s", out)
	}
}

// The expanded help carries a build-detail line: version always, plus the
// Go toolchain from the binary's build info (test binaries have no VCS
// stamps, so commit/date are rightly absent here). The collapsed footer
// must not carry it — it lives behind '?'.
func TestHelpShowsBuildDetail(t *testing.T) {
	a, _ := testApp()
	m, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a = m.(*App)

	if !strings.HasPrefix(a.buildLine, "llmtop test") {
		t.Fatalf("buildLine = %q, want it to lead with the version", a.buildLine)
	}
	if !strings.Contains(a.buildLine, "go1.") {
		t.Fatalf("buildLine = %q, want the Go toolchain version", a.buildLine)
	}

	if out := a.render(); strings.Contains(out, "go1.") {
		t.Fatal("collapsed footer must not show build detail")
	}
	a.showHelp = true
	if out := a.render(); !strings.Contains(out, a.buildLine) {
		t.Fatalf("expanded help must contain %q:\n%s", a.buildLine, a.render())
	}
}

// TestViewFitsTinyTerminal pins that the frame never exceeds the terminal
// height: with no room for a body the header joins the footer directly (no
// blank line), and an expanded help taller than the terminal is clamped.
func TestViewFitsTinyTerminal(t *testing.T) {
	a, ch := testApp()
	ch <- client.Update{Host: "h1", Snapshot: &model.Snapshot{
		Timestamp: time.Unix(1_700_000_000, 0),
		GPUs:      []model.GPU{{Index: 0, Name: "GPU", MemTotal: 1}},
	}}
	m, _ := a.Update(a.readUpdate()())
	a = m.(*App)

	for _, tc := range []struct {
		name   string
		height int
		help   bool
	}{
		{"header+footer only", 2, false},
		{"one line short of a body", 3, false},
		{"help taller than terminal", 3, true},
		{"single line", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: tc.height})
			a = m.(*App)
			a.showHelp = tc.help
			out := a.render()
			if got := len(strings.Split(out, "\n")); got > tc.height {
				t.Fatalf("View rendered %d lines > height %d:\n%s", got, tc.height, out)
			}
			if !tc.help && tc.height == 2 && strings.Contains(out, "\n\n") {
				t.Fatalf("empty body must not leave a blank line:\n%q", out)
			}
		})
	}
}

// TestLayoutKeyCycles: 'l' walks the layout ring and the header names the
// mode whenever it is not the default heuristic.
func TestLayoutKeyCycles(t *testing.T) {
	a, _ := testApp()
	m, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a = m.(*App)
	if header := strings.SplitN(a.render(), "\n", 2)[0]; strings.Contains(header, "layout") {
		t.Fatalf("auto layout must stay out of the header: %q", header)
	}

	for _, want := range []LayoutMode{LayoutOneRow, LayoutStackByHost, LayoutAuto} {
		m, _ = a.Update(key("l"))
		a = m.(*App)
		if a.layout.Mode != want {
			t.Fatalf("after 'l': mode = %v, want %v", a.layout.Mode, want)
		}
	}

	m, _ = a.Update(key("l")) // back to one-row
	a = m.(*App)
	if header := strings.SplitN(a.render(), "\n", 2)[0]; header != "llmtop · refresh 1.0s · layout one-row" {
		t.Fatalf("header = %q, want the layout named", header)
	}
}

// TestConfiguredLayoutReachesTheApp: the [layout] preference from the config
// is the App's starting point, and its column target stays in the ring.
func TestConfiguredLayoutReachesTheApp(t *testing.T) {
	ch := make(chan client.Update, 1)
	a := NewApp(ViewConfig{
		Hosts:  []HostSpec{{Name: "h1"}},
		Layout: LayoutPref{Mode: LayoutColumns, Columns: 3},
	}, ch, time.Second, func(time.Duration) {}, "test")
	m, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a = m.(*App)
	if header := strings.SplitN(a.render(), "\n", 2)[0]; !strings.Contains(header, "layout columns 3") {
		t.Fatalf("header = %q, want the configured column target", header)
	}
	m, _ = a.Update(key("l"))
	a = m.(*App)
	if a.layout.Mode != LayoutAuto || a.layout.Columns != 3 {
		t.Fatalf("after 'l': %+v, want auto keeping the column target", a.layout)
	}
}
