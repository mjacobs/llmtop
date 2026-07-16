package tui

import (
	"github.com/mjacobs/llmtop/internal/model"
)

// hostRender pairs a host with its per-frame server→GPU mapping so the
// solver and renderers agree on where every server block goes.
type hostRender struct {
	h         *HostState
	byGPU     map[int][]*model.LLMServer
	hostLevel []*model.LLMServer
}

// buildHostRenders computes the server→GPU mapping for every host once per
// frame.
func buildHostRenders(hosts []*HostState) []hostRender {
	hrs := make([]hostRender, 0, len(hosts))
	for _, h := range hosts {
		hr := hostRender{h: h}
		hr.byGPU, hr.hostLevel = mapServersToGPUs(h.Snap)
		hrs = append(hrs, hr)
	}
	return hrs
}

// mapServersToGPUs assigns each server to every GPU its process runs on,
// matching LLMServer.PID against Procs[].GPUIndex (a tensor-split server
// appears under each GPU it touches). Servers with a router role, no PID,
// or a PID absent from Procs are host-level. Order within each bucket
// follows Snapshot.Servers order; no server is ever dropped.
func mapServersToGPUs(snap *model.Snapshot) (byGPU map[int][]*model.LLMServer, hostLevel []*model.LLMServer) {
	byGPU = map[int][]*model.LLMServer{}
	if snap == nil {
		return byGPU, nil
	}
	pidGPUs := map[int32][]int{}
	seen := map[[2]int64]bool{} // dedupe (pid, gpu): Procs may repeat a pair
	for _, p := range snap.Procs {
		k := [2]int64{int64(p.PID), int64(p.GPUIndex)}
		if p.PID != 0 && !seen[k] {
			seen[k] = true
			pidGPUs[p.PID] = append(pidGPUs[p.PID], p.GPUIndex)
		}
	}
	for si := range snap.Servers {
		srv := &snap.Servers[si]
		gpus := pidGPUs[srv.PID]
		if srv.Role == "router" || srv.PID == 0 || len(gpus) == 0 {
			hostLevel = append(hostLevel, srv)
			continue
		}
		for _, gi := range gpus {
			byGPU[gi] = append(byGPU[gi], srv)
		}
	}
	return byGPU, hostLevel
}
