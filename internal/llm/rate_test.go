package llm

import (
	"testing"
	"time"
)

func TestRateTracker(t *testing.T) {
	rt := NewRateTracker()
	t0 := time.Unix(1000, 0)

	if _, _, ok := rt.TPS("http://a", t0, 1000, 5000); ok {
		t.Fatal("first sample must return ok=false")
	}
	p, g, ok := rt.TPS("http://a", t0.Add(2*time.Second), 1200, 5100)
	if !ok || p != 100 || g != 50 {
		t.Fatalf("TPS = (%v, %v, %v), want (100, 50, true)", p, g, ok)
	}
	// Independent per endpoint.
	if _, _, ok := rt.TPS("http://b", t0, 1, 1); ok {
		t.Fatal("new endpoint first sample must return ok=false")
	}
	// Counter reset (server restart) → ok=false, state re-primed.
	if _, _, ok := rt.TPS("http://a", t0.Add(4*time.Second), 10, 10); ok {
		t.Fatal("counter regression must return ok=false")
	}
	p, g, ok = rt.TPS("http://a", t0.Add(6*time.Second), 30, 50)
	if !ok || p != 10 || g != 20 {
		t.Fatalf("after reset TPS = (%v, %v, %v), want (10, 20, true)", p, g, ok)
	}
	// Zero/negative elapsed → ok=false.
	if _, _, ok := rt.TPS("http://a", t0.Add(6*time.Second), 40, 60); ok {
		t.Fatal("zero elapsed must return ok=false")
	}
}
