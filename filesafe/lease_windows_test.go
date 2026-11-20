package filesafe

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestLease_UnsupportedOnWindows(t *testing.T) {
	forms := map[string]func(path string) (*Lease, error){
		"AcquireLease":       func(p string) (*Lease, error) { return AcquireLease(t.Context(), p) },
		"TryLease":           TryLease,
		"AcquireSharedLease": func(p string) (*Lease, error) { return AcquireSharedLease(t.Context(), p) },
		"TrySharedLease":     TrySharedLease,
	}
	for name, take := range forms {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.lock")
			_, err := take(path)
			if !errors.Is(err, ErrModeUnsupported) || !errors.Is(err, errors.ErrUnsupported) {
				t.Errorf("err = %v, want ErrModeUnsupported and errors.ErrUnsupported", err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Lstat(lock file) = %v, want fs.ErrNotExist: nothing created", err)
			}
		})
	}
}
