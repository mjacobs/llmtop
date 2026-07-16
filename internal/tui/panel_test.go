package tui

import (
	"testing"

	"github.com/mjacobs/llmtop/internal/model"
)

// mappingSnap builds a snapshot with two GPUs and four servers covering
// every mapping rule: PID on one GPU, PID spanning both GPUs (tensor
// split), a router, and a PID that matches no proc.
func mappingSnap() *model.Snapshot {
	return &model.Snapshot{
		GPUs: []model.GPU{{Index: 0}, {Index: 1}},
		Procs: []model.GPUProc{
			{PID: 100, Name: "llama-server", GPUIndex: 0, Class: model.ProcLLM},
			{PID: 200, Name: "llama-server", GPUIndex: 0, Class: model.ProcLLM},
			{PID: 200, Name: "llama-server", GPUIndex: 1, Class: model.ProcLLM},
			{PID: 300, Name: "chrome", GPUIndex: 1, Class: model.ProcGraphics},
		},
		Servers: []model.LLMServer{
			{Endpoint: "http://h:8081", PID: 100},                 // → GPU0
			{Endpoint: "http://h:8082", PID: 200},                 // → GPU0 and GPU1
			{Endpoint: "http://h:8080", Role: "router", PID: 100}, // router → host
			{Endpoint: "http://h:8083", PID: 999},                 // no proc → host
			{Endpoint: "http://h:8084"},                           // no PID → host
		},
	}
}

func TestMapServersToGPUs(t *testing.T) {
	byGPU, hostLevel := mapServersToGPUs(mappingSnap())

	if got := len(byGPU[0]); got != 2 {
		t.Errorf("GPU0 servers = %d, want 2 (:8081 and :8082)", got)
	}
	if got := len(byGPU[1]); got != 1 {
		t.Errorf("GPU1 servers = %d, want 1 (:8082 spans both GPUs)", got)
	}
	if byGPU[0][0].Endpoint != "http://h:8081" || byGPU[0][1].Endpoint != "http://h:8082" {
		t.Errorf("GPU0 order/content wrong: %v, %v", byGPU[0][0].Endpoint, byGPU[0][1].Endpoint)
	}
	if got := len(hostLevel); got != 3 {
		t.Fatalf("host-level servers = %d, want 3 (router, unmatched PID, no PID)", got)
	}
	// Nothing dropped: every server appears at least once.
	total := len(hostLevel)
	for _, s := range byGPU {
		total += len(s)
	}
	if total != 6 { // 5 servers, one counted twice (spans 2 GPUs)
		t.Errorf("total placements = %d, want 6", total)
	}
}

func TestMapServersToGPUsNilSnap(t *testing.T) {
	byGPU, hostLevel := mapServersToGPUs(nil)
	if len(byGPU) != 0 || hostLevel != nil {
		t.Errorf("nil snap must map to nothing, got %v / %v", byGPU, hostLevel)
	}
}

func TestBuildHostRenders(t *testing.T) {
	hosts := SampleHostStates()
	hrs := buildHostRenders(hosts)
	if len(hrs) != len(hosts) {
		t.Fatalf("hostRenders = %d, want %d", len(hrs), len(hosts))
	}
	// alpha: after testdata gains server PIDs, each server maps to its GPU.
	alpha := hrs[0]
	if len(alpha.byGPU[0]) != 1 || len(alpha.byGPU[1]) != 1 {
		t.Errorf("alpha mapping = GPU0:%d GPU1:%d, want 1 and 1",
			len(alpha.byGPU[0]), len(alpha.byGPU[1]))
	}
	if len(alpha.hostLevel) != 0 {
		t.Errorf("alpha host-level = %d, want 0", len(alpha.hostLevel))
	}
	// bravo: its server has no PID → host-level.
	if len(hrs[1].hostLevel) != 1 {
		t.Errorf("bravo host-level = %d, want 1", len(hrs[1].hostLevel))
	}
	// charlie: unreachable, no Snap → empty mapping, no panic.
	if len(hrs[2].byGPU) != 0 || len(hrs[2].hostLevel) != 0 {
		t.Errorf("dead host must have empty mapping")
	}
}
