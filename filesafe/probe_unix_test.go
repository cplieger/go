//go:build !windows

package filesafe

import (
	"os"
	"testing"
)

// TestProbeData_UnflushableHandleFailsAtSync uses a pipe's write end, which
// accepts a write and fails Sync with EINVAL, standing in for a network
// filesystem that defers its error to fsync. On Windows FlushFileBuffers on a
// pipe waits for the reader to drain it instead of failing
// (https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers).
func TestProbeData_UnflushableHandleFailsAtSync(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	stage, err := probeData(w)
	if stage != ProbePhaseSync {
		t.Errorf("stage = %v, want %v (err %v)", stage, ProbePhaseSync, err)
	}
	if err == nil {
		t.Error("err = nil, want the Sync error")
	}
}
