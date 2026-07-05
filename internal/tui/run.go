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

	hostNames := hostNamesFor(cfg, connects, demo)

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

	app := NewApp(hostNames, updates, refresh, onRefreshChange, version)
	p := tea.NewProgram(app, tea.WithAltScreen())
	_, err := p.Run()
	cancel()
	return err
}

// hostNamesFor computes the fixed, config-order list of host names the App
// tracks. It never changes across an onRefreshChange restart.
func hostNamesFor(cfg config.Config, connects []string, demo bool) []string {
	if demo {
		return []string{"demo-rig", "demo-node"}
	}
	names := make([]string, 0, len(cfg.Hosts)+len(connects))
	for _, h := range cfg.Hosts {
		names = append(names, h.Name)
	}
	names = append(names, connects...)
	return names
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
// GPU vendor fails to initialize (no NVML, no driver, ...), the host still
// gets a source: a goroutine that reports the error every tick so the panel
// renders as unreachable instead of the whole program refusing to start.
func startLocalHost(ctx context.Context, h config.HostConfig, updates chan<- client.Update, interval time.Duration, version string) {
	col := collector.New(h.Name, gpu.NewNVML(), procs.NewProcFS(), []llm.Adapter{llm.NewLlamaCpp()}, endpoints(h), version)
	if err := col.Init(); err != nil {
		startErrSource(ctx, h.Name, err, interval, updates)
		return
	}
	client.StartLocal(ctx, h.Name, col, interval, updates)
}

// startErrSource repeatedly reports a fixed error for a host whose
// collector failed to initialize, at the same cadence a healthy source
// would report snapshots.
func startErrSource(ctx context.Context, name string, err error, interval time.Duration, updates chan<- client.Update) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case updates <- client.Update{Host: name, Err: err}:
			case <-ctx.Done():
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// endpoints extracts static LLM endpoint URLs from a host's config.
func endpoints(h config.HostConfig) []string {
	out := make([]string, len(h.LLMEndpoints))
	for i, e := range h.LLMEndpoints {
		out[i] = e.URL
	}
	return out
}
