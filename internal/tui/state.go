package tui

import (
	"github.com/mjacobs/llmtop/internal/client"
	"github.com/mjacobs/llmtop/internal/history"
	"github.com/mjacobs/llmtop/internal/model"
)

// ringCapacity is the sample depth kept per series — enough history for a
// sparkline/braille-graph at any view width without unbounded growth.
const ringCapacity = 300

// HostState accumulates the latest Snapshot (or error) for one host plus
// rolling history for its charts. Rings are keyed by GPU index (util/mem,
// total and LLM-only split) or by server endpoint (generation throughput).
// Snapshots pass through view.filterSnapshot on the way in, so Snap already
// reflects the config's display settings (GPU selection/order, hidden
// graphics processes, redacted addresses).
type HostState struct {
	Name    string // routing key for incoming updates
	Display string // rendered title; differs from Name only when addresses are hidden
	Snap    *model.Snapshot
	ErrMsg  string
	view    hostView

	UtilHist    map[int]*history.Ring[float64]
	MemHist     map[int]*history.Ring[float64]
	LLMUtilHist map[int]*history.Ring[float64]
	LLMMemHist  map[int]*history.Ring[float64]
	GenTPSHist  map[string]*history.Ring[float64]
}

func NewHostState(name string) *HostState {
	return &HostState{
		Name:        name,
		Display:     name,
		view:        hostView{showGraphics: true, showAddrs: true},
		UtilHist:    map[int]*history.Ring[float64]{},
		MemHist:     map[int]*history.Ring[float64]{},
		LLMUtilHist: map[int]*history.Ring[float64]{},
		LLMMemHist:  map[int]*history.Ring[float64]{},
		GenTPSHist:  map[string]*history.Ring[float64]{},
	}
}

// Apply folds one client.Update into the host's state.
//
//   - u.Err != nil: record ErrMsg but keep the last Snap around so the view
//     can render a "stale" panel instead of blanking it.
//   - u.Snapshot != nil: clear ErrMsg, adopt the new Snap, and push one
//     sample per series. Remote sources can redeliver the same Snapshot at
//     poll cadence (no new sample yet upstream) — dedupe on Timestamp so
//     that repeat doesn't double-push ring points.
func (h *HostState) Apply(u client.Update) {
	if u.Err != nil {
		h.ErrMsg = h.view.scrubErr(u.Err.Error())
	}
	if u.Snapshot == nil {
		return
	}

	if h.Snap != nil && u.Snapshot.Timestamp.Equal(h.Snap.Timestamp) {
		// Duplicate sample at poll cadence: still clears a transient error
		// (the source is reachable again), but skip Snap replacement and
		// ring pushes — both would be no-ops on identical data anyway.
		h.ErrMsg = ""
		return
	}

	h.ErrMsg = ""
	h.Snap = h.view.filterSnapshot(u.Snapshot)

	for _, gpu := range h.Snap.GPUs {
		ringPush(h.UtilHist, gpu.Index, float64(gpu.UtilPct))
		ringPush(h.MemHist, gpu.Index, pct(gpu.MemUsed, gpu.MemTotal))
		ringPush(h.LLMUtilHist, gpu.Index, float64(gpu.LLMUtilPct))
		ringPush(h.LLMMemHist, gpu.Index, pct(gpu.LLMMemBytes, gpu.MemTotal))
	}
	for _, srv := range h.Snap.Servers {
		if srv.Metrics == nil {
			continue
		}
		ringPushStr(h.GenTPSHist, srv.Endpoint, srv.Metrics.PredictedTPS)
	}
}

// pct returns used/total*100, or 0 when total is 0 (avoids a NaN/Inf sample
// from a malformed or not-yet-populated GPU record).
func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func ringPush(m map[int]*history.Ring[float64], idx int, v float64) {
	r, ok := m[idx]
	if !ok {
		r = history.NewRing[float64](ringCapacity)
		m[idx] = r
	}
	r.Push(v)
}

func ringPushStr(m map[string]*history.Ring[float64], key string, v float64) {
	r, ok := m[key]
	if !ok {
		r = history.NewRing[float64](ringCapacity)
		m[key] = r
	}
	r.Push(v)
}
