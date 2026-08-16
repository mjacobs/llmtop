// llmtop: a btop-style TUI for LLM-serving + GPU monitoring, local and remote.
package main

import (
	"context"
	"flag"
	"fmt"
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
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "agent":
			if err := runAgent(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "llmtop agent:", err)
				os.Exit(1)
			}
			return
		case "version", "help":
			if len(args) > 1 {
				fmt.Fprintf(os.Stderr, "llmtop %s: unexpected argument %q\n", args[0], args[1])
				os.Exit(1)
			}
			if args[0] == "version" {
				fmt.Println("llmtop", versionString())
				return
			}
			fs, _ := tuiFlags()
			fs.SetOutput(os.Stdout)
			fs.Usage()
			return
		}
	}
	if err := runTUI(args); err != nil {
		fmt.Fprintln(os.Stderr, "llmtop:", err)
		os.Exit(1)
	}
}

const rootUsage = `Usage:
  llmtop [flags]          monitor GPUs and llama.cpp servers (TUI)
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

// tuiFlags builds the root FlagSet. It is shared by runTUI and the
// `help` subcommand so both print the same usage.
func tuiFlags() (*flag.FlagSet, *tuiOpts) {
	var o tuiOpts
	fs := flag.NewFlagSet("llmtop", flag.ExitOnError)
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

func runTUI(args []string) error {
	fs, o := tuiFlags()
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (try: llmtop -h)", fs.Arg(0))
	}
	if o.version {
		fmt.Println("llmtop", versionString())
		return nil
	}
	cfg, err := loadConfig(fs, o.cfgPath)
	if err != nil {
		return err
	}
	return tui.Run(cfg, o.connects, o.demo, versionString())
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

func runAgent(args []string) error {
	fs := flag.NewFlagSet("llmtop agent", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), agentUsage) //nolint:errcheck // usage text; nothing to do on failure
		fs.PrintDefaults()
	}
	cfgPath := fs.String("config", config.DefaultPath(), "config file path")
	listen := fs.String("listen", "", "listen address (overrides config)")
	token := fs.String("token", "", "bearer token (overrides LLMTOP_TOKEN and config)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (try: llmtop agent -h)", fs.Arg(0))
	}
	cfg, err := loadConfig(fs, *cfgPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Agent.Listen = *listen
	}
	// Token precedence: flag (even an explicit empty --token=, which turns
	// auth off) > LLMTOP_TOKEN > config file.
	if flagWasSet(fs, "token") {
		cfg.Agent.Token = *token
	} else if t := os.Getenv("LLMTOP_TOKEN"); t != "" {
		cfg.Agent.Token = t
	}
	var static []string
	for _, h := range cfg.Hosts {
		if h.Local {
			for _, e := range h.LLMEndpoints {
				static = append(static, e.URL)
			}
		}
	}
	name, _ := os.Hostname()
	col := collector.New(name, gpu.NewNVML(), procs.NewProcFS(),
		[]llm.Adapter{llm.NewLlamaCpp()}, static, versionString())
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
