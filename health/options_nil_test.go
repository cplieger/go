package health

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewStatus_OptionsApplyBeforeTheMarkerCheck pins that every option is in
// place before NewStatus checks the marker path, so the degraded-mode warning
// reaches a logger given after WithMarker, and that a nil option is skipped
// here and in inspect alike. A directory at the marker path takes the
// degraded path for any user, root included.
func TestNewStatus_OptionsApplyBeforeTheMarkerCheck(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "app.healthy")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	l, buf := jsonLog()
	_ = NewStatus(WithMarker(p), nil, WithLogger(l))
	if n := len(withMsg(t, buf, msgMarkerDegraded)); n != 1 {
		t.Errorf("WithLogger got %q, want the degraded-mode warning", buf.String())
	}
	if f := inspect(filepath.Join(t.TempDir(), "missing"), nil); f.healthy() {
		t.Errorf("inspect(missing marker, nil option) = %+v, want unhealthy", f)
	}
}

// TestWithMarkerLastWins: of two WithMarker options the last decides the path.
func TestWithMarkerLastWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, last := filepath.Join(dir, "first.healthy"), filepath.Join(dir, "last.healthy")
	st := NewStatus(WithLogger(discard), WithMarker(first), WithMarker(last))
	st.Set(true)
	if exists(t, first) || !exists(t, last) {
		t.Errorf("first present = %v, last present = %v; want only the last", exists(t, first), exists(t, last))
	}
}
