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

func TestCleanString(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain ascii unchanged", "qwen36-35b-mtp", "qwen36-35b-mtp"},
		{"empty", "", ""},
		{"non-ascii letters unchanged", "modèle-größe-模型", "modèle-größe-模型"},
		{"esc sequence stripped", "\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"newline stripped", "line1\nline2\r\n", "line1line2"},
		{"tab stripped", "a\tb", "ab"},
		{"nul and del stripped", "a\x00b\x7fc", "abc"},
		{"c1 controls stripped", "a\u0085b\u009bc", "abc"}, // NEL and CSI, encoded as UTF-8
		{"only controls", "\x1b\n\t", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanString(c.in); got != c.want {
				t.Fatalf("CleanString(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSnapshotSanitize(t *testing.T) {
	const bad = "\x1b\r\n\t\x7f" // ESC, CR, LF, TAB, DEL: all removed
	s := Snapshot{
		Host:  HostInfo{Name: "alpha" + bad, Hostname: bad + "alpha", DriverVersion: "580" + bad + ".65", AgentVersion: bad + "0.1.0" + bad},
		GPUs:  []GPU{{UUID: "GPU-" + bad + "abc", Name: "NVIDIA" + bad}, {UUID: bad, Name: bad}},
		Procs: []GPUProc{{PID: 1, Name: "llama" + bad + "-server"}, {PID: 2, Name: bad}},
		Servers: []LLMServer{
			{Kind: "llama.cpp" + bad, Endpoint: "http://127.0.0.1:8080" + bad, Role: bad + "router", Status: StatusOK + bad, Model: bad + "m" + bad},
			{Kind: bad, Endpoint: bad, Role: bad, Status: bad, Model: bad},
		},
		Errors: []string{"gpu1: temp read failed" + bad, bad},
	}
	s.Sanitize()
	want := Snapshot{
		Host:  HostInfo{Name: "alpha", Hostname: "alpha", DriverVersion: "580.65", AgentVersion: "0.1.0"},
		GPUs:  []GPU{{UUID: "GPU-abc", Name: "NVIDIA"}, {UUID: "", Name: ""}},
		Procs: []GPUProc{{PID: 1, Name: "llama-server"}, {PID: 2, Name: ""}},
		Servers: []LLMServer{
			{Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8080", Role: "router", Status: StatusOK, Model: "m"},
			{},
		},
		Errors: []string{"gpu1: temp read failed", ""},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("Sanitize mismatch:\ngot:  %+v\nwant: %+v", s, want)
	}
	if b, _ := json.Marshal(s); strings.ContainsAny(string(b), "\x1b\n\r") {
		t.Fatalf("control bytes survived Sanitize: %q", b)
	}
}

func TestSanitizeLeavesCleanSnapshotUntouched(t *testing.T) {
	in := sample()
	got := sample()
	got.Sanitize()
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("Sanitize changed a clean snapshot:\nin:  %+v\nout: %+v", in, got)
	}
}
