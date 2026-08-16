package gpu

import (
	"errors"
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// nvmlVendor implements Vendor via NVIDIA's go-nvml bindings.
//
// go-nvml resolves libnvidia-ml symbols lazily, so calling any NVML
// function before Init has succeeded (no driver installed, Init never
// called) aborts the process with "symbol lookup error" instead of
// returning an error. initErr guards every entry point: NewNVML seeds it
// and only a successful Init clears it.
type nvmlVendor struct {
	initErr error          // why NVML is unusable; nil once Init succeeded
	lastTs  map[int]uint64 // per-device GetProcessUtilization watermark
}

func NewNVML() Vendor {
	return &nvmlVendor{initErr: errors.New("nvml not initialized"), lastTs: map[int]uint64{}}
}

func (n *nvmlVendor) Init() error {
	if ret := nvml.Init(); ret != nvml.SUCCESS {
		n.initErr = initErr(ret)
		return n.initErr
	}
	n.initErr = nil
	return nil
}

// initErr turns an nvml.Init failure into a message a user can act on.
// The two common driverless cases get plain English; anything else keeps
// the raw NVML enum name.
func initErr(ret nvml.Return) error {
	switch ret {
	case nvml.ERROR_LIBRARY_NOT_FOUND:
		return errors.New("nvml init: no NVIDIA driver found (libnvidia-ml.so.1 is not installed) — try 'llmtop --demo' for synthetic data")
	case nvml.ERROR_DRIVER_NOT_LOADED:
		return errors.New("nvml init: NVIDIA driver is installed but not loaded")
	}
	return fmt.Errorf("nvml init: %s", nvml.ErrorString(ret))
}

// Shutdown is a no-op unless Init succeeded, so callers may defer it
// unconditionally.
func (n *nvmlVendor) Shutdown() {
	if n.initErr == nil {
		_ = nvml.Shutdown()
	}
}

func (n *nvmlVendor) DriverVersion() (string, error) {
	if n.initErr != nil {
		return "", n.initErr
	}
	v, ret := nvml.SystemGetDriverVersion()
	if ret != nvml.SUCCESS {
		return "", fmt.Errorf("driver version: %s", nvml.ErrorString(ret))
	}
	return v, nil
}

// Sample is not safe for concurrent use: it is a single-caller contract,
// since the per-device lastTs watermark map it reads and updates is
// unsynchronized.
func (n *nvmlVendor) Sample() ([]DeviceSample, []ProcSample, error) {
	if n.initErr != nil {
		return nil, nil, n.initErr
	}
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil, nil, fmt.Errorf("device count: %s", nvml.ErrorString(ret))
	}
	var devs []DeviceSample
	var procs []ProcSample
	for i := 0; i < count; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}
		d := DeviceSample{Index: i}
		if v, ret := dev.GetUUID(); ret == nvml.SUCCESS {
			d.UUID = v
		}
		if v, ret := dev.GetName(); ret == nvml.SUCCESS {
			d.Name = v
		}
		if u, ret := dev.GetUtilizationRates(); ret == nvml.SUCCESS {
			d.UtilPct = int(u.Gpu)
		}
		if m, ret := dev.GetMemoryInfo(); ret == nvml.SUCCESS {
			d.MemUsed, d.MemTotal = m.Used, m.Total
		}
		if t, ret := dev.GetTemperature(nvml.TEMPERATURE_GPU); ret == nvml.SUCCESS {
			d.TempC = int(t)
		}
		if p, ret := dev.GetPowerUsage(); ret == nvml.SUCCESS {
			d.PowerW = int(p) / 1000
		}
		if p, ret := dev.GetEnforcedPowerLimit(); ret == nvml.SUCCESS {
			d.PowerCapW = int(p) / 1000
		}
		devs = append(devs, d)
		procs = append(procs, n.deviceProcs(dev, i)...)
	}
	return devs, procs, nil
}

// deviceProcs merges compute+graphics process lists with per-process
// SM utilization samples (best effort; -1 when unavailable).
func (n *nvmlVendor) deviceProcs(dev nvml.Device, idx int) []ProcSample {
	smByPid := map[int32]int{}
	if samples, ret := dev.GetProcessUtilization(n.lastTs[idx]); ret == nvml.SUCCESS {
		for _, s := range samples {
			smByPid[int32(s.Pid)] = int(s.SmUtil)
			if s.TimeStamp > n.lastTs[idx] {
				n.lastTs[idx] = s.TimeStamp
			}
		}
	} // ERROR_NOT_FOUND (no samples yet) and others: leave smByPid empty
	var out []ProcSample
	add := func(infos []nvml.ProcessInfo, graphics bool) {
		for _, pi := range infos {
			mem := pi.UsedGpuMemory
			if mem == ^uint64(0) { // NVML "value not available" sentinel
				mem = 0
			}
			sm, ok := smByPid[int32(pi.Pid)]
			if !ok {
				sm = -1
			}
			out = append(out, ProcSample{
				PID: int32(pi.Pid), GPUIndex: idx, MemBytes: mem,
				SMUtilPct: sm, Graphics: graphics,
			})
		}
	}
	if infos, ret := dev.GetComputeRunningProcesses(); ret == nvml.SUCCESS {
		add(infos, false)
	}
	if infos, ret := dev.GetGraphicsRunningProcesses(); ret == nvml.SUCCESS {
		add(infos, true)
	}
	return out
}
