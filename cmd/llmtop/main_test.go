package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Use a disposable default config directory even when a test passes no flags.
func cliEnv(t *testing.T, token string) func(string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return func(key string) string {
		if key == "LLMTOP_TOKEN" {
			return token
		}
		return ""
	}
}

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alpha.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommandDispatch(t *testing.T) {
	getenv := cliEnv(t, "")
	path := writeConfig(t, "refresh_ms = 250\n[[hosts]]\nname = 'alpha'\nlocal = true\n")
	for _, tc := range []struct {
		name, command string
		args          []string
		demo          bool
		connects      []string
	}{
		{name: "default", command: "tui"},
		{name: "agent", command: "agent", args: []string{"agent"}},
		{name: "version", command: "version", args: []string{"version"}},
		{name: "help", command: "help", args: []string{"help"}},
		{name: "version flag", command: "version", args: []string{"--version"}},
		{name: "version skips config", command: "version", args: []string{"--version", "--config", filepath.Join(t.TempDir(), "missing.toml")}},
		{name: "demo", command: "tui", args: []string{"--demo"}, demo: true},
		{name: "config", command: "tui", args: []string{"--config", path}},
		{name: "connect", command: "tui", args: []string{"--connect", "127.0.0.1:9832", "--connect=192.0.2.1:9832"}, connects: []string{"127.0.0.1:9832", "192.0.2.1:9832"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := parseCommand(tc.args, getenv)
			if err != nil {
				t.Fatal(err)
			}
			if cmd.name != tc.command || cmd.demo != tc.demo || !reflect.DeepEqual(cmd.connects, tc.connects) {
				t.Fatalf("command = %+v; want %s, demo=%v, connects=%v", cmd, tc.command, tc.demo, tc.connects)
			}
			if tc.name == "config" && (cmd.cfg.RefreshMS != 250 || cmd.cfg.Hosts[0].Name != "alpha") {
				t.Fatalf("config not resolved: %+v", cmd.cfg)
			}
			if tc.command == "help" && (!strings.HasPrefix(cmd.help, rootUsage) || !strings.Contains(cmd.help, "-connect")) {
				t.Fatalf("incomplete root help: %q", cmd.help)
			}
		})
	}
}

func TestStrayArguments(t *testing.T) {
	getenv := cliEnv(t, "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"alpha"}, `llmtop: unexpected argument "alpha" (try: llmtop -h)`},
		{[]string{"--demo", "alpha"}, `llmtop: unexpected argument "alpha" (try: llmtop -h)`},
		{[]string{"--", "alpha"}, `llmtop: unexpected argument "alpha" (try: llmtop -h)`},
		{[]string{"--version", "alpha"}, `llmtop: unexpected argument "alpha" (try: llmtop -h)`},
		{[]string{"agent", "alpha"}, `llmtop agent: unexpected argument "alpha" (try: llmtop agent -h)`},
		{[]string{"agent", "--", "alpha"}, `llmtop agent: unexpected argument "alpha" (try: llmtop agent -h)`},
		{[]string{"version", "alpha"}, `llmtop version: unexpected argument "alpha"`},
		{[]string{"version", "--help"}, `llmtop version: unexpected argument "--help"`},
		{[]string{"help", "alpha"}, `llmtop help: unexpected argument "alpha"`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, err := parseCommand(tc.args, getenv)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v; want %s", err, tc.want)
			}
		})
	}
}

func TestAgentTokenPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, fileToken, env, want string
		args                       []string
	}{
		{name: "flag wins", fileToken: "alpha", env: "bravo", args: []string{"--token=charlie"}, want: "charlie"},
		{name: "split flag wins", fileToken: "alpha", env: "bravo", args: []string{"--token", "charlie"}, want: "charlie"},
		{name: "empty flag disables auth", fileToken: "alpha", env: "bravo", args: []string{"--token="}},
		{name: "split empty flag disables auth", fileToken: "alpha", env: "bravo", args: []string{"--token", ""}},
		{name: "last token flag wins", fileToken: "alpha", env: "bravo", args: []string{"--token=charlie", "--token="}},
		{name: "environment wins", fileToken: "alpha", env: "bravo", want: "bravo"},
		{name: "config fallback", fileToken: "alpha", want: "alpha"},
		{name: "no token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := cliEnv(t, tc.env)
			path := writeConfig(t, "[agent]\ntoken = '"+tc.fileToken+"'\n")
			args := append([]string{"--config", path}, tc.args...)
			cfg, err := agentConfig(args, getenv)
			if err != nil || cfg.Agent.Token != tc.want {
				t.Fatalf("token = %q, err = %v; want %q", cfg.Agent.Token, err, tc.want)
			}
		})
	}
}

func TestAgentListen(t *testing.T) {
	getenv := cliEnv(t, "")
	path := writeConfig(t, "[agent]\nlisten = '192.0.2.1:9832'\n")
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"default", ":9832", nil},
		{"config", "192.0.2.1:9832", []string{"--config", path}},
		{"override", "127.0.0.1:0", []string{"--config", path, "--listen", "127.0.0.1:0"}},
		{"empty flag retains config", "192.0.2.1:9832", []string{"--config", path, "--listen="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := agentConfig(tc.args, getenv)
			if err != nil || cfg.Agent.Listen != tc.want {
				t.Fatalf("listen = %q, err = %v; want %q", cfg.Agent.Listen, err, tc.want)
			}
		})
	}
}

func TestConfigErrors(t *testing.T) {
	getenv := cliEnv(t, "")
	missing := filepath.Join(t.TempDir(), "missing.toml")
	invalid := writeConfig(t, "[agent\n")
	for _, prefix := range [][]string{nil, {"agent"}} {
		for _, path := range []string{missing, invalid, ""} {
			args := append(append([]string{}, prefix...), "--config", path)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				_, err := parseCommand(args, getenv)
				if err == nil || !strings.Contains(err.Error(), path) {
					t.Fatalf("expected config error for %q; got %v", path, err)
				}
				if path == missing && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing explicit config error = %v; want os.ErrNotExist", err)
				}
			})
		}
	}
}

func TestFlagErrorsAndHelp(t *testing.T) {
	getenv := cliEnv(t, "")
	for _, prefix := range [][]string{nil, {"agent"}} {
		usage := rootUsage
		if len(prefix) != 0 {
			usage = agentUsage
		}
		for _, tc := range []struct {
			arg  string
			code int
			text string
		}{
			{"-h", 0, usage},
			{"--help", 0, usage},
			{"--alpha", 2, "flag provided but not defined: -alpha\n" + usage},
			{"--config", 2, "flag needs an argument: -config\n" + usage},
		} {
			args := append(append([]string{}, prefix...), tc.arg)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				_, err := parseCommand(args, getenv)
				var fe *flagError
				if !errors.As(err, &fe) || fe.code != tc.code || !strings.HasPrefix(fe.output, tc.text) {
					t.Fatalf("error = %v (%+v); want code %d and output starting %q", err, fe, tc.code, tc.text)
				}
				if tc.code == 0 && !errors.Is(err, flag.ErrHelp) {
					t.Fatalf("help error = %v; want flag.ErrHelp", err)
				}
				var stdout, stderr bytes.Buffer
				if code := run(args, getenv, &stdout, &stderr); code != tc.code || stdout.Len() != 0 || stderr.String() != fe.output {
					t.Fatalf("run: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
				}
			})
		}
	}
}

func TestRunOutput(t *testing.T) {
	getenv := cliEnv(t, "")
	for _, tc := range []struct {
		args         []string
		code         int
		stdoutPrefix string
		stderr       string
	}{
		{args: []string{"version"}, stdoutPrefix: "llmtop " + versionString() + "\n"},
		{args: []string{"--version"}, stdoutPrefix: "llmtop " + versionString() + "\n"},
		{args: []string{"help"}, stdoutPrefix: rootUsage},
		{args: []string{"version", "alpha"}, code: 1, stderr: "llmtop version: unexpected argument \"alpha\"\n"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, getenv, &stdout, &stderr)
			if code != tc.code || !strings.HasPrefix(stdout.String(), tc.stdoutPrefix) || stderr.String() != tc.stderr {
				t.Fatalf("run: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestVersionStringOverride(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })
	version = "alpha"
	if got := versionString(); got != "alpha" {
		t.Fatalf("version = %q; want alpha", got)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{":9832", false},
		{"localhost:9832", true},
		{"127.0.0.1:9832", true},
		{"127.0.0.2:0", true},
		{"[::1]:9832", true},
		{"192.0.2.1:9832", false},
		{"[::]:9832", false},
		{"alpha.example.com:9832", false},
		{"localhost", false},
		{"[::1]", false},
		{"", false},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isLoopbackAddr(tc.addr); got != tc.want {
				t.Fatalf("isLoopbackAddr(%q) = %v; want %v", tc.addr, got, tc.want)
			}
		})
	}
}
