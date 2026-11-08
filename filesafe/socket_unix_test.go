//go:build !windows

package filesafe

import (
	"errors"
	"net"
	"os"
	"path/filepath"
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
