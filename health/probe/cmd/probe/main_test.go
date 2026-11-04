package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain re-executes the test binary as probe itself when the re-exec
// variable is set, so main()'s argument handling — which ends in os.Exit on
// every branch — can be observed from a child process. Without the variable it
// just runs the package's tests.
func TestMain(m *testing.M) {
	if os.Getenv("PROBE_TEST_REEXEC_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// runMain re-executes this test binary as probe with args, returning the
// child's exit code and its combined output.
func runMain(t *testing.T, args ...string) (int, string) {
	t.Helper()
	// Bounded: every case here asserts the child EXITS. A regression that made
	// it wait instead would otherwise hold CombinedOutput open until the
	// package-wide test timeout panics; the kill turns that into this failure.
	// Background rather than t.Context(), because cancel is registered as a
	// Cleanup and t.Context() is already cancelled when Cleanups run.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), "PROBE_TEST_REEXEC_MAIN=1")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), string(out)
	default:
		t.Fatalf("re-exec %v: %v", args, err)
		return -1, ""
	}
}

// The command's doc comment publishes three exit codes, and consumer images
// depend on the usage one specifically: a Docker HEALTHCHECK treats any
// non-zero status as unhealthy, so 2 is what tells an operator the arguments
// are wrong rather than the service. It had no test at the library, and every
// consumer that wanted it asserted it locally instead — docker-caddy carried a
// Dockerfile assertion for exactly this until the argument path there became
// unreachable, at which point the deletion was right and the contract was left
// unpinned everywhere.
func TestMain_noURLsIsAUsageError(t *testing.T) {
	code, out := runMain(t)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (usage); output: %s", code, out)
	}
	if !strings.Contains(out, "usage:") {
		t.Errorf("output does not carry a usage line: %s", out)
	}
	if !strings.Contains(out, "url [url ...]") {
		t.Errorf("usage line does not name the URL operand: %s", out)
	}
	// -timeout is the one flag, and its default is the value a caller relies on
	// when they pass none, so the usage output has to state it.
	if !strings.Contains(out, "-timeout") {
		t.Errorf("usage output does not document -timeout: %s", out)
	}
}

// An unparseable flag is the other way in: flag's own error path, which also
// exits 2. Pinned because the command overrides flag.Usage, and an override
// that wrote to the wrong stream or exited 1 would make a malformed
// HEALTHCHECK indistinguishable from a failing service.
func TestMain_unparseableFlagIsAUsageError(t *testing.T) {
	code, out := runMain(t, "-timeout", "not-a-duration", "http://127.0.0.1:1/")
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (usage); output: %s", code, out)
	}
	if !strings.Contains(out, "usage:") {
		t.Errorf("output does not carry a usage line: %s", out)
	}
}

// The usage path must not be reachable from a well-formed invocation. This
// one cannot connect, so it exits 1 — the distinction between "your arguments
// are wrong" and "the service is down" is the whole point of two codes.
func TestMain_unreachableURLIsAFailureNotAUsageError(t *testing.T) {
	// Port 1 with a 1s budget: nothing listens, so this refuses immediately
	// rather than waiting out the default timeout.
	code, out := runMain(t, "-timeout", "1s", "http://127.0.0.1:1/")
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (probe failure); output: %s", code, out)
	}
	if strings.Contains(out, "usage:") {
		t.Errorf("a well-formed invocation printed usage: %s", out)
	}
}
