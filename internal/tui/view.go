package tui

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/mjacobs/llmtop/internal/model"
)

// HostSpec describes one host the App tracks: its update-routing name plus
// the display-only settings that came from its config entry.
type HostSpec struct {
	Name string // routing key: config name, or the --connect address
	Addr string // remote address ("" for local); struck from error text when addresses are hidden
	GPUs []int  // GPU indices to show, in order; nil = all in device order
}

// ViewConfig carries the config file's display-only settings into the TUI.
// Everything here shapes rendering; nothing here changes collection.
type ViewConfig struct {
	Hosts             []HostSpec
	ShowAddresses     bool
	ShowGraphicsProcs bool
	// Layout is the [layout] section's preferred arrangement. The zero
	// value is the built-in heuristic.
	Layout LayoutPref
}

// hostView is a HostSpec resolved against the global toggles, held by each
// HostState and applied to every snapshot as it arrives (filterSnapshot),
// so the solver and renderers only ever see the filtered data and their
// line accounting stays untouched.
type hostView struct {
	gpus         []int // nil = all
	showGraphics bool
	showAddrs    bool
	redact       []string // address strings struck from displayed errors
}

// passthrough reports whether the view changes nothing, letting Apply keep
// the snapshot as delivered.
func (v hostView) passthrough() bool {
	return v.gpus == nil && v.showGraphics && v.showAddrs
}

// filterSnapshot applies the view to one snapshot, returning s itself when
// nothing filters. GPU selection drops the hidden GPUs' processes too, so a
// server running only on hidden GPUs falls through mapServersToGPUs to the
// host strip instead of vanishing. Hiding graphics processes only trims the
// process table: the mem chart's total (mauve) series reads GPU.MemUsed,
// which still includes their VRAM. Hiding addresses redacts server
// endpoints to ":port" — GenTPSHist is keyed by the redacted endpoint from
// the first sample on, so history stays consistent.
func (v hostView) filterSnapshot(s *model.Snapshot) *model.Snapshot {
	if v.passthrough() {
		return s
	}
	out := *s
	if v.gpus != nil {
		out.GPUs = make([]model.GPU, 0, len(v.gpus))
		for _, want := range v.gpus {
			for _, g := range s.GPUs {
				if g.Index == want {
					out.GPUs = append(out.GPUs, g)
					break
				}
			}
		}
		visible := make(map[int]bool, len(out.GPUs))
		for _, g := range out.GPUs {
			visible[g.Index] = true
		}
		out.Procs = filterProcs(out.Procs, func(p model.GPUProc) bool { return visible[p.GPUIndex] })
	}
	if !v.showGraphics {
		out.Procs = filterProcs(out.Procs, func(p model.GPUProc) bool { return p.Class != model.ProcGraphics })
	}
	if !v.showAddrs {
		servers := make([]model.LLMServer, len(out.Servers))
		copy(servers, out.Servers)
		for i := range servers {
			servers[i].Endpoint = redactEndpoint(servers[i].Endpoint)
		}
		out.Servers = servers
	}
	return &out
}

// filterProcs returns the processes keep admits, always in a fresh slice so
// the caller never mutates the snapshot it was handed.
func filterProcs(procs []model.GPUProc, keep func(model.GPUProc) bool) []model.GPUProc {
	out := make([]model.GPUProc, 0, len(procs))
	for _, p := range procs {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}

// redactEndpoint strips the host from an endpoint URL, keeping the port so
// servers on one host stay distinguishable: "http://192.0.2.10:8080" →
// ":8080". Anything without a recognizable port redacts entirely.
func redactEndpoint(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Port() != "" {
		return ":" + u.Port()
	}
	return "…"
}

// scrubErr strikes the host's known address strings from an error message
// (dial/HTTP errors embed the target URL) before it reaches the banner.
func (v hostView) scrubErr(msg string) string {
	for _, r := range v.redact {
		msg = strings.ReplaceAll(msg, r, "…")
	}
	return msg
}

// redactStrings lists the substrings of addr worth striking from error
// text, longest first: the address as configured, its host:port without a
// scheme or trailing path, and the bare host (DNS errors mention it alone).
func redactStrings(addr string) []string {
	if addr == "" {
		return nil
	}
	out := []string{addr}
	hostport := addr
	if i := strings.Index(hostport, "://"); i >= 0 {
		hostport = hostport[i+3:]
	}
	hostport = strings.TrimRight(hostport, "/")
	if hostport != addr && hostport != "" {
		out = append(out, hostport)
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil && host != "" {
		out = append(out, host)
	} else if hostport != "" {
		// No port (e.g. addr = "https://gpu.example.com"): the remainder
		// is the bare host, already covered unless a path was trimmed.
		out = append(out, hostport)
	}
	return dedupeStrings(out)
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// hostStates builds the App's HostState list from a ViewConfig, resolving
// each spec against the global toggles. With addresses hidden, hosts
// display as host1, host2, … in config order.
func hostStates(view ViewConfig) []*HostState {
	states := make([]*HostState, 0, len(view.Hosts))
	for i, spec := range view.Hosts {
		h := NewHostState(spec.Name)
		h.view = hostView{
			gpus:         spec.GPUs,
			showGraphics: view.ShowGraphicsProcs,
			showAddrs:    view.ShowAddresses,
		}
		if !view.ShowAddresses {
			h.Display = fmt.Sprintf("host%d", i+1)
			h.view.redact = redactStrings(spec.Addr)
		}
		states = append(states, h)
	}
	return states
}
