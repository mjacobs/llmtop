package gpu

import "testing"

func TestFakeVendorDeterministicAndBounded(t *testing.T) {
	f := NewFake(2)
	if err := f.Init(); err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown()
	if v, err := f.DriverVersion(); err != nil || v == "" {
		t.Fatalf("DriverVersion = %q, %v", v, err)
	}
	devsA, _, err := f.Sample()
	if err != nil || len(devsA) != 2 {
		t.Fatalf("Sample: %v, %d devices", err, len(devsA))
	}
	for _, d := range devsA {
		if d.UtilPct < 0 || d.UtilPct > 100 {
			t.Errorf("gpu%d UtilPct %d out of range", d.Index, d.UtilPct)
		}
		if d.MemUsed > d.MemTotal || d.MemTotal == 0 {
			t.Errorf("gpu%d memory invalid: %d/%d", d.Index, d.MemUsed, d.MemTotal)
		}
		if d.Name == "" || d.UUID == "" {
			t.Errorf("gpu%d missing identity", d.Index)
		}
	}
	// Two Fakes advance identically (deterministic step counter).
	g := NewFake(2)
	_ = g.Init()
	devsB, _, _ := g.Sample()
	if devsA[0].UtilPct != devsB[0].UtilPct {
		t.Error("fakes must be deterministic across instances")
	}
	// Values wander over successive samples.
	changed := false
	for i := 0; i < 10 && !changed; i++ {
		next, _, _ := f.Sample()
		changed = next[0].UtilPct != devsA[0].UtilPct
	}
	if !changed {
		t.Error("util should wander across samples")
	}
}

func TestFakeInjectedProcs(t *testing.T) {
	f := NewFake(1)
	_ = f.Init()
	f.SetProcs([]ProcSample{{PID: 42, GPUIndex: 0, MemBytes: 1 << 30, SMUtilPct: 55}})
	_, procs, _ := f.Sample()
	if len(procs) != 1 || procs[0].PID != 42 {
		t.Fatalf("procs = %+v", procs)
	}
}
