// llmtop: a btop-style TUI for LLM-serving + GPU monitoring, local and remote.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/mjacobs/llmtop/internal/agent"
	"github.com/mjacobs/llmtop/internal/collector"
	"github.com/mjacobs/llmtop/internal/config"
	"github.com/mjacobs/llmtop/internal/gpu"
	"github.com/mjacobs/llmtop/internal/llm"
	"github.com/mjacobs/llmtop/internal/procs"
	"github.com/mjacobs/llmtop/internal/tui"
)

// version is the release string. It is empty in the source tree; the
// Makefile injects it at build time with -ldflags "-X main.version=...".
// See versionString for what is shown when it is not set.
var version string

// versionString returns the version to display: the ldflags-injected
// value; else the module version Go stamps into the binary (a real tag for
// `go install ...@vX.Y.Z`, a pseudo-version for `go build` from a git
// checkout); else "dev".
func versionString() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run owns process output and translates parsing/runtime errors to exit codes.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd, err := parseCommand(args, getenv)
	if err != nil {
		var fe *flagError
		if errors.As(err, &fe) {
			fmt.Fprint(stderr, fe.output) //nolint:errcheck // process output
			return fe.code
		}
		fmt.Fprintln(stderr, err) //nolint:errcheck // process output
		return 1
	}
	switch cmd.name {
	case "version":
		fmt.Fprintln(stdout, "llmtop", versionString()) //nolint:errcheck // process output
	case "help":
		fmt.Fprint(stdout, cmd.help) //nolint:errcheck // process output
	case "agent":
		err = runAgent(cmd.cfg)
	default:
		err = tui.Run(cmd.cfg, cmd.connects, cmd.demo, versionString())
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd.prefix(), err) //nolint:errcheck // process output
		return 1
	}
	return 0
}

type command struct {
	name     string
	cfg      config.Config
	demo     bool
	connects []string
	help     string
}

func (c command) prefix() string {
	if c.name == "tui" {
		return "llmtop"
	}
	return "llmtop " + c.name
}

// parseCommand resolves dispatch, flags and configuration without starting any
// telemetry or terminal runtime. getenv supplies the agent's token environment.
func parseCommand(args []string, getenv func(string) string) (cmd command, err error) {
	cmd.name = "tui"
	if len(args) > 0 {
		switch args[0] {
		case "agent", "version", "help":
			cmd.name = args[0]
			args = args[1:]
		}
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", cmd.prefix(), err)
		}
	}()
	switch cmd.name {
	case "agent":
		cmd.cfg, err = agentConfig(args, getenv)
		return cmd, err
	case "version", "help":
		if len(args) > 0 {
			return cmd, fmt.Errorf("unexpected argument %q", args[0])
		}
		if cmd.name == "help" {
			fs, _ := tuiFlags()
			var out bytes.Buffer
			fs.SetOutput(&out)
			fs.Usage()
			cmd.help = out.String()
		}
		return cmd, nil
	}
	fs, o := tuiFlags()
	if err := parseFlags(fs, args); err != nil {
		return cmd, err
	}
	if fs.NArg() > 0 {
		return cmd, fmt.Errorf("unexpected argument %q (try: llmtop -h)", fs.Arg(0))
	}
	if o.version {
		cmd.name = "version"
		return cmd, nil
	}
	cmd.cfg, err = loadConfig(fs, o.cfgPath)
	cmd.demo, cmd.connects = o.demo, o.connects
	return cmd, err
}

// flagError retains flag's diagnostic output and exit status: help is 0,
// malformed flags are 2. Resolution and positional-argument errors use 1.
type flagError struct {
	err    error
	output string
	code   int
}

func (e *flagError) Error() string { return e.err.Error() }
func (e *flagError) Unwrap() error { return e.err }

func parseFlags(fs *flag.FlagSet, args []string) error {
	var out bytes.Buffer
	fs.SetOutput(&out)
	if err := fs.Parse(args); err != nil {
		code := 2
		if errors.Is(err, flag.ErrHelp) {
			code = 0
		}
		return &flagError{err: err, output: out.String(), code: code}
	}
	return nil
}

const rootUsage = `Usage:
  llmtop [flags]          monitor GPUs and LLM servers (TUI)
  llmtop agent [flags]    serve this host's telemetry over HTTP
  llmtop version          print the version

Flags:
`

const agentUsage = `Usage:
  llmtop agent [flags]    serve this host's telemetry over HTTP

Flags:
`

type tuiOpts struct {
	cfgPath  string
	demo     bool
	version  bool
	connects multiFlag
}

// tuiFlags builds the root FlagSet. It is shared by parseCommand and the
// `help` subcommand so both print the same usage.
func tuiFlags() (*flag.FlagSet, *tuiOpts) {
	var o tuiOpts
	fs := flag.NewFlagSet("llmtop", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), rootUsage) //nolint:errcheck // usage text; nothing to do on failure
		fs.PrintDefaults()
	}
	fs.StringVar(&o.cfgPath, "config", config.DefaultPath(), "config file path")
	fs.BoolVar(&o.demo, "demo", false, "synthetic data, no GPUs needed")
	fs.BoolVar(&o.version, "version", false, "print the version and exit")
	fs.Var(&o.connects, "connect", "agent address host:port (repeatable)")
	return fs, &o
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// loadConfig loads the config file at path. The built-in default path
// may be absent (Load then yields defaults), but a path the user gave with
// --config must exist.
func loadConfig(fs *flag.FlagSet, path string) (config.Config, error) {
	if flagWasSet(fs, "config") {
		return config.LoadExplicit(path)
	}
	return config.Load(path)
}

// flagWasSet reports whether name appeared on the command line.
func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func agentConfig(args []string, getenv func(string) string) (config.Config, error) {
	fs := flag.NewFlagSet("llmtop agent", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), agentUsage) //nolint:errcheck // usage text; nothing to do on failure
		fs.PrintDefaults()
	}
	cfgPath := fs.String("config", config.DefaultPath(), "config file path")
	listen := fs.String("listen", "", "listen address (overrides config)")
	token := fs.String("token", "", "bearer token (overrides LLMTOP_TOKEN and config)")
	if err := parseFlags(fs, args); err != nil {
		return config.Config{}, err
	}
	if fs.NArg() > 0 {
		return config.Config{}, fmt.Errorf("unexpected argument %q (try: llmtop agent -h)", fs.Arg(0))
	}
	cfg, err := loadConfig(fs, *cfgPath)
	if err != nil {
		return config.Config{}, err
	}
	if *listen != "" {
		cfg.Agent.Listen = *listen
	}
	// Token precedence: flag (even an explicit empty --token=, which turns
	// auth off) > LLMTOP_TOKEN > config file.
	if flagWasSet(fs, "token") {
		cfg.Agent.Token = *token
	} else if t := getenv("LLMTOP_TOKEN"); t != "" {
		cfg.Agent.Token = t
	}
	return cfg, nil
}

func runAgent(cfg config.Config) error {
	var static []llm.StaticEndpoint
	for _, h := range cfg.Hosts {
		if h.Local {
			for _, e := range h.LLMEndpoints {
				static = append(static, llm.StaticEndpoint{URL: e.URL, Kind: e.Kind, APIKey: e.APIKey, SleepIdle: e.SleepIdle})
			}
		}
	}
	name, _ := os.Hostname()
	col := collector.New(name, gpu.NewNVML(), procs.NewProcFS(),
		llm.Adapters(), static, versionString())
	// A host without a working NVIDIA driver still has processes and LLM
	// servers worth reporting, and exiting here would make a service
	// manager restart-loop the agent. Keep serving; each snapshot carries
	// the GPU error.
	if err := col.Init(); err != nil {
		log.Printf("warning: GPU telemetry unavailable: %v — serving process/LLM data only", err)
	}
	defer col.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	auth := "none"
	if cfg.Agent.Token != "" {
		auth = "token"
	}
	log.Printf("llmtop agent %s listening on %s (auth: %s)", versionString(), cfg.Agent.Listen, auth)
	if cfg.Agent.Token == "" && !isLoopbackAddr(cfg.Agent.Listen) {
		log.Printf("warning: no token set and listening on a non-loopback address — anyone who can reach this port can read GPU, process and model telemetry (see README \"Agent security\")")
	}
	return agent.Run(ctx, col, cfg.Agent.Listen, cfg.Agent.Token,
		time.Duration(cfg.RefreshMS)*time.Millisecond)
}

// isLoopbackAddr reports whether a listen address binds only a loopback
// interface. An empty host (":9832") binds every interface, and a hostname
// other than "localhost" is not trusted to be loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
