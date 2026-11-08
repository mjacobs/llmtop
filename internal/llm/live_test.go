package llm

import (
	"net/url"
	"os"
	"testing"

	"github.com/mjacobs/llmtop/internal/model"
)

func TestLiveAdapter(t *testing.T) {
	if os.Getenv("LLMTOP_LIVE_TEST") != "1" {
		t.Skip("set LLMTOP_LIVE_TEST=1 to run against a real engine")
	}
	endpoint := os.Getenv("LLMTOP_LIVE_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		t.Fatal("LLMTOP_LIVE_ENDPOINT must be an HTTP origin without credentials, path, query or fragment")
	}
	var adapter Adapter
	switch os.Getenv("LLMTOP_LIVE_KIND") {
	case "llama.cpp":
		if os.Getenv("LLMTOP_LIVE_ALLOW_LLAMA_ACTIVITY") != "1" {
			t.Fatal("set LLMTOP_LIVE_ALLOW_LLAMA_ACTIVITY=1 only for a llama.cpp server you are willing to wake")
		}
		adapter = NewLlamaCpp()
	case "vllm":
		adapter = NewVLLM()
	case "ninfer":
		adapter = NewNInfer()
	case "strata":
		adapter = NewStrata()
	default:
		t.Fatal("LLMTOP_LIVE_KIND must be llama.cpp, vllm, ninfer or strata")
	}

	sample := adapter.Fetch(t.Context(), normalizeEndpoint(endpoint), FetchHint{APIKey: os.Getenv("LLMTOP_LIVE_TOKEN")})
	if sample.Kind != adapter.Kind() || (sample.Status != model.StatusOK && sample.Status != model.StatusSleeping) {
		t.Fatalf("%s adapter reported status %q", adapter.Kind(), sample.Status)
	}
	if sample.Model == "" && sample.Role != "router" {
		t.Fatal("the live adapter reported neither a model nor a router")
	}
	t.Logf("%s adapter returned a plausible %s sample", adapter.Kind(), sample.Status)
}
