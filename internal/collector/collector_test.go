package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mjacobs/llmtop/internal/gpu"
	"github.com/mjacobs/llmtop/internal/llm"
	"github.com/mjacobs/llmtop/internal/model"
	"github.com/mjacobs/llmtop/internal/procs"
)

func fakeLlama(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model_alias":"testmodel","model_path":"/m/t.gguf","total_slots":2,"default_generation_settings":{"n_ctx":8192}}`))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(501) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCollect(t *testing.T) {
	srv := fakeLlama(t)

	vendor := gpu.NewFake(1)
	vendor.SetProcs([]gpu.ProcSample{
		{PID: 100, GPUIndex: 0, MemBytes: 8 << 30, SMUtilPct: 60},                // llama-server
		{PID: 200, GPUIndex: 0, MemBytes: 2 << 30, SMUtilPct: 5, Graphics: true}, // kwin
		{PID: 300, GPUIndex: 0, MemBytes: 1 << 30, SMUtilPct: -1},                // other compute
	})
	scanner := &procs.Fake{Table: map[int32]procs.ProcInfo{
		100: {PID: 100, PPID: 1, Comm: "llama-server", Cmdline: []string{"llama-server"}}, // no --port flags: NOT discovered via scan
		200: {PID: 200, PPID: 1, Comm: "kwin_wayland"},
		300: {PID: 300, PPID: 1, Comm: "onnx-runner"},
	}}

	c := New("testhost", vendor, scanner, []llm.Adapter{llm.NewLlamaCpp()}, []string{srv.URL}, "0.1.0")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	s := c.Collect(context.Background())

	if s.Host.Name != "testhost" || s.Host.AgentVersion != "0.1.0" || s.Host.DriverVersion == "" {
		t.Fatalf("host = %+v", s.Host)
	}
	if len(s.GPUs) != 1 || len(s.Procs) != 3 {
		t.Fatalf("gpus=%d procs=%d", len(s.GPUs), len(s.Procs))
	}
	// Note: pid 100 IS discovered via scan too (cmdline parses to default
	// endpoint http://127.0.0.1:8080) — but classification comes from its
	// comm either way. Static endpoint srv.URL is fetched successfully.
	var static *model.LLMServer
	for i := range s.Servers {
		if s.Servers[i].Endpoint == srv.URL {
			static = &s.Servers[i]
		}
	}
	if static == nil || static.Model != "testmodel" || static.SlotsTotal != 2 || static.Metrics != nil {
		t.Fatalf("static server = %+v", static)
	}

	byPID := map[int32]model.GPUProc{}
	for _, p := range s.Procs {
		byPID[p.PID] = p
	}
	if byPID[100].Class != model.ProcLLM || byPID[100].Name != "llama-server" {
		t.Fatalf("pid100 = %+v", byPID[100])
	}
	if byPID[200].Class != model.ProcGraphics || byPID[300].Class != model.ProcOther {
		t.Fatalf("classes: %+v / %+v", byPID[200], byPID[300])
	}

	g := s.GPUs[0]
	if g.LLMMemBytes != 8<<30 {
		t.Fatalf("LLMMemBytes = %d", g.LLMMemBytes)
	}
	if g.OtherMemBytes != g.MemUsed-8<<30 {
		t.Fatalf("OtherMemBytes = %d (MemUsed %d)", g.OtherMemBytes, g.MemUsed)
	}
	if g.LLMUtilPct > g.UtilPct || g.LLMUtilPct < 0 {
		t.Fatalf("LLMUtilPct %d out of [0,%d]", g.LLMUtilPct, g.UtilPct)
	}
}

func TestCollectVendorFailure(t *testing.T) {
	c := New("h", failingVendor{}, &procs.Fake{}, nil, nil, "0.1.0")
	_ = c.Init()
	s := c.Collect(context.Background())
	if len(s.Errors) == 0 || len(s.GPUs) != 0 {
		t.Fatalf("want error snapshot, got %+v", s)
	}
	if s.Host.Name != "h" || s.Timestamp.IsZero() {
		t.Fatal("error snapshot must still be stamped")
	}
}

type failingVendor struct{}

func (failingVendor) Init() error                    { return nil }
func (failingVendor) Shutdown()                      {}
func (failingVendor) DriverVersion() (string, error) { return "x", nil }
func (failingVendor) Sample() ([]gpu.DeviceSample, []gpu.ProcSample, error) {
	return nil, nil, context.DeadlineExceeded
}
