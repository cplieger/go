package measure

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegistry_LoggerGetsAFailedScrapeWrite pins that the registry's
// WithLogger, not slog.Default, receives the handler's failed write, and that
// a nil option is skipped.
func TestRegistry_LoggerGetsAFailedScrapeWrite(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := NewRegistry("t", nil, WithLogger(l))
	r.Handler()(&failWriter{}, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if !strings.Contains(buf.String(), "writing prometheus exposition failed") {
		t.Errorf("registry logger got %q, want the failed-write line", buf.String())
	}
}
