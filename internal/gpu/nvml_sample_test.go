//go:build cgo

package gpu

import (
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock/dgxa100"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock/gpus"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock/server"
)

func TestNVMLSample(t *testing.T) {
	for _, degraded := range []bool{false, true} {
		name := "all devices"
		if degraded {
			name = "handle and sensor failures"
		}
		t.Run(name, func(t *testing.T) {
			lib := dgxa100.NewWithGPUs(gpus.Multiple(3, gpus.A100_SXM4_40GB)...)
			for i, device := range lib.Devices {
				dev := device.(*server.Device)
				dev.MemoryInfo.Used = uint64(i+1) << 30
				dev.GetUtilizationRatesFunc = func() (nvml.Utilization, nvml.Return) {
					return nvml.Utilization{Gpu: uint32(40 + i)}, nvml.SUCCESS
				}
				dev.GetTemperatureFunc = func(nvml.TemperatureSensors) (uint32, nvml.Return) {
					if degraded && i == 2 {
						return 99, nvml.ERROR_NOT_SUPPORTED
					}
					return uint32(50 + i), nvml.SUCCESS
				}
				dev.GetPowerUsageFunc = func() (uint32, nvml.Return) { return 127999, nvml.SUCCESS }
				dev.GetEnforcedPowerLimitFunc = func() (uint32, nvml.Return) {
					if degraded && i == 2 {
						return 999999, nvml.ERROR_NOT_SUPPORTED
					}
					return 300500, nvml.SUCCESS
				}
				dev.GetProcessUtilizationFunc = func(uint64) ([]nvml.ProcessUtilizationSample, nvml.Return) {
					return nil, nvml.ERROR_NOT_FOUND
				}
				dev.GetComputeRunningProcessesFunc = func() ([]nvml.ProcessInfo, nvml.Return) { return nil, nvml.SUCCESS }
				dev.GetGraphicsRunningProcessesFunc = func() ([]nvml.ProcessInfo, nvml.Return) { return nil, nvml.SUCCESS }
			}

			indices := []int{0, 1, 2}
			if degraded {
				getHandle := lib.DeviceGetHandleByIndexFunc
				lib.DeviceGetHandleByIndexFunc = func(index int) (nvml.Device, nvml.Return) {
					if index == 1 {
						return nil, nvml.ERROR_NO_PERMISSION
					}
					return getHandle(index)
				}
				indices = []int{0, 2}
			}

			v := newNVML(lib)
			if err := v.Init(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(v.Shutdown)
			if driver, err := v.DriverVersion(); driver != lib.DriverVersion || err != nil {
				t.Fatalf("driver = %q, %v; want %q", driver, err, lib.DriverVersion)
			}
			devices, _, err := v.Sample()
			if err != nil || len(devices) != len(indices) {
				t.Fatalf("Sample returned %d devices, %v; want %d devices", len(devices), err, len(indices))
			}
			for pos, index := range indices {
				mockDevice := lib.Devices[index].(*server.Device)
				want := DeviceSample{
					Index: index, UUID: mockDevice.UUID, Name: mockDevice.Config.Name,
					UtilPct: 40 + index, MemUsed: uint64(index+1) << 30, MemTotal: 40 << 30,
					TempC: 50 + index, PowerW: 127, PowerCapW: 300,
				}
				if degraded && index == 2 {
					want.TempC, want.PowerCapW = 0, 0
				}
				if got := devices[pos]; got != want {
					t.Errorf("device %d = %+v; want %+v", pos, got, want)
				}
			}
		})
	}
}
