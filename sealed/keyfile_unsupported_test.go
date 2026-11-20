//go:build !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd)

package sealed

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/go/filesafe"
)

// TestKeyFileUnsupported checks OpenKeyFile on a system where no owner,
// mode or flock(2) check can keep another user out (the ErrUnsafeKeyFile
// godoc: "on a system with no file owners or modes it wraps
// filesafe.ErrModeUnsupported"; filesafe's README: on Windows every mode and
// EnsurePrivateDir answer ErrModeUnsupported). A valid key file and a
// missing one are both refused, before anything is read or created.
func TestKeyFileUnsupported(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "sealing.key")
	if err := os.WriteFile(valid, []byte(loadVectors(t).KeyFiles[0].Text), 0o600); err != nil {
		t.Fatalf("write the key file: %v", err)
	}
	for _, tc := range []struct{ name, path string }{
		{"key file", valid},
		{"no key file", filepath.Join(dir, "missing.key")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := OpenKeyFile(t.Context(), tc.path)
			if f != nil || !errors.Is(err, ErrUnsafeKeyFile) || !errors.Is(err, filesafe.ErrModeUnsupported) {
				t.Errorf("OpenKeyFile = a KeyFile %v, %v; want none, ErrUnsafeKeyFile and ErrModeUnsupported", f != nil, err)
			}
			if _, err := os.Lstat(tc.path + lockSuffix); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Lstat(the lock file) = %v; want none made", err)
			}
		})
	}
}
