// llmtop: an nvtop-like TUI for GPU + LLM-serving monitoring.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
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

const version = "0.1.0-dev"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "agent" {
		if err := runAgent(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "llmtop agent:", err)
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "version" {
		fmt.Println("llmtop", version)
		return
	}
	if err := runTUI(args); err != nil {
		fmt.Fprintln(os.Stderr, "llmtop:", err)
		os.Exit(1)
	}
}

func runTUI(args []string) error {
	fs := flag.NewFlagSet("llmtop", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file path")
	demo := fs.Bool("demo", false, "synthetic data, no GPUs needed")
	var connects multiFlag
	fs.Var(&connects, "connect", "agent address host:port (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	return tui.Run(cfg, connects, *demo, version)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func runAgent(args []string) error {
	fs := flag.NewFlagSet("llmtop agent", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file path")
	listen := fs.String("listen", "", "listen address (overrides config)")
	token := fs.String("token", "", "bearer token (overrides config)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Agent.Listen = *listen
	}
	if *token != "" {
		cfg.Agent.Token = *token
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
		[]llm.Adapter{llm.NewLlamaCpp()}, static, version)
	if err := col.Init(); err != nil {
		return fmt.Errorf("collector init: %w", err)
	}
	defer col.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("llmtop agent %s listening on %s", version, cfg.Agent.Listen)
	return agent.Run(ctx, col, cfg.Agent.Listen, cfg.Agent.Token,
		time.Duration(cfg.RefreshMS)*time.Millisecond)
}
