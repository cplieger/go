//go:build !windows

package filesafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A failed Commit lands in the committed state with a cached *WriteError; a
// second Commit must replay that identical error value and the same zero
// Result, without re-running the barrier or leaking a temp. Closing the
// embedded fd makes the barrier's first step under WithMode (the mode
// enforcement) fail, so this also pins the PhaseTempChmod tag and the
// on-barrier-failure temp cleanup.
func TestPendingFile_FailedCommit_ReplaysSameError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "failed-commit-replay.txt")

	pf, err := NewPendingFile(t.Context(), path, WithMode(0o600))
	if err != nil {
		t.Fatalf("NewPendingFile: %v", err)
	}
	if _, err := pf.Write([]byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := pf.File.Close(); err != nil {
		t.Fatalf("File.Close: %v", err)
	}

	firstRes, firstErr := pf.Commit(t.Context())
	we, ok := errors.AsType[*WriteError](firstErr)
	if !ok || we.Phase != PhaseTempChmod {
		t.Fatalf("first Commit = %v, want *WriteError{PhaseTempChmod}", firstErr)
	}
	if firstRes != (Result{}) {
		t.Fatalf("first Commit result = %+v, want zero Result", firstRes)
	}

	secondRes, secondErr := pf.Commit(t.Context())
	if secondErr != firstErr {
		t.Errorf("second Commit err = %v, want the cached first err %v (identical value)", secondErr, firstErr)
	}
	if errors.Is(secondErr, ErrAborted) {
		t.Errorf("second Commit err = %v, want the cached WriteError, not ErrAborted", secondErr)
	}
	if secondRes != firstRes {
		t.Errorf("second Commit result = %+v, want cached %+v", secondRes, firstRes)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("final file exists after replayed failed Commit, want absent")
	}
	assertNoTempLeak(t, dir)
}
