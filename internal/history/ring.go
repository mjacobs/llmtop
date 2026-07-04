// Package history provides a fixed-capacity ring buffer for time series.
package history

type Ring[T any] struct {
	buf   []T
	start int // index of oldest element
	n     int // number of valid elements
}

func NewRing[T any](capacity int) *Ring[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring[T]{buf: make([]T, capacity)}
}

func (r *Ring[T]) Push(v T) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

func (r *Ring[T]) Len() int { return r.n }

func (r *Ring[T]) Last() (T, bool) {
	var zero T
	if r.n == 0 {
		return zero, false
	}
	return r.buf[(r.start+r.n-1)%len(r.buf)], true
}

// Slice returns elements oldest→newest as a fresh copy.
func (r *Ring[T]) Slice() []T {
	out := make([]T, 0, r.n)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(r.start+i)%len(r.buf)])
	}
	return out
}
