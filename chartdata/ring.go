package chartdata

// ring holds the last len(buf) values pushed, oldest first by index.
type ring[T any] struct {
	buf  []T
	head int // the index in buf of the oldest value
	n    int
}

func newRing[T any](capacity int) *ring[T] {
	return &ring[T]{buf: make([]T, capacity)}
}

// push appends v, dropping the oldest value when the ring is full.
func (r *ring[T]) push(v T) {
	if r.n < len(r.buf) {
		r.buf[(r.head+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.head] = v
	r.head = (r.head + 1) % len(r.buf)
}

// at returns the i-th oldest value.
func (r *ring[T]) at(i int) T {
	return r.buf[(r.head+i)%len(r.buf)]
}
