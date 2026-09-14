package llm

import (
	"sort"
	"strings"

	"github.com/mjacobs/llmtop/internal/procs"
)

type Discovered struct {
	Endpoint string
	PID      int32 // 0 when declared via static config only
	Adapter  Adapter
}

// StaticEndpoint is an LLM server declared in config because the process
// scan cannot see it (a container, say). Kind names the engine behind the
// URL so the right adapter queries it — a vLLM server polled with the
// llama.cpp adapter reports nothing useful. Empty Kind means llama.cpp,
// which is what the key defaulted to before engines other than llama.cpp
// were supported.
type StaticEndpoint struct {
	URL  string
	Kind string
}

// Discover merges process-scan detections with static config endpoints.
func Discover(table map[int32]procs.ProcInfo, adapters []Adapter, static []StaticEndpoint) []Discovered {
	byEP := map[string]Discovered{}
	for _, info := range table {
		for _, a := range adapters {
			if ep, ok := a.Detect(info.Cmdline); ok {
				ep = normalizeEndpoint(ep)
				// Keep the entry with a real PID; on a scan-vs-scan
				// collision, deterministically keep the smaller PID.
				if prev, dup := byEP[ep]; !dup || prev.PID == 0 || (info.PID != 0 && info.PID < prev.PID) {
					byEP[ep] = Discovered{Endpoint: ep, PID: info.PID, Adapter: a}
				}
				break
			}
		}
	}
	// A static endpoint is useless without an adapter to fetch it.
	for _, se := range static {
		ep := normalizeEndpoint(se.URL)
		if _, dup := byEP[ep]; dup {
			continue
		}
		if a := adapterForKind(adapters, se.Kind); a != nil {
			byEP[ep] = Discovered{Endpoint: ep, Adapter: a}
		}
	}
	out := make([]Discovered, 0, len(byEP))
	for _, d := range byEP {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

// adapterForKind picks the adapter that speaks kind's API. An empty kind
// means llama.cpp, falling back to the first adapter when llama.cpp is not
// among them (a single-adapter setup has nothing to disambiguate). A kind
// no adapter serves yields nil, and the endpoint is dropped rather than
// queried with the wrong API.
func adapterForKind(adapters []Adapter, kind string) Adapter {
	for _, a := range adapters {
		if a.Kind() == kind || (kind == "" && a.Kind() == KindLlamaCpp) {
			return a
		}
	}
	if kind == "" && len(adapters) > 0 {
		return adapters[0]
	}
	return nil
}

func normalizeEndpoint(ep string) string { return strings.TrimRight(ep, "/") }
