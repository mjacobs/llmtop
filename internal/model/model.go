// Package model defines llmtop's wire types, shared by collector, agent,
// client, and TUI. The agent serializes Snapshot to JSON verbatim.
package model

import (
	"strings"
	"time"
	"unicode"
)

type HostInfo struct {
	Name          string `json:"name"`     // config name (display)
	Hostname      string `json:"hostname"` // os.Hostname of collector
	DriverVersion string `json:"driver_version"`
	AgentVersion  string `json:"agent_version"`
}

type GPU struct {
	Index     int    `json:"index"`
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	UtilPct   int    `json:"util_pct"`
	MemUsed   uint64 `json:"mem_used"`
	MemTotal  uint64 `json:"mem_total"`
	TempC     int    `json:"temp_c"`
	PowerW    int    `json:"power_w"`
	PowerCapW int    `json:"power_cap_w"`
	// LLM-vs-other split, computed by the collector.
	// Memory split is exact (per-process VRAM); util split is approximate
	// (NVML per-process SM samples, clamped to UtilPct).
	LLMMemBytes   uint64 `json:"llm_mem_bytes"`
	OtherMemBytes uint64 `json:"other_mem_bytes"`
	LLMUtilPct    int    `json:"llm_util_pct"`
}

type ProcClass string

const (
	ProcLLM      ProcClass = "llm"
	ProcGraphics ProcClass = "graphics"
	ProcOther    ProcClass = "other"
)

type GPUProc struct {
	PID       int32     `json:"pid"`
	Name      string    `json:"name"` // comm
	GPUIndex  int       `json:"gpu_index"`
	MemBytes  uint64    `json:"mem_bytes"`
	SMUtilPct int       `json:"sm_util_pct"` // -1 = unknown
	Class     ProcClass `json:"class"`
}

const (
	StatusOK          = "ok"
	StatusLoading     = "loading"
	StatusUnreachable = "unreachable"
	// StatusSleeping: the server process is alive but its model is unloaded
	// (llama.cpp --sleep-idle-seconds); VRAM is released until the next
	// inference request wakes it.
	StatusSleeping = "sleeping"
)

type LLMServer struct {
	Kind       string `json:"kind"`     // adapter name, "llama.cpp"
	Endpoint   string `json:"endpoint"` // http://host:port
	PID        int32  `json:"pid,omitempty"`
	Role       string `json:"role,omitempty"` // "router" for llama.cpp router mode
	Status     string `json:"status"`         // StatusOK | StatusLoading | StatusUnreachable | StatusSleeping
	Model      string `json:"model"`          // alias, else basename(model_path)
	CtxSize    int    `json:"ctx_size"`
	SlotsTotal int    `json:"slots_total"`
	// Metrics is nil when /metrics is unavailable (sole signal for
	// "metrics off"; Status stays "ok" per spec).
	Metrics *LLMMetrics `json:"metrics,omitempty"`
}

type LLMMetrics struct {
	PromptTokensTotal    float64 `json:"prompt_tokens_total"`
	PredictedTokensTotal float64 `json:"predicted_tokens_total"`
	PromptTPS            float64 `json:"prompt_tps"`     // instantaneous (counter delta); avg gauge on first sample
	PredictedTPS         float64 `json:"predicted_tps"`  // same semantics
	KVCacheUsage         float64 `json:"kv_cache_usage"` // 0..1; -1 = unknown (metric absent in this build)
	RequestsProcessing   int     `json:"requests_processing"`
	RequestsDeferred     int     `json:"requests_deferred"`
	// PredictedTPSEst is true when PredictedTPS is a decode-derived
	// in-flight estimate rather than a completion-window measurement.
	PredictedTPSEst bool `json:"predicted_tps_est,omitempty"`
}

type Snapshot struct {
	Host      HostInfo    `json:"host"`
	Timestamp time.Time   `json:"timestamp"`
	GPUs      []GPU       `json:"gpus"`
	Procs     []GPUProc   `json:"procs"`
	Servers   []LLMServer `json:"servers"`
	Errors    []string    `json:"errors,omitempty"` // non-fatal collection errors
}

// CleanString returns s with control characters (Unicode Cc: U+0000..U+001F
// and U+007F..U+009F, so newlines, tabs and ESC included) removed. Strings
// that reach the terminal come from untrusted places — another process's
// argv, a server's /props, a remote agent — and must not be able to carry
// escape sequences or line breaks into the display. Returns s itself when
// there is nothing to remove.
func CleanString(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// Sanitize runs CleanString over every free-form string in the snapshot.
// Collectors call it before handing a snapshot on, and clients call it right
// after decoding one from a remote agent, so downstream code (including map
// keys derived from Endpoint) only ever sees clean values.
func (s *Snapshot) Sanitize() {
	s.Host.Name = CleanString(s.Host.Name)
	s.Host.Hostname = CleanString(s.Host.Hostname)
	s.Host.DriverVersion = CleanString(s.Host.DriverVersion)
	s.Host.AgentVersion = CleanString(s.Host.AgentVersion)
	for i := range s.GPUs {
		s.GPUs[i].UUID = CleanString(s.GPUs[i].UUID)
		s.GPUs[i].Name = CleanString(s.GPUs[i].Name)
	}
	for i := range s.Procs {
		s.Procs[i].Name = CleanString(s.Procs[i].Name)
	}
	for i := range s.Servers {
		srv := &s.Servers[i]
		srv.Kind = CleanString(srv.Kind)
		srv.Endpoint = CleanString(srv.Endpoint)
		srv.Role = CleanString(srv.Role)
		srv.Status = CleanString(srv.Status)
		srv.Model = CleanString(srv.Model)
	}
	for i := range s.Errors {
		s.Errors[i] = CleanString(s.Errors[i])
	}
}
