// Package gpu abstracts GPU telemetry vendors. v1 ships NVML (NVIDIA) and
// a deterministic fake for tests and --demo mode.
package gpu

type DeviceSample struct {
	Index     int
	UUID      string
	Name      string
	UtilPct   int
	MemUsed   uint64
	MemTotal  uint64
	TempC     int
	PowerW    int
	PowerCapW int
}

type ProcSample struct {
	PID       int32
	GPUIndex  int
	MemBytes  uint64
	SMUtilPct int // -1 = unknown
	Graphics  bool
}

type Vendor interface {
	Init() error
	Shutdown()
	DriverVersion() (string, error)
	Sample() ([]DeviceSample, []ProcSample, error)
}
