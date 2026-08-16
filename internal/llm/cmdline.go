// Package llm discovers and queries LLM inference servers (llama.cpp in v1).
package llm

import (
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// EndpointFromCmdline extracts the HTTP endpoint of a llama-server process
// from its argv. ok=false when argv is not a llama-server invocation, or when
// its --host/--port values do not look like a host and a port: argv comes
// from another process's /proc cmdline and must not be able to steer the
// URL we end up fetching.
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
		if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
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
	if net.ParseIP(host) == nil && !validHostname(host) {
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	// JoinHostPort brackets IPv6 literals; url.URL keeps the result a
	// well-formed origin with no path or query smuggled in from argv.
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(n))}
	return u.String(), true
}

// validHostname reports whether s is a conservative DNS-style hostname:
// dot-separated labels of letters, digits and '-', no label starting or
// ending with '-', 253 characters at most.
func validHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			switch c := label[i]; {
			case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-':
			default:
				return false
			}
		}
	}
	return true
}

// sleepIdleFromCmdline reports whether argv enables llama-server's idle sleep
// (--sleep-idle-seconds with a positive value; -1/absent = disabled).
func sleepIdleFromCmdline(argv []string) bool {
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		var val string
		switch {
		case strings.HasPrefix(arg, "--sleep-idle-seconds="):
			val = arg[len("--sleep-idle-seconds="):]
		case arg == "--sleep-idle-seconds" && i+1 < len(argv):
			val = argv[i+1]
		default:
			continue
		}
		secs, err := strconv.Atoi(val)
		return err == nil && secs > 0
	}
	return false
}
