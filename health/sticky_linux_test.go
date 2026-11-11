package health

import (
	"errors"
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

// stickyFolder returns a root-owned 1777 folder that other uids can reach. It
// needs root, so it skips elsewhere.
func stickyFolder(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to create files owned by other users")
	}
	root := t.TempDir()
	for _, d := range []string{filepath.Dir(root), root} { // t.TempDir's are 0700
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "shm")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, step := range []error{os.Chown(dir, 0, 0), os.Chmod(dir, os.ModeSticky|0o777)} {
		if step != nil {
			t.Fatal(step)
		}
	}
	return dir
}

// fileOwnedBy writes a 0666 file at path holding data, owned by uid.
func fileOwnedBy(t *testing.T, path string, uid int, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o666); err != nil {
		t.Fatal(err)
	}
	for _, step := range []error{os.Chown(path, uid, uid), os.Chmod(path, 0o666)} {
		if step != nil {
			t.Fatal(step)
		}
	}
}

// TestAnotherUsersMarkerInAStickyFolder pins the write's open without
// O_CREATE: under fs.protected_regular, an O_CREAT open of a regular file the
// caller does not own, in a world-writable sticky folder owned by someone
// else, is refused. The service can open that file but not remove it, so
// NewStatus and every unhealthy write warn, and every healthy write refreshes
// it.
func TestAnotherUsersMarkerInAStickyFolder(t *testing.T) {
	dir := stickyFolder(t)
	if b, err := os.ReadFile("/proc/sys/fs/protected_regular"); err != nil || len(b) == 0 || b[0] == '0' {
		t.Skip("fs.protected_regular is off")
	}
	p := filepath.Join(dir, "app.healthy")
	fileOwnedBy(t, p, 65533, "")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	var skip string
	asUID(t, 65534, func() {
		if err := probeHealthDir(p); err != nil {
			skip = "the other uid cannot write the sticky folder on this filesystem: " + err.Error()
			return
		}
		if f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0o666); err == nil {
			_ = f.Close()
			skip = "the kernel lets an O_CREAT open through here, so there is nothing to pin"
		}
	})
	if skip != "" {
		t.Skip(skip)
	}
	l, buf := jsonLog()
	var st *Status
	asUID(t, 65534, func() { st = NewStatus(WithLogger(l), WithMarker(p)) })
	if st.mirror.degraded {
		t.Error("a marker a write can refresh: want no degraded mode")
	}
	if n := len(withMsg(t, buf, msgRemoveFailed)); n != 1 {
		t.Errorf("%q lines after NewStatus = %d, want 1", msgRemoveFailed, n)
	}
	asUID(t, 65534, func() { st.Set(true) })
	if got := inspect(p, WithMaxAge(time.Hour)).state; got != markerFresh {
		t.Errorf("after Set(true): inspect = %v, want markerFresh", got)
	}
	asUID(t, 65534, func() { st.Set(false) })
	if !exists(t, p) {
		t.Error("Set(false) removed another user's file from a sticky folder")
	}
	if n := len(withMsg(t, buf, msgRemoveFailed)); n != 2 {
		t.Errorf("%q lines after Set(false) = %d, want 2", msgRemoveFailed, n)
	}
}

// TestSymlinkInAStickyFolderIsNeverFollowed: a link at the marker path,
// whether its owner is a neighbour (whose link the kernel refuses to follow
// under fs.protected_symlinks) or the folder's owner (whose link it follows),
// is degraded mode and never writes the file it names.
func TestSymlinkInAStickyFolderIsNeverFollowed(t *testing.T) {
	dir := stickyFolder(t)
	for name, linkOwner := range map[string]int{"a neighbour's link": 65533, "the folder owner's link": 0} {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(dir, "target-"+name)
			fileOwnedBy(t, target, 65534, "keep")
			p := filepath.Join(dir, "app-"+name+".healthy")
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			if err := os.Lchown(p, linkOwner, linkOwner); err != nil {
				t.Fatal(err)
			}
			l, buf := jsonLog()
			var dirErr error
			code := -1
			asUID(t, 65534, func() {
				if dirErr = probeHealthDir(p); dirErr != nil {
					return
				}
				st := NewStatus(WithLogger(l), WithMarker(p))
				st.Set(true)
				code = probeCode(p)
			})
			if dirErr != nil {
				t.Skip("the other uid cannot write the sticky folder on this filesystem: " + dirErr.Error())
			}
			if n := len(withMsg(t, buf, msgMarkerDegraded)); n != 1 {
				t.Errorf("%q lines = %d, want 1: %q", msgMarkerDegraded, n, buf.String())
			}
			if code != 0 {
				t.Errorf("probe = %d, want 0 (degraded)", code)
			}
			assertKept(t, target)
		})
	}
}

// TestALeaseDoesNotDegradeTheMarker: a read lease another open holds on the
// marker makes the non-blocking open fail with EAGAIN instead of waiting for
// the lease to break. That clears on its own, so it must not put the marker
// in degraded mode for the life of the process.
func TestALeaseDoesNotDegradeTheMarker(t *testing.T) {
	const fSetLease, fRdLck, fUnLck = 1024, 0, 2
	p := filepath.Join(t.TempDir(), "app.healthy")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, r.Fd(), fSetLease, fRdLck); e != 0 {
		t.Skipf("F_SETLEASE: %v", e)
	}
	defer func() { _, _, _ = syscall.Syscall(syscall.SYS_FCNTL, r.Fd(), fSetLease, fUnLck) }()
	if err := openMarker(p, 0); !errors.Is(err, syscall.EAGAIN) {
		t.Skipf("a held lease does not refuse this open here (%v); nothing to pin", err)
	}
	if st := NewStatus(WithLogger(discard), WithMarker(p)); st.mirror.degraded {
		t.Error("a marker under a lease: want no degraded mode, the lease breaks on its own")
	}
}
