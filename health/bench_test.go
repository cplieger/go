package health

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkProbe_Healthy benchmarks the steady-state hot path when
// the marker file exists.
func BenchmarkProbe_Healthy(b *testing.B) {
	path := filepath.Join(b.TempDir(), "app.healthy")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		probeCode(path)
	}
}

// BenchmarkProbe_Unhealthy benchmarks the probe when the marker is
// absent from a writable directory.
func BenchmarkProbe_Unhealthy(b *testing.B) {
	path := filepath.Join(b.TempDir(), "app.healthy")
	b.ResetTimer()
	for b.Loop() {
		probeCode(path)
	}
}

// BenchmarkStatusHealthy benchmarks the read every health request makes per
// check.
func BenchmarkStatusHealthy(b *testing.B) {
	st := NewStatus(WithLogger(discard))
	st.Set(true)
	for b.Loop() {
		st.Healthy()
	}
}

// BenchmarkHandlerHealthy benchmarks the full HTTP handler render path
// when the signal reports healthy.
func BenchmarkHandlerHealthy(b *testing.B) {
	h := CheckHandler(Check{Signal: stubSignal{healthy: true}, Reason: "the store is not writable"})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkHandlerUnhealthy benchmarks the handler render for the 503 path.
func BenchmarkHandlerUnhealthy(b *testing.B) {
	h := CheckHandler(Check{Signal: stubSignal{healthy: false}, Reason: "the store is not writable"})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkStatusSetWithMarker benchmarks Set(true) with a marker, which
// refreshes the file on every call.
func BenchmarkStatusSetWithMarker(b *testing.B) {
	st := NewStatus(WithLogger(discard), WithMarker(filepath.Join(b.TempDir(), "app.healthy")))
	for b.Loop() {
		st.Set(true)
	}
}
