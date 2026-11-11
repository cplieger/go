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
// path makes NewStatus degraded instead of hanging it, a stale one is a
// healthy probe instead of a hung one, and a FIFO that appears after
// construction fails the write instead of hanging it.
func TestAFIFOAtTheMarkerPathNeverBlocks(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "app.healthy")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	var st *Status
	within(t, "NewStatus", func() { st = NewStatus(WithLogger(discard), WithMarker(path)) })
	if !st.mirror.degraded {
		t.Error("a FIFO at the marker path: want degraded mode")
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	var f freshness
	within(t, "inspect", func() { f = inspect(path, WithMaxAge(time.Hour)) })
	if f.state != markerDegraded {
		t.Errorf("stale FIFO: inspect = %+v, want markerDegraded", f)
	}

	late := filepath.Join(t.TempDir(), "app.healthy")
	l, buf := jsonLog()
	lst := NewStatus(WithLogger(l), WithMarker(late))
	if err := syscall.Mkfifo(late, 0o600); err != nil {
		t.Fatal(err)
	}
	within(t, "Set(true)", func() { lst.Set(true) })
	if n := len(withMsg(t, buf, msgCreateFailed)); n != 1 {
		t.Errorf("Set(true) on a FIFO: %q lines = %d, want 1", msgCreateFailed, n)
	}
}

// TestAFIFOWithAReaderIsNotAMarker: with a reader holding the FIFO open, the
// write-only open succeeds, and only the regular-file check stops Set from
// counting an open that refreshes nothing as a healthy write.
func TestAFIFOWithAReaderIsNotAMarker(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "app.healthy")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
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
