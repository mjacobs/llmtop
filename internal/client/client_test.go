package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mjacobs/llmtop/internal/agent"
	"github.com/mjacobs/llmtop/internal/collector"
	"github.com/mjacobs/llmtop/internal/gpu"
	"github.com/mjacobs/llmtop/internal/model"
	"github.com/mjacobs/llmtop/internal/procs"
)

func recv(t *testing.T, ch <-chan Update, timeout time.Duration) Update {
	t.Helper()
	select {
	case u := <-ch:
		return u
	case <-time.After(timeout):
		t.Fatal("timed out waiting for update")
		panic("unreachable")
	}
}

func TestStartLocal(t *testing.T) {
	col := collector.New("loc", gpu.NewFake(1), &procs.Fake{}, nil, nil, "t")
	if err := col.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(col.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Update, 16)
	StartLocal(ctx, "loc", col, 10*time.Millisecond, ch)
	for i := 0; i < 2; i++ {
		u := recv(t, ch, 2*time.Second)
		if u.Host != "loc" || u.Err != nil || u.Snapshot == nil || len(u.Snapshot.GPUs) != 1 {
			t.Fatalf("update %d = %+v", i, u)
		}
	}
}

func TestStartRemoteBackfillLiveAndError(t *testing.T) {
	col := collector.New("rem", gpu.NewFake(1), &procs.Fake{}, nil, nil, "t")
	if err := col.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(col.Close)
	srv := agent.New(col, "tok")
	srv.SampleNow(context.Background())
	srv.SampleNow(context.Background()) // 2 buffered → backfill
	ts := httptest.NewServer(srv.Handler())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Update, 64)
	StartRemote(ctx, "rem", ts.URL, "tok", 10*time.Millisecond, ch)

	// Backfill: first two updates are the buffered snapshots.
	for i := 0; i < 2; i++ {
		if u := recv(t, ch, 2*time.Second); u.Err != nil || u.Snapshot == nil {
			t.Fatalf("backfill %d = %+v", i, u)
		}
	}
	// Live polling keeps producing.
	if u := recv(t, ch, 2*time.Second); u.Err != nil || u.Snapshot.Host.Name != "rem" {
		t.Fatalf("live = %+v", u)
	}
	// Server death → error updates.
	ts.Close()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-ch:
			if u.Err != nil {
				return // got the error update
			}
		case <-deadline:
			t.Fatal("no error update after server close")
		}
	}
}

// TestStartRemoteBackfillWedgeFallsBackToLive covers the fix for a
// persistently failing /api/v1/history: after maxBackfillFailures
// consecutive errors, StartRemote must stop retrying backfill and fall
// back to live-snapshot polling instead of wedging forever.
func TestStartRemoteBackfillWedgeFallsBackToLive(t *testing.T) {
	snap := model.Snapshot{Host: model.HostInfo{Name: "rem"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Update, 64)
	StartRemote(ctx, "rem", ts.URL, "", 10*time.Millisecond, ch)

	for i := 0; i < 3; i++ {
		if u := recv(t, ch, 10*time.Second); u.Err == nil {
			t.Fatalf("backfill failure %d: want error update, got %+v", i, u)
		}
	}
	if u := recv(t, ch, 10*time.Second); u.Err != nil || u.Snapshot == nil || u.Snapshot.Host.Name != "rem" {
		t.Fatalf("after giving up on backfill: want live snapshot update, got %+v", u)
	}
}

func TestStartRemoteBadToken(t *testing.T) {
	col := collector.New("rem", gpu.NewFake(1), &procs.Fake{}, nil, nil, "t")
	_ = col.Init()
	t.Cleanup(col.Close)
	srv := agent.New(col, "right")
	srv.SampleNow(context.Background())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Update, 16)
	StartRemote(ctx, "rem", ts.URL, "wrong", 10*time.Millisecond, ch)
	if u := recv(t, ch, 2*time.Second); u.Err == nil {
		t.Fatalf("want auth error update, got %+v", u)
	}
}
