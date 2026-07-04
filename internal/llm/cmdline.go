// Package llm discovers and queries LLM inference servers (llama.cpp in v1).
package llm

import (
	"fmt"
	"path/filepath"
	"strings"
)

// EndpointFromCmdline extracts the HTTP endpoint of a llama-server process
// from its argv. ok=false when argv is not a llama-server invocation.
func EndpointFromCmdline(argv []string) (string, bool) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "llama-server" {
		return "", false
	}
	host, port := "127.0.0.1", "8080"
	flagVal := func(i int) (string, bool) { // value of --flag at argv[i]
		arg := argv[i]
		if eq := strings.IndexByte(arg, '='); eq >= 0 {
			return arg[eq+1:], true
		}
		if i+1 < len(argv) {
			return argv[i+1], true
		}
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		switch {
		case argv[i] == "--host" || strings.HasPrefix(argv[i], "--host="):
			if v, ok := flagVal(i); ok {
				host = v
			}
		case argv[i] == "--port" || strings.HasPrefix(argv[i], "--port="):
			if v, ok := flagVal(i); ok {
				port = v
			}
		}
	}
	// Wildcard binds are reachable via loopback from the same host.
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s", host, port), true
}
