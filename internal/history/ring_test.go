package history

import (
	"reflect"
	"testing"
)

func TestRingPushWrapAndSlice(t *testing.T) {
	r := NewRing[int](3)
	if r.Len() != 0 {
		t.Fatal("new ring should be empty")
	}
	if _, ok := r.Last(); ok {
		t.Fatal("Last on empty ring must return ok=false")
	}
	r.Push(1)
	r.Push(2)
	if got := r.Slice(); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("Slice = %v, want [1 2]", got)
	}
	r.Push(3)
	r.Push(4) // evicts 1
	if got := r.Slice(); !reflect.DeepEqual(got, []int{2, 3, 4}) {
		t.Fatalf("Slice after wrap = %v, want [2 3 4]", got)
	}
	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3", r.Len())
	}
	if last, ok := r.Last(); !ok || last != 4 {
		t.Fatalf("Last = %v %v, want 4 true", last, ok)
	}
}

func TestRingCapacityOne(t *testing.T) {
	r := NewRing[string](1)
	r.Push("a")
	r.Push("b")
	if got := r.Slice(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("Slice = %v, want [b]", got)
	}
}
