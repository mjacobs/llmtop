package config

import (
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

[[hosts]]
name  = "local"
local = true

  [[hosts.llm_endpoints]]
  url = "http://127.0.0.1:8081"

[[hosts]]
name  = "charlie"
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
	if len(cfg.Hosts[0].LLMEndpoints) != 1 || cfg.Hosts[0].LLMEndpoints[0].URL != "http://127.0.0.1:8081" {
		t.Fatalf("llm_endpoints = %+v", cfg.Hosts[0].LLMEndpoints)
	}
	if cfg.Agent.Listen != ":9000" {
		t.Fatalf("agent = %+v", cfg.Agent)
	}
}

func TestLoadMissingFileGivesDefault(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal("missing config file must not be an error")
	}
	assertDefault(t, cfg)
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
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"both local and addr":     "[[hosts]]\nname=\"x\"\nlocal=true\naddr=\"h:1\"\n",
		"neither":                 "[[hosts]]\nname=\"x\"\n",
		"dup names":               "[[hosts]]\nname=\"x\"\nlocal=true\n[[hosts]]\nname=\"x\"\naddr=\"h:1\"\n",
		"llm_endpoints on remote": "[[hosts]]\nname=\"x\"\naddr=\"h:1\"\n[[hosts.llm_endpoints]]\nurl=\"http://127.0.0.1:8081\"\n",
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}
