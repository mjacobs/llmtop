// Package agent exposes a collector over HTTP for remote llmtop TUIs.
package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mjacobs/llmtop/internal/collector"
	"github.com/mjacobs/llmtop/internal/history"
	"github.com/mjacobs/llmtop/internal/model"
)

const ringCapacity = 600 // ~10 min at 1s cadence

// historyResponseCap bounds a single /api/v1/history response to the
// newest N snapshots after the since-filter: matches the TUI ring
// capacity, and bounds response size by construction regardless of how
// wide a since= window a client asks for.
const historyResponseCap = 300

type Server struct {
	col   *collector.Collector
	token string

	mu   sync.RWMutex
	ring *history.Ring[model.Snapshot]
}

func New(col *collector.Collector, token string) *Server {
	return &Server{col: col, token: token, ring: history.NewRing[model.Snapshot](ringCapacity)}
}

func (s *Server) SampleNow(ctx context.Context) {
	snap := s.col.Collect(ctx)
	s.mu.Lock()
	s.ring.Push(snap)
	s.mu.Unlock()
}

func (s *Server) RunSampling(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	s.SampleNow(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SampleNow(ctx)
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.Handle("GET /api/v1/snapshot", s.auth(http.HandlerFunc(s.handleSnapshot)))
	mux.Handle("GET /api/v1/history", s.auth(http.HandlerFunc(s.handleHistory)))
	return mux
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	snap, ok := s.ring.Last()
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no sample yet"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	s.mu.RLock()
	all := s.ring.Slice()
	s.mu.RUnlock()
	out := make([]model.Snapshot, 0, len(all))
	for _, snap := range all {
		if snap.Timestamp.UnixMilli() > since {
			out = append(out, snap)
		}
	}
	if len(out) > historyResponseCap {
		out = out[len(out)-historyResponseCap:]
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Run serves the agent until ctx is cancelled.
func Run(ctx context.Context, col *collector.Collector, listen, token string, interval time.Duration) error {
	s := New(col, token)
	go s.RunSampling(ctx, interval)
	srv := &http.Server{Addr: listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shctx)
		return nil
	case err := <-errc:
		return err
	}
}
