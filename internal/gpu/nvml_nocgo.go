//go:build !cgo

package gpu

import "errors"

// nvmlUnavailable is the Vendor used in cgo-free builds. The real NVML
// bindings (nvml.go) require cgo to link libnvidia-ml, so a
// CGO_ENABLED=0 build substitutes this stub: Init always fails with a
// message pointing at the cgo requirement, keeping --demo and --connect
// usable without a C toolchain while making GPU sampling unavailable.
type nvmlUnavailable struct{}

func NewNVML() Vendor {
	return nvmlUnavailable{}
}

func (nvmlUnavailable) Init() error {
	return errors.New("nvml: this binary was built with CGO_ENABLED=0, so NVIDIA GPU support (which needs cgo to link libnvidia-ml) is unavailable — rebuild with cgo enabled (a C toolchain installed) to get GPU data, or use 'llmtop --demo' for synthetic data")
}

func (nvmlUnavailable) Shutdown() {}

func (nvmlUnavailable) DriverVersion() (string, error) {
	return "", errors.New("nvml: not available (built with CGO_ENABLED=0)")
}

func (nvmlUnavailable) Sample() ([]DeviceSample, []ProcSample, error) {
	return nil, nil, errors.New("nvml: not available (built with CGO_ENABLED=0)")
}
