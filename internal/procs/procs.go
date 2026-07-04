// Package procs reads the local process table and classifies GPU processes.
package procs

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ProcInfo struct {
	PID     int32
	PPID    int32
	Comm    string
	Cmdline []string
}

type Scanner interface {
	Procs() (map[int32]ProcInfo, error)
}

type Fake struct{ Table map[int32]ProcInfo }

func (f *Fake) Procs() (map[int32]ProcInfo, error) { return f.Table, nil }

type procFS struct{}

func NewProcFS() Scanner { return procFS{} }

func (procFS) Procs() (map[int32]ProcInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make(map[int32]ProcInfo, len(entries))
	for _, e := range entries {
		pid64, err := strconv.ParseInt(e.Name(), 10, 32)
		if err != nil {
			continue // not a pid dir
		}
		pid := int32(pid64)
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // process exited or unreadable; skip
		}
		comm, ppid, ok := parseStat(stat)
		if !ok {
			continue
		}
		var cmdline []string
		if raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline")); err == nil {
			for _, f := range bytes.Split(raw, []byte{0}) {
				if len(f) > 0 {
					cmdline = append(cmdline, string(f))
				}
			}
		}
		out[pid] = ProcInfo{PID: pid, PPID: ppid, Comm: comm, Cmdline: cmdline}
	}
	return out, nil
}

// parseStat extracts comm (field 2, parenthesized, may contain spaces)
// and ppid (field 4) from /proc/<pid>/stat.
func parseStat(stat []byte) (comm string, ppid int32, ok bool) {
	s := string(stat)
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close < open || close+2 >= len(s) {
		return "", 0, false
	}
	comm = s[open+1 : close]
	rest := strings.Fields(s[close+2:]) // rest[0]=state, rest[1]=ppid
	if len(rest) < 2 {
		return "", 0, false
	}
	p, err := strconv.ParseInt(rest[1], 10, 32)
	if err != nil {
		return "", 0, false
	}
	return comm, int32(p), true
}
