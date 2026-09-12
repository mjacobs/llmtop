package procs

import (
	"strings"

	"github.com/mjacobs/llmtop/internal/model"
)

// llmNamePatterns are matched case-insensitively against comm: vLLM's engine
// process, which holds the model's VRAM, titles itself "VLLM::EngineCore";
// NInfer's server binary is "ninfer-serve".
var llmNamePatterns = []string{"llama-server", "vllm", "sglang", "ollama", "ninfer"}

func looksLLM(comm string) bool {
	comm = strings.ToLower(comm)
	for _, p := range llmNamePatterns {
		if strings.Contains(comm, p) {
			return true
		}
	}
	return false
}

// maxHops bounds the ancestry walks below; /proc's tree is never that deep,
// and the bound keeps a corrupt or racing table from spinning.
const maxHops = 64

// Classify assigns a GPU process to llm | graphics | other. A pid whose
// ancestry (bounded walk, cycle-safe) reaches a discovered LLM server pid
// or an LLM-named process is "llm"; otherwise NVML's graphics flag decides.
func Classify(pid int32, table map[int32]ProcInfo, llmPIDs map[int32]bool, graphics bool) model.ProcClass {
	p := pid
	for hops := 0; p > 0 && hops < maxHops; hops++ {
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

// Owner returns the discovered LLM server pid that pid belongs to: pid
// itself when it is a server, else its nearest ancestor in serverPIDs
// (bounded walk, cycle-safe). ok=false when there is none. Servers whose
// GPU work runs in a child process — vLLM's API server spawns a
// VLLM::EngineCore that holds the model — are attributed to their GPUs
// through this walk.
func Owner(pid int32, table map[int32]ProcInfo, serverPIDs map[int32]bool) (int32, bool) {
	p := pid
	for hops := 0; p > 0 && hops < maxHops; hops++ {
		if serverPIDs[p] {
			return p, true
		}
		info, ok := table[p]
		if !ok || info.PPID == p {
			break
		}
		p = info.PPID
	}
	return 0, false
}
