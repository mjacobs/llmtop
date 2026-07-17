package llm

import (
	"testing"
	"time"
)

// TestRateTracker_FirstSample: first sample per endpoint must return ok=false
// and only prime state (no computed rate).
func TestRateTracker_FirstSample(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(1000, 0)

	if _, ok := rt.TPS("http://a", t0, 1000, 5000, 100, false); ok {
		t.Fatal("first sample must return ok=false")
	}
}

// TestRateTracker_IdleThenBusyAmortizedCompletion covers:
//   - idle polls with static counters produce rate 0, anchors advance
//   - while busy with frozen predicted counter, PredictedTPS is a decode-
//     derived estimate (default ratio 1.0)
//   - on the completion poll, PredictedTPS is amortized over the whole
//     busy window since the last idle poll's anchor (not the last poll
//     interval), and Estimated is false
func TestRateTracker_IdleThenBusyAmortizedCompletion(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(1000, 0)

	// Prime.
	if _, ok := rt.TPS("http://a", t0, 1000, 5000, 100, false); ok {
		t.Fatal("first sample must return ok=false")
	}

	// Idle poll: counters static, not busy -> rate 0, anchor advances to t1.
	t1 := t0.Add(1 * time.Second)
	res, ok := rt.TPS("http://a", t1, 1000, 5000, 100, false)
	if !ok || res.PromptTPS != 0 || res.PredictedTPS != 0 || res.Estimated {
		t.Fatalf("idle poll = %+v, ok=%v, want all-zero non-estimated", res, ok)
	}

	// Request starts: busy=true, predicted frozen, decodes ticking.
	t2 := t1.Add(1 * time.Second)
	res, ok = rt.TPS("http://a", t2, 1000, 5000, 110, true) // +10 decodes over 1s
	if !ok || !res.Estimated || res.PredictedTPS != 10 {
		t.Fatalf("busy poll 1 = %+v, ok=%v, want Estimated PredictedTPS=10", res, ok)
	}

	t3 := t2.Add(1 * time.Second)
	res, ok = rt.TPS("http://a", t3, 1000, 5000, 120, true) // +10 decodes over 1s
	if !ok || !res.Estimated || res.PredictedTPS != 10 {
		t.Fatalf("busy poll 2 = %+v, ok=%v, want Estimated PredictedTPS=10", res, ok)
	}

	// Completion poll: predicted jumps +300 over the anchor window
	// (t1 -> t4 = 3s), NOT over the last 1s poll interval.
	t4 := t3.Add(1 * time.Second)
	res, ok = rt.TPS("http://a", t4, 1000, 5300, 130, false)
	if !ok {
		t.Fatalf("completion poll: ok=false")
	}
	if res.Estimated {
		t.Fatalf("completion poll should be measured, not estimated: %+v", res)
	}
	if res.PredictedTPS != 100 {
		t.Fatalf("completion PredictedTPS = %v, want 100 (300 tokens / 3s window)", res.PredictedTPS)
	}
}

// TestRateTracker_RatioLearning verifies the tokens-per-decode ratio is
// learned on a predicted-change event and then used for subsequent
// in-flight estimates.
func TestRateTracker_RatioLearning(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(2000, 0)

	rt.TPS("http://a", t0, 0, 0, 0, false) // prime

	t1 := t0.Add(1 * time.Second)
	res, ok := rt.TPS("http://a", t1, 0, 0, 0, false) // idle, anchor -> t1
	if !ok || res.PredictedTPS != 0 || res.Estimated {
		t.Fatalf("idle poll = %+v, ok=%v", res, ok)
	}

	// Completion: predicted +300 over 3s window, decodes advanced 150 over
	// the same window -> ratio = 300/150 = 2.0.
	t2 := t1.Add(3 * time.Second)
	res, ok = rt.TPS("http://a", t2, 0, 300, 150, false)
	if !ok || res.Estimated || res.PredictedTPS != 100 {
		t.Fatalf("completion poll = %+v, ok=%v, want measured PredictedTPS=100", res, ok)
	}

	// Next in-flight frozen poll: decodes +30 over 1s -> estimate = 30*2.0 = 60.
	t3 := t2.Add(1 * time.Second)
	res, ok = rt.TPS("http://a", t3, 0, 300, 180, true)
	if !ok || !res.Estimated || res.PredictedTPS != 60 {
		t.Fatalf("post-ratio estimate = %+v, ok=%v, want Estimated PredictedTPS=60", res, ok)
	}
}

// TestRateTracker_RatioClamps verifies the learned ratio is clamped to
// [0.25, 16].
func TestRateTracker_RatioClamps(t *testing.T) {
	t.Run("clamps high", func(t *testing.T) {
		rt := NewRateTracker()
		t0 := time.Unix(3000, 0)
		rt.TPS("http://a", t0, 0, 0, 0, false) // prime

		t1 := t0.Add(1 * time.Second)
		if _, ok := rt.TPS("http://a", t1, 0, 0, 0, false); !ok { // idle, anchor -> t1
			t.Fatal("idle poll should be ok")
		}

		// ratio would be 1000/10 = 100 -> clamp to 16.
		t2 := t1.Add(1 * time.Second)
		res, ok := rt.TPS("http://a", t2, 0, 1000, 10, false)
		if !ok || res.PredictedTPS != 1000 {
			t.Fatalf("completion poll = %+v, ok=%v, want measured PredictedTPS=1000", res, ok)
		}

		t3 := t2.Add(1 * time.Second)
		res, ok = rt.TPS("http://a", t3, 0, 1000, 15, true) // decodes +5/1s
		if !ok || !res.Estimated || res.PredictedTPS != 80 {
			t.Fatalf("clamped-high estimate = %+v, ok=%v, want Estimated PredictedTPS=80 (5*16)", res, ok)
		}
	})

	t.Run("clamps low", func(t *testing.T) {
		rt := NewRateTracker()
		t0 := time.Unix(4000, 0)
		rt.TPS("http://a", t0, 0, 0, 0, false) // prime

		t1 := t0.Add(1 * time.Second)
		if _, ok := rt.TPS("http://a", t1, 0, 0, 0, false); !ok { // idle, anchor -> t1
			t.Fatal("idle poll should be ok")
		}

		// ratio would be 10/1000 = 0.01 -> clamp to 0.25.
		t2 := t1.Add(1 * time.Second)
		res, ok := rt.TPS("http://a", t2, 0, 10, 1000, false)
		if !ok || res.PredictedTPS != 10 {
			t.Fatalf("completion poll = %+v, ok=%v, want measured PredictedTPS=10", res, ok)
		}

		t3 := t2.Add(1 * time.Second)
		res, ok = rt.TPS("http://a", t3, 0, 10, 1100, true) // decodes +100/1s
		if !ok || !res.Estimated || res.PredictedTPS != 25 {
			t.Fatalf("clamped-low estimate = %+v, ok=%v, want Estimated PredictedTPS=25 (100*0.25)", res, ok)
		}
	})
}

// TestRateTracker_BusyDoesNotAdvanceAnchor verifies that unchanged+busy
// polls do not advance the anchor, while unchanged+idle polls do: the
// amortization window on completion counts only from the last idle poll.
func TestRateTracker_BusyDoesNotAdvanceAnchor(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(5000, 0)

	rt.TPS("http://a", t0, 1000, 5000, 100, false) // prime

	t1 := t0.Add(1 * time.Second)
	if _, ok := rt.TPS("http://a", t1, 1000, 5000, 100, false); !ok { // idle #1
		t.Fatal("idle poll 1 should be ok")
	}
	t2 := t1.Add(1 * time.Second)
	if _, ok := rt.TPS("http://a", t2, 1000, 5000, 100, false); !ok { // idle #2, last idle poll: anchor -> t2
		t.Fatal("idle poll 2 should be ok")
	}

	// Busy+frozen polls: must NOT advance the anchor from t2.
	t3 := t2.Add(1 * time.Second)
	if _, ok := rt.TPS("http://a", t3, 1000, 5000, 110, true); !ok {
		t.Fatal("busy poll 1 should be ok")
	}
	t4 := t3.Add(1 * time.Second)
	if _, ok := rt.TPS("http://a", t4, 1000, 5000, 120, true); !ok {
		t.Fatal("busy poll 2 should be ok")
	}

	// Completion: window must be t2 -> t5 = 3s, not t0 -> t5 (5s) or
	// t4 -> t5 (1s).
	t5 := t4.Add(1 * time.Second)
	res, ok := rt.TPS("http://a", t5, 1000, 5300, 130, false)
	if !ok {
		t.Fatalf("completion poll: ok=false")
	}
	if res.PredictedTPS != 100 {
		t.Fatalf("completion PredictedTPS = %v, want 100 (anchor must stay at last idle poll)", res.PredictedTPS)
	}
}

// TestRateTracker_Regression verifies that a regression on any of the
// three counters (prompt, predicted, decode) returns ok=false and fully
// re-primes state for that endpoint.
func TestRateTracker_Regression(t *testing.T) {
	cases := []struct {
		name                                string
		promptTotal, predictedTotal, decode float64
	}{
		{"prompt regresses", 50, 210, 310},
		{"predicted regresses", 110, 150, 310},
		{"decode regresses", 110, 210, 250},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := NewRateTracker()
			t0 := time.Unix(6000, 0)
			rt.TPS("http://a", t0, 100, 200, 300, false) // prime

			t1 := t0.Add(1 * time.Second)
			if _, ok := rt.TPS("http://a", t1, 100, 200, 300, false); !ok {
				t.Fatal("baseline poll should be ok")
			}

			t2 := t1.Add(1 * time.Second)
			if _, ok := rt.TPS("http://a", t2, tc.promptTotal, tc.predictedTotal, tc.decode, false); ok {
				t.Fatalf("%s: expected ok=false", tc.name)
			}

			// Full re-prime: the very next (monotonic) poll succeeds again,
			// treating the regressed sample as the new baseline.
			t3 := t2.Add(1 * time.Second)
			res, ok := rt.TPS("http://a", t3, tc.promptTotal+10, tc.predictedTotal+10, tc.decode+10, false)
			if !ok {
				t.Fatalf("%s: poll after re-prime should be ok", tc.name)
			}
			if res.PromptTPS != 10 || res.PredictedTPS != 10 {
				t.Fatalf("%s: post-re-prime rates = %+v, want (10, 10)", tc.name, res)
			}
		})
	}
}

// TestRateTracker_PromptIndependentAndNeverEstimated verifies PromptTPS
// amortizes over its own anchor (independent of the predicted counter's
// anchor) and is never substituted by a decode-derived estimate.
func TestRateTracker_PromptIndependentAndNeverEstimated(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(7000, 0)

	rt.TPS("http://a", t0, 0, 0, 0, false) // prime

	// Prompt processing: prompt changes while predicted stays at 0 and
	// decode hasn't ticked yet (prefill phase), busy=true.
	t1 := t0.Add(2 * time.Second)
	res, ok := rt.TPS("http://a", t1, 200, 0, 0, true)
	if !ok {
		t.Fatalf("prefill poll: ok=false")
	}
	if res.PromptTPS != 100 {
		t.Fatalf("PromptTPS = %v, want 100 (200 tokens / 2s)", res.PromptTPS)
	}
	if res.PredictedTPS != 0 || !res.Estimated {
		t.Fatalf("predicted side = (%v, estimated=%v), want (0, true) independent of prompt", res.PredictedTPS, res.Estimated)
	}

	// Prompt now frozen (its own request phase ended) while busy continues
	// and decode ticks for generation: PromptTPS must stay exactly 0, never
	// picking up the decode-derived rate.
	t2 := t1.Add(1 * time.Second)
	res, ok = rt.TPS("http://a", t2, 200, 0, 20, true)
	if !ok {
		t.Fatalf("generation poll: ok=false")
	}
	if res.PromptTPS != 0 {
		t.Fatalf("frozen+busy PromptTPS = %v, want 0 (must never substitute a decode estimate)", res.PromptTPS)
	}
	if !res.Estimated || res.PredictedTPS != 20 {
		t.Fatalf("predicted side = (%v, estimated=%v), want (20, true)", res.PredictedTPS, res.Estimated)
	}
}

// TestRateTracker_DecodeAlsoFrozenEstimatesZero verifies that when the
// predicted counter is frozen, busy is true, AND the decode counter is
// also frozen, the in-flight estimate is exactly 0 (not left over from a
// prior poll), with Estimated still true.
func TestRateTracker_DecodeAlsoFrozenEstimatesZero(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(9100, 0)

	rt.TPS("http://a", t0, 100, 200, 300, false) // prime

	t1 := t0.Add(1 * time.Second)
	if _, ok := rt.TPS("http://a", t1, 100, 200, 300, false); !ok { // idle, anchor -> t1
		t.Fatal("idle poll should be ok")
	}

	t2 := t1.Add(1 * time.Second)
	res, ok := rt.TPS("http://a", t2, 100, 200, 300, true) // busy, predicted AND decode frozen
	if !ok {
		t.Fatalf("busy+frozen poll: ok=false")
	}
	if !res.Estimated {
		t.Fatalf("res.Estimated = false, want true")
	}
	if res.PredictedTPS != 0 {
		t.Fatalf("PredictedTPS = %v, want exactly 0 (decode counter also frozen)", res.PredictedTPS)
	}
}

// TestRateTracker_RatioRetainedWhenDeltaDecodesZero verifies that a
// predicted-change event whose Δdecodes is 0 (over the same anchor window)
// skips the ratio update, retaining the prior learned ratio for later
// estimates rather than resetting to the default or recomputing from a
// zero denominator.
func TestRateTracker_RatioRetainedWhenDeltaDecodesZero(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(9200, 0)

	rt.TPS("http://a", t0, 0, 0, 0, false) // prime

	t1 := t0.Add(1 * time.Second)
	if _, ok := rt.TPS("http://a", t1, 0, 0, 0, false); !ok { // idle, anchor -> t1
		t.Fatal("idle poll should be ok")
	}

	// Completion: predicted +300 over 3s, decodes +150 over the same
	// window -> ratio = 300/150 = 2.0.
	t2 := t1.Add(3 * time.Second)
	res, ok := rt.TPS("http://a", t2, 0, 300, 150, false)
	if !ok || res.Estimated || res.PredictedTPS != 100 {
		t.Fatalf("completion poll = %+v, ok=%v, want measured PredictedTPS=100", res, ok)
	}

	// Predicted changes again, but the decode counter is unchanged since
	// the previous anchor (Δdecodes==0): ratio update must be skipped, so
	// the previously learned ratio (2.0) is retained.
	t3 := t2.Add(1 * time.Second)
	res, ok = rt.TPS("http://a", t3, 0, 350, 150, true)
	if !ok || res.Estimated || res.PredictedTPS != 50 {
		t.Fatalf("delta-decodes-zero poll = %+v, ok=%v, want measured PredictedTPS=50 (350-300 over 1s)", res, ok)
	}

	// Prove the ratio is still 2.0 (not reset to default 1.0, and not
	// recomputed): a subsequent frozen+busy poll with decodes +10/1s
	// should estimate 10*2.0=20, not 10*1.0=10.
	t4 := t3.Add(1 * time.Second)
	res, ok = rt.TPS("http://a", t4, 0, 350, 160, true)
	if !ok || !res.Estimated || res.PredictedTPS != 20 {
		t.Fatalf("post-retention estimate = %+v, ok=%v, want Estimated PredictedTPS=20 (10 decodes * retained ratio 2.0)", res, ok)
	}
}

// TestRateTracker_NonPositiveElapsed verifies dt<=0 returns ok=false and
// does not mutate tracker state.
func TestRateTracker_NonPositiveElapsed(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(8000, 0)

	rt.TPS("http://a", t0, 100, 200, 300, false) // prime

	// Same instant: dt=0.
	if _, ok := rt.TPS("http://a", t0, 100, 200, 300, false); ok {
		t.Fatal("dt=0 must return ok=false")
	}
	// Time going backwards: dt<0.
	if _, ok := rt.TPS("http://a", t0.Add(-1*time.Second), 100, 200, 300, false); ok {
		t.Fatal("dt<0 must return ok=false")
	}

	// State must be unaffected by the bad-dt calls: this poll is still
	// relative to t0, and is a valid idle poll.
	t1 := t0.Add(1 * time.Second)
	res, ok := rt.TPS("http://a", t1, 100, 200, 300, false)
	if !ok || res.PromptTPS != 0 || res.PredictedTPS != 0 {
		t.Fatalf("poll after bad-dt calls = %+v, ok=%v, want zero rates and ok=true", res, ok)
	}
}
