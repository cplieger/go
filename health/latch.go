package health

import "sync"

// Latch makes shutdown health monotonic toward unhealthy. BeginDrain marks
// unhealthy immediately, and later healthy writes are dropped; unhealthy
// writes still land. Its lock covers both the drain check and the marker write,
// giving shutdown precedence over a racing completion.
//
// A Latch is safe for concurrent use. Construct it with NewLatch and keep the
// Marker for Cleanup. The caller decides healthy, unhealthy, or no write.
type Latch struct {
	marker   *Marker
	mu       sync.Mutex
	draining bool
}

// NewLatch returns a latch writing through m.
func NewLatch(m *Marker) *Latch {
	return &Latch{marker: m}
}

// Set records the current liveness state through the underlying Marker,
// except that a healthy value is dropped once BeginDrain has run.
func (l *Latch) Set(healthy bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.draining && healthy {
		return
	}
	l.marker.Set(healthy)
}

// BeginDrain latches shutdown and marks unhealthy immediately, so observers
// see the draining state before in-flight work finishes. After it, Set can
// never restore healthy.
func (l *Latch) BeginDrain() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.draining = true
	l.marker.Set(false)
}
