// Package collector orchestrates GPU sampling, process classification,
// and LLM server discovery into model.Snapshot values. One Collector
// serves both the TUI's local host and `llmtop agent`.
package collector

import (
	"context"
	"fmt"
	"os"
	"sort"
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

// Init records the hostname and brings up the GPU vendor. The hostname is
// taken first so a host whose GPU library fails to initialise still reports
// it (the driver version stays empty).
func (c *Collector) Init() error {
	c.hostname, _ = os.Hostname()
	if err := c.vendor.Init(); err != nil {
		return err
	}
	c.driver, _ = c.vendor.DriverVersion()
	return nil
}

func (c *Collector) Close() { c.vendor.Shutdown() }

func (c *Collector) Collect(ctx context.Context) model.Snapshot {
	s := model.Snapshot{
		Host: model.HostInfo{Name: c.name, Hostname: c.hostname,
			DriverVersion: c.driver, AgentVersion: c.agentVersion},
		Timestamp: time.Now().UTC(),
	}

	// A failed GPU sample (library never initialised, transient NVML error)
	// degrades the tick to "no GPUs" but must not hide the process table or
	// the LLM servers: the rest of the snapshot is still useful.
	devs, gpuProcs, err := c.vendor.Sample()
	if err != nil {
		s.Errors = append(s.Errors, fmt.Sprintf("gpu sample: %v", err))
		devs, gpuProcs = nil, nil
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
	serverGPUs := map[int32]map[int]bool{} // server PID → GPU indices its tree holds
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
		if owner, ok := procs.Owner(p.PID, table, llmPIDs); ok {
			if serverGPUs[owner] == nil {
				serverGPUs[owner] = map[int]bool{}
			}
			serverGPUs[owner][p.GPUIndex] = true
		}
	}
	// A server is shown under the GPUs its process tree holds — the process
	// itself, or a child such as vLLM's engine process (model.LLMServer.GPUs).
	for i := range s.Servers {
		srv := &s.Servers[i]
		if held := serverGPUs[srv.PID]; srv.PID != 0 && len(held) > 0 {
			srv.GPUs = make([]int, 0, len(held))
			for gi := range held {
				srv.GPUs = append(srv.GPUs, gi)
			}
			sort.Ints(srv.GPUs)
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
	// Model aliases, comm names, GPU names and endpoints all originate
	// outside llmtop; strip control bytes before anything downstream keys
	// on or renders them.
	s.Sanitize()
	return s
}
