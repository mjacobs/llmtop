package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sample() Snapshot {
	return Snapshot{
		Host:      HostInfo{Name: "alpha", Hostname: "alpha", DriverVersion: "580.65", AgentVersion: "0.1.0"},
		Timestamp: time.Unix(1751600000, 0).UTC(),
		GPUs: []GPU{{
			Index: 0, UUID: "GPU-abc", Name: "NVIDIA GeForce RTX 5090",
			UtilPct: 72, MemUsed: 24 << 30, MemTotal: 32 << 30,
			TempC: 64, PowerW: 412, PowerCapW: 575,
			LLMMemBytes: 20 << 30, OtherMemBytes: 4 << 30, LLMUtilPct: 65,
		}},
		Procs: []GPUProc{{PID: 2310469, Name: "llama-server", GPUIndex: 0, MemBytes: 20 << 30, SMUtilPct: 65, Class: ProcLLM}},
		Servers: []LLMServer{{
			Kind: "llama.cpp", Endpoint: "http://127.0.0.1:33767", PID: 2310469,
			Status: StatusOK, Model: "qwen36-35b-mtp", CtxSize: 262144, SlotsTotal: 1,
			Metrics: &LLMMetrics{PromptTokensTotal: 16121, PredictedTokensTotal: 247195,
				PromptTPS: 2754.78, PredictedTPS: 42.3, KVCacheUsage: -1,
				RequestsProcessing: 1, RequestsDeferred: 0},
		}},
		Errors: []string{"gpu1: temp read failed"},
	}
}

func TestSnapshotJSONRoundTrip(t *testing.T) {
	in := sample()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Snapshot
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\nin:  %+v\nout: %+v", in, out)
	}
}

func TestMetricsOmittedWhenNil(t *testing.T) {
	s := sample()
	s.Servers[0].Metrics = nil
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), `"metrics"`) {
		t.Fatalf("nil Metrics must be omitted from JSON, got: %s", b)
	}
}
