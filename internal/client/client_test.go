package client

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mjacobs/llmtop/internal/agent"
	"github.com/mjacobs/llmtop/internal/collector"
	"github.com/mjacobs/llmtop/internal/gpu"
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
