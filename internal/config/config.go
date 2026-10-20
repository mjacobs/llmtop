// Package config loads llmtop's TOML configuration.
// Default path: ~/.config/llmtop/config.toml; a missing file is not an
// error — it yields Default() (monitor the local machine only). A path
// the user named explicitly (LoadExplicit) must exist.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/mjacobs/llmtop/internal/llm"
)

type LLMEndpoint struct {
	URL string `toml:"url"`
	// Kind names the engine serving this URL ("llama.cpp", "vllm",
	// "ninfer"); it picks the adapter that queries it. Empty means
	// llama.cpp.
	Kind string `toml:"kind"`
	// APIKey, when set, is sent as "Authorization: Bearer <APIKey>" on
	// requests to this endpoint — for a llama-server started with its own
	// --api-key flag.
	APIKey string `toml:"api_key"`
	// SleepIdle arms the /metrics sleep-idle gate for this endpoint. Process
	// discovery infers the gate from a running server's --sleep-idle-seconds
	// cmdline flag, but a static llm_endpoints entry has no process (PID 0,
	// no cmdline) to inspect, so it is polled unconditionally unless this is
	// set — set it when the server behind this URL runs with
	// --sleep-idle-seconds itself. GPU-activity is unavailable for these
	// entries too (no PID to watch), so once gated, polling resumes only on
	// the server's own is_sleeping=false transition.
	SleepIdle bool `toml:"sleep_idle"`
}

type HostConfig struct {
	Name  string `toml:"name"`
	Local bool   `toml:"local"`
	Addr  string `toml:"addr"`
	Token string `toml:"token"`
	// GPUs selects which of the host's GPUs the TUI shows and in what
	// order, by device index. nil (key absent) means all GPUs in device
	// order; an empty list hides them all. Display-only: collection and
	// the agent's wire snapshots are unaffected.
	GPUs         []int         `toml:"gpus"`
	LLMEndpoints []LLMEndpoint `toml:"llm_endpoints"`
}

type AgentConfig struct {
	Listen string `toml:"listen"`
	Token  string `toml:"token"`
}

// UIConfig holds display-only toggles for the TUI. All default to true;
// they never change what is collected or what an agent serves.
type UIConfig struct {
	// ShowAddresses=false hides network addresses from the display: server
	// endpoints render as :port; error banners, collection-error lines and
	// server status and metrics-error text have the host's configured
	// address and every IP address struck out (IPv4 or IPv6, with a port,
	// brackets or zone, inside a URL or beside punctuation; not decimal,
	// hex, octal or zero-padded IPv4, name-shaped text such as
	// ip-192-0-2-10, or an address joined to letters or digits without a
	// separator). To bound that work per snapshot, each text is cut to its
	// first 4 KB, at most 8 collection-error lines show with a count of the
	// rest, and past 32 KB of text in one snapshot the rest is hidden, not
	// shown unscrubbed. A host title that is only an address (an IPv4 or IPv6
	// address with the usual wrapping, or the same text as the host's own
	// address or its host part, as a --connect target is until its agent
	// reports a name) becomes host1, host2, …. Decimal, hex, octal and
	// zero-padded IPv4 spellings, a trailing dot, and name-shaped text such
	// as ip-192-0-2-77 are not recognised and still show. Other host names
	// stay; see ShowHostnames.
	ShowAddresses bool `toml:"show_addresses"`
	// ShowHostnames=false replaces every host title with host1, host2, …
	// in config order. Screenshot mode sets both this and ShowAddresses to
	// false.
	ShowHostnames bool `toml:"show_hostnames"`
	// ShowGraphicsProcs=false drops graphics-class rows from the GPU
	// PROCESSES table. Their VRAM stays in the mem chart's total series —
	// that comes from the GPU's own used-memory counter, not the table.
	ShowGraphicsProcs bool `toml:"show_graphics_procs"`
}

// Layout mode names for the [layout] section's `mode` key.
const (
	// LayoutModeAuto is the built-in heuristic: one row of panels when the
	// terminal is wide enough, otherwise hosts stacked in one or two columns.
	LayoutModeAuto = "auto"
	// LayoutModeOneRow asks for every GPU panel side by side in one row.
	LayoutModeOneRow = "one-row"
	// LayoutModeStackByHost asks for hosts stacked vertically, each wrapping
	// its own GPUs, and never the one-row arrangement.
	LayoutModeStackByHost = "stack-by-host"
	// LayoutModeColumns asks for the stacked grid at a target column count
	// (the `columns` key).
	LayoutModeColumns = "columns"
)

// LayoutSlot names one GPU panel: a host by its config `name` and a GPU by
// its device index (the same numbering as hosts[].gpus).
type LayoutSlot struct {
	Host string `toml:"host"`
	GPU  int    `toml:"gpu"`
}

// LayoutConfig is the [layout] section: a *preference* for how the overview
// arranges GPU panels. It never overrides the fit: the solver shrinks the
// column count and degrades through the FULL/CHARTS/COMPACT tiers whenever
// the terminal cannot hold the preferred arrangement.
type LayoutConfig struct {
	// Mode is one of the LayoutMode* constants. Empty means auto, unless
	// Columns is set, which implies "columns".
	Mode string `toml:"mode"`
	// Columns is the target column count for LayoutModeColumns.
	Columns int `toml:"columns"`
	// Slots is an optional explicit panel order. Panels it names render
	// first, in the listed order; anything it does not name follows in
	// configured order. All slots for one host must be contiguous, because
	// the overview groups panels under their host's strip.
	Slots []LayoutSlot `toml:"slots"`
}

// EffectiveMode returns the mode the solver should honour: the configured
// one, or "columns" when only a column count was given, or "auto".
func (l LayoutConfig) EffectiveMode() string {
	if l.Mode != "" {
		return l.Mode
	}
	if l.Columns > 0 {
		return LayoutModeColumns
	}
	return LayoutModeAuto
}

type Config struct {
	RefreshMS int          `toml:"refresh_ms"`
	Hosts     []HostConfig `toml:"hosts"`
	Agent     AgentConfig  `toml:"agent"`
	UI        UIConfig     `toml:"ui"`
	Layout    LayoutConfig `toml:"layout"`
}

// DefaultPath is ~/.config/llmtop/config.toml; when that file does not
// exist but the flat ~/.config/llmtop.toml does, the flat file is used
// instead. The directory form stays canonical (it's what a fresh setup
// should create) — the flat form is an accepted alternative, not a merge.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	canonical := filepath.Join(dir, "llmtop", "config.toml")
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	flat := filepath.Join(dir, "llmtop.toml")
	if _, err := os.Stat(flat); err == nil {
		return flat
	}
	return canonical
}

func Default() Config {
	name, _ := os.Hostname()
	if name == "" {
		name = "local"
	}
	return Config{
		RefreshMS: 1000,
		Hosts:     []HostConfig{{Name: name, Local: true}},
		Agent:     AgentConfig{Listen: ":9832"},
		UI:        UIConfig{ShowAddresses: true, ShowHostnames: true, ShowGraphicsProcs: true},
		// Layout stays zero: EffectiveMode reads that as "auto".
	}
}

// Load reads the config at path. A missing file is not an error: it
// yields Default(), which is what the default path usually hits.
func Load(path string) (Config, error) { return load(path, false) }

// LoadExplicit is Load for a path the user named on the command line
// (--config): a missing file is an error, not a silent fallback.
func LoadExplicit(path string) (Config, error) { return load(path, true) }

func load(path string, mustExist bool) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !mustExist {
			return cfg, nil
		}
		// Drop the "open" verb from *PathError so the message reads
		// "config /path: no such file or directory".
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	// User-provided hosts replace the default local-only list.
	cfg.Hosts = nil
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.RefreshMS <= 0 {
		cfg.RefreshMS = 1000
	}
	if cfg.Agent.Listen == "" {
		cfg.Agent.Listen = ":9832"
	}
	if len(cfg.Hosts) == 0 {
		cfg.Hosts = Default().Hosts
	}
	if err := validate(cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// validKind reports whether an llm_endpoints kind names a known engine.
// Empty is the llama.cpp default.
func validKind(kind string) bool {
	if kind == "" {
		return true
	}
	for _, k := range llm.Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

func validate(cfg Config) error {
	if err := validateLayout(cfg.Layout); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, h := range cfg.Hosts {
		if h.Name == "" {
			return fmt.Errorf("hosts[%d]: name is required", i)
		}
		if seen[h.Name] {
			return fmt.Errorf("hosts[%d]: duplicate name %q", i, h.Name)
		}
		seen[h.Name] = true
		if h.Local == (h.Addr != "") {
			return fmt.Errorf("host %q: exactly one of local=true or addr must be set", h.Name)
		}
		if !h.Local && len(h.LLMEndpoints) > 0 {
			return fmt.Errorf("host %q: llm_endpoints only apply to local hosts — declare them in that host's agent config", h.Name)
		}
		for _, e := range h.LLMEndpoints {
			if !validKind(e.Kind) {
				return fmt.Errorf("host %q: llm_endpoints url %q has unknown kind %q — use one of %s",
					h.Name, e.URL, e.Kind, strings.Join(llm.Kinds(), ", "))
			}
		}
		seenGPU := map[int]bool{}
		for _, idx := range h.GPUs {
			if idx < 0 {
				return fmt.Errorf("host %q: gpus contains negative index %d", h.Name, idx)
			}
			if seenGPU[idx] {
				return fmt.Errorf("host %q: gpus lists index %d twice", h.Name, idx)
			}
			seenGPU[idx] = true
		}
	}
	return nil
}

// validateLayout checks the [layout] section. It deliberately does not
// require a slot's host to exist: a config shared across machines may name
// a rig that is not up today, and an unmatched slot is simply ignored when
// the overview is arranged.
func validateLayout(l LayoutConfig) error {
	switch l.Mode {
	case "", LayoutModeAuto, LayoutModeOneRow, LayoutModeStackByHost, LayoutModeColumns:
	default:
		return fmt.Errorf("layout: unknown mode %q (want %q, %q, %q or %q)",
			l.Mode, LayoutModeAuto, LayoutModeOneRow, LayoutModeStackByHost, LayoutModeColumns)
	}
	if l.Columns < 0 {
		return fmt.Errorf("layout: columns must be positive, got %d", l.Columns)
	}
	if l.Mode == LayoutModeColumns && l.Columns == 0 {
		return fmt.Errorf("layout: mode = %q needs a columns count", LayoutModeColumns)
	}
	if l.Columns > 0 && (l.Mode == LayoutModeOneRow || l.Mode == LayoutModeStackByHost) {
		return fmt.Errorf("layout: columns only applies to mode = %q, not %q", LayoutModeColumns, l.Mode)
	}
	seen := map[LayoutSlot]bool{}
	group := map[string]int{} // host → index of the contiguous run it owns
	run := -1
	prev := ""
	for i, s := range l.Slots {
		if s.Host == "" {
			return fmt.Errorf("layout.slots[%d]: host is required", i)
		}
		if s.GPU < 0 {
			return fmt.Errorf("layout.slots[%d]: negative gpu index %d", i, s.GPU)
		}
		if seen[s] {
			return fmt.Errorf("layout.slots[%d]: duplicate slot host %q gpu %d", i, s.Host, s.GPU)
		}
		seen[s] = true
		if s.Host != prev {
			run++
			prev = s.Host
		}
		if at, ok := group[s.Host]; ok && at != run {
			return fmt.Errorf("layout.slots[%d]: host %q appears in two separate runs; "+
				"list a host's GPUs together (the overview groups panels under their host)", i, s.Host)
		}
		group[s.Host] = run
	}
	return nil
}
