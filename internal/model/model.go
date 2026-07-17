// Package model defines llmtop's wire types, shared by collector, agent,
// client, and TUI. The agent serializes Snapshot to JSON verbatim.
package model

import "time"

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
)

type LLMServer struct {
	Kind       string `json:"kind"`     // adapter name, "llama.cpp"
	Endpoint   string `json:"endpoint"` // http://host:port
	PID        int32  `json:"pid,omitempty"`
	Role       string `json:"role,omitempty"` // "router" for llama.cpp router mode
	Status     string `json:"status"`         // StatusOK | StatusLoading | StatusUnreachable
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
