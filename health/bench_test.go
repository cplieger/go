package health

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkProbeCheck_Healthy benchmarks the steady-state hot path when
// the marker file exists.
func BenchmarkProbeCheck_Healthy(b *testing.B) {
	path := filepath.Join(b.TempDir(), ".healthy")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		ProbeCheck(path)
	}
}

// BenchmarkProbeCheck_Unhealthy benchmarks the probe when the marker is
// absent from a writable directory.
func BenchmarkProbeCheck_Unhealthy(b *testing.B) {
	path := filepath.Join(b.TempDir(), ".healthy")
	b.ResetTimer()
	for b.Loop() {
		ProbeCheck(path)
	}
}

// BenchmarkMarkerCheckHealthy benchmarks the os.Stat call HTTP handlers
// invoke on every request (Signal.Healthy delegates to it).
func BenchmarkMarkerCheckHealthy(b *testing.B) {
	path := filepath.Join(b.TempDir(), ".healthy")
	m := NewMarker(path)
	m.Set(true)
	b.ResetTimer()
	for b.Loop() {
		m.CheckHealthy()
	}
}

// BenchmarkHandlerHealthy benchmarks the full HTTP handler render path
// when the signal reports healthy.
func BenchmarkHandlerHealthy(b *testing.B) {
	h := Handler(stubSignal{healthy: true})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkHandlerUnhealthy benchmarks the handler render for the 503 path.
func BenchmarkHandlerUnhealthy(b *testing.B) {
	h := Handler(stubSignal{healthy: false})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkMarkerSet benchmarks Set(true) which touches the marker file —
// the write path exercised on every liveness state change.
func BenchmarkMarkerSet(b *testing.B) {
	path := filepath.Join(b.TempDir(), ".healthy")
	m := NewMarker(path)
	b.ResetTimer()
	for b.Loop() {
		m.Set(true)
	}
}
