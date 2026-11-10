package health

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewMarker_OptionsApplyBeforeTheMarkerProbe pins that WithLogger is in
// place before NewMarker probes the marker path, so the degraded-mode warning
// reaches the caller's logger, and that a nil option is skipped here and in
// Inspect alike. A directory at the marker path takes the degraded path for
// any user, root included.
func TestNewMarker_OptionsApplyBeforeTheMarkerProbe(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	_ = NewMarker(path, nil, WithLogger(l))
	if !strings.Contains(buf.String(), "health marker not writable") {
		t.Errorf("WithLogger got %q, want the degraded-mode warning", buf.String())
	}
	if f := Inspect(filepath.Join(t.TempDir(), "missing"), nil); f.Healthy() {
		t.Errorf("Inspect(missing marker, nil option) = %+v, want unhealthy", f)
	}
}

// TestNewMarker_degradedWarningNamesBothVerdicts pins that the warning states
// what degraded mode reports through each path, since the container's verdict
// depends on which one its healthcheck uses: the health subcommand reports
// healthy, and an HTTP Handler reports unhealthy.
func TestNewMarker_degradedWarningNamesBothVerdicts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_ = NewMarker(path, WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	logged := buf.String()
	want := `effect="the health subcommand reports healthy; Handler, Healthy and Within report unhealthy"`
	if !strings.Contains(logged, want) {
		t.Errorf("degraded-mode warning = %q, want it to carry %s", logged, want)
	}
	if strings.Contains(logged, "container will report healthy") {
		t.Errorf("degraded-mode warning = %q, still promises a healthy container", logged)
	}
}
