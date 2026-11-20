package trigger

import (
	"context"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// testPayload is the parameterized-payload stand-in: arguments that must
// replay exactly on the daemon side (the renovate shape, minus the environ).
type testPayload struct {
	Repos []string `json:"repos,omitempty"`
}

// maxSunPath is the longest usable unix-socket path: Linux sun_path is
// 108 bytes including the trailing NUL.
const maxSunPath = 107

// testSocketPath returns an unbound socket path in a fresh directory. Under a
// deep TMPDIR, t.TempDir's path overflows sun_path and bind fails with EINVAL,
// so the directory then moves to /tmp.
func testSocketPath(t *testing.T) string {
	t.Helper()
	if path := filepath.Join(t.TempDir(), "s.sock"); len(path) <= maxSunPath {
		return path
	}
	dir, err := os.MkdirTemp("/tmp", "trig-sock-")
	if err != nil {
		t.Fatalf("mktemp for socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// listenTestSocket binds a unix socket at a fresh testSocketPath and closes it
// at cleanup; closing it earlier is harmless.
func listenTestSocket(t *testing.T) (net.Listener, string) {
	t.Helper()
	sock := testSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("net.Listen(unix, %q) = %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, sock
}

// startExecutor drains q on a background goroutine, running fn per job (fn
// owns Start/Finish). Returns a done channel that closes when the queue
// drains.
func startExecutor(q *Queue[testPayload], fn func(*Job[testPayload])) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for j := range q.Jobs() {
			fn(j)
		}
	}()
	return done
}

// recordHandler is a minimal capturing slog handler (the library must not
// grow a test dependency for log capture; stdlib-only, matching the parent
// package's posture).
type recordHandler struct {
	mu      *sync.Mutex
	records *[]slog.Record
}

func newRecordHandler() recordHandler {
	return recordHandler{mu: &sync.Mutex{}, records: &[]slog.Record{}}
}

func (recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, r)
	return nil
}
func (h recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordHandler) WithGroup(string) slog.Handler      { return h }

func (h recordHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), *h.records...)
}

// captureLogs swaps slog.Default for a capturing handler for the test's
// duration and restores all three globals slog.SetDefault writes. Tests using
// it must not run in parallel (process-global default). SetDefault also points
// the log package at the installed handler and SKIPS that redirect for slog's
// own default handler, so restoring slog alone leaves log writing into the
// capture; slog goes back first, because reinstalling a non-default handler
// re-runs the redirect.
func captureLogs(t *testing.T) recordHandler {
	t.Helper()
	h := newRecordHandler()
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return h
}

// TestCaptureLogs_restoresLogPackageGlobals pins the restore this package's log
// assertions rest on: a leaked redirect silences every later slog call in the
// binary, because the stock default handler emits through log.Output.
func TestCaptureLogs_restoresLogPackageGlobals(t *testing.T) {
	wantWriter, wantFlags := log.Writer(), log.Flags()

	t.Run("capture", func(t *testing.T) {
		captureLogs(t)
		if log.Writer() == wantWriter {
			t.Fatal("slog.SetDefault did not redirect log's writer, so this test cannot observe the restore")
		}
	})

	if log.Writer() != wantWriter {
		t.Error("log.Writer() not restored; later slog calls write into the capturing handler")
	}
	if got := log.Flags(); got != wantFlags {
		t.Errorf("log.Flags() = %d, want %d", got, wantFlags)
	}
}
