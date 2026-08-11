// Package collector orchestrates GPU sampling, process classification,
// and LLM server discovery into model.Snapshot values. One Collector
// serves both the TUI's local host and `llmtop agent`.
package collector

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mjacobs/llmtop/internal/gpu"
	"github.com/mjacobs/llmtop/internal/llm"
	"github.com/mjacobs/llmtop/internal/model"
	"github.com/mjacobs/llmtop/internal/procs"
)

const llmFetchTimeout = 500 * time.Millisecond

type Collector struct {
	name         string
	vendor       gpu.Vendor
	scanner      procs.Scanner
	adapters     []llm.Adapter
	static       []string
	agentVersion string
	driver       string
	hostname     string
}

func New(name string, vendor gpu.Vendor, scanner procs.Scanner, adapters []llm.Adapter, staticEndpoints []string, agentVersion string) *Collector {
	return &Collector{name: name, vendor: vendor, scanner: scanner,
		adapters: adapters, static: staticEndpoints, agentVersion: agentVersion}
}

func (c *Collector) Init() error {
	if err := c.vendor.Init(); err != nil {
		return err
	}
	c.driver, _ = c.vendor.DriverVersion()
	c.hostname, _ = os.Hostname()
	return nil
}

func (c *Collector) Close() { c.vendor.Shutdown() }

func (c *Collector) Collect(ctx context.Context) model.Snapshot {
	s := model.Snapshot{
		Host: model.HostInfo{Name: c.name, Hostname: c.hostname,
			DriverVersion: c.driver, AgentVersion: c.agentVersion},
		Timestamp: time.Now().UTC(),
	}

	devs, gpuProcs, err := c.vendor.Sample()
	if err != nil {
		s.Errors = append(s.Errors, fmt.Sprintf("gpu sample: %v", err))
		return s
	}

	table, err := c.scanner.Procs()
	if err != nil {
		s.Errors = append(s.Errors, fmt.Sprintf("proc scan: %v", err))
		table = nil
	}

	// Per-PID GPU activity from this tick's sample, fed to adapters as a
	// poll hint (see llm.FetchHint).
	activePIDs := map[int32]bool{}
	for _, p := range gpuProcs {
		if p.SMUtilPct > 0 {
			activePIDs[p.PID] = true
		}
	}

	discovered := llm.Discover(table, c.adapters, c.static)
	s.Servers = make([]model.LLMServer, len(discovered))
	var wg sync.WaitGroup
	for i, d := range discovered {
		wg.Add(1)
		go func(i int, d llm.Discovered) {
			defer wg.Done()
			fctx, cancel := context.WithTimeout(ctx, llmFetchTimeout)
			defer cancel()
			srv := d.Adapter.Fetch(fctx, d.Endpoint, llm.FetchHint{GPUActive: activePIDs[d.PID]})
			srv.PID = d.PID
			s.Servers[i] = srv
		}(i, d)
	}
	wg.Wait()

	llmPIDs := map[int32]bool{}
	for _, srv := range s.Servers {
		if srv.PID != 0 {
			llmPIDs[srv.PID] = true
		}
	}

	// Merge duplicate (PID, GPU) entries from compute+graphics lists.
	type key struct {
		pid int32
		gpu int
	}
	merged := map[key]gpu.ProcSample{}
	order := []key{}
	for _, p := range gpuProcs {
		k := key{p.PID, p.GPUIndex}
		if prev, ok := merged[k]; ok {
			if p.MemBytes > prev.MemBytes {
				prev.MemBytes = p.MemBytes
			}
			if p.SMUtilPct > prev.SMUtilPct {
				prev.SMUtilPct = p.SMUtilPct
			}
			prev.Graphics = prev.Graphics || p.Graphics
			merged[k] = prev
			continue
		}
		merged[k] = p
		order = append(order, k)
	}

	llmMem := map[int]uint64{}
	llmUtil := map[int]int{}
	for _, k := range order {
		p := merged[k]
		name := fmt.Sprintf("pid %d", p.PID)
		if info, ok := table[p.PID]; ok && info.Comm != "" {
			name = info.Comm
		}
		class := procs.Classify(p.PID, table, llmPIDs, p.Graphics)
		s.Procs = append(s.Procs, model.GPUProc{
			PID: p.PID, Name: name, GPUIndex: p.GPUIndex,
			MemBytes: p.MemBytes, SMUtilPct: p.SMUtilPct, Class: class,
		})
		if class == model.ProcLLM {
			llmMem[p.GPUIndex] += p.MemBytes
			if p.SMUtilPct > 0 {
				llmUtil[p.GPUIndex] += p.SMUtilPct
			}
		}
	}

	s.GPUs = make([]model.GPU, len(devs))
	for i, d := range devs {
		g := model.GPU{Index: d.Index, UUID: d.UUID, Name: d.Name,
			UtilPct: d.UtilPct, MemUsed: d.MemUsed, MemTotal: d.MemTotal,
			TempC: d.TempC, PowerW: d.PowerW, PowerCapW: d.PowerCapW}
		g.LLMMemBytes = llmMem[d.Index]
		if g.LLMMemBytes > g.MemUsed {
			g.LLMMemBytes = g.MemUsed
		}
		g.OtherMemBytes = g.MemUsed - g.LLMMemBytes
		u := llmUtil[d.Index]
		if u > g.UtilPct {
			u = g.UtilPct
		}
		g.LLMUtilPct = u
		s.GPUs[i] = g
	}
	return s
}
