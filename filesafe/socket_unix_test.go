//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestReaders_RefuseAUnixSocketWithErrNotRegular pins that every regular-file
// reader refuses a Unix socket with a *NotRegularError, not open(2)'s raw
// ENXIO, which a socket answers before a descriptor exists to check.
func TestReaders_RefuseAUnixSocketWithErrNotRegular(t *testing.T) {
	t.Parallel()
	// A short directory: a socket path is capped near 108 bytes.
	dir, err := os.MkdirTemp("", "fs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	ctx := t.Context()
	for name, call := range map[string]func() error{
		"ReadBounded":       func() error { _, err := ReadBounded(ctx, sock, 64); return err },
		"ReadBoundedInRoot": func() error { _, err := ReadBoundedInRoot(ctx, root, "s", 64); return err },
		"OpenRegularNoFollow": func() error {
			f, _, err := OpenRegularNoFollow(sock)
			if err == nil {
				_ = f.Close()
			}
			return err
		},
		"OpenRegularInRoot": func() error {
			f, _, err := OpenRegularInRoot(root, "s")
			if err == nil {
				_ = f.Close()
			}
			return err
		},
		"OpenRegularInRootNoFollow": func() error {
			f, _, err := OpenRegularInRootNoFollow(root, "s")
			if err == nil {
				_ = f.Close()
			}
			return err
		},
	} {
		err := call()
		var nre *NotRegularError
		if !errors.Is(err, ErrNotRegular) || !errors.As(err, &nre) || nre.Mode&os.ModeSocket == 0 {
			t.Errorf("%s(socket) = %v, want a *NotRegularError naming a socket", name, err)
		}
	}
}

// TestReaders_RefuseASymlinkToASocket pins the stat each following reader
// passes to socketRefusal: through a link to a socket the verdict names the
// socket, not the link, and OpenRegularNoFollow still refuses the link.
func TestReaders_RefuseASymlinkToASocket(t *testing.T) {
	t.Parallel()
	root, dir := shortTestRoot(t)
	listenUnixForTest(t, filepath.Join(dir, "s"))
	if err := os.Symlink("s", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "l")
	ctx := t.Context()
	for name, call := range map[string]func() error{
		"ReadBounded":       func() error { _, err := ReadBounded(ctx, link, 64); return err },
		"ReadBoundedInRoot": func() error { _, err := ReadBoundedInRoot(ctx, root, "l", 64); return err },
		"OpenRegularInRoot": func() error {
			f, _, err := OpenRegularInRoot(root, "l")
			if err == nil {
				_ = f.Close()
			}
			return err
		},
	} {
		var nre *NotRegularError
		if err := call(); !errors.As(err, &nre) || nre.Mode&os.ModeSocket == 0 {
			t.Errorf("%s(link to a socket) = %v, want a *NotRegularError naming the socket", name, err)
		}
	}
	if _, _, err := OpenRegularNoFollow(link); !errors.Is(err, ErrSymlinkTarget) {
		t.Errorf("OpenRegularNoFollow(link to a socket) = %v, want ErrSymlinkTarget", err)
	}
}

// TestSocketRefusal_KeepsOtherOutcomes pins socketRefusal's other arms: a
// non-ENXIO error, a stat that finds a regular file and a stat that fails all
// return the open's error unchanged.
func TestSocketRefusal_KeepsOtherOutcomes(t *testing.T) {
	t.Parallel()
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	regular := func() (os.FileInfo, error) { return os.Stat(f) }
	failing := func() (os.FileInfo, error) { return nil, syscall.EIO }
	enxio := &fs.PathError{Op: "open", Path: "x", Err: syscall.ENXIO}
	for name, tc := range map[string]struct {
		err  error
		stat func() (os.FileInfo, error)
	}{
		"not ENXIO":    {&fs.PathError{Op: "open", Path: "x", Err: syscall.EACCES}, regular},
		"regular file": {enxio, regular},
		"stat fails":   {enxio, failing},
	} {
		if got := socketRefusal(tc.err, "x", tc.stat); got != tc.err { //nolint:errorlint // identity: the same error comes back
			t.Errorf("%s: socketRefusal = %v, want the open's error unchanged", name, got)
		}
	}
}
