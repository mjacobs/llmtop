package gpu

import (
	"fmt"
	"math"
	"sync"
)

// Fake is a deterministic Vendor: utilization follows per-device sine
// waves over a step counter, so charts wander plausibly in --demo mode
// and tests reproduce exactly. No wall clock, no math/rand.
type Fake struct {
	mu    sync.Mutex
	n     int
	step  int
	procs []ProcSample
}

func NewFake(devices int) *Fake { return &Fake{n: devices} }

func (f *Fake) Init() error                    { return nil }
func (f *Fake) Shutdown()                      {}
func (f *Fake) DriverVersion() (string, error) { return "580.00-fake", nil }

// SetProcs copies p, so callers may reuse or mutate their slice afterwards.
func (f *Fake) SetProcs(p []ProcSample) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]ProcSample, len(p))
	copy(cp, p)
	f.procs = cp
}

func (f *Fake) Sample() ([]DeviceSample, []ProcSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.step++
	devs := make([]DeviceSample, f.n)
	for i := range devs {
		phase := float64(f.step)/7.0 + float64(i)*2.1
		util := 50 + 45*math.Sin(phase) // 5..95
		mem := 0.45 + 0.35*math.Sin(phase/3.0+1.0)
		total := uint64(32) << 30
		devs[i] = DeviceSample{
			Index:     i,
			UUID:      fmt.Sprintf("GPU-fake-%d", i),
			Name:      fmt.Sprintf("Fake GPU %d", i),
			UtilPct:   int(util),
			MemUsed:   uint64(mem * float64(total)),
			MemTotal:  total,
			TempC:     40 + int(util/4),
			PowerW:    100 + int(util*4),
			PowerCapW: 575,
		}
	}
	procs := make([]ProcSample, len(f.procs))
	copy(procs, f.procs)
	return devs, procs, nil
}
