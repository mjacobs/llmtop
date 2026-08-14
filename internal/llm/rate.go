package llm

import (
	"sync"
	"time"
)

// ratioMin and ratioMax clamp the learned tokens-per-decode ratio used for
// the in-flight generation-rate estimate.
const (
	ratioMin     = 0.25
	ratioMax     = 16
	ratioDefault = 1.0
)

// RateTracker turns monotonically increasing token/decode counters into
// truthful tokens/sec, keyed by endpoint. Safe for concurrent use.
//
// Per endpoint, per counter (prompt, predicted), it keeps an anchor: the
// last instant at which we knew there were no unattributed tokens
// accumulating. A rate is only emitted (amortized) once the counter
// actually changes; while a request is in flight and the predicted
// counter is frozen, PredictedTPS is instead a live estimate derived from
// the decode counter (which ticks continuously) and a learned
// tokens-per-decode ratio.
type RateTracker struct {
	mu    sync.Mutex
	state map[string]*endpointState
}

type endpointState struct {
	lastPollT time.Time

	promptAnchorT time.Time
	promptAnchorV float64
	promptLastV   float64

	predAnchorT       time.Time
	predAnchorV       float64
	predAnchorDecodeV float64
	predLastV         float64

	decodeLastV float64
	ratio       float64
}

// TPSResult is the per-poll rate result for one endpoint.
type TPSResult struct {
	PromptTPS    float64
	PredictedTPS float64 // measured (amortized) or estimated
	Estimated    bool    // PredictedTPS is a decode-derived estimate
}

func NewRateTracker() *RateTracker {
	return &RateTracker{state: make(map[string]*endpointState)}
}

// Reset forgets an endpoint's anchors and learned ratio. Call it when the
// server behind the endpoint is known to have been replaced or reconfigured:
// TPS self-heals on counter regression, but a replacement whose counters are
// equal or higher would otherwise be amortized against the old anchors,
// emitting rates that span two server lifetimes.
func (r *RateTracker) Reset(endpoint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.state, endpoint)
}

// TPS returns truthful per-second token rates for this endpoint, given the
// latest cumulative counters and whether a request is currently in flight
// (requestsProcessing > 0).
//
// ok=false on the first sample for this endpoint, on regression of any of
// the three counters (server restart/model swap — state is fully
// re-primed), and on non-positive elapsed time (state is left untouched).
func (r *RateTracker) TPS(endpoint string, now time.Time, promptTotal, predictedTotal, decodeTotal float64, busy bool) (res TPSResult, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	st, seen := r.state[endpoint]
	if !seen || promptTotal < st.promptLastV || predictedTotal < st.predLastV || decodeTotal < st.decodeLastV {
		r.state[endpoint] = &endpointState{
			lastPollT:         now,
			promptAnchorT:     now,
			promptAnchorV:     promptTotal,
			promptLastV:       promptTotal,
			predAnchorT:       now,
			predAnchorV:       predictedTotal,
			predAnchorDecodeV: decodeTotal,
			predLastV:         predictedTotal,
			decodeLastV:       decodeTotal,
			ratio:             ratioDefault,
		}
		return TPSResult{}, false
	}

	dt := now.Sub(st.lastPollT).Seconds()
	if dt <= 0 {
		return TPSResult{}, false
	}

	// Prompt: amortized rate only, never an estimate.
	if promptTotal != st.promptLastV {
		res.PromptTPS = (promptTotal - st.promptAnchorV) / now.Sub(st.promptAnchorT).Seconds()
		st.promptAnchorT = now
		st.promptAnchorV = promptTotal
	} else if !busy {
		st.promptAnchorT = now
		st.promptAnchorV = promptTotal
	}
	// else: unchanged and busy — anchor stays, rate 0.
	st.promptLastV = promptTotal

	// Predicted: amortized rate, ratio learning, and decode-derived estimate.
	if predictedTotal != st.predLastV {
		window := now.Sub(st.predAnchorT).Seconds()
		deltaPredicted := predictedTotal - st.predAnchorV
		res.PredictedTPS = deltaPredicted / window

		if deltaDecodes := decodeTotal - st.predAnchorDecodeV; deltaDecodes != 0 {
			ratio := deltaPredicted / deltaDecodes
			if ratio < ratioMin {
				ratio = ratioMin
			} else if ratio > ratioMax {
				ratio = ratioMax
			}
			st.ratio = ratio
		}

		st.predAnchorT = now
		st.predAnchorV = predictedTotal
		st.predAnchorDecodeV = decodeTotal
	} else if !busy {
		st.predAnchorT = now
		st.predAnchorV = predictedTotal
		st.predAnchorDecodeV = decodeTotal
	} else {
		// Unchanged and busy: anchor stays, substitute the live estimate.
		decodesPerSec := (decodeTotal - st.decodeLastV) / dt
		res.PredictedTPS = decodesPerSec * st.ratio
		res.Estimated = true
	}
	st.predLastV = predictedTotal

	st.decodeLastV = decodeTotal
	st.lastPollT = now

	return res, true
}
