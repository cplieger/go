package httpwire

import "testing"

// TestQuietStateForgetsReturnedRequests pins that the in-flight set holds
// only requests still running, so it never grows with traffic.
func TestQuietStateForgetsReturnedRequests(t *testing.T) {
	var s quietState
	for _, refused := range []bool{false, true, false} {
		in, _, ok := s.enter(t.Context(), refused, false)
		if !ok {
			t.Fatalf("enter refused a request with no maintenance")
		}
		s.leave(in)
	}
	if len(s.running) != 0 {
		t.Errorf("%d returned requests still in flight", len(s.running))
	}
}
