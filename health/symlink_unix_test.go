//go:build unix

package health

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// keepFile writes a file holding "keep" and returns its path.
func keepFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "target")
	if err := os.WriteFile(p, []byte("keep"), 0o666); err != nil {
		t.Fatal(err)
	}
	return p
}

// assertKept fails unless the file at path still holds "keep".
func assertKept(t *testing.T, path string) {
	t.Helper()
	if b, err := os.ReadFile(path); err != nil || string(b) != "keep" {
		t.Errorf("the file the link names holds %q (%v), want \"keep\"", b, err)
	}
}

// assertLink fails unless path is still a symlink.
func assertLink(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Lstat(path); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("marker path after the writes = %v (%v), want the symlink left in place", info, err)
	}
}

func TestSymlinkAtTheMarkerPathIsDegraded(t *testing.T) {
	t.Parallel()
	for name, target := range map[string]func(t *testing.T, dir, p string) string{
		"to a regular file": func(t *testing.T, dir, _ string) string { return keepFile(t, dir) },
		"dangling":          func(_ *testing.T, dir, _ string) string { return filepath.Join(dir, "never-created") },
		"to itself":         func(_ *testing.T, _, p string) string { return p },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := filepath.Join(dir, "app.healthy")
			to := target(t, dir, p)
			if err := os.Symlink(to, p); err != nil {
				t.Fatal(err)
			}
			l, buf := jsonLog()
			st := NewStatus(WithLogger(l), WithMarker(p))
			got := withMsg(t, buf, msgMarkerDegraded)
			if len(got) != 1 || !strings.Contains(got[0]["hint"].(string), "a symlink") || !strings.Contains(got[0]["error"].(string), "is a symlink") {
				t.Fatalf("degraded lines = %v, want one whose hint and error name a symlink", got)
			}
			for _, ok := range []bool{true, false, true} {
				st.Set(ok)
				if st.Healthy() != ok {
					t.Errorf("Healthy() after Set(%v) = %v", ok, st.Healthy())
				}
			}
			assertLink(t, p)
			switch name {
			case "to a regular file":
				assertKept(t, to)
			case "dangling":
				if exists(t, to) {
					t.Error("a write created the file the dangling link names")
				}
			}
			if probeCode(p) != 0 || probeCode(p, WithMaxAge(time.Nanosecond)) != 0 {
				t.Errorf("probe = %d, with a 1ns lease %d; want 0 and 0 (degraded)", probeCode(p), probeCode(p, WithMaxAge(time.Nanosecond)))
			}
		})
	}
}

func TestSetNeverFollowsASymlinkPlantedAfterStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "app.healthy")
	l, buf := jsonLog()
	st := NewStatus(WithLogger(l), WithMarker(p))
	st.Set(false)
	target := keepFile(t, dir)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	st.Set(true)
	if n := len(withMsg(t, buf, msgCreateFailed)); n != 1 {
		t.Errorf("%q lines = %d, want 1", msgCreateFailed, n)
	}
	assertKept(t, target)
	assertLink(t, p)
	if !st.Healthy() {
		t.Error("Healthy() after Set(true) with a planted link = false, want the verdict Set gave")
	}
	if got := probeCode(p); got != 0 {
		t.Errorf("probe on a planted link = %d, want 0 (degraded)", got)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	st.Set(true)
	if n := len(withMsg(t, buf, msgRecovered)); n != 1 {
		t.Errorf("%q lines once the link is gone = %d, want 1", msgRecovered, n)
	}
	if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() {
		t.Errorf("marker once the link is gone = %v (%v), want a regular file", info, err)
	}
}
