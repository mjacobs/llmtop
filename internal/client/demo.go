package client

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/mjacobs/llmtop/internal/model"
)

// StartDemo feeds synthetic two-host data so the TUI can be developed,
// reviewed, and screenshotted with no GPUs, servers, or network.
func StartDemo(ctx context.Context, ch chan<- Update, interval time.Duration) {
	go demoHost(ctx, ch, interval, "demo-rig", 2, 0)
	go demoHost(ctx, ch, interval, "demo-node", 1, 3.7)
}

func demoHost(ctx context.Context, ch chan<- Update, interval time.Duration, name string, gpus int, phase float64) {
	t := time.NewTicker(interval)
	defer t.Stop()
	step := 0
	for {
		step++
		snap := demoSnapshot(name, gpus, phase, step)
		if !send(ctx, ch, Update{Host: name, Snapshot: &snap}) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func demoSnapshot(name string, gpus int, phase float64, step int) model.Snapshot {
	s := model.Snapshot{
		Host:      model.HostInfo{Name: name, Hostname: name, DriverVersion: "580.00-demo", AgentVersion: "demo"},
		Timestamp: time.Now().UTC(),
	}
	for i := 0; i < gpus; i++ {
		w := math.Sin(float64(step)/9.0 + phase + float64(i)*1.7)
		util := int(55 + 40*w)
		total := uint64(32) << 30
		used := uint64((0.55 + 0.3*w) * float64(total))
		llmMem := used * 8 / 10
		llmUtil := util * 9 / 10
		s.GPUs = append(s.GPUs, model.GPU{
			Index: i, UUID: fmt.Sprintf("GPU-demo-%s-%d", name, i),
			Name: "Demo RTX 5090", UtilPct: util, MemUsed: used, MemTotal: total,
			TempC: 45 + util/4, PowerW: 120 + util*4, PowerCapW: 575,
			LLMMemBytes: llmMem, OtherMemBytes: used - llmMem, LLMUtilPct: llmUtil,
		})
		s.Procs = append(s.Procs,
			model.GPUProc{PID: int32(1000 + i), Name: "llama-server", GPUIndex: i, MemBytes: llmMem, SMUtilPct: llmUtil, Class: model.ProcLLM},
			model.GPUProc{PID: 42, Name: "kwin_wayland", GPUIndex: i, MemBytes: (used - llmMem) / 2, SMUtilPct: 3, Class: model.ProcGraphics},
			model.GPUProc{PID: 77, Name: "chrome", GPUIndex: i, MemBytes: (used - llmMem) / 2, SMUtilPct: 2, Class: model.ProcOther},
		)
	}
	gen := 30 + 25*math.Sin(float64(step)/6.0+phase)
	s.Servers = append(s.Servers, model.LLMServer{
		Kind: "llama.cpp", Endpoint: "http://127.0.0.1:33767", PID: 1000,
		Status: model.StatusOK, Model: "qwen36-35b-demo", CtxSize: 262144, SlotsTotal: 2,
		Metrics: &model.LLMMetrics{
			PromptTokensTotal: float64(10000 + step*37), PredictedTokensTotal: float64(50000 + step*11),
			PromptTPS: 2400 + 500*math.Sin(float64(step)/4.0), PredictedTPS: gen,
			KVCacheUsage: 0.2 + 0.15*math.Sin(float64(step)/11.0), RequestsProcessing: step % 3, RequestsDeferred: 0,
		},
	})
	if name == "demo-rig" { // metrics-off server exercises the degraded path
		s.Servers = append(s.Servers, model.LLMServer{
			Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8090", PID: 1001,
			Status: model.StatusOK, Model: "embeddinggemma", CtxSize: 8192, SlotsTotal: 4,
		})
	}
	return s
}
