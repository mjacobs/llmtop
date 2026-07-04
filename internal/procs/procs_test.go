package procs

import (
	"os"
	"testing"
)

func TestProcFSSeesInitAndSelf(t *testing.T) {
	table, err := NewProcFS().Procs()
	if err != nil {
		t.Fatal(err)
	}
	if info, ok := table[1]; !ok || info.Comm == "" {
		t.Fatalf("expected PID 1 with comm, got %+v ok=%v", table[1], ok)
	}
	self := int32(os.Getpid())
	info, ok := table[self]
	if !ok {
		t.Fatalf("expected self pid %d in table", self)
	}
	if len(info.Cmdline) == 0 || info.PPID == 0 {
		t.Fatalf("self entry incomplete: %+v", info)
	}
}

func TestFakeScanner(t *testing.T) {
	f := &Fake{Table: map[int32]ProcInfo{7: {PID: 7, Comm: "x"}}}
	got, err := f.Procs()
	if err != nil || len(got) != 1 || got[7].Comm != "x" {
		t.Fatalf("Fake.Procs() = %v, %v", got, err)
	}
}
