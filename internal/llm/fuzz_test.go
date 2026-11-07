package llm

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func FuzzParsePromSamples(f *testing.F) {
	for _, fixture := range []string{promFixture, promLabeledFixture} {
		f.Add([]byte(fixture))
		for line := range strings.SplitSeq(fixture, "\n") {
			f.Add([]byte(line))
		}
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, sample := range ParsePromSamples(body) {
			if sample.Name == "" {
				t.Fatalf("ParsePromSamples(%q) returned a sample with no name: %+v", body, sample)
			}
		}
	})
}

func FuzzEndpointFromCmdline(f *testing.F) {
	for _, tc := range endpointCmdlineCases {
		f.Add(strings.Join(tc.argv, "\x00"))
	}
	for _, tc := range vllmEndpointCmdlineCases {
		f.Add(strings.Join(tc.argv, "\x00"))
	}
	for _, tc := range ninferEndpointCmdlineCases {
		f.Add(strings.Join(tc.argv, "\x00"))
	}
	for _, tc := range strataEndpointCmdlineCases {
		f.Add(strings.Join(tc.argv, "\x00"))
	}
	// Keep this independent of the machine's interfaces and restore the
	// global after seed tests as well as after fuzzing. Do not parallelize it.
	orig := isLocalIP
	isLocalIP = func(net.IP) bool { return false }
	f.Cleanup(func() { isLocalIP = orig })
	f.Fuzz(func(t *testing.T, joined string) {
		argv := strings.Split(joined, "\x00")
		for _, parser := range []struct {
			name  string
			parse func([]string) (string, bool)
		}{
			{"llama.cpp", EndpointFromCmdline},
			{"vLLM", VLLMEndpointFromCmdline},
			{"NInfer", NInferEndpointFromCmdline},
			{"Strata", StrataEndpointFromCmdline},
		} {
			ep, ok := parser.parse(argv)
			if !ok {
				continue
			}
			u, err := url.Parse(ep)
			if err != nil {
				t.Fatalf("%s(%q) returned an invalid URL %q: %v", parser.name, argv, ep, err)
			}
			if u.Scheme != "http" || u.Host == "" || u.Opaque != "" || u.User != nil ||
				u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery ||
				u.Fragment != "" || u.RawFragment != "" {
				t.Fatalf("%s(%q) returned a non-origin URL %q", parser.name, argv, ep)
			}
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				t.Fatalf("%s(%q) returned an invalid port in %q", parser.name, argv, ep)
			}
			if ip := net.ParseIP(u.Hostname()); u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
				t.Fatalf("%s(%q) returned a non-loopback host in %q", parser.name, argv, ep)
			}
		}
	})
}
