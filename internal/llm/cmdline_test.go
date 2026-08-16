package llm

import (
	"net"
	"testing"
)

func TestEndpointFromCmdline(t *testing.T) {
	// Pretend 192.0.2.5 and fd00::5 are bound to local interfaces and
	// nothing else is, so the table does not depend on the test machine.
	prev := isLocalIP
	isLocalIP = func(ip net.IP) bool { return ip.Equal(net.ParseIP("192.0.2.5")) || ip.Equal(net.ParseIP("fd00::5")) }
	defer func() { isLocalIP = prev }()

	cases := []struct {
		name string
		argv []string
		want string
		ok   bool
	}{
		{"explicit host+port", []string{"/opt/llama.cpp/llama-server", "-m", "/models/emb.gguf", "--embeddings", "--host", "127.0.0.1", "--port", "8090", "--alias", "embeddinggemma"}, "http://127.0.0.1:8090", true},
		{"wildcard host dials loopback", []string{"/opt/llama.cpp-local/llama-server", "--models-dir", "/tmp/m", "--metrics", "--host", "0.0.0.0", "--port", "8080"}, "http://127.0.0.1:8080", true},
		{"port only", []string{"/opt/llama.cpp-local/llama-server", "--jinja", "--metrics", "--port", "33767", "--alias", "q"}, "http://127.0.0.1:33767", true},
		{"all defaults", []string{"/usr/bin/llama-server", "-m", "x.gguf"}, "http://127.0.0.1:8080", true},
		{"equals form, local interface address", []string{"llama-server", "--host=192.0.2.5", "--port=9000"}, "http://192.0.2.5:9000", true},
		{"localhost", []string{"llama-server", "--host", "localhost", "--port", "8082"}, "http://localhost:8082", true},
		{"foreign address rejected", []string{"llama-server", "--host=198.51.100.7", "--port=9000"}, "", false},
		{"ipv6 wildcard", []string{"llama-server", "--host", "::"}, "http://127.0.0.1:8080", true},
		{"ipv6 wildcard equals form", []string{"llama-server", "--host=::"}, "http://127.0.0.1:8080", true},
		{"ipv6 literal is bracketed", []string{"llama-server", "--host", "::1"}, "http://[::1]:8080", true},
		{"ipv6 ula literal", []string{"llama-server", "--host=fd00::5", "--port=9000"}, "http://[fd00::5]:9000", true},
		{"hostname rejected", []string{"llama-server", "--host=gpu-box.example", "--port=8081"}, "", false},
		{"valueless host followed by port", []string{"llama-server", "--host", "--port", "9000"}, "http://127.0.0.1:9000", true},
		{"host with slash rejected", []string{"llama-server", "--host", "attacker.invalid/x?y="}, "", false},
		{"host with space rejected", []string{"llama-server", "--host=a b"}, "", false},
		{"port 0 rejected", []string{"llama-server", "--port", "0"}, "", false},
		{"port 70000 rejected", []string{"llama-server", "--port=70000"}, "", false},
		{"port abc rejected", []string{"llama-server", "--port", "abc"}, "", false},
		{"not llama-server", []string{"/usr/bin/vllm", "serve"}, "", false},
		{"empty argv", []string{}, "", false},
	}
	for _, c := range cases {
		got, ok := EndpointFromCmdline(c.argv)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: EndpointFromCmdline(%v) = (%q, %v), want (%q, %v)", c.name, c.argv, got, ok, c.want, c.ok)
		}
	}
}
