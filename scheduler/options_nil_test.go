package scheduler

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestOptions_NilIsSkipped pins one rule across the module: a nil option is
// skipped, and WithIntervalLogger(nil) keeps the default logger, so the
// warning for an unparseable value still has somewhere to go.
func TestOptions_NilIsSkipped(t *testing.T) {
	t.Parallel()
	_ = NewExclusive(t.TempDir(), nil)
	got := ParseInterval("not-a-duration", time.Hour, nil, WithIntervalLogger(nil))
	if got.Interval != time.Hour {
		t.Errorf("ParseInterval(garbage, 1h, nil, WithIntervalLogger(nil)).Interval = %s, want 1h", got.Interval)
	}
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	_ = ParseInterval("not-a-duration", time.Hour, WithIntervalLogger(l), WithIntervalLogger(nil))
	if !strings.Contains(buf.String(), "default=1h0m0s") {
		t.Errorf("logger after a later WithIntervalLogger(nil) got %q, want the fallback warning", buf.String())
	}
}
