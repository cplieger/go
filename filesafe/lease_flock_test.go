//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package filesafe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// leaseForm is one lease entry point, so a refusal is pinned for each.
type leaseForm struct {
	take func(ctx context.Context, path string) (*Lease, error)
	name string
}

var (
	acquireForm       = leaseForm{name: "AcquireLease", take: AcquireLease}
	tryForm           = leaseForm{name: "TryLease", take: func(_ context.Context, p string) (*Lease, error) { return TryLease(p) }}
	acquireSharedForm = leaseForm{name: "AcquireSharedLease", take: AcquireSharedLease}
	trySharedForm     = leaseForm{name: "TrySharedLease", take: func(_ context.Context, p string) (*Lease, error) { return TrySharedLease(p) }}

	allLeaseForms       = []leaseForm{acquireForm, tryForm, acquireSharedForm, trySharedForm}
	exclusiveLeaseForms = []leaseForm{acquireForm, tryForm}
	sharedLeaseForms    = []leaseForm{acquireSharedForm, trySharedForm}
	blockingLeaseForms  = []leaseForm{acquireForm, acquireSharedForm}
	singleTryLeaseForms = []leaseForm{tryForm, trySharedForm}
)

// newLockFile makes an owner-only lock file for the shared forms, which never
// create one, and returns its path.
func newLockFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod lock file: %v", err)
	}
	return path
}

func mustTryLease(t *testing.T, path string) *Lease {
	t.Helper()
	l, err := TryLease(path)
	if err != nil {
		t.Fatalf("TryLease(%s) = %v, want a lease", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func mustTrySharedLease(t *testing.T, path string) *Lease {
	t.Helper()
	l, err := TrySharedLease(path)
	if err != nil {
		t.Fatalf("TrySharedLease(%s) = %v, want a lease", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// pause waits d, or until the test ends.
func pause(t *testing.T, d time.Duration) {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
	}
}

// stubLeaseLockedHook sets leaseLockedHook to fn, restoring it on test end.
// Callers must not use t.Parallel: package state.
func stubLeaseLockedHook(t *testing.T, fn func(path string)) {
	t.Helper()
	orig := leaseLockedHook
	t.Cleanup(func() { leaseLockedHook = orig })
	leaseLockedHook = fn
}

// replaceOnce returns a leaseLockedHook that removes the lock file and makes
// a new one at its path the first time it runs, so the locked file is no
// longer the one at the path.
func replaceOnce(t *testing.T) func(path string) {
	t.Helper()
	var once sync.Once
	return func(path string) {
		once.Do(func() {
			if err := os.Remove(path); err != nil {
				t.Errorf("remove lock file: %v", err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Errorf("recreate lock file: %v", err)
			}
		})
	}
}

const (
	leaseHelperEnv     = "FILESAFE_LEASE_HELPER"
	leaseModeEnv       = "FILESAFE_LEASE_MODE"
	leaseFileName      = "app.lock"
	leaseExitHeld      = 3
	leaseHelperTimeout = 10 * time.Second
)

// TestLeaseHelperProcess is the child half of the two-process lease tests,
// leasing leaseFileName in its working directory. Modes "hold" and
// "hold-shared" take the lease, print "held" and keep it until stdin reaches
// EOF; "try" and "try-shared" exit 0 when they got the lease, leaseExitHeld
// on ErrLeaseHeld and 1 otherwise. The path is not passed in the environment
// because gosec's taint analysis would then flag every open in the package.
func TestLeaseHelperProcess(t *testing.T) {
	if os.Getenv(leaseHelperEnv) != "1" {
		t.Skip("helper process for the two-process lease tests")
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	path := filepath.Join(wd, leaseFileName)
	switch mode := os.Getenv(leaseModeEnv); mode {
	case "hold", "hold-shared":
		ctx, cancel := context.WithTimeout(context.Background(), leaseHelperTimeout)
		defer cancel()
		acquire := AcquireLease
		if mode == "hold-shared" {
			acquire = AcquireSharedLease
		}
		l, err := acquire(ctx, path)
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Println("held")
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = l.Close()
		os.Exit(0)
	case "try", "try-shared":
		try := TryLease
		if mode == "try-shared" {
			try = TrySharedLease
		}
		l, err := try(path)
		switch {
		case err == nil:
			_ = l.Close()
			os.Exit(0)
		case errors.Is(err, ErrLeaseHeld):
			os.Exit(leaseExitHeld)
		default:
			fmt.Println("error:", err)
			os.Exit(1)
		}
	default:
		fmt.Println("unknown mode", mode)
		os.Exit(1)
	}
}

func leaseHelperCmd(t *testing.T, path, mode string) *exec.Cmd {
	t.Helper()
	if filepath.Base(path) != leaseFileName {
		t.Fatalf("helper lock path %s, want one named %s", path, leaseFileName)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLeaseHelperProcess$") // #nosec G204 -- re-executes this test binary
	cmd.Dir = filepath.Dir(path)
	cmd.Env = append(os.Environ(),
		leaseHelperEnv+"=1", leaseModeEnv+"="+mode,
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	return cmd
}

// syncBuffer is a bytes.Buffer safe for exec's copy goroutine and a reader.
type syncBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// leaseHolder is a child process holding a lease until release.
type leaseHolder struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// startLeaseHolder starts a child in mode "hold" or "hold-shared" and
// returns once it reports the lease held.
func startLeaseHolder(t *testing.T, path, mode string) *leaseHolder {
	t.Helper()
	cmd := leaseHelperCmd(t, path, mode)
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper: %v", err)
	}
	h := &leaseHolder{cmd: cmd, stdin: stdin}
	t.Cleanup(func() { _ = h.release() })

	deadline := time.Now().Add(leaseHelperTimeout)
	for !strings.Contains(out.String(), "held\n") {
		if strings.Contains(out.String(), "error:") || time.Now().After(deadline) {
			t.Fatalf("helper never held the lease; output %q", out.String())
		}
		pause(t, 5*time.Millisecond)
	}
	return h
}

// release closes the child's stdin and waits for it to exit.
func (h *leaseHolder) release() error {
	_ = h.stdin.Close()
	return h.cmd.Wait()
}

// runLeaseTry runs a child in mode "try" or "try-shared" and returns its
// exit code.
func runLeaseTry(t *testing.T, path, mode string) int {
	t.Helper()
	cmd := leaseHelperCmd(t, path, mode)
	out, err := cmd.CombinedOutput()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if exitErr.ExitCode() != leaseExitHeld {
			t.Logf("helper output: %q", out)
		}
		return exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("running helper: %v (output %q)", err, out)
	}
	return 0
}

func TestLease_ExcludesAnotherProcess(t *testing.T) {
	t.Run("a child's lease refuses this process", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.lock")
		child := startLeaseHolder(t, path, "hold")

		if l, err := TryLease(path); !errors.Is(err, ErrLeaseHeld) {
			_ = closeIfLease(l)
			t.Errorf("TryLease while a child holds it = %v, want ErrLeaseHeld", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		l, err := AcquireLease(ctx, path)
		if !errors.Is(err, ErrLeaseHeld) || !errors.Is(err, context.DeadlineExceeded) {
			_ = closeIfLease(l)
			t.Errorf("AcquireLease(200ms) while a child holds it = %v, want ErrLeaseHeld and DeadlineExceeded", err)
		}

		if err := child.release(); err != nil {
			t.Fatalf("helper exit: %v", err)
		}
		ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		l, err = AcquireLease(ctx, path)
		if err != nil {
			t.Fatalf("AcquireLease after the child released = %v, want a lease", err)
		}
		_ = l.Close()
	})

	t.Run("this process's lease refuses a child", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.lock")
		l := mustTryLease(t, path)
		if code := runLeaseTry(t, path, "try"); code != leaseExitHeld {
			t.Errorf("child TryLease while this process holds it exited %d, want %d (ErrLeaseHeld)", code, leaseExitHeld)
		}
		if code := runLeaseTry(t, path, "try-shared"); code != leaseExitHeld {
			t.Errorf("child TrySharedLease while this process holds it exited %d, want %d (ErrLeaseHeld)", code, leaseExitHeld)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if code := runLeaseTry(t, path, "try"); code != 0 {
			t.Errorf("child TryLease after Close exited %d, want 0", code)
		}
	})

	t.Run("a killed child's lease is released", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.lock")
		child := startLeaseHolder(t, path, "hold")
		if err := child.cmd.Process.Kill(); err != nil {
			t.Fatalf("kill helper: %v", err)
		}
		_ = child.cmd.Wait()
		l := mustTryLease(t, path)
		_ = l.Close()
	})
}

func closeIfLease(l *Lease) error {
	if l == nil {
		return nil
	}
	return l.Close()
}

func TestLease_SecondLeaseInOneProcessWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.lock")
	mustTryLease(t, path)
	if l, err := TryLease(path); !errors.Is(err, ErrLeaseHeld) {
		_ = closeIfLease(l)
		t.Errorf("second TryLease in one process = %v, want ErrLeaseHeld", err)
	}
	if l, err := TrySharedLease(path); !errors.Is(err, ErrLeaseHeld) {
		_ = closeIfLease(l)
		t.Errorf("TrySharedLease beside this process's exclusive lease = %v, want ErrLeaseHeld", err)
	}
}

func TestAcquireLease_ContextCancelWhileWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.lock")
	mustTryLease(t, path)
	for _, form := range blockingLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			stop := time.AfterFunc(120*time.Millisecond, cancel)
			defer stop.Stop()
			start := time.Now()
			l, err := form.take(ctx, path)
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("returned %v after the start, want soon after the 120ms cancel", elapsed)
			}
			if !errors.Is(err, ErrLeaseHeld) || !errors.Is(err, context.Canceled) {
				_ = closeIfLease(l)
				t.Errorf("err = %v, want ErrLeaseHeld and context.Canceled", err)
			}
		})
	}
}

func TestAcquireLease_SeesAReleaseWithinOnePoll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.lock")
	holder, err := TryLease(path)
	if err != nil {
		t.Fatalf("TryLease: %v", err)
	}
	type result struct {
		at  time.Time
		err error
	}
	got := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		l, err := AcquireLease(ctx, path)
		at := time.Now()
		_ = closeIfLease(l)
		got <- result{at: at, err: err}
	}()
	pause(t, 170*time.Millisecond)
	if err := holder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed := time.Now()
	r := <-got
	if r.err != nil {
		t.Fatalf("waiting AcquireLease = %v, want a lease", r.err)
	}
	if d := r.at.Sub(closed); d > 100*time.Millisecond {
		t.Errorf("waiter returned %v after the holder's Close, want within 100ms", d)
	}
}

func TestAcquireLease_DoneContextOpensNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	t.Run("AcquireLease", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.lock")
		if l, err := AcquireLease(ctx, path); !errors.Is(err, context.Canceled) {
			_ = closeIfLease(l)
			t.Errorf("AcquireLease(done ctx) = %v, want context.Canceled", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(lock file) = %v, want fs.ErrNotExist: a done ctx creates nothing", err)
		}
	})
	t.Run("AcquireSharedLease", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.lock")
		if l, err := AcquireSharedLease(ctx, path); !errors.Is(err, context.Canceled) || errors.Is(err, fs.ErrNotExist) {
			_ = closeIfLease(l)
			t.Errorf("AcquireSharedLease(done ctx, missing file) = %v, want context.Canceled before any open", err)
		}
	})
}

func TestAcquireLease_RefusesADirectory(t *testing.T) {
	for _, form := range allLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.lock")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			l, err := form.take(t.Context(), path)
			if !errors.Is(err, ErrNotRegular) {
				_ = closeIfLease(l)
				t.Fatalf("err = %v, want ErrNotRegular", err)
			}
			nr, ok := errors.AsType[*NotRegularError](err)
			if !ok || !nr.Mode.IsDir() {
				t.Errorf("errors.As *NotRegularError = %v (ok %v), want a directory mode", nr, ok)
			}
		})
	}
}

func TestAcquireLease_RefusesASymlink(t *testing.T) {
	for _, form := range allLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			t.Run("to a regular file", func(t *testing.T) {
				target := newLockFile(t)
				path := filepath.Join(t.TempDir(), "app.lock")
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("symlink: %v", err)
				}
				if l, err := form.take(t.Context(), path); !errors.Is(err, ErrSymlinkTarget) {
					_ = closeIfLease(l)
					t.Errorf("err = %v, want ErrSymlinkTarget", err)
				}
				l := mustTryLease(t, target)
				_ = l.Close()
			})
			t.Run("dangling", func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "missing")
				path := filepath.Join(dir, "app.lock")
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("symlink: %v", err)
				}
				if l, err := form.take(t.Context(), path); !errors.Is(err, ErrSymlinkTarget) {
					_ = closeIfLease(l)
					t.Errorf("err = %v, want ErrSymlinkTarget", err)
				}
				if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Lstat(link target) = %v, want fs.ErrNotExist: nothing created through the link", err)
				}
			})
		})
	}
}

func TestAcquireLease_RefusesAFIFO(t *testing.T) {
	for _, form := range allLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.lock")
			if err := MkfifoForTest(path); err != nil {
				t.Fatalf("mkfifo: %v", err)
			}
			l, err := form.take(t.Context(), path)
			if !errors.Is(err, ErrNotRegular) {
				_ = closeIfLease(l)
				t.Errorf("err = %v, want ErrNotRegular", err)
			}
		})
	}
}

func TestAcquireLease_RefusesAWideMode(t *testing.T) {
	for _, form := range allLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := newLockFile(t)
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			if l, err := form.take(t.Context(), path); !errors.Is(err, ErrModeTooOpen) {
				_ = closeIfLease(l)
				t.Errorf("err = %v, want ErrModeTooOpen", err)
			}
			fi, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("Lstat: %v", err)
			}
			if got := fi.Mode().Perm(); got != 0o644 {
				t.Errorf("mode after the refusal = %#o, want 0644 unchanged", got)
			}
		})
	}
}

func TestAcquireLease_ReportsAModeTheFilesystemDidNotStore(t *testing.T) {
	for _, form := range exclusiveLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			stubEnforceMode(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "app.lock")
			if l, err := form.take(t.Context(), path); !errors.Is(err, ErrModeNotStored) {
				_ = closeIfLease(l)
				t.Errorf("err = %v, want ErrModeNotStored", err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Lstat(lock file) = %v, want fs.ErrNotExist: a file whose mode failed never appears", err)
			}
			assertNoTempLeak(t, dir)
		})
	}
}

func TestAcquireLease_RefusesAForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown the lock file to another uid")
	}
	for _, form := range allLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := newLockFile(t)
			if err := os.Chown(path, 65534, 65534); err != nil {
				t.Fatalf("chown: %v", err)
			}
			if l, err := form.take(t.Context(), path); !errors.Is(err, ErrNotOwned) {
				_ = closeIfLease(l)
				t.Errorf("err = %v, want ErrNotOwned", err)
			}
		})
	}
}

func TestAcquireLease_CreatesTheLockFileAt0600AndLeavesIt(t *testing.T) {
	for _, form := range exclusiveLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			withUmask(t, 0)
			path := filepath.Join(t.TempDir(), "app.lock")
			l, err := form.take(t.Context(), path)
			if err != nil {
				t.Fatalf("err = %v, want a lease", err)
			}
			fi, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("Lstat(lock file): %v", err)
			}
			if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
				t.Errorf("lock file mode = %v, want a regular file at 0600", fi.Mode())
			}
			if err := l.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Errorf("Lstat(lock file) after Close = %v, want it left in place", err)
			}
		})
	}
}

// wrapEnforceMode runs before ahead of the real enforceMode, restoring it on
// test end. Callers must not use t.Parallel: package state.
func wrapEnforceMode(t *testing.T, before func()) {
	t.Helper()
	orig := enforceMode
	t.Cleanup(func() { enforceMode = orig })
	enforceMode = func(f *os.File, want os.FileMode) (os.FileMode, error) {
		before()
		return orig(f, want)
	}
}

func TestAcquireLease_SetsTheModeBeforeTheLockFileAppears(t *testing.T) {
	for _, form := range exclusiveLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "app.lock")
			wrapEnforceMode(t, func() {
				if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Lstat(lock file) while its mode is set = %v, want fs.ErrNotExist", err)
				}
			})
			l, err := form.take(t.Context(), path)
			if err != nil {
				t.Fatalf("err = %v, want a lease", err)
			}
			defer func() { _ = l.Close() }()
			if ok, err := l.Covers(); !ok || err != nil {
				t.Errorf("Covers = (%v, %v), want (true, nil)", ok, err)
			}
			assertNoTempLeak(t, dir)
		})
	}
}

func TestAcquireLease_LockFileMadeByARacingCreatorIsTheOneLocked(t *testing.T) {
	for _, form := range exclusiveLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "app.lock")
			var racer os.FileInfo
			wrapEnforceMode(t, func() {
				f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					t.Errorf("racing creator: %v", err)
					return
				}
				racer, err = f.Stat()
				if err != nil {
					t.Errorf("racing creator stat: %v", err)
				}
				_ = f.Close()
			})
			l, err := form.take(t.Context(), path)
			if err != nil {
				t.Fatalf("err = %v, want a lease on the racer's lock file", err)
			}
			defer func() { _ = l.Close() }()
			held, err := l.f.Stat()
			if err != nil {
				t.Fatalf("stat held file: %v", err)
			}
			if racer == nil || !os.SameFile(held, racer) {
				t.Errorf("held lock file is not the one the racer created")
			}
			assertNoTempLeak(t, dir)
		})
	}
}

func TestAcquireLease_RejectsBadPaths(t *testing.T) {
	cases := []struct {
		want error
		name string
		path string
	}{
		{name: "empty", path: "", want: ErrEmptyPath},
		{name: "relative", path: "app.lock", want: ErrUnsafePath},
		{name: "null byte", path: "/tmp/app\x00.lock", want: ErrUnsafePath},
	}
	for _, form := range allLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					if l, err := form.take(t.Context(), tc.path); !errors.Is(err, tc.want) {
						_ = closeIfLease(l)
						t.Errorf("err = %v, want %v", err, tc.want)
					}
				})
			}
			t.Run("missing parent", func(t *testing.T) {
				parent := filepath.Join(t.TempDir(), "missing")
				if l, err := form.take(t.Context(), filepath.Join(parent, "app.lock")); !errors.Is(err, fs.ErrNotExist) {
					_ = closeIfLease(l)
					t.Errorf("err = %v, want fs.ErrNotExist", err)
				}
				if _, err := os.Lstat(parent); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Lstat(parent) = %v, want fs.ErrNotExist: a lease creates no directory", err)
				}
			})
		})
	}
}

func TestLease_CloseReleases(t *testing.T) {
	for _, form := range allLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := newLockFile(t)
			l, err := form.take(t.Context(), path)
			if err != nil {
				t.Fatalf("err = %v, want a lease", err)
			}
			if err := l.Close(); err != nil {
				t.Fatalf("Close = %v", err)
			}
			mustTryLease(t, path)
			if err := l.Close(); !errors.Is(err, fs.ErrClosed) {
				t.Errorf("second Close = %v, want fs.ErrClosed", err)
			}
		})
	}
}

func TestLease_Covers(t *testing.T) {
	t.Run("in place", func(t *testing.T) {
		l := mustTryLease(t, filepath.Join(t.TempDir(), "app.lock"))
		if ok, err := l.Covers(); !ok || err != nil {
			t.Errorf("Covers = (%v, %v), want (true, nil)", ok, err)
		}
	})
	t.Run("removed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.lock")
		l := mustTryLease(t, path)
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if ok, err := l.Covers(); ok || err != nil {
			t.Errorf("Covers = (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run("removed and made again", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.lock")
		l := mustTryLease(t, path)
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("recreate: %v", err)
		}
		if ok, err := l.Covers(); ok || err != nil {
			t.Errorf("Covers = (%v, %v), want (false, nil)", ok, err)
		}
	})
}

func TestTryLease_ReplacedLockFileIsRaced(t *testing.T) {
	for _, form := range singleTryLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := newLockFile(t)
			stubLeaseLockedHook(t, replaceOnce(t))
			if l, err := form.take(t.Context(), path); !errors.Is(err, ErrRaced) {
				_ = closeIfLease(l)
				t.Errorf("err = %v, want ErrRaced", err)
			}
		})
	}
}

func TestAcquireLease_RetriesAReplacedLockFile(t *testing.T) {
	for _, form := range blockingLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := newLockFile(t)
			stubLeaseLockedHook(t, replaceOnce(t))
			l, err := form.take(t.Context(), path)
			if err != nil {
				t.Fatalf("err = %v, want a lease on the new lock file", err)
			}
			defer func() { _ = l.Close() }()
			if ok, err := l.Covers(); !ok || err != nil {
				t.Errorf("Covers = (%v, %v), want (true, nil)", ok, err)
			}
		})
	}
}

// Nobody holds the lease while its lock file keeps being replaced, so a
// waiter whose context ends reports the race, not a holder.
func TestAcquireLease_ContextEndWhileTheLockFileKeepsBeingReplacedIsRaced(t *testing.T) {
	for _, form := range blockingLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := newLockFile(t)
			stubLeaseLockedHook(t, func(path string) {
				if err := os.Remove(path); err != nil {
					t.Errorf("remove lock file: %v", err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Errorf("recreate lock file: %v", err)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			l, err := form.take(ctx, path)
			if !errors.Is(err, ErrRaced) || !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %v, want ErrRaced and context.DeadlineExceeded", err)
			}
			if errors.Is(err, ErrLeaseHeld) {
				t.Errorf("err = %v, want it not to match ErrLeaseHeld: nobody held the lease", err)
			}
			_ = closeIfLease(l)
		})
	}
}

func TestSharedLease_RefusedWhileExclusiveHeld(t *testing.T) {
	path := newLockFile(t)
	mustTryLease(t, path)
	if l, err := TrySharedLease(path); !errors.Is(err, ErrLeaseHeld) {
		_ = closeIfLease(l)
		t.Errorf("TrySharedLease = %v, want ErrLeaseHeld", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if l, err := AcquireSharedLease(ctx, path); !errors.Is(err, ErrLeaseHeld) || !errors.Is(err, context.DeadlineExceeded) {
		_ = closeIfLease(l)
		t.Errorf("AcquireSharedLease(200ms) = %v, want ErrLeaseHeld and DeadlineExceeded", err)
	}
}

func TestSharedLease_TwoHoldersTogether(t *testing.T) {
	t.Run("in one process", func(t *testing.T) {
		path := newLockFile(t)
		mustTrySharedLease(t, path)
		mustTrySharedLease(t, path)
		if l, err := TryLease(path); !errors.Is(err, ErrLeaseHeld) {
			_ = closeIfLease(l)
			t.Errorf("TryLease beside two shared holders = %v, want ErrLeaseHeld", err)
		}
	})
	t.Run("across two processes", func(t *testing.T) {
		path := newLockFile(t)
		startLeaseHolder(t, path, "hold-shared")
		l := mustTrySharedLease(t, path)
		if code := runLeaseTry(t, path, "try"); code != leaseExitHeld {
			t.Errorf("child TryLease beside two shared holders exited %d, want %d (ErrLeaseHeld)", code, leaseExitHeld)
		}
		_ = l.Close()
		if code := runLeaseTry(t, path, "try-shared"); code != 0 {
			t.Errorf("child TrySharedLease beside a shared holder exited %d, want 0", code)
		}
	})
}

func TestAcquireLease_WaitsOnSharedHolders(t *testing.T) {
	path := newLockFile(t)
	first, err := TrySharedLease(path)
	if err != nil {
		t.Fatalf("TrySharedLease: %v", err)
	}
	second, err := TrySharedLease(path)
	if err != nil {
		t.Fatalf("TrySharedLease: %v", err)
	}
	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		l, err := AcquireLease(ctx, path)
		_ = closeIfLease(l)
		got <- err
	}()

	pause(t, 150*time.Millisecond)
	_ = first.Close()
	pause(t, 150*time.Millisecond)
	select {
	case err := <-got:
		t.Fatalf("AcquireLease returned %v while a shared holder remained, want it waiting", err)
	default:
	}
	_ = second.Close()
	if err := <-got; err != nil {
		t.Errorf("AcquireLease after the last shared Close = %v, want a lease", err)
	}
}

func TestSharedLease_MissingFileIsNotExist(t *testing.T) {
	for _, form := range sharedLeaseForms {
		t.Run(form.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.lock")
			if l, err := form.take(t.Context(), path); !errors.Is(err, fs.ErrNotExist) {
				_ = closeIfLease(l)
				t.Errorf("err = %v, want fs.ErrNotExist", err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Lstat(lock file) = %v, want fs.ErrNotExist: a shared lease creates nothing", err)
			}
		})
	}
}
