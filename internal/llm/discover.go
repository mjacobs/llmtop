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

// Discover merges process-scan detections with static config endpoints.
func Discover(table map[int32]procs.ProcInfo, adapters []Adapter, static []string) []Discovered {
	byEP := map[string]Discovered{}
	for _, info := range table {
		for _, a := range adapters {
			if ep, ok := a.Detect(info.Cmdline); ok {
				ep = normalizeEndpoint(ep)
				if prev, dup := byEP[ep]; !dup || prev.PID == 0 {
					byEP[ep] = Discovered{Endpoint: ep, PID: info.PID, Adapter: a}
				}
				break
			}
		}
	}
	for _, url := range static {
		ep := normalizeEndpoint(url)
		if _, dup := byEP[ep]; !dup {
			byEP[ep] = Discovered{Endpoint: ep, Adapter: adapters[0]}
		}
	}
	out := make([]Discovered, 0, len(byEP))
	for _, d := range byEP {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

func normalizeEndpoint(ep string) string { return strings.TrimRight(ep, "/") }
