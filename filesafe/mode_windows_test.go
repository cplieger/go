package filesafe

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestModeOptionsRefuse pins that every mode-setting entry point refuses on
// Windows with ErrModeUnsupported, as a plain error and before anything is
// created: no target, temp or directory appears and an existing target keeps
// its content.
func TestModeOptionsRefuse(t *testing.T) {
	t.Parallel()
	opts := map[string]Option{"WithMode": WithMode(0o600), "WithMkdirMode": WithMkdirMode(0o700)}

	for _, form := range writeForms() {
		for optName, opt := range opts {
			t.Run(form.name+"_"+optName, func(t *testing.T) {
				t.Parallel()
				root, dir := openTestRoot(t)
				existing := filepath.Join(dir, "existing")
				if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
					t.Fatalf("seed: %v", err)
				}

				_, err := form.write(t.Context(), root, dir, "existing", []byte("new"), opt)
				if !errors.Is(err, ErrModeUnsupported) {
					t.Fatalf("%s(existing, %s) = %v, want ErrModeUnsupported", form.name, optName, err)
				}
				if _, ok := errors.AsType[*WriteError](err); ok {
					t.Errorf("err = %v is a *WriteError, want a plain pre-write error", err)
				}
				assertContent(t, existing, "old")

				_, err = form.write(t.Context(), root, dir, filepath.Join("sub", "new"), []byte("new"), opt)
				if !errors.Is(err, ErrModeUnsupported) {
					t.Fatalf("%s(sub/new, %s) = %v, want ErrModeUnsupported", form.name, optName, err)
				}
				if _, statErr := os.Lstat(filepath.Join(dir, "sub")); !errors.Is(statErr, fs.ErrNotExist) {
					t.Errorf("Lstat(sub) = %v, want fs.ErrNotExist (no directory created)", statErr)
				}
				assertNoTempLeak(t, dir)
			})
		}
	}

	for optName, opt := range opts {
		t.Run("ProbeWritable_"+optName, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "probe")
			if _, err := ProbeWritable(t.Context(), dir, opt); !errors.Is(err, ErrModeUnsupported) {
				t.Fatalf("ProbeWritable(%s) = %v, want ErrModeUnsupported", optName, err)
			}
			if _, statErr := os.Lstat(dir); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("Lstat(probe dir) = %v, want fs.ErrNotExist", statErr)
			}
		})
		t.Run("ProbeWritableInRoot_"+optName, func(t *testing.T) {
			t.Parallel()
			root, dir := openTestRoot(t)
			if _, err := ProbeWritableInRoot(t.Context(), root, "probe", opt); !errors.Is(err, ErrModeUnsupported) {
				t.Fatalf("ProbeWritableInRoot(%s) = %v, want ErrModeUnsupported", optName, err)
			}
			if _, statErr := os.Lstat(filepath.Join(dir, "probe")); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("Lstat(probe dir) = %v, want fs.ErrNotExist", statErr)
			}
		})
	}

	t.Run("EnforceMode", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "f")
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		defer f.Close()
		// 0o400 would set the read-only attribute if a chmod ran.
		if _, err := EnforceMode(f, 0o400); !errors.Is(err, ErrModeUnsupported) {
			t.Fatalf("EnforceMode = %v, want ErrModeUnsupported", err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if fi.Mode().Perm()&0o200 == 0 {
			t.Errorf("mode = %v, want the file still writable (no chmod)", fi.Mode())
		}
	})

	t.Run("EnsurePrivateDir", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "private")
		if _, err := EnsurePrivateDir(dir); !errors.Is(err, ErrModeUnsupported) {
			t.Fatalf("EnsurePrivateDir = %v, want ErrModeUnsupported", err)
		}
		if _, statErr := os.Lstat(dir); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("Lstat(private) = %v, want fs.ErrNotExist (nothing created)", statErr)
		}
	})
}
