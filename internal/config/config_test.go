package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFull(t *testing.T) {
	p := write(t, `
refresh_ms = 500

[ui]
show_addresses      = false
show_graphics_procs = false

[[hosts]]
name  = "local"
local = true
gpus  = [1, 0]

  [[hosts.llm_endpoints]]
  url        = "http://127.0.0.1:8081"
  api_key    = "sk-local"
  sleep_idle = true

  [[hosts.llm_endpoints]]
  url = "http://127.0.0.1:8082"

[[hosts]]
name  = "remote-host"
addr  = "192.0.2.40:9832"
token = "s3cret"

[agent]
listen = ":9000"
token  = "s3cret"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshMS != 500 {
		t.Fatalf("RefreshMS = %d", cfg.RefreshMS)
	}
	if len(cfg.Hosts) != 2 || !cfg.Hosts[0].Local || cfg.Hosts[1].Addr != "192.0.2.40:9832" {
		t.Fatalf("hosts = %+v", cfg.Hosts)
	}
	if len(cfg.Hosts[0].LLMEndpoints) != 2 || cfg.Hosts[0].LLMEndpoints[0].URL != "http://127.0.0.1:8081" ||
		cfg.Hosts[0].LLMEndpoints[0].APIKey != "sk-local" {
		t.Fatalf("llm_endpoints = %+v", cfg.Hosts[0].LLMEndpoints)
	}
	if !cfg.Hosts[0].LLMEndpoints[0].SleepIdle {
		t.Fatalf("llm_endpoints[0].SleepIdle = false, want true")
	}
	if cfg.Hosts[0].LLMEndpoints[1].SleepIdle {
		t.Fatalf("llm_endpoints[1].SleepIdle = true, want false (default, key omitted)")
	}
	if cfg.Agent.Listen != ":9000" {
		t.Fatalf("agent = %+v", cfg.Agent)
	}
	if cfg.UI.ShowAddresses || cfg.UI.ShowGraphicsProcs {
		t.Fatalf("ui = %+v, want both toggles false", cfg.UI)
	}
	if got := cfg.Hosts[0].GPUs; len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Fatalf("gpus = %v, want [1 0]", got)
	}
	if cfg.Hosts[1].GPUs != nil {
		t.Fatalf("gpus on host without the key = %v, want nil (all GPUs)", cfg.Hosts[1].GPUs)
	}
}

// The UI toggles must default to true both with no config file (Default)
// and when the file exists but omits them — [ui] absent entirely, and [ui]
// present with only one key set.
func TestUIDefaultsOn(t *testing.T) {
	cfg, err := Load(write(t, "refresh_ms = 250\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UI.ShowAddresses || !cfg.UI.ShowGraphicsProcs {
		t.Fatalf("ui with no [ui] table = %+v, want both true", cfg.UI)
	}

	cfg, err = Load(write(t, "[ui]\nshow_addresses = false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.ShowAddresses {
		t.Fatal("show_addresses = false was not honoured")
	}
	if !cfg.UI.ShowGraphicsProcs {
		t.Fatal("show_graphics_procs must stay true when only show_addresses is set")
	}
}

// DefaultPath prefers the canonical llmtop/config.toml, falls back to the
// flat llmtop.toml when only it exists, and names the canonical path when
// neither exists.
func TestDefaultPathFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	canonical := filepath.Join(dir, "llmtop", "config.toml")
	flat := filepath.Join(dir, "llmtop.toml")

	if got := DefaultPath(); got != canonical {
		t.Fatalf("no files: DefaultPath = %q, want %q", got, canonical)
	}
	if err := os.WriteFile(flat, []byte("refresh_ms = 250\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DefaultPath(); got != flat {
		t.Fatalf("flat only: DefaultPath = %q, want %q", got, flat)
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("refresh_ms = 250\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DefaultPath(); got != canonical {
		t.Fatalf("both present: DefaultPath = %q, want canonical %q", got, canonical)
	}
}

func TestLoadMissingFileGivesDefault(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal("missing config file must not be an error")
	}
	assertDefault(t, cfg)
}

func TestLoadExplicit(t *testing.T) {
	t.Run("missing file is an error", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "nope.toml")
		_, err := LoadExplicit(p)
		if err == nil {
			t.Fatal("want error for a missing --config file, got nil")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want it to wrap fs.ErrNotExist", err)
		}
		if want := "config " + p + ": no such file or directory"; err.Error() != want {
			t.Errorf("err = %q, want %q", err, want)
		}
	})
	t.Run("existing file loads like Load", func(t *testing.T) {
		cfg, err := LoadExplicit(write(t, "refresh_ms = 250\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RefreshMS != 250 {
			t.Fatalf("RefreshMS = %d, want 250", cfg.RefreshMS)
		}
	})
}

func TestDefault(t *testing.T) { assertDefault(t, Default()) }

func assertDefault(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.RefreshMS != 1000 {
		t.Fatalf("RefreshMS = %d, want 1000", cfg.RefreshMS)
	}
	if len(cfg.Hosts) != 1 || !cfg.Hosts[0].Local || cfg.Hosts[0].Name == "" {
		t.Fatalf("default hosts = %+v, want single local host named after hostname", cfg.Hosts)
	}
	if cfg.Agent.Listen != ":9832" {
		t.Fatalf("agent listen = %q, want :9832", cfg.Agent.Listen)
	}
	if !cfg.UI.ShowAddresses || !cfg.UI.ShowGraphicsProcs {
		t.Fatalf("default ui = %+v, want both toggles true", cfg.UI)
	}
	if cfg.Layout.EffectiveMode() != LayoutModeAuto || len(cfg.Layout.Slots) != 0 {
		t.Fatalf("default layout = %+v, want the auto heuristic with no slots", cfg.Layout)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"both local and addr":     "[[hosts]]\nname=\"x\"\nlocal=true\naddr=\"h:1\"\n",
		"neither":                 "[[hosts]]\nname=\"x\"\n",
		"dup names":               "[[hosts]]\nname=\"x\"\nlocal=true\n[[hosts]]\nname=\"x\"\naddr=\"h:1\"\n",
		"llm_endpoints on remote": "[[hosts]]\nname=\"x\"\naddr=\"h:1\"\n[[hosts.llm_endpoints]]\nurl=\"http://127.0.0.1:8081\"\n",
		"duplicate gpu index":     "[[hosts]]\nname=\"x\"\nlocal=true\ngpus=[0,1,0]\n",
		"negative gpu index":      "[[hosts]]\nname=\"x\"\nlocal=true\ngpus=[-1]\n",
		"unknown endpoint kind":   "[[hosts]]\nname=\"x\"\nlocal=true\n[[hosts.llm_endpoints]]\nurl=\"http://127.0.0.1:8081\"\nkind=\"vLLM\"\n",
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

// kind names the engine behind a static endpoint so the right adapter
// queries it; the known kinds load and an absent kind stays llama.cpp.
func TestLLMEndpointKind(t *testing.T) {
	body := "[[hosts]]\nname=\"x\"\nlocal=true\n" +
		"[[hosts.llm_endpoints]]\nurl=\"http://127.0.0.1:8000\"\nkind=\"vllm\"\n" +
		"[[hosts.llm_endpoints]]\nurl=\"http://127.0.0.1:8001\"\nkind=\"ninfer\"\n" +
		"[[hosts.llm_endpoints]]\nurl=\"http://127.0.0.1:8002\"\n"
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vllm", "ninfer", ""}
	eps := cfg.Hosts[0].LLMEndpoints
	if len(eps) != len(want) {
		t.Fatalf("llm_endpoints = %+v", eps)
	}
	for i, k := range want {
		if eps[i].Kind != k {
			t.Errorf("[%d] kind = %q, want %q", i, eps[i].Kind, k)
		}
	}
}

func TestLoadLayoutSection(t *testing.T) {
	p := write(t, `
[layout]
mode    = "columns"
columns = 3

  [[layout.slots]]
  host = "alpha"
  gpu  = 1

  [[layout.slots]]
  host = "alpha"
  gpu  = 0

  [[layout.slots]]
  host = "bravo"
  gpu  = 0
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Layout.EffectiveMode() != LayoutModeColumns || cfg.Layout.Columns != 3 {
		t.Fatalf("layout = %+v, want columns/3", cfg.Layout)
	}
	want := []LayoutSlot{{Host: "alpha", GPU: 1}, {Host: "alpha", GPU: 0}, {Host: "bravo", GPU: 0}}
	if len(cfg.Layout.Slots) != len(want) {
		t.Fatalf("slots = %+v, want %+v", cfg.Layout.Slots, want)
	}
	for i, w := range want {
		if cfg.Layout.Slots[i] != w {
			t.Errorf("slots[%d] = %+v, want %+v", i, cfg.Layout.Slots[i], w)
		}
	}
}

func TestLayoutEffectiveMode(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"absent section":   {body: "", want: LayoutModeAuto},
		"explicit auto":    {body: "[layout]\nmode = \"auto\"\n", want: LayoutModeAuto},
		"one row":          {body: "[layout]\nmode = \"one-row\"\n", want: LayoutModeOneRow},
		"stack by host":    {body: "[layout]\nmode = \"stack-by-host\"\n", want: LayoutModeStackByHost},
		"columns implied":  {body: "[layout]\ncolumns = 4\n", want: LayoutModeColumns},
		"columns explicit": {body: "[layout]\nmode = \"columns\"\ncolumns = 2\n", want: LayoutModeColumns},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(write(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Layout.EffectiveMode(); got != tc.want {
				t.Fatalf("EffectiveMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLayoutValidationErrors(t *testing.T) {
	cases := map[string]string{
		"unknown mode":         "[layout]\nmode = \"diagonal\"\n",
		"columns without n":    "[layout]\nmode = \"columns\"\n",
		"negative columns":     "[layout]\ncolumns = -1\n",
		"columns with one-row": "[layout]\nmode = \"one-row\"\ncolumns = 2\n",
		"slot without host":    "[layout]\n[[layout.slots]]\ngpu = 0\n",
		"negative slot gpu":    "[layout]\n[[layout.slots]]\nhost = \"a\"\ngpu = -1\n",
		"duplicate slot": "[layout]\n" +
			"[[layout.slots]]\nhost = \"a\"\ngpu = 0\n" +
			"[[layout.slots]]\nhost = \"a\"\ngpu = 0\n",
		"host split across runs": "[layout]\n" +
			"[[layout.slots]]\nhost = \"a\"\ngpu = 0\n" +
			"[[layout.slots]]\nhost = \"b\"\ngpu = 0\n" +
			"[[layout.slots]]\nhost = \"a\"\ngpu = 1\n",
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}
