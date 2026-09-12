package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/mjacobs/llmtop/internal/gpu"
	"github.com/mjacobs/llmtop/internal/llm"
	"github.com/mjacobs/llmtop/internal/model"
	"github.com/mjacobs/llmtop/internal/procs"
)

func fakeLlama(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model_alias":"testmodel","model_path":"/m/t.gguf","total_slots":2,"default_generation_settings":{"n_ctx":8192}}`))
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

func TestCollectMergesDuplicateProcEntries(t *testing.T) {
	// A PID appearing in both NVML compute and graphics lists must yield
	// ONE GPUProc: max MemBytes, max SMUtilPct, Graphics OR'd for classify.
	vendor := gpu.NewFake(1)
	vendor.SetProcs([]gpu.ProcSample{
		{PID: 100, GPUIndex: 0, MemBytes: 4 << 30, SMUtilPct: 10, Graphics: false},
		{PID: 100, GPUIndex: 0, MemBytes: 2 << 30, SMUtilPct: 60, Graphics: true},
	})
	scanner := &procs.Fake{Table: map[int32]procs.ProcInfo{
		100: {PID: 100, PPID: 1, Comm: "gnome-shell"},
	}}

	c := New("h", vendor, scanner, nil, nil, "0.1.0")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	s := c.Collect(context.Background())

	if len(s.Procs) != 1 {
		t.Fatalf("want 1 merged proc, got %d: %+v", len(s.Procs), s.Procs)
	}
	p := s.Procs[0]
	if p.PID != 100 || p.GPUIndex != 0 {
		t.Fatalf("merged key = (%d,%d)", p.PID, p.GPUIndex)
	}
	if p.MemBytes != 4<<30 {
		t.Fatalf("MemBytes = %d, want max %d", p.MemBytes, uint64(4<<30))
	}
	if p.SMUtilPct != 60 {
		t.Fatalf("SMUtilPct = %d, want max 60", p.SMUtilPct)
	}
	if p.Class != model.ProcGraphics {
		t.Fatalf("Class = %q, want graphics (Graphics OR'd true, comm not LLM-ish)", p.Class)
	}
	if p.Name != "gnome-shell" {
		t.Fatalf("Name = %q", p.Name)
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

// echoAdapter answers every fetch with a fixed server record (Endpoint filled
// in), so tests can feed the collector strings of their choosing.
type echoAdapter struct{ srv model.LLMServer }

func (echoAdapter) Kind() string { return "fake" }
func (echoAdapter) Detect(cmdline []string) (string, bool) {
	if len(cmdline) == 2 && cmdline[0] == "fake-server" {
		return "http://127.0.0.1:" + cmdline[1], true
	}
	return "", false
}
func (e echoAdapter) Fetch(_ context.Context, endpoint string, _ llm.FetchHint) model.LLMServer {
	srv := e.srv
	srv.Endpoint = endpoint
	return srv
}

func TestCollectVendorFailureStillDiscoversServers(t *testing.T) {
	// GPU sampling failing (NVML never came up, transient error) must not
	// short-circuit the tick: the proc scan and LLM discovery still run.
	scanner := &procs.Fake{Table: map[int32]procs.ProcInfo{
		100: {PID: 100, PPID: 1, Comm: "fake-server", Cmdline: []string{"fake-server", "1001"}},
	}}
	ad := echoAdapter{srv: model.LLMServer{Kind: "fake", Status: model.StatusOK, Model: "m"}}
	c := New("h", failingVendor{}, scanner, []llm.Adapter{ad}, []string{"http://127.0.0.1:1002"}, "0.1.0")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	s := c.Collect(context.Background())

	if len(s.Errors) == 0 || len(s.GPUs) != 0 || len(s.Procs) != 0 {
		t.Fatalf("want gpu error and no gpu data, got %+v", s)
	}
	if len(s.Servers) != 2 {
		t.Fatalf("want scanned + static server, got %+v", s.Servers)
	}
	byEP := map[string]model.LLMServer{}
	for _, srv := range s.Servers {
		byEP[srv.Endpoint] = srv
	}
	if srv := byEP["http://127.0.0.1:1001"]; srv.PID != 100 || srv.Model != "m" {
		t.Fatalf("scanned server = %+v", srv)
	}
	if srv := byEP["http://127.0.0.1:1002"]; srv.PID != 0 || srv.Model != "m" {
		t.Fatalf("static server = %+v", srv)
	}
}

type initFailingVendor struct{ failingVendor }

func (initFailingVendor) Init() error { return context.DeadlineExceeded }

func TestInitRecordsHostnameBeforeVendor(t *testing.T) {
	c := New("h", initFailingVendor{}, &procs.Fake{}, nil, nil, "0.1.0")
	if err := c.Init(); err == nil {
		t.Fatal("Init: want vendor error")
	}
	s := c.Collect(context.Background())
	hn, _ := os.Hostname()
	if s.Host.Hostname != hn {
		t.Fatalf("Hostname = %q, want %q even when the vendor fails to init", s.Host.Hostname, hn)
	}
	if s.Host.DriverVersion != "" {
		t.Fatalf("DriverVersion = %q, want empty when the vendor fails to init", s.Host.DriverVersion)
	}
}

func TestCollectSanitizesUntrustedStrings(t *testing.T) {
	// Comm names, adapter-reported model aliases and the like come from
	// other processes; control bytes must be gone by the time the
	// snapshot leaves the collector.
	vendor := gpu.NewFake(1)
	vendor.SetProcs([]gpu.ProcSample{{PID: 100, GPUIndex: 0, MemBytes: 1 << 30, SMUtilPct: 10}})
	scanner := &procs.Fake{Table: map[int32]procs.ProcInfo{
		100: {PID: 100, PPID: 1, Comm: "fake\x1b[31m-server", Cmdline: []string{"fake-server", "1001"}},
	}}
	ad := echoAdapter{srv: model.LLMServer{Kind: "fake", Status: model.StatusOK, Model: "\x1b[31mevil\r\nmodel"}}
	c := New("h\x1b[2J", vendor, scanner, []llm.Adapter{ad}, nil, "0.1.0")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	s := c.Collect(context.Background())

	if len(s.Servers) != 1 || s.Servers[0].Model != "[31mevilmodel" {
		t.Fatalf("servers = %+v, want model alias with control bytes stripped", s.Servers)
	}
	if len(s.Procs) != 1 || s.Procs[0].Name != "fake[31m-server" {
		t.Fatalf("procs = %+v, want comm with control bytes stripped", s.Procs)
	}
	if s.Host.Name != "h[2J" {
		t.Fatalf("Host.Name = %q, want control bytes stripped", s.Host.Name)
	}
}

// hintAdapter records the FetchHint per endpoint to pin the collector's
// GPU-activity plumbing (SMUtilPct > 0 for the server's PID => GPUActive).
type hintAdapter struct {
	mu    sync.Mutex
	hints map[string]llm.FetchHint
}

func (h *hintAdapter) Kind() string { return "fake" }
func (h *hintAdapter) Detect(cmdline []string) (string, bool) {
	if len(cmdline) == 2 && cmdline[0] == "fake-server" {
		return "http://127.0.0.1:" + cmdline[1], true
	}
	return "", false
}
func (h *hintAdapter) Fetch(ctx context.Context, endpoint string, hint llm.FetchHint) model.LLMServer {
	h.mu.Lock()
	h.hints[endpoint] = hint
	h.mu.Unlock()
	return model.LLMServer{Kind: "fake", Endpoint: endpoint, Status: model.StatusOK}
}

func TestCollectPassesGPUActivityHint(t *testing.T) {
	vendor := gpu.NewFake(1)
	vendor.SetProcs([]gpu.ProcSample{
		{PID: 100, GPUIndex: 0, MemBytes: 8 << 30, SMUtilPct: 60}, // busy
		{PID: 101, GPUIndex: 0, MemBytes: 8 << 30, SMUtilPct: 0},  // idle
	})
	scanner := &procs.Fake{Table: map[int32]procs.ProcInfo{
		100: {PID: 100, PPID: 1, Comm: "fake-server", Cmdline: []string{"fake-server", "1001"}},
		101: {PID: 101, PPID: 1, Comm: "fake-server", Cmdline: []string{"fake-server", "1002"}},
	}}
	ad := &hintAdapter{hints: map[string]llm.FetchHint{}}

	c := New("h", vendor, scanner, []llm.Adapter{ad}, nil, "test")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Collect(context.Background())

	if !ad.hints["http://127.0.0.1:1001"].GPUActive {
		t.Fatal("busy PID 100: GPUActive hint = false, want true")
	}
	if ad.hints["http://127.0.0.1:1002"].GPUActive {
		t.Fatal("idle PID 101: GPUActive hint = true, want false")
	}
}

func TestCollectAttributesServerGPUsThroughChildren(t *testing.T) {
	// vLLM: the API server (discovered, PID 100) holds no VRAM; its
	// VLLM::EngineCore child (PID 101) does. The server must be attributed
	// to GPU 0 via the child, and the child classified as llm. A llama-server
	// (PID 200) holding its own memory on GPU 1 is attributed directly.
	vendor := gpu.NewFake(2)
	vendor.SetProcs([]gpu.ProcSample{
		{PID: 101, GPUIndex: 0, MemBytes: 2 << 30, SMUtilPct: 100},
		{PID: 200, GPUIndex: 1, MemBytes: 4 << 30, SMUtilPct: 10},
		{PID: 300, GPUIndex: 0, MemBytes: 1 << 30, SMUtilPct: -1},
	})
	scanner := &procs.Fake{Table: map[int32]procs.ProcInfo{
		100: {PID: 100, PPID: 1, Comm: "fake-server", Cmdline: []string{"fake-server", "1001"}},
		101: {PID: 101, PPID: 100, Comm: "VLLM::EngineCor", Cmdline: []string{"VLLM::EngineCore"}},
		200: {PID: 200, PPID: 1, Comm: "fake-server", Cmdline: []string{"fake-server", "1002"}},
		300: {PID: 300, PPID: 1, Comm: "onnx-runner"},
	}}
	ad := echoAdapter{srv: model.LLMServer{Kind: "fake", Status: model.StatusOK}}
	c := New("h", vendor, scanner, []llm.Adapter{ad}, []string{"http://127.0.0.1:1003"}, "0.1.0")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	s := c.Collect(context.Background())

	byEP := map[string]model.LLMServer{}
	for _, srv := range s.Servers {
		byEP[srv.Endpoint] = srv
	}
	if got := byEP["http://127.0.0.1:1001"].GPUs; len(got) != 1 || got[0] != 0 {
		t.Fatalf("vllm-style server GPUs = %v, want [0] (via EngineCore child)", got)
	}
	if got := byEP["http://127.0.0.1:1002"].GPUs; len(got) != 1 || got[0] != 1 {
		t.Fatalf("direct server GPUs = %v, want [1]", got)
	}
	if got := byEP["http://127.0.0.1:1003"].GPUs; got != nil {
		t.Fatalf("static (PID 0) server GPUs = %v, want none", got)
	}
	byPID := map[int32]model.GPUProc{}
	for _, p := range s.Procs {
		byPID[p.PID] = p
	}
	if byPID[101].Class != model.ProcLLM || byPID[300].Class != model.ProcOther {
		t.Fatalf("classes: engine=%q other=%q", byPID[101].Class, byPID[300].Class)
	}
	if s.GPUs[0].LLMMemBytes != 2<<30 {
		t.Fatalf("GPU0 LLMMemBytes = %d, want the engine's 2G", s.GPUs[0].LLMMemBytes)
	}
}
