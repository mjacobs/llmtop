package model

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var updateWire = flag.Bool("update", false, "regenerate testdata/wire/snapshot.json")

func TestSnapshotJSONGolden(t *testing.T) {
	got, err := json.MarshalIndent(sample(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "wire", "snapshot.json")
	if *updateWire {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("snapshot JSON differs from %s; review the wire change, then run go test ./internal/model -update\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

func TestSnapshotJSONFrozenCompatibility(t *testing.T) {
	dir := filepath.Join("testdata", "wire")
	paths, err := filepath.Glob(filepath.Join(dir, "v*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		// Before the first release, exercise discovery and decoding with a
		// temporary version fixture. Never create a fictitious release in-tree.
		b, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
		if err != nil {
			t.Fatal(err)
		}
		dir = t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "v0.0.0.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	testFrozenSnapshots(t, dir)
}

func testFrozenSnapshots(t *testing.T, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "v*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no frozen snapshots checked")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got Snapshot
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.GPUs) != 1 || len(got.Procs) != 1 || len(got.Servers) != 1 {
				t.Fatalf("want one GPU, process and server; got %d, %d, %d", len(got.GPUs), len(got.Procs), len(got.Servers))
			}
			if got.Servers[0].Metrics == nil {
				t.Fatal("server metrics missing")
			}
			// Keep these expectations independent of sample() and JSON tags:
			// encoding and decoding with the same renamed tag must not pass.
			// Check stable key values, allowing future additive fields.
			checks := []struct {
				name      string
				got, want any
			}{
				{"host name", got.Host.Name, "alpha"},
				{"hostname", got.Host.Hostname, "alpha"},
				{"driver", got.Host.DriverVersion, "580.65"},
				{"agent version", got.Host.AgentVersion, "0.1.0"},
				{"timestamp", got.Timestamp, time.Unix(1751600000, 0).UTC()},
				{"GPU UUID", got.GPUs[0].UUID, "GPU-abc"},
				{"GPU utilization", got.GPUs[0].UtilPct, 72},
				{"GPU memory", got.GPUs[0].MemUsed, uint64(24 << 30)},
				{"process PID", got.Procs[0].PID, int32(2310469)},
				{"process name", got.Procs[0].Name, "llama-server"},
				{"process class", got.Procs[0].Class, ProcClass("llm")},
				{"process memory", got.Procs[0].MemBytes, uint64(20 << 30)},
				{"server kind", got.Servers[0].Kind, "llama.cpp"},
				{"server endpoint", got.Servers[0].Endpoint, "http://127.0.0.1:33767"},
				{"server status", got.Servers[0].Status, "ok"},
				{"server model", got.Servers[0].Model, "qwen36-35b-mtp"},
				{"server context", got.Servers[0].CtxSize, 262144},
				{"prompt tokens", got.Servers[0].Metrics.PromptTokensTotal, float64(16121)},
				{"predicted tokens", got.Servers[0].Metrics.PredictedTokensTotal, float64(247195)},
				{"prompt rate", got.Servers[0].Metrics.PromptTPS, 2754.78},
				{"predicted rate", got.Servers[0].Metrics.PredictedTPS, 42.3},
				{"KV cache", got.Servers[0].Metrics.KVCacheUsage, float64(-1)},
				{"active requests", got.Servers[0].Metrics.RequestsProcessing, 1},
				{"errors", got.Errors, []string{"gpu1: temp read failed"}},
			}
			for _, check := range checks {
				if !reflect.DeepEqual(check.got, check.want) {
					t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
				}
			}
		})
	}
}
