package tui

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/config"
	"github.com/mjacobs/llmtop/internal/model"
)

// viewSnapshot builds a 2-GPU snapshot with one server per GPU and a
// graphics process on GPU 0, the shape the filter tests slice up.
func viewSnapshot(ts time.Time) *model.Snapshot {
	return &model.Snapshot{
		Host:      model.HostInfo{Name: "alpha", Hostname: "alpha"},
		Timestamp: ts,
		GPUs: []model.GPU{
			{Index: 0, Name: "demo-gpu-0", MemUsed: 8 << 30, MemTotal: 16 << 30},
			{Index: 1, Name: "demo-gpu-1", MemUsed: 4 << 30, MemTotal: 16 << 30},
		},
		Servers: []model.LLMServer{
			{Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8080", Status: model.StatusOK,
				Model: "demo-model", PID: 42, Metrics: &model.LLMMetrics{PredictedTPS: 10}},
			{Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8090", Status: model.StatusOK,
				Model: "demo-embed", PID: 43},
		},
		Procs: []model.GPUProc{
			{PID: 42, Name: "llama-server", GPUIndex: 0, MemBytes: 6 << 30, Class: model.ProcLLM},
			{PID: 77, Name: "Xorg", GPUIndex: 0, MemBytes: 1 << 30, Class: model.ProcGraphics},
			{PID: 43, Name: "llama-server", GPUIndex: 1, MemBytes: 3 << 30, Class: model.ProcLLM},
			{PID: 78, Name: "chrome", GPUIndex: 1, MemBytes: 1 << 29, Class: model.ProcOther},
		},
	}
}

func TestFilterSnapshotPassthrough(t *testing.T) {
	h := NewHostState("alpha")
	snap := viewSnapshot(sampleTS)
	h.Apply(client.Update{Host: "alpha", Snapshot: snap})
	if h.Snap != snap {
		t.Fatal("a default view must adopt the snapshot unmodified (same pointer)")
	}
}

func TestGPUSelectionAndOrder(t *testing.T) {
	h := NewHostState("alpha")
	h.view.gpus = []int{1, 0}
	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})
	if len(h.Snap.GPUs) != 2 || h.Snap.GPUs[0].Index != 1 || h.Snap.GPUs[1].Index != 0 {
		t.Fatalf("GPUs = %+v, want indices [1 0]", h.Snap.GPUs)
	}
}

func TestGPUDisableDropsPanelAndProcs(t *testing.T) {
	h := NewHostState("alpha")
	h.view.gpus = []int{1}
	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})

	if len(h.Snap.GPUs) != 1 || h.Snap.GPUs[0].Index != 1 {
		t.Fatalf("GPUs = %+v, want only index 1", h.Snap.GPUs)
	}
	for _, p := range h.Snap.Procs {
		if p.GPUIndex != 1 {
			t.Fatalf("proc %+v survived on hidden GPU 0", p)
		}
	}

	// The server on the hidden GPU must fall through to the host strip
	// (its PID no longer maps to any visible GPU), not vanish.
	hr := buildHostRenders([]*HostState{h})[0]
	if len(hr.byGPU[0]) != 0 {
		t.Fatalf("byGPU[0] = %+v, want empty for a hidden GPU", hr.byGPU[0])
	}
	found := false
	for _, srv := range hr.hostLevel {
		if srv.PID == 42 {
			found = true
		}
	}
	if !found {
		t.Fatal("server on the hidden GPU must become host-level, not be dropped")
	}
}

func TestHideGraphicsProcsKeepsMemTotals(t *testing.T) {
	h := NewHostState("alpha")
	h.view.showGraphics = false
	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})

	for _, p := range h.Snap.Procs {
		if p.Class == model.ProcGraphics {
			t.Fatalf("graphics proc %+v survived", p)
		}
	}
	if len(h.Snap.Procs) != 3 {
		t.Fatalf("procs = %d, want 3 (only the graphics row dropped)", len(h.Snap.Procs))
	}
	// The mem chart's total series reads GPU.MemUsed, which must still
	// include the hidden graphics VRAM.
	if h.Snap.GPUs[0].MemUsed != 8<<30 {
		t.Fatalf("MemUsed = %d, want the full 8GiB including graphics VRAM", h.Snap.GPUs[0].MemUsed)
	}
}

func TestHideAddressesRedactsEndpointsAndErrors(t *testing.T) {
	states := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "192.0.2.40:9832"}},
		ShowAddresses:     false,
		ShowGraphicsProcs: true,
	})
	h := states[0]
	if h.Display != "host1" || h.Name != "alpha" {
		t.Fatalf("Display = %q Name = %q, want host1/alpha", h.Display, h.Name)
	}

	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})
	if got := h.Snap.Servers[0].Endpoint; got != ":8080" {
		t.Fatalf("endpoint = %q, want :8080", got)
	}
	if _, ok := h.GenTPSHist[":8080"]; !ok {
		t.Fatal("gen-t/s history must be keyed by the redacted endpoint")
	}

	err := errors.New(`Get "http://192.0.2.40:9832/v1/snapshot": dial tcp 192.0.2.40:9832: connect: connection refused`)
	h.Apply(client.Update{Host: "alpha", Err: err})
	if want := `Get "http://…/v1/snapshot": dial tcp …: connect: connection refused`; h.ErrMsg != want {
		t.Fatalf("ErrMsg = %q, want %q", h.ErrMsg, want)
	}
}

func TestShowAddressesLeavesDisplayAlone(t *testing.T) {
	states := hostStates(ViewConfig{
		Hosts:             []HostSpec{{Name: "alpha", Addr: "192.0.2.40:9832"}},
		ShowAddresses:     true,
		ShowGraphicsProcs: true,
	})
	h := states[0]
	if h.Display != "alpha" {
		t.Fatalf("Display = %q, want alpha", h.Display)
	}
	h.Apply(client.Update{Host: "alpha", Snapshot: viewSnapshot(sampleTS)})
	if got := h.Snap.Servers[0].Endpoint; got != "http://127.0.0.1:8080" {
		t.Fatalf("endpoint = %q, want unredacted", got)
	}
}

func TestRedactStrings(t *testing.T) {
	cases := []struct {
		addr string
		want []string
	}{
		{"", nil},
		{"192.0.2.40:9832", []string{"192.0.2.40:9832", "192.0.2.40"}},
		{"http://192.0.2.40:9832", []string{"http://192.0.2.40:9832", "192.0.2.40:9832", "192.0.2.40"}},
		{"https://gpu.example.com", []string{"https://gpu.example.com", "gpu.example.com"}},
	}
	for _, c := range cases {
		if got := redactStrings(c.addr); !reflect.DeepEqual(got, c.want) {
			t.Errorf("redactStrings(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestViewFor(t *testing.T) {
	cfg := config.Config{
		Hosts: []config.HostConfig{
			{Name: "alpha", Local: true, GPUs: []int{1}},
			{Name: "bravo", Addr: "192.0.2.40:9832"},
		},
		UI: config.UIConfig{ShowAddresses: false, ShowGraphicsProcs: true},
	}

	v := viewFor(cfg, []string{"192.0.2.50:9832"}, false)
	want := []HostSpec{
		{Name: "alpha", GPUs: []int{1}},
		{Name: "bravo", Addr: "192.0.2.40:9832"},
		{Name: "192.0.2.50:9832", Addr: "192.0.2.50:9832"},
	}
	if !reflect.DeepEqual(v.Hosts, want) {
		t.Fatalf("hosts = %+v, want %+v", v.Hosts, want)
	}
	if v.ShowAddresses || !v.ShowGraphicsProcs {
		t.Fatalf("toggles = %+v, want carried through from cfg.UI", v)
	}

	// Demo hosts are fixed and carry no per-host gpus list, but the global
	// toggles still apply.
	d := viewFor(cfg, nil, true)
	if len(d.Hosts) != 2 || d.Hosts[0].Name != "demo-rig" || d.Hosts[1].Name != "demo-node" {
		t.Fatalf("demo hosts = %+v", d.Hosts)
	}
	if d.ShowAddresses {
		t.Fatal("demo mode must still honour show_addresses=false")
	}
}
