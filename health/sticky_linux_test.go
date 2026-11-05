package health

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// asUID runs fn on a thread whose filesystem uid is uid, the uid the kernel
// checks file access against. The thread stays locked and ends with the
// goroutine, so the changed uid never reaches another goroutine.
func asUID(t *testing.T, uid int, fn func()) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread exits with this goroutine
		if err := syscall.Setfsuid(uid); err != nil {
			done <- err
			return
		}
		fn()
		done <- nil
	}()
	if err := <-done; err != nil {
		t.Fatalf("setfsuid(%d): %v", uid, err)
	}
}

// TestSetRefreshesAnotherUsersMarkerInAStickyFolder pins Set's open without
// O_CREATE: under fs.protected_regular, an O_CREAT open of a regular file the
// caller does not own, in a world-writable sticky folder owned by someone
// else, is refused, so os.Create failed every Set and an armed lease
// restart-looped. It needs root to plant the other users' files and to
// switch the thread's filesystem uid, so it skips elsewhere.
func TestSetRefreshesAnotherUsersMarkerInAStickyFolder(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create files owned by other users")
	}
	if b, err := os.ReadFile("/proc/sys/fs/protected_regular"); err != nil || len(b) == 0 || b[0] == '0' {
		t.Skip("fs.protected_regular is off")
	}
	root := t.TempDir()
	for _, d := range []string{filepath.Dir(root), root} { // t.TempDir's are 0700
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "tmp")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".healthy")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	for _, step := range []error{os.Chown(path, 65533, 65533), os.Chmod(path, 0o666), os.Chown(dir, 0, 0), os.Chmod(dir, os.ModeSticky|0o777)} {
		if step != nil {
			t.Fatal(step)
		}
	}
	var skip string
	asUID(t, 65534, func() {
		if err := probeHealthDir(path); err != nil {
			skip = "the other uid cannot write the sticky folder on this filesystem: " + err.Error()
			return
		}
		if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o666); err == nil {
			_ = f.Close()
			skip = "the kernel lets an O_CREAT open through here, so there is nothing to pin"
			return
		}
		m := NewMarker(path)
		if m.degraded {
			t.Error("a marker Set can refresh: want no degraded mode")
			return
		}
		if err := m.SetChecked(true); err != nil {
			t.Errorf("SetChecked(true) on another user's marker in a sticky folder: %v", err)
		}
	})
	if skip != "" {
		t.Skip(skip)
	}
	if got := Inspect(path, WithMaxAge(time.Hour)).State; got != MarkerFresh {
		t.Errorf("after Set(true): Inspect = %v, want MarkerFresh", got)
	}
}
