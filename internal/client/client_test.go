package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mjacobs/llmtop/internal/agent"
	"github.com/mjacobs/llmtop/internal/collector"
	"github.com/mjacobs/llmtop/internal/gpu"
	"github.com/mjacobs/llmtop/internal/model"
	"github.com/mjacobs/llmtop/internal/procs"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// handlerClient never opens a socket: network I/O is not durably blocked in
// synctest, whereas serving the handler directly lets the fake clock advance.
func handlerClient(h http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Result(), nil
	})}
}

func newTestCollector(t *testing.T) *collector.Collector {
	t.Helper()
	col := collector.New("alpha", gpu.NewFake(1), &procs.Fake{}, nil, nil, "test")
	if err := col.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(col.Close)
	return col
}

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
	synctest.Test(t, func(t *testing.T) {
		col := newTestCollector(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ch := make(chan Update, 1)
		start := time.Now()
		StartLocal(ctx, "alpha", col, 5*time.Second, ch)
		for i := 0; i < 3; i++ {
			u := recv(t, ch, 6*time.Second)
			if u.Host != "alpha" || u.Err != nil || u.Snapshot == nil || len(u.Snapshot.GPUs) != 1 {
				t.Fatalf("update %d = %+v", i, u)
			}
			want := start.Add(time.Duration(i) * 5 * time.Second)
			if !u.Snapshot.Timestamp.Equal(want) || !time.Now().Equal(want) {
				t.Fatalf("sample %d: timestamp %v, received %v, want %v", i, u.Snapshot.Timestamp, time.Now(), want)
			}
		}
		synctest.Wait() // parked on the next tick
		cancel()
		synctest.Wait()
		time.Sleep(time.Hour)
		if len(ch) != 0 {
			t.Fatal("sample after cancellation")
		}
	})
}

func TestStartLocalCancelBlockedSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		StartLocal(ctx, "alpha", newTestCollector(t), time.Second, make(chan Update))
		synctest.Wait() // no receiver: the immediate sample is blocked in send
		cancel()
		// synctest.Test also waits for the source goroutine to exit.
	})
}

func TestStartRemoteBackoffSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		attempts := make(chan time.Duration, 16)
		hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts <- time.Since(start)
			return nil, errors.New("connection refused")
		})}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ch := make(chan Update, 16)
		startRemote(ctx, "alpha", "192.0.2.10:9832", "", time.Second, ch, hc)
		for i, seconds := range []int{0, 1, 3, 7, 15, 31, 61, 91} {
			select {
			case got := <-attempts:
				if want := time.Duration(seconds) * time.Second; got != want {
					t.Fatalf("attempt %d at %v, want %v", i, got, want)
				}
			case <-time.After(31 * time.Second):
				t.Fatalf("missing attempt %d", i)
			}
			if u := recv(t, ch, time.Second); u.Host != "alpha" || u.Err == nil || u.Snapshot != nil {
				t.Fatalf("attempt %d: want error update, got %+v", i, u)
			}
		}
		synctest.Wait() // cancellation interrupts the capped backoff
		cancel()
	})
}

func TestStartRemoteBackfillLiveAndError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := agent.New(newTestCollector(t), "tok")
		srv.SampleNow(t.Context())
		time.Sleep(time.Millisecond)
		srv.SampleNow(t.Context())
		h := srv.Handler()
		var offline atomic.Bool
		var queries []string
		hc := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/history" {
				queries = append(queries, r.URL.Query().Get("since"))
			}
			if offline.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			h.ServeHTTP(w, r)
		}))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ch := make(chan Update, 4)
		start := time.Now()
		startRemote(ctx, "alpha", "http://example.com/", "tok", time.Second, ch, hc)

		var latest *model.Snapshot
		for i := 0; i < 2; i++ {
			u := recv(t, ch, time.Second)
			if u.Host != "alpha" || u.Err != nil || u.Snapshot == nil || !time.Now().Equal(start) {
				t.Fatalf("backfill %d = %+v at %v", i, u, time.Since(start))
			}
			latest = u.Snapshot
		}
		if u := recv(t, ch, 2*time.Second); u.Err != nil || u.Snapshot == nil || !u.Snapshot.Timestamp.Equal(latest.Timestamp) || time.Since(start) != time.Second {
			t.Fatalf("live = %+v at %v", u, time.Since(start))
		}
		synctest.Wait()
		offline.Store(true)
		if u := recv(t, ch, 2*time.Second); u.Err == nil || time.Since(start) != 2*time.Second {
			t.Fatalf("offline = %+v at %v", u, time.Since(start))
		}
		synctest.Wait()
		offline.Store(false)
		srv.SampleNow(t.Context())
		if u := recv(t, ch, 2*time.Second); u.Err != nil || u.Snapshot == nil || !u.Snapshot.Timestamp.After(latest.Timestamp) || time.Since(start) != 3*time.Second {
			t.Fatalf("reconnected backfill = %+v at %v", u, time.Since(start))
		}
		synctest.Wait()
		if len(queries) != 2 || queries[0] != "0" || queries[1] != strconv.FormatInt(latest.Timestamp.UnixMilli(), 10) {
			t.Fatalf("history cursors = %v, want [0 %d]", queries, latest.Timestamp.UnixMilli())
		}
	})
}

// A persistently failing history endpoint must yield to live polling after
// exactly three attempts, at 0, 1 and 3 seconds; live starts at 7 seconds.
func TestStartRemoteBackfillWedgeFallsBackToLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := agent.New(newTestCollector(t), "")
		srv.SampleNow(t.Context())
		h := srv.Handler()
		start := time.Now()
		var paths []string
		var times []time.Duration
		hc := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			times = append(times, time.Since(start))
			if r.URL.Path == "/api/v1/history" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			h.ServeHTTP(w, r)
		}))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ch := make(chan Update, 4)
		startRemote(ctx, "alpha", "example.com", "", time.Second, ch, hc)
		for i := 0; i < 3; i++ {
			if u := recv(t, ch, 5*time.Second); u.Err == nil || u.Snapshot != nil {
				t.Fatalf("backfill failure %d: want error, got %+v", i, u)
			}
		}
		for i := 0; i < 2; i++ {
			if u := recv(t, ch, 5*time.Second); u.Err != nil || u.Snapshot == nil || u.Snapshot.Host.Name != "alpha" {
				t.Fatalf("after failed backfills: want live snapshot, got %+v", u)
			}
		}
		synctest.Wait()
		// The ticker has a pending tick after backoff, so the first two live
		// polls both happen at 7 s. Further polls resume the regular cadence.
		wantTimes := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 7 * time.Second}
		if len(paths) != len(wantTimes) {
			t.Fatalf("requests = %v at %v", paths, times)
		}
		for i, want := range wantTimes {
			path := "/api/v1/history"
			if i >= 3 {
				path = "/api/v1/snapshot"
			}
			if paths[i] != path || times[i] != want {
				t.Fatalf("request %d = %s at %v, want %s at %v", i, paths[i], times[i], path, want)
			}
		}
	})
}

func TestStartRemoteCancelWhileWaiting(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "poll"
		if fail {
			name = "backoff"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				hc := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if fail {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					_, _ = w.Write([]byte(`[{}]`))
				}))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ch := make(chan Update, 1)
				startRemote(ctx, "alpha", "example.com", "", time.Second, ch, hc)
				_ = recv(t, ch, time.Second)
				synctest.Wait()
				cancel()
				synctest.Wait()
				time.Sleep(time.Hour)
				if calls != 1 || len(ch) != 0 {
					t.Fatalf("activity after cancel: %d requests, %d updates", calls, len(ch))
				}
			})
		})
	}
}

func TestStartRemoteCancelBlockedSend(t *testing.T) {
	for _, phase := range []string{"backfill", "live", "error"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				hc := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					switch {
					case phase == "error":
						w.WriteHeader(http.StatusInternalServerError)
					case phase == "live" && r.URL.Path == "/api/v1/history":
						_, _ = w.Write([]byte(`[]`))
					case r.URL.Path == "/api/v1/history":
						_, _ = w.Write([]byte(`[{}]`))
					default:
						_, _ = w.Write([]byte(`{}`))
					}
				}))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				startRemote(ctx, "alpha", "example.com", "", time.Second, make(chan Update), hc)
				synctest.Wait()
				wantCalls := 1
				if phase == "live" {
					time.Sleep(time.Second)
					synctest.Wait()
					wantCalls = 2
				}
				if calls != wantCalls {
					t.Fatalf("%d requests, want %d before blocked send", calls, wantCalls)
				}
				cancel() // the bubble must exit despite having no channel receiver
			})
		})
	}
}

// Strings from remote backfill and live responses must be sanitized before
// reaching the TUI. The transport deliberately bypasses collector sanitizing.
func TestStartRemoteSanitizesDecodedSnapshots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dirty := model.Snapshot{
			Host:      model.HostInfo{Name: "alpha", Hostname: "alpha\x1b[2J"},
			Timestamp: time.Unix(1751600000, 0).UTC(),
			Servers:   []model.LLMServer{{Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8080\r\n", Model: "\x1b[31mm", Status: model.StatusOK}},
			Errors:    []string{"gpu sample: boom\x1b[31m\nlost"},
		}
		hc := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/history" {
				_ = json.NewEncoder(w).Encode([]model.Snapshot{dirty})
			} else {
				live := dirty
				live.Timestamp = dirty.Timestamp.Add(time.Second)
				_ = json.NewEncoder(w).Encode(live)
			}
		}))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ch := make(chan Update, 4)
		startRemote(ctx, "alpha", "example.com", "", time.Second, ch, hc)
		for _, path := range []string{"backfill", "live"} {
			u := recv(t, ch, 2*time.Second)
			if u.Err != nil || u.Snapshot == nil {
				t.Fatalf("%s: want snapshot update, got %+v", path, u)
			}
			s := u.Snapshot
			if s.Host.Hostname != "alpha[2J" || s.Servers[0].Endpoint != "http://127.0.0.1:8080" ||
				s.Servers[0].Model != "[31mm" || s.Errors[0] != "gpu sample: boom[31mlost" {
				t.Fatalf("%s: control bytes survived decode: %+v", path, s)
			}
		}
	})
}

func TestStartRemoteBadToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := agent.New(newTestCollector(t), "right")
		srv.SampleNow(t.Context())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ch := make(chan Update, 1)
		startRemote(ctx, "alpha", "example.com", "wrong", time.Second, ch, handlerClient(srv.Handler()))
		if u := recv(t, ch, time.Second); u.Err == nil {
			t.Fatalf("want auth error update, got %+v", u)
		}
	})
}
