package llm

import (
	"context"

	"github.com/mjacobs/llmtop/internal/model"
)

// Adapter integrates one LLM serving engine. Fetch never returns an error:
// failures are encoded in Status so a flaky server degrades its own panel
// instead of the tick.
type Adapter interface {
	Kind() string
	Detect(cmdline []string) (endpoint string, ok bool)
	Fetch(ctx context.Context, endpoint string, hint FetchHint) model.LLMServer
}

// Adapters is the collector's built-in set, tried in order on each process.
// Engine kinds, as reported by Adapter.Kind and named in a static
// endpoint's kind key.
const (
	KindLlamaCpp = llamaKind
	KindVLLM     = vllmKind
	KindNInfer   = ninferKind
)

// Kinds lists the engine kinds Adapters covers, in adapter order. Config
// validation uses it so a new adapter needs no second list.
func Kinds() []string {
	as := Adapters()
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Kind()
	}
	return out
}

func Adapters() []Adapter {
	return []Adapter{NewLlamaCpp(), NewVLLM(), NewNInfer()}
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
