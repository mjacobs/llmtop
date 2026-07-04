// Package config loads llmtop's TOML configuration.
// Default path: ~/.config/llmtop/config.toml; a missing file is not an
// error — it yields Default() (monitor the local machine only).
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type LLMEndpoint struct {
	URL string `toml:"url"`
}

type HostConfig struct {
	Name         string        `toml:"name"`
	Local        bool          `toml:"local"`
	Addr         string        `toml:"addr"`
	Token        string        `toml:"token"`
	LLMEndpoints []LLMEndpoint `toml:"llm_endpoints"`
}

type AgentConfig struct {
	Listen string `toml:"listen"`
	Token  string `toml:"token"`
}

type Config struct {
	RefreshMS int          `toml:"refresh_ms"`
	Hosts     []HostConfig `toml:"hosts"`
	Agent     AgentConfig  `toml:"agent"`
}

func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "llmtop", "config.toml")
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
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
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

func validate(cfg Config) error {
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
	}
	return nil
}
