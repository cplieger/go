//go:build unix && !aix && !solaris

package health

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// within fails the test when fn does not return in five seconds: a
// write-only open of a FIFO with no reader blocks unless it is non-blocking.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked on a FIFO at the marker path", what)
	}
}

// TestAFIFOAtTheMarkerPathNeverBlocks: a FIFO with no reader at the marker
// path makes the marker degraded instead of hanging NewMarker, a stale one is
// a healthy probe instead of a hung one, and a FIFO that appears after
// construction fails Set instead of hanging it.
func TestAFIFOAtTheMarkerPathNeverBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	var m *Marker
	within(t, "NewMarker", func() { m = NewMarker(path) })
	if !m.degraded.Load() {
		t.Error("a FIFO at the marker path: want degraded mode")
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	var f Freshness
	within(t, "Inspect", func() { f = Inspect(path, WithMaxAge(time.Hour)) })
	if f.State != MarkerDirUnavailable {
		t.Errorf("stale FIFO: Inspect = %+v, want MarkerDirUnavailable", f)
	}

	late := filepath.Join(t.TempDir(), ".healthy")
	lm := NewMarker(late)
	if lm.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	if err := syscall.Mkfifo(late, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	within(t, "Set(true)", func() { err = lm.SetChecked(true) })
	if err == nil {
		t.Error("Set(true) on a FIFO: want an error, not a refreshed marker")
	}
}

// TestAFIFOWithAReaderIsNotAMarker: with a reader holding the FIFO open, the
// write-only open succeeds, and only the regular-file check stops Set from
// counting an open that refreshes nothing as a heartbeat.
func TestAFIFOWithAReaderIsNotAMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := markerWritable(path); err == nil {
		t.Error("a FIFO with a reader: want not writable as a marker")
	}
	if err := writeMarker(path); err == nil {
		t.Error("writeMarker on a FIFO with a reader: want an error")
	}
}

func TestTransient(t *testing.T) {
	for err, want := range map[error]bool{
		&os.PathError{Op: "open", Err: syscall.EAGAIN}: true,
		&os.PathError{Op: "open", Err: syscall.EINTR}:  true,
		&os.PathError{Op: "open", Err: syscall.EMFILE}: true,
		&os.PathError{Op: "open", Err: syscall.EACCES}: false,
		&os.PathError{Op: "open", Err: syscall.ENXIO}:  false,
		&os.PathError{Op: "open", Err: errNotRegular}:  false,
		nil: false,
	} {
		t.Run(fmt.Sprint(err), func(t *testing.T) {
			if got := transient(err); got != want {
				t.Errorf("transient(%v) = %v, want %v", err, got, want)
			}
		})
	}
}
