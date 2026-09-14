// Package llm discovers and queries LLM inference servers.
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
// its --host/--port values are not a local bind address and a port.
//
// argv comes from another process's /proc cmdline, so it must not be able
// to steer the URL we end up fetching. --host is the address the server
// binds on this machine, so it is only accepted as "localhost" or an IP
// literal that is loopback or assigned to a local interface; hostnames and
// foreign addresses are rejected (declare such servers as static
// llm_endpoints instead).
func EndpointFromCmdline(argv []string) (string, bool) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "llama-server" {
		return "", false
	}
	host, port := overlayHostPort(argv, "127.0.0.1", "8080")
	return httpBindEndpoint(host, port)
}

// VLLMEndpointFromCmdline extracts the HTTP endpoint of a vLLM OpenAI-API
// server from its argv. ok=false when argv is not `vllm serve` (or
// `python -m vllm.entrypoints.openai.api_server`), or when --host/--port are
// not a local bind address. Default listen is 127.0.0.1:8000 (vLLM's port
// default; a wildcard --host is dialed via loopback, same as llama-server).
func VLLMEndpointFromCmdline(argv []string) (string, bool) {
	if !isVLLMServe(argv) {
		return "", false
	}
	// A Unix-socket listener has no HTTP origin we can poll.
	if argvHasFlag(argv, "--uds") {
		return "", false
	}
	host, port := overlayHostPort(argv, "127.0.0.1", "8000")
	return httpBindEndpoint(host, port)
}

// NInferEndpointFromCmdline extracts the HTTP endpoint of a `ninfer-serve`
// process from its argv. ok=false when argv is not ninfer-serve, or when
// --host/--port are not a local bind address. Default listen is
// 127.0.0.1:8080, the same as llama-server.
func NInferEndpointFromCmdline(argv []string) (string, bool) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "ninfer-serve" {
		return "", false
	}
	host, port := overlayHostPort(argv, "127.0.0.1", "8080")
	return httpBindEndpoint(host, port)
}

func isVLLMServe(argv []string) bool {
	for i, a := range argv {
		if filepath.Base(a) == "vllm" {
			return i+1 < len(argv) && argv[i+1] == "serve"
		}
		if a == "-m" && i+1 < len(argv) && argv[i+1] == "vllm.entrypoints.openai.api_server" {
			return true
		}
	}
	return false
}

func argvHasFlag(argv []string, name string) bool {
	for _, a := range argv {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

func overlayHostPort(argv []string, host, port string) (string, string) {
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--host" || strings.HasPrefix(argv[i], "--host="):
			if v, ok := argvFlagVal(argv, i); ok {
				host = v
			}
		case argv[i] == "--port" || strings.HasPrefix(argv[i], "--port="):
			if v, ok := argvFlagVal(argv, i); ok {
				port = v
			}
		}
	}
	return host, port
}

func argvFlagVal(argv []string, i int) (string, bool) {
	arg := argv[i]
	if eq := strings.IndexByte(arg, '='); eq >= 0 {
		return arg[eq+1:], true
	}
	if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
		return argv[i+1], true
	}
	return "", false
}

func argvIntFlag(argv []string, name string) (int, bool) {
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		var val string
		switch {
		case arg == name && i+1 < len(argv):
			val = argv[i+1]
		case strings.HasPrefix(arg, name+"="):
			val = arg[len(name)+1:]
		default:
			continue
		}
		n, err := strconv.Atoi(val)
		return n, err == nil
	}
	return 0, false
}

func argvStringFlag(argv []string, name string) string {
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == name || strings.HasPrefix(arg, name+"=") {
			if v, ok := argvFlagVal(argv, i); ok {
				return v
			}
		}
	}
	return ""
}

// vllmModelFromCmdline extracts the served model from a `vllm serve` (or
// `python -m vllm.entrypoints.openai.api_server`) command line, in the order
// vLLM itself resolves it: --served-model-name, then --model, then the single
// positional model tag that `vllm serve` accepts immediately after the
// subcommand.
//
// Only that one position is read. argv holds another process's arguments,
// including secrets like --api-key, and scanning further for "the first thing
// that does not start with a dash" would happily return an option's value:
// `vllm serve --api-key sk-… ` would publish the key as the model name and
// leak it through the snapshot and history APIs. An unrecognised shape yields
// "" and lets /v1/models or the metrics model_name label name the model.
func vllmModelFromCmdline(argv []string) string {
	if s := argvStringFlag(argv, "--served-model-name"); s != "" {
		return s
	}
	if s := argvStringFlag(argv, "--model"); s != "" {
		return filepath.Base(s)
	}
	for i, a := range argv {
		if a != "serve" {
			continue
		}
		// `vllm serve <model_tag>`: the tag is positional and first.
		// Anything else (an option, or no argument) means no model here.
		if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
			return filepath.Base(argv[i+1])
		}
		return ""
	}
	return ""
}

// ninferModelFromCmdline prefers --model-id (the public alias ninfer-serve
// advertises), else the basename of the positional .ninfer container path
// without its extension. Without --model-id the server advertises the
// container's embedded identity, which /v1/models then reports.
func ninferModelFromCmdline(argv []string) string {
	if s := argvStringFlag(argv, "--model-id"); s != "" {
		return s
	}
	// The container path is recognisable by its extension wherever it sits
	// among the flags; fall back to positional scanning for a path without
	// one.
	for _, a := range argv[1:] {
		if !strings.HasPrefix(a, "-") && strings.HasSuffix(a, ".ninfer") {
			return strings.TrimSuffix(filepath.Base(a), ".ninfer")
		}
	}
	for i := 1; i < len(argv); i++ {
		if strings.HasPrefix(argv[i], "-") {
			// `--flag value` consumes the next argv; `--flag=value` and a
			// flag followed by another flag do not.
			if !strings.Contains(argv[i], "=") && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				i++
			}
			continue
		}
		return strings.TrimSuffix(filepath.Base(argv[i]), ".ninfer")
	}
	return ""
}

func httpBindEndpoint(host, port string) (string, bool) {
	// Wildcard binds (0.0.0.0, ::, ::0, "") are reachable via loopback from
	// the same host.
	ip := net.ParseIP(host)
	switch {
	case host == "" || (ip != nil && ip.IsUnspecified()):
		host = "127.0.0.1"
	case strings.EqualFold(host, "localhost"):
		host = "localhost"
	case ip == nil || (!ip.IsLoopback() && !isLocalIP(ip)):
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

// isLocalIP reports whether ip is assigned to one of this machine's
// interfaces. A variable so tests can substitute a fixed answer.
var isLocalIP = func(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(ip) {
			return true
		}
	}
	return false
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
