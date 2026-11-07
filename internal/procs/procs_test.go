package procs

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

var statCases = []struct {
	name string
	stat string
	comm string
	ppid int32
	ok   bool
}{
	{"ordinary", "7 (alpha) S 1 2 3", "alpha", 1, true},
	{"spaces in comm", "7 (alpha bravo) R 42 2 3", "alpha bravo", 42, true},
	{"parentheses in comm", "7 (alpha (bravo)) S 123 2 3", "alpha (bravo)", 123, true},
	{"closing parenthesis in comm", "7 (alpha) bravo) S 0", "alpha) bravo", 0, true},
	{"empty comm", "7 () S 1", "", 1, true},
	{"int32 boundary", "7 (alpha) S 2147483647", "alpha", 2147483647, true},
	{"empty", "", "", 0, false},
	{"missing open", "7 alpha) S 1", "", 0, false},
	{"missing close", "7 (alpha S 1", "", 0, false},
	{"reversed parentheses", "7 )alpha( S 1", "", 0, false},
	{"missing state and ppid", "7 (alpha)", "", 0, false},
	{"missing ppid", "7 (alpha) S", "", 0, false},
	{"invalid ppid", "7 (alpha) S bravo", "", 0, false},
	{"overflow ppid", "7 (alpha) S 2147483648", "", 0, false},
}

func TestParseStat(t *testing.T) {
	for _, tc := range statCases {
		t.Run(tc.name, func(t *testing.T) {
			comm, ppid, ok := parseStat([]byte(tc.stat))
			if comm != tc.comm || ppid != tc.ppid || ok != tc.ok {
				t.Fatalf("parseStat(%q) = (%q, %d, %v), want (%q, %d, %v)",
					tc.stat, comm, ppid, ok, tc.comm, tc.ppid, tc.ok)
			}
		})
	}
}

func FuzzParseStat(f *testing.F) {
	for _, tc := range statCases {
		f.Add([]byte(tc.stat))
	}
	f.Fuzz(func(t *testing.T, stat []byte) {
		_, ppid, ok := parseStat(stat)
		if !ok {
			return
		}
		// Field 2 can contain spaces and parentheses. Fields 3 (state)
		// and 4 (ppid) follow the last closing parenthesis.
		line := string(stat)
		close := strings.LastIndexByte(line, ')')
		if close < 0 {
			t.Fatalf("parseStat(%q) succeeded without a closing parenthesis", stat)
		}
		fields := strings.Fields(line[close+1:])
		if len(fields) < 2 {
			t.Fatalf("parseStat(%q) succeeded without field 4", stat)
		}
		want, err := strconv.ParseInt(fields[1], 10, 32)
		if err != nil || int64(ppid) != want {
			t.Fatalf("parseStat(%q) returned ppid %d; field 4 is %q (error: %v)", stat, ppid, fields[1], err)
		}
	})
}

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
