package gpu

import (
	"os"
	"testing"
)

// Real-hardware smoke test; run manually on a GPU host:
//
//	LLMTOP_NVML_TEST=1 go test ./internal/gpu/ -run NVML -v
func TestNVMLSmoke(t *testing.T) {
	if os.Getenv("LLMTOP_NVML_TEST") != "1" {
		t.Skip("set LLMTOP_NVML_TEST=1 to run against real NVML")
	}
	v := NewNVML()
	if err := v.Init(); err != nil {
		t.Fatal(err)
	}
	defer v.Shutdown()
	if ver, err := v.DriverVersion(); err != nil || ver == "" {
		t.Fatalf("driver: %q %v", ver, err)
	}
	devs, procs, err := v.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) == 0 {
		t.Fatal("no devices")
	}
	for _, d := range devs {
		t.Logf("gpu%d %s util=%d%% mem=%d/%d temp=%dC power=%d/%dW",
			d.Index, d.Name, d.UtilPct, d.MemUsed, d.MemTotal, d.TempC, d.PowerW, d.PowerCapW)
		if d.MemTotal == 0 || d.Name == "" {
			t.Errorf("gpu%d: implausible sample %+v", d.Index, d)
		}
	}
	t.Logf("%d gpu procs", len(procs))
}
