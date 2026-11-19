//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

func TestEnsurePrivateDir_FailedSyncAfterCreateRemovesTheEmptyDirectory(t *testing.T) {
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
	if _, lerr := os.Lstat(dir); !errors.Is(lerr, fs.ErrNotExist) {
		t.Errorf("Lstat(dir) = %v, want fs.ErrNotExist: the created directory must be removed", lerr)
	}
}

func TestEnsurePrivateDir_FailedSyncAfterCreateKeepsADirectoryARacerFilled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "priv")
	file := filepath.Join(dir, "racer")
	stubFsyncDir(t, func(string) error {
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Errorf("racer write: %v", err)
		}
		return errInjectedSync
	})

	_, err := EnsurePrivateDir(dir)
	if !errors.Is(err, ErrParentNotSynced) {
		t.Errorf("err = %v, want ErrParentNotSynced", err)
	}
	if _, serr := os.Stat(file); serr != nil {
		t.Errorf("Stat(racer's file) = %v, want it left in place", serr)
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
		if !res.Created {
			t.Errorf("retry Created = false, want true: the failed call removed the directory")
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
