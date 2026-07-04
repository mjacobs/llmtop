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
	Fetch(ctx context.Context, endpoint string) model.LLMServer
}
