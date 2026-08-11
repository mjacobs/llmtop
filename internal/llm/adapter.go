package llm

import (
	"context"

	"github.com/mjacobs/llmtop/internal/model"
)

// Adapter integrates one LLM serving engine (llama.cpp in v1; vllm/sglang
// later). Fetch never returns an error: failures are encoded in Status so
// a flaky server degrades its own panel instead of the tick.
type Adapter interface {
	Kind() string
	Detect(cmdline []string) (endpoint string, ok bool)
	Fetch(ctx context.Context, endpoint string, hint FetchHint) model.LLMServer
}

// FetchHint carries collector-side observations the adapter cannot see from
// HTTP alone. GPUActive is true when the server's process showed nonzero GPU
// SM utilization this tick — used to re-enable /metrics polling on servers
// whose idle-sleep timer a poll would otherwise reset (llama.cpp counts any
// /metrics scrape as activity, so unconditional polling keeps the model
// resident forever).
type FetchHint struct {
	GPUActive bool
}
