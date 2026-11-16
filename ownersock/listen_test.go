//go:build !windows

package ownersock

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/filesafe"
)

// pathOfLen is a socket path of exactly n bytes in a directory not yet made.
func pathOfLen(t *testing.T, n int) (dir, path string) {
	t.Helper()
	dir = filepath.Join(shortRoot(t), "d")
	pad := n - len(dir) - len("/.sock")
	if pad < 1 {
		t.Fatalf("the temporary root %s is too long for a %d-byte path", dir, n)
	}
	path = filepath.Join(dir, strings.Repeat("p", pad)+".sock")
	if len(path) != n {
		t.Fatalf("built a %d-byte path, want %d", len(path), n)
	}
	return dir, path
}

func absent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want it absent", p, err)
	}
}

// unix(7): sun_path is 108 bytes and a pathname socket's path is
// NUL-terminated inside it, so 107 bytes is the longest that binds.
func TestListen_pathBound(t *testing.T) {
	t.Run("a 108-byte path is refused before the directory is made", func(t *testing.T) {
		dir, path := pathOfLen(t, 108)
		ln, err := Listen(Config{Path: path})
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen of a 108-byte path succeeded, want refused")
		}
		if msg := err.Error(); !strings.Contains(msg, path) || !strings.Contains(msg, "107") {
			t.Errorf("error %q, want it to name the path and the 107-byte limit", msg)
		}
		absent(t, dir)
	})
	t.Run("a 107-byte path binds", func(t *testing.T) {
		_, path := pathOfLen(t, 107)
		listens(t, path, nil)
		dials(t, path)
	})
	t.Run("an over-long path is named without its control characters", func(t *testing.T) {
		dir, path := pathOfLen(t, 120)
		path = strings.Replace(path, "pp", "p\x1b[2J\n", 1)
		_, err := Listen(Config{Path: path})
		if err == nil {
			t.Fatal("Listen succeeded, want refused")
		}
		if msg := err.Error(); strings.ContainsAny(msg, "\x1b\n") {
			t.Errorf("error %q carries the path's control characters", msg)
		}
		absent(t, dir)
	})
}

func TestListen_refusesAPathItCannotCheck(t *testing.T) {
	root := shortRoot(t)
	for name, path := range map[string]string{
		"an empty path":                "",
		"a relative path":              "d/s.sock",
		"a path through a parent step": root + "/a/../b/s.sock",
		"a path ending in a separator": root + "/b/s.sock/",
	} {
		t.Run(name, func(t *testing.T) {
			ln, err := Listen(Config{Path: path})
			if err == nil {
				_ = ln.Close()
				t.Fatalf("Listen(%q) succeeded, want refused", path)
			}
			for _, d := range []string{"a", "b", "d"} {
				absent(t, filepath.Join(root, d))
			}
		})
	}
}

func TestListen_custody(t *testing.T) {
	t.Run("a fresh directory is 0700 and the socket 0600", func(t *testing.T) {
		dir, path := sockPaths(t)
		listens(t, path, nil)
		fi, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("directory mode %v, want a 0700 directory", fi.Mode())
		}
		fi, err = os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o600 {
			t.Errorf("socket mode %v, want a socket with mode 0600", fi.Mode())
		}
		dials(t, path)
	})
	t.Run("a missing parent is refused and not created", func(t *testing.T) {
		dir, _ := sockPaths(t)
		path := filepath.Join(dir, "sub", "s.sock")
		if _, err := Listen(Config{Path: path}); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Listen: %v, want fs.ErrNotExist", err)
		}
		absent(t, dir)
	})
	for _, mode := range []os.FileMode{0o750, 0o770} {
		t.Run("a "+mode.String()+" directory is refused unchanged", func(t *testing.T) {
			dir, path := sockPaths(t)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Listen(Config{Path: path}); !errors.Is(err, filesafe.ErrModeTooOpen) {
				t.Errorf("Listen: %v, want ErrModeTooOpen", err)
			}
			if fi, err := os.Lstat(dir); err != nil || fi.Mode().Perm() != mode {
				t.Errorf("the refused directory changed: %v %v", fi.Mode(), err)
			}
			absent(t, path)
		})
	}
	t.Run("a symlinked directory is refused", func(t *testing.T) {
		dir, path := sockPaths(t)
		target := dir + "-target"
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(Config{Path: path}); !errors.Is(err, filesafe.ErrSymlinkTarget) {
			t.Errorf("Listen: %v, want ErrSymlinkTarget", err)
		}
		absent(t, filepath.Join(target, filepath.Base(path)))
	})
	t.Run("a directory of another owner is refused", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("needs root to chown; the mode and symlink cases are its unprivileged witnesses")
		}
		dir, path := sockPaths(t)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(Config{Path: path}); !errors.Is(err, filesafe.ErrNotOwned) {
			t.Errorf("Listen: %v, want ErrNotOwned", err)
		}
		absent(t, path)
	})
}

func TestListen_whatIsAlreadyAtThePath(t *testing.T) {
	t.Run("a live socket refuses a second Listen and keeps answering", func(t *testing.T) {
		_, path := sockPaths(t)
		listens(t, path, nil)
		ln, err := Listen(Config{Path: path})
		if err == nil {
			_ = ln.Close()
			t.Fatal("a second Listen on a live socket succeeded, want refused")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error %q, want it to name the path", err)
		}
		dials(t, path)
	})
	t.Run("a dead server's socket is replaced", func(t *testing.T) {
		dir, path := sockPaths(t)
		deadSocket(t, dir, path)
		listens(t, path, nil)
		dials(t, path)
	})
	t.Run("a regular file is refused and left as it was", func(t *testing.T) {
		dir, path := sockPaths(t)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		ln, err := Listen(Config{Path: path})
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen over a regular file succeeded, want refused")
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "keep me" {
			t.Errorf("the file reads %q, %v after the refusal; want it untouched", got, err)
		}
	})
	t.Run("a symlink is refused and left as it was", func(t *testing.T) {
		dir, path := sockPaths(t)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		_, live := sockPaths(t)
		listens(t, live, nil)
		if err := os.Symlink(live, path); err != nil {
			t.Fatal(err)
		}
		ln, err := Listen(Config{Path: path})
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen over a symlink succeeded, want refused")
		}
		if got, err := os.Readlink(path); err != nil || got != live {
			t.Errorf("the link reads %q, %v after the refusal; want it untouched", got, err)
		}
		dials(t, live)
	})
}

func TestListen_closeRemovesTheSocket(t *testing.T) {
	_, path := sockPaths(t)
	ln := listens(t, path, nil)
	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	absent(t, path)
}

func TestListen_hidesTheUncheckedAccept(t *testing.T) {
	_, path := sockPaths(t)
	ln := listens(t, path, nil)
	if _, ok := ln.(interface {
		AcceptUnix() (*net.UnixConn, error)
	}); ok {
		t.Error("the listener exposes AcceptUnix, which skips the peer check")
	}
	if got := ln.Addr().String(); got != path {
		t.Errorf("Addr() = %q, want %q", got, path)
	}
}
