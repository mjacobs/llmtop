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
		{"LOCALHOST is case-insensitive", []string{"llama-server", "--host", "LOCALHOST", "--port", "8082"}, "http://localhost:8082", true},
		{"ipv6 ::0 wildcard", []string{"llama-server", "--host=::0", "--port=8083"}, "http://127.0.0.1:8083", true},
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

func TestVLLMEndpointFromCmdline(t *testing.T) {
	prev := isLocalIP
	isLocalIP = func(ip net.IP) bool { return ip.Equal(net.ParseIP("192.0.2.5")) }
	defer func() { isLocalIP = prev }()

	cases := []struct {
		name string
		argv []string
		want string
		ok   bool
	}{
		{"venv entrypoint", []string{"/opt/venv/bin/python", "/opt/venv/bin/vllm", "serve", "Qwen/Qwen2-7B", "--host", "0.0.0.0", "--port", "8082"}, "http://127.0.0.1:8082", true},
		{"bare vllm serve defaults", []string{"vllm", "serve", "org/model"}, "http://127.0.0.1:8000", true},
		{"equals form local address", []string{"vllm", "serve", "m", "--host=192.0.2.5", "--port=9000"}, "http://192.0.2.5:9000", true},
		{"module api_server", []string{"python", "-m", "vllm.entrypoints.openai.api_server", "--port", "8001"}, "http://127.0.0.1:8001", true},
		{"enginecore is not the HTTP server", []string{"VLLM::EngineCore"}, "", false},
		{"vllm bench is not serve", []string{"vllm", "bench", "serve", "--port", "8082"}, "", false},
		{"unix socket rejected", []string{"vllm", "serve", "m", "--uds", "/run/vllm.sock"}, "", false},
		{"foreign address rejected", []string{"vllm", "serve", "m", "--host=198.51.100.7", "--port", "8000"}, "", false},
		{"llama-server is not vllm", []string{"llama-server", "--port", "8080"}, "", false},
	}
	for _, c := range cases {
		got, ok := VLLMEndpointFromCmdline(c.argv)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: VLLMEndpointFromCmdline(%v) = (%q, %v), want (%q, %v)", c.name, c.argv, got, ok, c.want, c.ok)
		}
	}
}

func TestNInferEndpointFromCmdline(t *testing.T) {
	prev := isLocalIP
	isLocalIP = func(ip net.IP) bool { return ip.Equal(net.ParseIP("192.0.2.5")) }
	defer func() { isLocalIP = prev }()

	cases := []struct {
		name string
		argv []string
		want string
		ok   bool
	}{
		{"build tree binary", []string{"/opt/ninfer/build/apps/ninfer-serve", "/data/m.ninfer", "--model-id", "m", "--host", "0.0.0.0", "--port", "8002"}, "http://127.0.0.1:8002", true},
		{"bare defaults", []string{"ninfer-serve", "m.ninfer"}, "http://127.0.0.1:8080", true},
		{"equals form local address", []string{"ninfer-serve", "m.ninfer", "--host=192.0.2.5", "--port=9000"}, "http://192.0.2.5:9000", true},
		{"foreign address rejected", []string{"ninfer-serve", "m.ninfer", "--host=198.51.100.7"}, "", false},
		{"ninfer cli is not the server", []string{"ninfer-cli", "--port", "8002"}, "", false},
		{"llama-server is not ninfer", []string{"llama-server", "--port", "8080"}, "", false},
	}
	for _, c := range cases {
		got, ok := NInferEndpointFromCmdline(c.argv)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: NInferEndpointFromCmdline(%v) = (%q, %v), want (%q, %v)", c.name, c.argv, got, ok, c.want, c.ok)
		}
	}
}

func TestVLLMModelFromCmdline(t *testing.T) {
	// argv belongs to another process and can carry secrets; only the
	// positions vLLM itself reads as the model may be reported.
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"positional tag", []string{"vllm", "serve", "org/Qwen2-7B", "--port", "8000"}, "Qwen2-7B"},
		{"served-model-name wins", []string{"vllm", "serve", "org/Qwen2-7B", "--served-model-name", "qwen-demo"}, "qwen-demo"},
		{"module entrypoint --model", []string{"python", "-m", "vllm.entrypoints.openai.api_server", "--model", "org/Qwen2-7B"}, "Qwen2-7B"},
		{"api key value is not a model", []string{"vllm", "serve", "--api-key", "sk-secret", "--port", "8000"}, ""},
		{"api key before the tag is not a model", []string{"vllm", "serve", "--api-key", "sk-secret", "org/Qwen2-7B"}, ""},
		{"host value is not a model", []string{"vllm", "serve", "--host", "0.0.0.0", "--port", "8000"}, ""},
		{"serve with no arguments", []string{"vllm", "serve"}, ""},
		{"not a vllm command line", []string{"llama-server", "--port", "8080"}, ""},
	}
	for _, c := range cases {
		if got := vllmModelFromCmdline(c.argv); got != c.want {
			t.Errorf("%s: vllmModelFromCmdline(%v) = %q, want %q", c.name, c.argv, got, c.want)
		}
	}
}
