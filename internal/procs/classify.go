package procs

import (
	"strings"

	"github.com/mjacobs/llmtop/internal/model"
)

var llmNamePatterns = []string{"llama-server", "vllm", "sglang", "ollama"}

func looksLLM(comm string) bool {
	for _, p := range llmNamePatterns {
		if strings.Contains(comm, p) {
			return true
		}
	}
	return false
}

// Classify assigns a GPU process to llm | graphics | other. A pid whose
// ancestry (bounded walk, cycle-safe) reaches a discovered LLM server pid
// or an LLM-named process is "llm"; otherwise NVML's graphics flag decides.
func Classify(pid int32, table map[int32]ProcInfo, llmPIDs map[int32]bool, graphics bool) model.ProcClass {
	p := pid
	for hops := 0; p > 0 && hops < 64; hops++ {
		if llmPIDs[p] {
			return model.ProcLLM
		}
		info, ok := table[p]
		if !ok {
			break
		}
		if looksLLM(info.Comm) {
			return model.ProcLLM
		}
		if info.PPID == p {
			break // pathological cycle
		}
		p = info.PPID
	}
	if graphics {
		return model.ProcGraphics
	}
	return model.ProcOther
}
