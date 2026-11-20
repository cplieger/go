//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

var errInjectedSync = errors.New("injected parent sync failure")

// failFirstFsyncDir makes the first parent sync fail with errInjectedSync
// and runs the real sync after that. Returns the number of syncs asked for.
func failFirstFsyncDir(t *testing.T) func() int {
	t.Helper()
	orig := fsyncDir
	calls := 0
	stubFsyncDir(t, func(dir string) error {
		calls++
		if calls == 1 {
			return errInjectedSync
		}
		return orig(dir)
	})
	return func() int { return calls }
}

func TestEnsurePrivateDir_SyncsTheParentOfADirectoryItCreates(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "priv")
	synced := recordFsyncDir(t)

	res, err := EnsurePrivateDir(dir)
	if err != nil {
		t.Fatalf("EnsurePrivateDir = %v, want nil", err)
	}
	if !res.Created {
		t.Errorf("Created = false, want true")
	}
	if got, want := synced(), []string{parent}; !slices.Equal(got, want) {
		t.Errorf("synced dirs = %q, want %q", got, want)
	}
}

func TestEnsurePrivateDir_SyncsTheParentOfADirectoryItFinds(t *testing.T) {
	t.Run("already private", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "priv")
		mkdirExact(t, dir, 0o700)
		synced := recordFsyncDir(t)

		res, err := EnsurePrivateDir(dir)
		if err != nil {
			t.Fatalf("EnsurePrivateDir = %v, want nil", err)
		}
		if res.Created {
			t.Errorf("Created = true, want false")
		}
		if got, want := synced(), []string{parent}; !slices.Equal(got, want) {
			t.Errorf("synced dirs = %q, want %q", got, want)
		}
	})
	t.Run("repaired under WithRepairOwnedDir", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "priv")
		mkdirExact(t, dir, 0o750)
		synced := recordFsyncDir(t)

		res, err := EnsurePrivateDir(dir, WithRepairOwnedDir(true))
		if err != nil {
			t.Fatalf("EnsurePrivateDir = %v, want nil", err)
		}
		if !res.Repaired {
			t.Errorf("Repaired = false, want true")
		}
		if got, want := synced(), []string{parent}; !slices.Equal(got, want) {
			t.Errorf("synced dirs = %q, want %q", got, want)
		}
	})
}

func TestEnsurePrivateDir_FailedSyncAfterCreateLeavesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "priv")
	stubFsyncDir(t, func(string) error { return errInjectedSync })

	res, err := EnsurePrivateDir(dir)
	if !errors.Is(err, ErrParentNotSynced) {
		t.Errorf("err = %v, want ErrParentNotSynced", err)
	}
	if !errors.Is(err, errInjectedSync) {
		t.Errorf("err = %v, want it to wrap the sync failure", err)
	}
	if res != (PrivateDirResult{}) {
		t.Errorf("result = %+v, want the zero result", res)
	}
	fi, lerr := os.Lstat(dir)
	if lerr != nil {
		t.Fatalf("Lstat(dir) = %v, want the created directory left in place", lerr)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want a directory at 0700", fi.Mode())
	}
}

// A second caller can find the directory, sync the parent and return nil
// while the creator's own sync is failing; the creator must not take the
// directory away from it.
func TestEnsurePrivateDir_FailedSyncAfterCreateKeepsAConcurrentFindersDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "priv")
	orig := fsyncDir
	var finderErr error
	calls := 0
	stubFsyncDir(t, func(parent string) error {
		calls++
		if calls > 1 {
			return orig(parent)
		}
		_, finderErr = EnsurePrivateDir(dir)
		return errInjectedSync
	})

	_, creatorErr := EnsurePrivateDir(dir)
	if !errors.Is(creatorErr, ErrParentNotSynced) {
		t.Errorf("creator err = %v, want ErrParentNotSynced", creatorErr)
	}
	if finderErr != nil {
		t.Errorf("finder err = %v, want nil", finderErr)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Errorf("Lstat(dir) = %v, want the directory the finder was promised", err)
	}
}

func TestEnsurePrivateDir_FailedSyncAfterFindRemovesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "priv")
	mkdirExact(t, dir, 0o700)
	file := filepath.Join(dir, "kept")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubFsyncDir(t, func(string) error { return errInjectedSync })

	res, err := EnsurePrivateDir(dir)
	if !errors.Is(err, ErrParentNotSynced) {
		t.Errorf("err = %v, want ErrParentNotSynced", err)
	}
	if !errors.Is(err, errInjectedSync) {
		t.Errorf("err = %v, want it to wrap the sync failure", err)
	}
	if res != (PrivateDirResult{}) {
		t.Errorf("result = %+v, want the zero result", res)
	}
	if _, serr := os.Stat(file); serr != nil {
		t.Errorf("Stat(file in found dir) = %v, want it left in place", serr)
	}
	if got := permOf(t, dir); got != 0o700 {
		t.Errorf("mode of found dir = %#o, want 0700 unchanged", got)
	}
}

// A parent the effective uid can traverse but not read fails the sync's open
// on every call, so a retry cannot cure it.
func TestEnsurePrivateDir_UnreadableParentIsFatal(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{name: "created", setup: func(*testing.T, string) {}},
		{name: "found", setup: func(t *testing.T, dir string) { mkdirExact(t, dir, 0o700) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "priv")
			tc.setup(t, dir)
			stubFsyncDir(t, func(parent string) error {
				return &fs.PathError{Op: "open", Path: parent, Err: syscall.EACCES}
			})

			_, err := EnsurePrivateDir(dir)
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("err = %v, want fs.ErrPermission", err)
			}
			if errors.Is(err, ErrParentNotSynced) {
				t.Errorf("err = %v, want it not to match ErrParentNotSynced, which promises a retry", err)
			}
			if _, lerr := os.Lstat(dir); lerr != nil {
				t.Errorf("Lstat(dir) = %v, want the directory left in place", lerr)
			}
		})
	}
}

func TestEnsurePrivateDir_RetryAfterAFailedSyncSucceeds(t *testing.T) {
	t.Run("after a create", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "priv")
		calls := failFirstFsyncDir(t)

		if _, err := EnsurePrivateDir(dir); !errors.Is(err, ErrParentNotSynced) {
			t.Fatalf("first EnsurePrivateDir = %v, want ErrParentNotSynced", err)
		}
		res, err := EnsurePrivateDir(dir)
		if err != nil {
			t.Fatalf("retry EnsurePrivateDir = %v, want nil", err)
		}
		if res.Created {
			t.Errorf("retry Created = true, want false: the failed call left the directory")
		}
		if got := calls(); got != 2 {
			t.Errorf("parent syncs = %d, want 2", got)
		}
	})
	t.Run("after a find", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "priv")
		mkdirExact(t, dir, 0o700)
		calls := failFirstFsyncDir(t)

		if _, err := EnsurePrivateDir(dir); !errors.Is(err, ErrParentNotSynced) {
			t.Fatalf("first EnsurePrivateDir = %v, want ErrParentNotSynced", err)
		}
		res, err := EnsurePrivateDir(dir)
		if err != nil {
			t.Fatalf("retry EnsurePrivateDir = %v, want nil", err)
		}
		if res.Created {
			t.Errorf("retry Created = true, want false")
		}
		if got := calls(); got != 2 {
			t.Errorf("parent syncs = %d, want 2", got)
		}
	})
}

func TestEnsurePrivateDir_RefusalsDoNotSync(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "priv")
	mkdirExact(t, dir, 0o755)
	synced := recordFsyncDir(t)

	if _, err := EnsurePrivateDir(dir); !errors.Is(err, ErrModeTooOpen) {
		t.Fatalf("EnsurePrivateDir = %v, want ErrModeTooOpen", err)
	}
	if got := synced(); len(got) != 0 {
		t.Errorf("synced dirs = %q, want none", got)
	}
}
