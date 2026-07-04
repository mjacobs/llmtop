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
	}
	for _, c := range cases {
		if got := Classify(c.pid, table, llmPIDs, c.graphics); got != c.want {
			t.Errorf("Classify(%d, graphics=%v) = %q, want %q", c.pid, c.graphics, got, c.want)
		}
	}
}
