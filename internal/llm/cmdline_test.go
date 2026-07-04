package llm

import "testing"

func TestEndpointFromCmdline(t *testing.T) {
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
		{"equals form", []string{"llama-server", "--host=192.0.2.5", "--port=9000"}, "http://192.0.2.5:9000", true},
		{"ipv6 wildcard", []string{"llama-server", "--host", "::"}, "http://127.0.0.1:8080", true},
		{"valueless host followed by port", []string{"llama-server", "--host", "--port", "9000"}, "http://127.0.0.1:9000", true},
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
