package scheduler

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// silentLogger returns a logger that discards output, for tests and fuzz
// targets that exercise ParseInterval's fallback paths without spamming stderr.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// childGORACE is the GORACE entry for a re-executed test binary: the inherited
// options plus atexit_sleep_ms=0, since a race-enabled binary otherwise sleeps
// 1 s before it exits (https://go.dev/doc/articles/race_detector#Options). It
// goes after os.Environ() in cmd.Env, because os/exec keeps a key's last value.
func childGORACE() string {
	return "GORACE=" + strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0")
}
