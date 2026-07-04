package client

import (
	"context"
	"testing"
	"time"
)

func TestStartDemoTwoHosts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Update, 64)
	StartDemo(ctx, ch, 10*time.Millisecond)

	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case u := <-ch:
			if u.Err != nil || u.Snapshot == nil {
				t.Fatalf("demo update = %+v", u)
			}
			if len(u.Snapshot.GPUs) == 0 || len(u.Snapshot.Servers) == 0 {
				t.Fatalf("demo snapshot too empty: %+v", u.Snapshot)
			}
			seen[u.Host] = true
		case <-deadline:
			t.Fatalf("only saw hosts %v", seen)
		}
	}
	// At least one demo server must exercise the metrics-off path.
	// (Checked across a few updates.)
	foundNilMetrics := false
	for i := 0; i < 20 && !foundNilMetrics; i++ {
		u := <-ch
		for _, srv := range u.Snapshot.Servers {
			if srv.Metrics == nil {
				foundNilMetrics = true
			}
		}
	}
	if !foundNilMetrics {
		t.Error("demo must include a metrics-off server")
	}
}
