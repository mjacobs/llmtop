// llmtop: an nvtop-like TUI for GPU + LLM-serving monitoring.
package main

import (
	"fmt"
	"os"
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

func runAgent(args []string) error { return fmt.Errorf("not implemented yet") }
func runTUI(args []string) error   { return fmt.Errorf("not implemented yet") }
