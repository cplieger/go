//go:build !windows

package filesafe

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempInDir returns the mode of the single staging file in dir, i.e. the
// entry that is not the target.
func tempInDir(t *testing.T, dir, target string) (string, os.FileMode) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.Name() == target {
			continue
		}
		fi, statErr := os.Lstat(filepath.Join(dir, e.Name()))
		if statErr != nil {
			t.Fatalf("lstat %s: %v", e.Name(), statErr)
		}
		return e.Name(), fi.Mode().Perm()
	}
	t.Fatalf("no staging file found in %s alongside %q", dir, target)
	return "", 0
}

// TestWrite_StagingFileIsOwnerOnlyBeforeAnyDataIsWritten pins the
// confidentiality window createTempInRoot's enforcement closes: the 0o600
// passed to O_CREATE|O_EXCL is only a request, and a filesystem with an
// inheritable group ACL can store something wider (measured: ZFS nfs4acl
// stores 0770). The observation happens INSIDE the reader the write engine
// pulls from between creating the temp and finalizing it — asserting the
// mode after the write returned would prove nothing, since finalizeTempFile
// has by then already applied the caller's mode.
func TestWrite_StagingFileIsOwnerOnlyBeforeAnyDataIsWritten(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.json")

	var (
		observedName string
		observedMode os.FileMode
	)
	probe := readerFunc(func(p []byte) (int, error) {
		if observedName == "" {
			observedName, observedMode = tempInDir(t, dir, "secret.json")
		}
		return strings.NewReader("").Read(p)
	})

	if _, err := WriteReader(t.Context(), target, io.MultiReader(strings.NewReader("token=hunter2\n"), probe),
		WithMode(0o600)); err != nil {
		t.Fatalf("WriteReader: %v", err)
	}
	if observedName == "" {
		t.Fatal("the probe never ran, so nothing was observed mid-write")
	}
	if observedMode&0o077 != 0 {
		t.Errorf("staging file %s was mode %#o while the payload was being written: "+
			"group or other could read the cleartext, and write to it before the rename published it",
			observedName, observedMode)
	}
}

// readerFunc adapts a func to io.Reader so the test can run an assertion at
// the exact point the write engine pulls data.
type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// TestWriteFile_WithModeStoresTheMode pins that an explicit mode is what
// ends up on disk, read back rather than assumed.
func TestWriteFile_WithModeStoresTheMode(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "cfg.json")
	if _, err := WriteFile(t.Context(), target, []byte("{}"), WithMode(0o600)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("published mode = %#o, want 0600", got)
	}
}
