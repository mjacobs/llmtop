// Package tui is llmtop's terminal front end: a bubbletea program that turns
// a stream of client.Update values into a per-host, per-GPU dashboard.
//
// The pipeline, file by file:
//
//   - run.go: the composition root. Builds the update sources (local
//     collector, remote agents, or the demo feed), wires them to an App, and
//     runs the bubbletea program.
//   - app.go: the bubbletea root model. Owns per-host state, the selected
//     GPU panel, pause/refresh controls, and the header/footer chrome.
//   - state.go: HostState — the latest Snapshot per host plus rolling
//     history rings (util/mem per GPU, gen t/s per server) that feed the charts.
//   - layout.go: the per-frame density solver. Picks the layout (one row,
//     stacked grid, or compact) and tier (FULL/CHARTS/COMPACT) that fits the
//     terminal, counting lines without rendering.
//   - overview.go, panel.go: the renderers — host strips, per-GPU panels,
//     server blocks, process tables, and the compact fallback. Their line
//     accounting must match the solver exactly (see the IMPORTANT comments).
//   - charts.go: braille area-chart and block-eighth sparkline primitives.
//   - theme.go: the color palette and lipgloss styles.
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/collector"
	"github.com/mjacobs/llmtop/internal/config"
	"github.com/mjacobs/llmtop/internal/gpu"
	"github.com/mjacobs/llmtop/internal/llm"
	"github.com/mjacobs/llmtop/internal/procs"
)

// updateChanBuf sizes the client.Update channel generously so a slow
// terminal render never makes a producer (collector tick, remote poll)
// block or drop a sample.
const updateChanBuf = 64

// Run is the composition root: it builds the update sources described by
// cfg (or --demo, or --connect targets), wires them into an App, and runs
// the bubbletea program until the user quits.
func Run(cfg config.Config, connects []string, demo bool, version string) error {
	updates := make(chan client.Update, updateChanBuf)
	refresh := time.Duration(cfg.RefreshMS) * time.Millisecond

	view := viewFor(cfg, connects, demo)

	ctx, cancel := context.WithCancel(context.Background())
	startSources(ctx, cfg, connects, demo, updates, refresh, version)

	// onRefreshChange restarts every source against a fresh context at the
	// new interval. Remote sources backfill on (re)connect, so the brief
	// gap this causes is invisible in the charts.
	onRefreshChange := func(d time.Duration) {
		cancel()
		ctx, cancel = context.WithCancel(context.Background())
		startSources(ctx, cfg, connects, demo, updates, d, version)
	}

	app := NewApp(view, updates, refresh, onRefreshChange, version)
	p := tea.NewProgram(app, tea.WithAltScreen())
	_, err := p.Run()
	cancel()
	return err
}

// viewFor computes the fixed, config-order host list and display settings
// the App tracks. It never changes across an onRefreshChange restart. The
// UI toggles apply in demo mode too (handy for previewing them without
// hardware); per-host gpus lists don't — demo hosts aren't config entries.
func viewFor(cfg config.Config, connects []string, demo bool) ViewConfig {
	view := ViewConfig{
		ShowAddresses:     cfg.UI.ShowAddresses,
		ShowGraphicsProcs: cfg.UI.ShowGraphicsProcs,
	}
	if demo {
		view.Hosts = []HostSpec{{Name: "demo-rig"}, {Name: "demo-node"}}
		return view
	}
	for _, h := range cfg.Hosts {
		view.Hosts = append(view.Hosts, HostSpec{Name: h.Name, Addr: h.Addr, GPUs: h.GPUs})
	}
	for _, addr := range connects {
		view.Hosts = append(view.Hosts, HostSpec{Name: addr, Addr: addr})
	}
	return view
}

// startSources launches one goroutine-based update source per configured
// host (plus any --connect targets), all rooted at ctx so onRefreshChange
// can tear them all down with a single cancel.
func startSources(ctx context.Context, cfg config.Config, connects []string, demo bool, updates chan<- client.Update, interval time.Duration, version string) {
	if demo {
		client.StartDemo(ctx, updates, interval)
		return
	}
	for _, h := range cfg.Hosts {
		if h.Local {
			startLocalHost(ctx, h, updates, interval, version)
			continue
		}
		client.StartRemote(ctx, h.Name, h.Addr, h.Token, interval, updates)
	}
	for _, addr := range connects {
		client.StartRemote(ctx, addr, addr, "", interval, updates)
	}
}

// startLocalHost builds a collector for a local host config entry. If the
// GPU vendor fails to initialize (no NVML, no driver, ...), the collector
// is started anyway: every snapshot then carries the GPU error in its Errors
// (rendered in the host strip) while processes and LLM servers are
// still reported — the same degraded mode `llmtop agent` runs in.
func startLocalHost(ctx context.Context, h config.HostConfig, updates chan<- client.Update, interval time.Duration, version string) {
	col := collector.New(h.Name, gpu.NewNVML(), procs.NewProcFS(), llm.Adapters(), endpoints(h), version)
	_ = col.Init() // a failed GPU init is reported per tick via Snapshot.Errors
	client.StartLocal(ctx, h.Name, col, interval, updates)
}

// endpoints extracts a host's statically declared LLM servers, each with
// the engine kind that decides which adapter queries it and its optional
// api_key.
func endpoints(h config.HostConfig) []llm.StaticEndpoint {
	out := make([]llm.StaticEndpoint, len(h.LLMEndpoints))
	for i, e := range h.LLMEndpoints {
		out[i] = llm.StaticEndpoint{URL: e.URL, Kind: e.Kind, APIKey: e.APIKey}
	}
	return out
}
