//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package ownersock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/filesafe"
)

const (
	listenHelperEnv     = "OWNERSOCK_LISTEN_HELPER"
	listenModeEnv       = "OWNERSOCK_LISTEN_MODE"
	helperSocketName    = "s.sock"
	helperExitHeld      = 3
	listenHelperTimeout = 10 * time.Second
)

// TestListenHelperProcess is the child half of the two-process lease tests,
// on helperSocketName in its working directory. Mode "listen" prints
// "listening" just before Listen, "bound" once it returns, and keeps the
// listener until stdin reaches EOF; "try-lease" exits 0 when it got the
// socket's lease, helperExitHeld on ErrLeaseHeld and 1 otherwise. The path is
// not passed in the environment because gosec's taint analysis would then
// flag every open in the package.
func TestListenHelperProcess(t *testing.T) {
	if os.Getenv(listenHelperEnv) != "1" {
		t.Skip("helper process for the two-process lease tests")
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	path := filepath.Join(wd, helperSocketName)
	switch mode := os.Getenv(listenModeEnv); mode {
	case "listen":
		fmt.Println("listening")
		ln, err := Listen(Config{Path: path, Logger: slog.New(slog.DiscardHandler)})
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Println("bound")
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = ln.Close()
		os.Exit(0)
	case "try-lease":
		l, err := filesafe.TryLease(path + ".lock")
		switch {
		case err == nil:
			_ = l.Close()
			os.Exit(0)
		case errors.Is(err, filesafe.ErrLeaseHeld):
			os.Exit(helperExitHeld)
		default:
			fmt.Println("error:", err)
			os.Exit(1)
		}
	default:
		fmt.Println("unknown mode", mode)
		os.Exit(1)
	}
}

func listenHelperCmd(t *testing.T, path, mode string) *exec.Cmd {
	t.Helper()
	if filepath.Base(path) != helperSocketName {
		t.Fatalf("helper socket path %s, want one named %s", path, helperSocketName)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestListenHelperProcess$") // #nosec G204 -- re-executes this test binary
	cmd.Dir = filepath.Dir(path)
	cmd.Env = append(os.Environ(),
		listenHelperEnv+"=1", listenModeEnv+"="+mode,
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	return cmd
}

// runTryLease runs a child in mode "try-lease" and returns its exit code.
func runTryLease(t *testing.T, path string) int {
	t.Helper()
	out, err := listenHelperCmd(t, path, "try-lease").CombinedOutput()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if exitErr.ExitCode() != helperExitHeld {
			t.Logf("helper output: %q", out)
		}
		return exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("running helper: %v (output %q)", err, out)
	}
	return 0
}

// waitFor polls out until it holds want, failing on an "error:" line or
// after listenHelperTimeout.
func waitFor(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(listenHelperTimeout)
	for !strings.Contains(out.String(), want) {
		if strings.Contains(out.String(), "error:") || time.Now().After(deadline) {
			t.Fatalf("helper never printed %q; output %q", want, out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func privateDir(t *testing.T) (dir, path string) {
	t.Helper()
	dir, path = sockPaths(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestListen_waitsWhileAnotherProcessHoldsTheSocketsLease(t *testing.T) {
	_, path := privateDir(t)
	lease, err := filesafe.TryLease(path + ".lock")
	if err != nil {
		t.Fatalf("TryLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })

	child := listenHelperCmd(t, path, "listen")
	out := &syncBuffer{}
	child.Stdout, child.Stderr = out, out
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = child.Wait()
	})

	waitFor(t, out, "listening\n")
	time.Sleep(300 * time.Millisecond)
	if got := out.String(); got != "listening\n" {
		t.Fatalf("the child's Listen returned while this process held the lease; output %q", got)
	}
	absent(t, path)
	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, out, "bound\n")
	dials(t, path)
}

func TestListen_holdsTheSocketsLeaseAcrossTheBind(t *testing.T) {
	dir, path := sockPaths(t)
	deadSocket(t, dir, path)
	var during int
	withProbeDial(t, func(context.Context, string, string) (net.Conn, error) {
		during = runTryLease(t, path)
		return nil, dialFailure(os.NewSyscallError("connect", syscall.ECONNREFUSED))
	})
	listens(t, path, nil)
	if during != helperExitHeld {
		t.Errorf("a child's TryLease during Listen's bind exited %d, want %d (ErrLeaseHeld)", during, helperExitHeld)
	}
	if code := runTryLease(t, path); code != 0 {
		t.Errorf("a child's TryLease after Listen returned exited %d, want 0: the lease outlived the bind", code)
	}
	dials(t, path)
}

func TestListen_lockFileBesideTheSocket(t *testing.T) {
	dir, path := sockPaths(t)
	ln := listens(t, path, nil)
	fi, err := os.Lstat(path + ".lock")
	if err != nil {
		t.Fatalf("Lstat(%s.lock): %v", path, err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode %v, want a regular file with mode 0600", fi.Mode())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		t.Errorf("lock file owner %v, want the euid %d", fi.Sys(), os.Geteuid())
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{filepath.Base(path) + ".lock"}; !slices.Equal(names, want) {
		t.Errorf("after Close the socket directory holds %q, want %q: the lock file stays and nothing else", names, want)
	}
}

func TestListen_takesNoLockOnTheSocketDirectory(t *testing.T) {
	dir, path := privateDir(t)
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := syscall.Flock(int(d.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock on the socket directory: %v", err)
	}
	type result struct {
		ln  net.Listener
		err error
	}
	done := make(chan result, 1)
	go func() {
		ln, err := Listen(Config{Path: path, Logger: slog.New(slog.DiscardHandler)})
		done <- result{ln, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Listen beside a flock on its directory: %v", r.err)
		}
		_ = r.ln.Close()
	case <-time.After(5 * time.Second):
		_ = d.Close()
		if r := <-done; r.ln != nil {
			_ = r.ln.Close()
		}
		t.Fatal("Listen waited on a flock held on the socket directory")
	}
}

func TestListen_refusesASymlinkAtTheLockPath(t *testing.T) {
	dir, path := privateDir(t)
	target := filepath.Join(dir, "elsewhere")
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(Config{Path: path})
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen with a symlink at the lock path succeeded, want refused")
	}
	if !errors.Is(err, filesafe.ErrSymlinkTarget) {
		t.Errorf("Listen: %v, want ErrSymlinkTarget", err)
	}
	absent(t, path)
	absent(t, target)
}
