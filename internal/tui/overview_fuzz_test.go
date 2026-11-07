package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/model"
)

func FuzzRenderOverviewFits(f *testing.F) {
	// Include every dimension used by the overview fit/robustness tests and
	// App's tiny-terminal fit test. Encode sizes minus one to preserve them
	// through the bounded fuzz-input mapping, including the upper bounds.
	sizes := [][2]int{
		{100, 63}, {140, 40}, {100, 45}, {120, 200}, {220, 55},
		{220, 200}, {400, 30}, {100, 8}, {100, 12}, {80, 5},
		{200, 6}, {120, 40}, {5, 3}, {80, 24}, {140, 50},
		{400, 200}, {20, 5}, {200, 100}, {80, 2}, {80, 3},
		{80, 1}, {80, 60}, {100, 60}, {100, 40}, {100, 1},
	}
	shapes := [][]byte{nil, {2, 2, 0, 1, 1, 7, 0, 4, 2}, {8, 4, 7}, {0, 4, 3}}
	names := []string{"alpha", "bravo界🙂", "charlie\n\t\x1b[31m", ""}
	for _, size := range sizes {
		for mode := uint8(0); mode < 4; mode++ {
			f.Add(uint16(size[0]-1), uint16(size[1]-1), shapes[mode], names[mode], mode)
		}
	}
	f.Fuzz(func(t *testing.T, w, h uint16, shape []byte, name string, mode uint8) {
		width, height := int(w%400)+1, int(h%200)+1
		hosts := overviewFuzzHosts(shape, model.CleanString(name))
		pref := LayoutPref{Mode: LayoutMode(mode % 4), Columns: int(mode/4)%8 + 1}
		if mode&16 != 0 {
			pref.Slots = []LayoutSlot{{Host: "bravo", GPU: 0}, {Host: "alpha", GPU: 1}, {Host: "alpha", GPU: 0}}
		}
		th := DefaultTheme()
		out := RenderOverview(hosts, int(mode), width, height, pref, th)
		if out != "" {
			lines := strings.Split(out, "\n")
			if len(lines) > height {
				t.Fatalf("%dx%d mode %s: %d lines", width, height, pref.Label(), len(lines))
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("%dx%d mode %s: line %d width %d: %q", width, height, pref.Label(), i, got, line)
				}
			}
		}

		// Remove borders and measure natural content with spare vertical room.
		// Passing the counter as the height would mask undercounts by clipping,
		// and counting padded output would mask overcounts. At least 64 columns
		// keeps these checks within the solver's valid GPU-panel width range.
		natural := th
		natural.Border = lipgloss.NewStyle()
		natural.BorderActive = lipgloss.NewStyle()
		panelW := max(width, minPanelInner+4)
		for hi, hr := range applySlots(buildHostRenders(hosts), pref.Slots) {
			if got, want := overviewFuzzContentHeight(renderHostStrip(hr, panelW, 0, natural)), hostStripContentLines(hr); got != want {
				t.Fatalf("host %d: strip content lines %d, counter %d", hi, got, want)
			}
			if hr.h.Snap == nil {
				continue
			}
			for si := range hr.h.Snap.Servers {
				srv := &hr.h.Snap.Servers[si]
				if got, want := len(serverBlockLines(hr.h, srv, th, panelW-4)), serverBlockLineCount(srv); got != want {
					t.Fatalf("host %d server %d: block lines %d, counter %d", hi, si, got, want)
				}
			}
			for pos := 0; pos < hostPanelCount(hr); pos++ {
				for _, tier := range []tier{tierFull, tierCharts} {
					chartH := int(h%8) + 1
					rendered := renderGPUPanel(hr, pos, panelW, 64, chartH, tier == tierFull, false, natural)
					got := overviewFuzzContentHeight(rendered) - 2*chartH
					if want := panelFixedContentLines(hr, pos, tier); got != want {
						t.Fatalf("host %d GPU position %d tier %d: fixed content lines %d, counter %d", hi, pos, tier, got, want)
					}
				}
			}
		}
	})
}

// Up to three independently shaped hosts; each triple encodes GPU count,
// server count, and optional processes/errors/staleness. Missing triples
// mean absent hosts, so the empty overview is also exercised.
func overviewFuzzHosts(shape []byte, name string) []*HostState {
	var hosts []*HostState
	for i, id := range []string{"alpha", "bravo", "charlie"} {
		if len(shape) < 3*(i+1) {
			break
		}
		nGPU, nSrv, flags := int(shape[3*i]%9), int(shape[3*i+1]%5), shape[3*i+2]
		host := NewHostState(id)
		host.Display = name
		snap := &model.Snapshot{Timestamp: time.Unix(1_700_000_000, 0), Host: model.HostInfo{Name: id, DriverVersion: name}}
		for gi := 0; gi < nGPU; gi++ {
			snap.GPUs = append(snap.GPUs, model.GPU{Index: gi, Name: name, MemTotal: 24 << 30, MemUsed: 8 << 30, UtilPct: 50})
			if flags&1 != 0 {
				// Three rows use exactly the solver's minimum process allocation.
				for pi := 0; pi < 3; pi++ {
					snap.Procs = append(snap.Procs, model.GPUProc{GPUIndex: gi, PID: int32(100 + gi*3 + pi), Name: name, Class: model.ProcLLM})
				}
			}
		}
		for si := 0; si < nSrv; si++ {
			variant := (int(flags) + si) % 8
			srv := model.LLMServer{Kind: []string{"llama.cpp", "vllm", "ninfer", "strata"}[si%4], Endpoint: fmt.Sprintf("http://127.0.0.1:%d", 8080+si), Model: name, PID: int32(100 + si), Status: []string{model.StatusOK, model.StatusLoading, model.StatusSleeping, model.StatusUnreachable}[variant%4]}
			if variant&1 != 0 {
				srv.Metrics = &model.LLMMetrics{PredictedTPS: 12, KVCacheUsage: .5}
			}
			if variant == 2 {
				srv.Role = "router"
			}
			if variant == 4 {
				srv.MetricsErr = name
			}
			if nGPU > 0 && variant&2 != 0 {
				srv.GPUs = []int{si % nGPU}
			}
			snap.Servers = append(snap.Servers, srv)
		}
		if flags&2 != 0 {
			snap.Errors = []string{name, "alpha: sample unavailable"}
		}
		if flags&8 == 0 {
			host.Apply(client.Update{Host: id, Snapshot: snap})
		}
		if flags&4 != 0 || flags&8 != 0 {
			host.Apply(client.Update{Host: id, Err: errors.New("alpha: " + name)})
		}
		hosts = append(hosts, host)
	}
	return hosts
}

func overviewFuzzContentHeight(s string) int {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return len(lines)
}
