package procs

import (
	"testing"

	"github.com/mjacobs/llmtop/internal/model"
)

func TestClassify(t *testing.T) {
	table := map[int32]ProcInfo{
		1:   {PID: 1, PPID: 0, Comm: "systemd"},
		100: {PID: 100, PPID: 1, Comm: "llama-server"},
		101: {PID: 101, PPID: 100, Comm: "cuda-worker"}, // child of llama-server
		200: {PID: 200, PPID: 1, Comm: "kwin_wayland"},
		300: {PID: 300, PPID: 1, Comm: "python3"},
		400: {PID: 400, PPID: 1, Comm: "ollama"},
		500: {PID: 500, PPID: 500, Comm: "self-loop"}, // pathological ppid cycle
		600: {PID: 600, PPID: 1, Comm: "python3"},
		601: {PID: 601, PPID: 600, Comm: "VLLM::EngineCore"},
		700: {PID: 700, PPID: 1, Comm: "ninfer-serve"},
	}
	llmPIDs := map[int32]bool{100: true}

	cases := []struct {
		pid      int32
		graphics bool
		want     model.ProcClass
	}{
		{100, false, model.ProcLLM},     // discovered server pid
		{101, false, model.ProcLLM},     // ancestor is a discovered server
		{400, false, model.ProcLLM},     // name pattern, not discovered
		{200, true, model.ProcGraphics}, // graphics list
		{300, false, model.ProcOther},   // plain compute
		{300, true, model.ProcGraphics}, // graphics flag wins for non-LLM
		{999, false, model.ProcOther},   // unknown pid
		{500, false, model.ProcOther},   // ppid cycle must terminate
		{601, false, model.ProcLLM},     // VLLM::EngineCore matches "vllm" case-insensitively
		{700, false, model.ProcLLM},     // ninfer-serve holds its own VRAM
	}
	for _, c := range cases {
		if got := Classify(c.pid, table, llmPIDs, c.graphics); got != c.want {
			t.Errorf("Classify(%d, graphics=%v) = %q, want %q", c.pid, c.graphics, got, c.want)
		}
	}
}

func TestOwner(t *testing.T) {
	table := map[int32]ProcInfo{
		1:   {PID: 1, PPID: 0, Comm: "systemd"},
		100: {PID: 100, PPID: 1, Comm: "python3"},
		101: {PID: 101, PPID: 100, Comm: "VLLM::EngineCore"},
		200: {PID: 200, PPID: 1, Comm: "llama-server"},
		300: {PID: 300, PPID: 1, Comm: "onnx-runner"},
		500: {PID: 500, PPID: 500, Comm: "self-loop"},
	}
	servers := map[int32]bool{100: true, 200: true}

	cases := []struct {
		pid  int32
		want int32
		ok   bool
	}{
		{100, 100, true},
		{101, 100, true}, // EngineCore child of vllm serve
		{200, 200, true},
		{300, 0, false},
		{500, 0, false},
		{999, 0, false},
	}
	for _, c := range cases {
		got, ok := Owner(c.pid, table, servers)
		if got != c.want || ok != c.ok {
			t.Errorf("Owner(%d) = (%d, %v), want (%d, %v)", c.pid, got, ok, c.want, c.ok)
		}
	}
}
