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
