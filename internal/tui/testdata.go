package tui

import (
	"errors"
	"time"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/model"
)

// sampleTS is a fixed instant (not time.Now) so SampleHostStates is
// deterministic across runs — view tests assert on the rendered string.
var sampleTS = time.Unix(1_700_000_000, 0)

// SampleHostStates returns 3 deterministic HostStates covering the shapes
// the overview/drill-down/app views need to exercise:
//
//   - "alpha": healthy, 2 GPUs, 2 servers (one with Metrics nil, i.e.
//     "--metrics off"), and 5 GPU processes (llm/graphics/other classes,
//     including an SM%-unknown one) for the drill-down process table.
//   - "bravo": reachable host whose one server has Status loading.
//   - "charlie": unreachable — ErrMsg set, no Snap.
//
// Built via NewHostState + Apply (not struct literals) so the returned
// states exercise the same code path production updates take, including
// ring population.
func SampleHostStates() []*HostState {
	alpha := NewHostState("alpha")
	alpha.Apply(client.Update{Host: "alpha", Snapshot: &model.Snapshot{
		Host: model.HostInfo{
			Name:          "alpha",
			Hostname:      "alpha",
			DriverVersion: "580.65.06",
			AgentVersion:  "0.4.0",
		},
		Timestamp: sampleTS,
		GPUs: []model.GPU{
			{
				Index: 0, UUID: "GPU-0", Name: "NVIDIA RTX 4090",
				UtilPct: 72, MemUsed: 24106127360, MemTotal: 32 << 30,
				TempC: 64, PowerW: 412, PowerCapW: 450,
				LLMMemBytes: 21 << 30, OtherMemBytes: 24106127360 - 21<<30,
				LLMUtilPct: 65,
			},
			{
				Index: 1, UUID: "GPU-1", Name: "NVIDIA RTX 3090",
				UtilPct: 18, MemUsed: 4 << 30, MemTotal: 24 << 30,
				TempC: 47, PowerW: 110, PowerCapW: 350,
				LLMMemBytes: 3 << 30, OtherMemBytes: 1 << 30,
				LLMUtilPct: 12,
			},
		},
		Servers: []model.LLMServer{
			{
				Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8080",
				Status: model.StatusOK, Model: "qwen36-35b-mtp",
				CtxSize: 262144, SlotsTotal: 2,
				Metrics: &model.LLMMetrics{
					PromptTokensTotal:    16121,
					PredictedTokensTotal: 247195,
					PredictedTPS:         42.3,
					KVCacheUsage:         0.37,
					RequestsProcessing:   1,
				},
			},
			{
				Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8090",
				Status: model.StatusOK, Model: "embeddinggemma",
				CtxSize: 8192, SlotsTotal: 1,
				Metrics: nil, // --metrics off
			},
		},
		Procs: []model.GPUProc{
			{PID: 2310469, Name: "llama-server", GPUIndex: 0, MemBytes: 21 << 30, SMUtilPct: 65, Class: model.ProcLLM},
			{PID: 4821, Name: "Xorg", GPUIndex: 0, MemBytes: (24106127360 - 21<<30) / 2, SMUtilPct: -1, Class: model.ProcGraphics},
			{PID: 5533, Name: "chrome", GPUIndex: 0, MemBytes: (24106127360 - 21<<30) / 2, SMUtilPct: 1, Class: model.ProcOther},
			{PID: 2310480, Name: "llama-server", GPUIndex: 1, MemBytes: 3 << 30, SMUtilPct: 12, Class: model.ProcLLM},
			{PID: 4822, Name: "kwin_wayland", GPUIndex: 1, MemBytes: 1 << 30, SMUtilPct: 2, Class: model.ProcGraphics},
		},
	}})

	loading := NewHostState("bravo")
	loading.Apply(client.Update{Host: "bravo", Snapshot: &model.Snapshot{
		Host: model.HostInfo{
			Name: "bravo", Hostname: "bravo",
			DriverVersion: "580.65.06", AgentVersion: "0.4.0",
		},
		Timestamp: sampleTS,
		GPUs: []model.GPU{
			{
				Index: 0, UUID: "GPU-0", Name: "NVIDIA RTX 4080",
				UtilPct: 3, MemUsed: 1 << 30, MemTotal: 16 << 30,
				TempC: 38, PowerW: 45, PowerCapW: 320,
			},
		},
		Servers: []model.LLMServer{
			{
				Kind: "llama.cpp", Endpoint: "http://192.0.2.20:8080",
				Status: model.StatusLoading,
			},
		},
	}})

	charlie := NewHostState("charlie")
	charlie.Apply(client.Update{Host: "charlie", Err: errors.New("dial tcp 192.0.2.30:8080: connect: connection refused")})

	return []*HostState{alpha, loading, charlie}
}
