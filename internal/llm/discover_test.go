package llm

import (
	"testing"

	"github.com/mjacobs/llmtop/internal/procs"
)

func TestDiscover(t *testing.T) {
	table := map[int32]procs.ProcInfo{
		3222:    {PID: 3222, Comm: "llama-server", Cmdline: []string{"/opt/llama.cpp/llama-server", "--host", "127.0.0.1", "--port", "8090"}},
		2298467: {PID: 2298467, Comm: "llama-server", Cmdline: []string{"/opt/llama.cpp-local/llama-server", "--host", "0.0.0.0", "--port", "8080"}},
		7176:    {PID: 7176, Comm: "chrome", Cmdline: []string{"/opt/google/chrome/chrome", "--type=gpu-process"}},
	}
	adapters := []Adapter{NewLlamaCpp()}
	static := []string{
		"http://127.0.0.1:8080/", // dup of scanned (trailing slash) → keeps PID
		"http://127.0.0.1:8081",  // container server, static-only
	}
	got := Discover(table, adapters, static)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(got), got)
	}
	// Sorted by endpoint.
	wantEP := []string{"http://127.0.0.1:8080", "http://127.0.0.1:8081", "http://127.0.0.1:8090"}
	wantPID := []int32{2298467, 0, 3222}
	for i := range got {
		if got[i].Endpoint != wantEP[i] || got[i].PID != wantPID[i] {
			t.Errorf("[%d] = {%s %d}, want {%s %d}", i, got[i].Endpoint, got[i].PID, wantEP[i], wantPID[i])
		}
		if got[i].Adapter == nil {
			t.Errorf("[%d] adapter must not be nil", i)
		}
	}
}

func TestDiscoverEmpty(t *testing.T) {
	if got := Discover(nil, []Adapter{NewLlamaCpp()}, nil); len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestDiscoverNoAdapters(t *testing.T) {
	if got := Discover(nil, nil, []string{"http://127.0.0.1:1"}); len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestDiscoverVLLM(t *testing.T) {
	table := map[int32]procs.ProcInfo{
		100: {PID: 100, Comm: "python3", Cmdline: []string{"/opt/venv/bin/python", "/opt/venv/bin/vllm", "serve", "Qwen/Qwen2-7B", "--host", "0.0.0.0", "--port", "8082"}},
		101: {PID: 101, PPID: 100, Comm: "VLLM::EngineCore", Cmdline: []string{"VLLM::EngineCore"}},
		200: {PID: 200, Comm: "llama-server", Cmdline: []string{"llama-server", "--port", "8090"}},
	}
	got := Discover(table, Adapters(), nil)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (vllm serve + llama-server, not EngineCore): %+v", len(got), got)
	}
	if got[0].Endpoint != "http://127.0.0.1:8082" || got[0].PID != 100 || got[0].Adapter.Kind() != vllmKind {
		t.Errorf("vllm = {%s pid=%d kind=%s}", got[0].Endpoint, got[0].PID, got[0].Adapter.Kind())
	}
	if got[1].Endpoint != "http://127.0.0.1:8090" || got[1].PID != 200 || got[1].Adapter.Kind() != llamaKind {
		t.Errorf("llama = {%s pid=%d kind=%s}", got[1].Endpoint, got[1].PID, got[1].Adapter.Kind())
	}
}

func TestDiscoverNInfer(t *testing.T) {
	table := map[int32]procs.ProcInfo{
		100: {PID: 100, Comm: "ninfer-serve", Cmdline: []string{"/opt/ninfer/build/apps/ninfer-serve", "/data/m.ninfer", "--model-id", "m", "--host", "0.0.0.0", "--port", "8002"}},
		200: {PID: 200, Comm: "llama-server", Cmdline: []string{"llama-server", "--port", "8090"}},
	}
	got := Discover(table, Adapters(), nil)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Endpoint != "http://127.0.0.1:8002" || got[0].PID != 100 || got[0].Adapter.Kind() != ninferKind {
		t.Errorf("ninfer = {%s pid=%d kind=%s}", got[0].Endpoint, got[0].PID, got[0].Adapter.Kind())
	}
	if got[1].Adapter.Kind() != llamaKind {
		t.Errorf("llama kind = %s", got[1].Adapter.Kind())
	}
}

func TestDiscoverScanCollisionDeterministic(t *testing.T) {
	cmdline := []string{"/opt/llama.cpp/llama-server", "--host", "127.0.0.1", "--port", "8080"}
	table := map[int32]procs.ProcInfo{
		500: {PID: 500, Comm: "llama-server", Cmdline: cmdline},
		300: {PID: 300, Comm: "llama-server", Cmdline: cmdline},
	}
	adapters := []Adapter{NewLlamaCpp()}
	for i := 0; i < 20; i++ {
		got := Discover(table, adapters, nil)
		if len(got) != 1 {
			t.Fatalf("iter %d: len = %d, want 1: %+v", i, len(got), got)
		}
		if got[0].PID != 300 {
			t.Fatalf("iter %d: PID = %d, want 300 (smaller PID must win)", i, got[0].PID)
		}
	}
}
