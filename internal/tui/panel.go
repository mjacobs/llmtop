package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

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

// renderGraph renders a two-series braille area chart, coloring "llm"-class
// cells with accent and "total"-class cells with base.
func renderGraph(total, llm []float64, chartW, chartH int, base, accent lipgloss.Style) string {
	rows := BrailleGraph(total, llm, 100, chartW, chartH)
	var b strings.Builder
	for y, row := range rows {
		if y > 0 {
			b.WriteByte('\n')
		}
		for _, cell := range row {
			switch cell[1] {
			case "llm":
				b.WriteString(accent.Render(cell[0]))
			case "total":
				b.WriteString(base.Render(cell[0]))
			default:
				b.WriteString(cell[0])
			}
		}
	}
	return b.String()
}

// utilLabel renders the util-chart label: `GPU0 NVIDIA RTX 4090 — util 72% (llm 65%)`.
func utilLabel(gpu model.GPU, th Theme) string {
	return fmt.Sprintf("GPU%d %s — ", gpu.Index, gpu.Name) +
		th.Util.Render(fmt.Sprintf("util %d%%", gpu.UtilPct)) + " " +
		th.LLM.Render(fmt.Sprintf("(llm %d%%)", gpu.LLMUtilPct))
}

// memLabel renders the mem-chart label: `mem 61% 22.5/32.0G (llm 20.0G)`.
func memLabel(gpu model.GPU, th Theme) string {
	memPct := int(pct(gpu.MemUsed, gpu.MemTotal) + 0.5)
	return th.Mem.Render(fmt.Sprintf("mem %d%% %s", memPct, FmtMemPair(gpu.MemUsed, gpu.MemTotal))) + " " +
		th.LLM.Render(fmt.Sprintf("(llm %s)", FmtBytes(gpu.LLMMemBytes)))
}

// serverLine1 renders a server's identity line: bullet, model/endpoint,
// kind, endpoint, status.
func serverLine1(srv *model.LLMServer, th Theme) string {
	label := srv.Model
	if label == "" {
		label = srv.Endpoint
	}
	return th.LLM.Render("● ") + strings.Join([]string{label, srv.Kind, srv.Endpoint, srv.Status}, "  ")
}

// serverLine2 renders a server's detail line: generation stats, a "metrics
// disabled" hint, or a router tag — indented two spaces under line 1.
func serverLine2(srv *model.LLMServer, th Theme) string {
	switch {
	case srv.Role == "router":
		return "  " + th.Dim.Render("router — spawns model instances on demand")
	case srv.Metrics != nil:
		m := srv.Metrics
		parts := []string{
			fmt.Sprintf("prompt %.1f t/s", m.PromptTPS),
			fmt.Sprintf("gen %.1f t/s", m.PredictedTPS),
		}
		if m.KVCacheUsage >= 0 {
			parts = append(parts, fmt.Sprintf("kv %.0f%%", m.KVCacheUsage*100))
		}
		parts = append(parts, fmt.Sprintf("queue %d", m.RequestsDeferred))
		if srv.SlotsTotal > 0 {
			parts = append(parts, fmt.Sprintf("slots %d/%d", m.RequestsProcessing, srv.SlotsTotal))
		}
		parts = append(parts, fmt.Sprintf("ctx %d", srv.CtxSize))
		return "  " + th.LLM.Render(strings.Join(parts, " · "))
	default:
		return "  " + th.Dim.Render("metrics disabled — start llama-server with --metrics")
	}
}
