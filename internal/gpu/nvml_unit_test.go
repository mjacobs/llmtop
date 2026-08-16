package gpu

import (
	"strings"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

func TestInitErrMessages(t *testing.T) {
	cases := []struct {
		name string
		ret  nvml.Return
		want string // substring the message must contain
	}{
		{"library not found", nvml.ERROR_LIBRARY_NOT_FOUND, "no NVIDIA driver found"},
		{"driver not loaded", nvml.ERROR_DRIVER_NOT_LOADED, "installed but not loaded"},
		{"other keeps enum", nvml.ERROR_UNKNOWN, "ERROR_UNKNOWN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := initErr(tc.ret).Error()
			if !strings.HasPrefix(got, "nvml init: ") {
				t.Errorf("got %q, want the \"nvml init: \" prefix", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// Before a successful Init every NVML call is a process-killing symbol
// lookup, so the vendor must answer without touching NVML. This test
// never calls Init and so is safe on a driverless machine.
func TestNVMLGuardsBeforeInit(t *testing.T) {
	v := NewNVML()
	if _, _, err := v.Sample(); err == nil {
		t.Error("Sample before Init: want error, got nil")
	}
	if _, err := v.DriverVersion(); err == nil {
		t.Error("DriverVersion before Init: want error, got nil")
	}
	v.Shutdown() // must be a no-op, not a crash
}

// fakeDevice implements nvml.Device for deviceProcs unit tests by embedding
// the interface and overriding only the methods deviceProcs calls. Calling
// any unoverridden method panics (nil embedded interface) — intentionally,
// so the tests fail loudly if deviceProcs starts calling something new.
type fakeDevice struct {
	nvml.Device
	samples    []nvml.ProcessUtilizationSample
	samplesRet nvml.Return
	compute    []nvml.ProcessInfo
	graphics   []nvml.ProcessInfo
	gotTs      []uint64 // every lastSeenTimestamp passed to GetProcessUtilization
}

func (f *fakeDevice) GetProcessUtilization(ts uint64) ([]nvml.ProcessUtilizationSample, nvml.Return) {
	f.gotTs = append(f.gotTs, ts)
	return f.samples, f.samplesRet
}

func (f *fakeDevice) GetComputeRunningProcesses() ([]nvml.ProcessInfo, nvml.Return) {
	return f.compute, nvml.SUCCESS
}

func (f *fakeDevice) GetGraphicsRunningProcesses() ([]nvml.ProcessInfo, nvml.Return) {
	return f.graphics, nvml.SUCCESS
}

func newTestNVML() *nvmlVendor { return &nvmlVendor{lastTs: map[int]uint64{}} }

func TestDeviceProcsMemSentinel(t *testing.T) {
	dev := &fakeDevice{
		samplesRet: nvml.ERROR_NOT_FOUND,
		compute: []nvml.ProcessInfo{
			{Pid: 100, UsedGpuMemory: ^uint64(0)}, // "value not available"
			{Pid: 200, UsedGpuMemory: 512},
		},
	}
	out := newTestNVML().deviceProcs(dev, 0)
	if len(out) != 2 {
		t.Fatalf("got %d procs, want 2", len(out))
	}
	if out[0].PID != 100 || out[0].MemBytes != 0 {
		t.Errorf("sentinel proc: got PID=%d MemBytes=%d, want PID=100 MemBytes=0", out[0].PID, out[0].MemBytes)
	}
	if out[1].PID != 200 || out[1].MemBytes != 512 {
		t.Errorf("normal proc: got PID=%d MemBytes=%d, want PID=200 MemBytes=512", out[1].PID, out[1].MemBytes)
	}
}

func TestDeviceProcsSMMatching(t *testing.T) {
	dev := &fakeDevice{
		samples: []nvml.ProcessUtilizationSample{
			{Pid: 100, TimeStamp: 5, SmUtil: 42},
		},
		samplesRet: nvml.SUCCESS,
		compute: []nvml.ProcessInfo{
			{Pid: 100, UsedGpuMemory: 1024}, // has an SM sample
			{Pid: 200, UsedGpuMemory: 2048}, // no SM sample
		},
	}
	out := newTestNVML().deviceProcs(dev, 3)
	if len(out) != 2 {
		t.Fatalf("got %d procs, want 2", len(out))
	}
	if out[0].SMUtilPct != 42 {
		t.Errorf("matched proc: got SMUtilPct=%d, want 42", out[0].SMUtilPct)
	}
	if out[1].SMUtilPct != -1 {
		t.Errorf("unmatched proc: got SMUtilPct=%d, want -1", out[1].SMUtilPct)
	}
	for _, p := range out {
		if p.GPUIndex != 3 {
			t.Errorf("PID %d: got GPUIndex=%d, want 3", p.PID, p.GPUIndex)
		}
	}
}

func TestDeviceProcsGraphicsFlag(t *testing.T) {
	dev := &fakeDevice{
		samplesRet: nvml.ERROR_NOT_FOUND,
		compute:    []nvml.ProcessInfo{{Pid: 100, UsedGpuMemory: 1}},
		graphics:   []nvml.ProcessInfo{{Pid: 200, UsedGpuMemory: 2}},
	}
	out := newTestNVML().deviceProcs(dev, 0)
	if len(out) != 2 {
		t.Fatalf("got %d procs, want 2", len(out))
	}
	if out[0].PID != 100 || out[0].Graphics {
		t.Errorf("compute proc: got PID=%d Graphics=%v, want PID=100 Graphics=false", out[0].PID, out[0].Graphics)
	}
	if out[1].PID != 200 || !out[1].Graphics {
		t.Errorf("graphics proc: got PID=%d Graphics=%v, want PID=200 Graphics=true", out[1].PID, out[1].Graphics)
	}
}

func TestDeviceProcsWatermark(t *testing.T) {
	dev := &fakeDevice{
		samples: []nvml.ProcessUtilizationSample{
			{Pid: 1, TimeStamp: 10, SmUtil: 1},
			{Pid: 2, TimeStamp: 30, SmUtil: 2},
			{Pid: 3, TimeStamp: 20, SmUtil: 3},
		},
		samplesRet: nvml.SUCCESS,
	}
	n := newTestNVML()
	const idx = 1

	n.deviceProcs(dev, idx)
	if got := n.lastTs[idx]; got != 30 {
		t.Fatalf("after first call: lastTs[%d]=%d, want 30 (max TimeStamp)", idx, got)
	}
	if len(dev.gotTs) != 1 || dev.gotTs[0] != 0 {
		t.Fatalf("first call passed ts %v, want [0]", dev.gotTs)
	}

	n.deviceProcs(dev, idx)
	if len(dev.gotTs) != 2 || dev.gotTs[1] != 30 {
		t.Fatalf("second call passed ts %v, want second element 30", dev.gotTs)
	}
	if got := n.lastTs[idx]; got != 30 {
		t.Errorf("after second call: lastTs[%d]=%d, want 30 (must not regress)", idx, got)
	}
}

func TestDeviceProcsUtilNotFound(t *testing.T) {
	dev := &fakeDevice{
		samplesRet: nvml.ERROR_NOT_FOUND, // no samples in window: not an error
		compute:    []nvml.ProcessInfo{{Pid: 100, UsedGpuMemory: 1}},
		graphics:   []nvml.ProcessInfo{{Pid: 200, UsedGpuMemory: 2}},
	}
	n := newTestNVML()
	out := n.deviceProcs(dev, 0)
	if len(out) != 2 {
		t.Fatalf("got %d procs, want 2", len(out))
	}
	for _, p := range out {
		if p.SMUtilPct != -1 {
			t.Errorf("PID %d: got SMUtilPct=%d, want -1", p.PID, p.SMUtilPct)
		}
	}
	if got := n.lastTs[0]; got != 0 {
		t.Errorf("lastTs[0]=%d, want 0 (no samples seen)", got)
	}
}
