package llm

import (
	"sync"
	"time"
)

// RateTracker turns monotonically increasing token counters into
// instantaneous tokens/sec, keyed by endpoint. Safe for concurrent use.
type RateTracker struct {
	mu   sync.Mutex
	prev map[string]rateSample
}

type rateSample struct {
	t                 time.Time
	prompt, predicted float64
}

func NewRateTracker() *RateTracker {
	return &RateTracker{prev: make(map[string]rateSample)}
}

// TPS returns per-second rates since the previous call for this endpoint.
// ok=false on the first sample, on counter regression (server restart),
// and on non-positive elapsed time; in all cases state is re-primed.
func (r *RateTracker) TPS(endpoint string, now time.Time, promptTotal, predictedTotal float64) (promptTPS, predictedTPS float64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, seen := r.prev[endpoint]
	r.prev[endpoint] = rateSample{t: now, prompt: promptTotal, predicted: predictedTotal}
	if !seen {
		return 0, 0, false
	}
	dt := now.Sub(prev.t).Seconds()
	if dt <= 0 || promptTotal < prev.prompt || predictedTotal < prev.predicted {
		return 0, 0, false
	}
	return (promptTotal - prev.prompt) / dt, (predictedTotal - prev.predicted) / dt, true
}
