package health

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a settable stand-in for now. Tests that use it swap a package
// global, so they never call t.Parallel.
type clock struct {
	t  time.Time
	mu sync.Mutex
}

var epoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func useClock(t *testing.T) *clock {
	t.Helper()
	c := &clock{t: epoch}
	prev := now
	now = c.now
	t.Cleanup(func() { now = prev })
	return c
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// jsonLog returns a logger writing JSON lines to the returned buffer.
func jsonLog() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

// records decodes every JSON line in buf.
func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// withMsg returns the records in buf whose message is msg.
func withMsg(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range records(t, buf) {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

var discard = slog.New(slog.DiscardHandler)

// captureLogs makes a text handler over a fresh buffer slog's default for the
// test's duration and returns the buffer. Callers must be serial (no
// t.Parallel): the default logger is a process global.
//
// slog.SetDefault also points the standard log package at the installed
// handler, and skips that redirect when the logger being installed carries
// slog's own default handler, so reinstalling the previous logger does not
// undo it: the writer and flags are restored explicitly, after slog.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return buf
}

// TestCaptureLogsRestoresLogGlobals pins the restore in captureLogs: without
// it, one test silences slog for the rest of the package, because slog's own
// default handler writes through log.Output.
func TestCaptureLogsRestoresLogGlobals(t *testing.T) {
	wantWriter, wantFlags := log.Writer(), log.Flags()

	t.Run("swap", func(t *testing.T) {
		captureLogs(t)
		if log.Writer() == wantWriter {
			t.Fatal("captureLogs did not redirect log.Writer(); the restore under test would guard nothing")
		}
	})

	if got := log.Writer(); got != wantWriter {
		t.Errorf("log.Writer() after captureLogs cleanup = %#v, want the original %#v", got, wantWriter)
	}
	if got := log.Flags(); got != wantFlags {
		t.Errorf("log.Flags() after captureLogs cleanup = %d, want %d", got, wantFlags)
	}
}

// markerIn returns a marker path in a folder of its own, which blockFolder can
// replace.
func markerIn(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "app.healthy")
}

// blockFolder replaces the empty folder of path with a file, so every write
// to path fails with ENOTDIR, for any user, root included.
func blockFolder(t *testing.T, path string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// unblockFolder undoes blockFolder.
func unblockFolder(t *testing.T, path string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

// exists reports whether something is at path, without following a symlink.
func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return err == nil
}

// probeCode is the exit code RunProbe would give path.
func probeCode(path string, opts ...ProbeOption) int {
	code, _ := probeCheck(path, opts...)
	return code
}
