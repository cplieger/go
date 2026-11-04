package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// FuzzHandlerSignal exercises the JSON render path with arbitrary
// signal states and request methods/paths, ensuring the status-code
// contract holds (200 healthy, 503 otherwise) with no panics.
func FuzzHandlerSignal(f *testing.F) {
	f.Add(true, "GET", "/health")
	f.Add(false, "GET", "/health")
	f.Add(true, "POST", "/healthz")
	f.Add(false, "HEAD", "/")

	f.Fuzz(func(t *testing.T, healthy bool, method, target string) {
		if method == "" {
			method = "GET"
		}
		if target == "" {
			target = "/"
		}
		h := Handler(stubSignal{healthy: healthy})
		req, err := http.NewRequest(method, target, nil)
		if err != nil {
			t.Skip() // invalid method/target can't construct a request; not a handler concern
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if healthy && rec.Code != http.StatusOK {
			t.Errorf("healthy signal: got %d, want 200", rec.Code)
		}
		if !healthy && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("unhealthy signal: got %d, want 503", rec.Code)
		}
	})
}
