package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mjacobs/llmtop/internal/collector"
	"github.com/mjacobs/llmtop/internal/gpu"
	"github.com/mjacobs/llmtop/internal/model"
	"github.com/mjacobs/llmtop/internal/procs"
)

func newTestServer(t *testing.T, token string) (*Server, *httptest.Server) {
	t.Helper()
	col := collector.New("agenthost", gpu.NewFake(1), &procs.Fake{}, nil, nil, "test")
	if err := col.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(col.Close)
	s := New(col, token)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func get(t *testing.T, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestSnapshotAndHistory(t *testing.T) {
	s, ts := newTestServer(t, "")

	resp, _ := get(t, ts.URL+"/api/v1/snapshot", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("before sample: %d, want 503", resp.StatusCode)
	}

	s.SampleNow(context.Background())
	time.Sleep(2 * time.Millisecond)
	s.SampleNow(context.Background())

	resp, body := get(t, ts.URL+"/api/v1/snapshot", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot: %d", resp.StatusCode)
	}
	var snap model.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil || snap.Host.Name != "agenthost" || len(snap.GPUs) != 1 {
		t.Fatalf("snapshot decode: %v, %+v", err, snap)
	}

	_, body = get(t, ts.URL+"/api/v1/history", "")
	var hist []model.Snapshot
	if err := json.Unmarshal(body, &hist); err != nil || len(hist) != 2 {
		t.Fatalf("history: %v, n=%d", err, len(hist))
	}

	since := hist[0].Timestamp.UnixMilli()
	_, body = get(t, fmt.Sprintf("%s/api/v1/history?since=%d", ts.URL, since), "")
	if err := json.Unmarshal(body, &hist); err != nil || len(hist) != 1 {
		t.Fatalf("history since: %v, n=%d", err, len(hist))
	}
}

func TestHistoryCapsToNewest(t *testing.T) {
	s, ts := newTestServer(t, "")

	for i := 0; i < 305; i++ {
		s.SampleNow(context.Background())
	}
	latest, ok := s.ring.Last()
	if !ok {
		t.Fatal("expected at least one sample")
	}

	_, body := get(t, ts.URL+"/api/v1/history", "")
	var hist []model.Snapshot
	if err := json.Unmarshal(body, &hist); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(hist) != historyResponseCap {
		t.Fatalf("len(hist) = %d, want %d", len(hist), historyResponseCap)
	}
	if last := hist[len(hist)-1]; !last.Timestamp.Equal(latest.Timestamp) {
		t.Fatalf("last returned = %v, want latest sample = %v", last.Timestamp, latest.Timestamp)
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("every server timeout must be set: header=%v read=%v write=%v idle=%v",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}

func TestAuth(t *testing.T) {
	s, ts := newTestServer(t, "s3cret")
	s.SampleNow(context.Background())

	if resp, _ := get(t, ts.URL+"/api/v1/snapshot", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", resp.StatusCode)
	}
	if resp, _ := get(t, ts.URL+"/api/v1/snapshot", "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d, want 401", resp.StatusCode)
	}
	if resp, _ := get(t, ts.URL+"/api/v1/snapshot", "s3cret"); resp.StatusCode != http.StatusOK {
		t.Fatalf("good token: %d, want 200", resp.StatusCode)
	}
	if resp, _ := get(t, ts.URL+"/healthz", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz must not require auth: %d", resp.StatusCode)
	}
}
