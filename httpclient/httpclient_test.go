package httpclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// bufLogger returns a slog.Logger writing text records (Debug and up) to buf.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// swapDefaultLogger installs logger as the process default for the test and
// restores all three globals slog.SetDefault writes. It also points the log
// package at the installed handler and SKIPS that redirect for slog's own
// default handler, so restoring slog alone leaves log writing into a buffer
// nothing reads. slog goes back first: reinstalling a non-default handler
// re-runs the redirect and would undo an earlier log restore. Restored with
// t.Cleanup rather than defer, which would not run on a subtest's failure path.
func swapDefaultLogger(t *testing.T, logger *slog.Logger) {
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
}

// TestSwapDefaultLogger_restoresLogPackageGlobals pins the restore every
// slog.Default() assertion in this package rests on: a leaked redirect silences
// every later slog call in the binary, because the stock default handler emits
// through log.Output.
func TestSwapDefaultLogger_restoresLogPackageGlobals(t *testing.T) {
	wantWriter, wantFlags := log.Writer(), log.Flags()

	t.Run("swap", func(t *testing.T) {
		var buf bytes.Buffer
		swapDefaultLogger(t, bufLogger(&buf))
		if log.Writer() == wantWriter {
			t.Fatal("slog.SetDefault did not redirect log's writer, so this test cannot observe the restore")
		}
	})

	if log.Writer() != wantWriter {
		t.Error("log.Writer() not restored; later slog calls write into the swapped handler's buffer")
	}
	if got := log.Flags(); got != wantFlags {
		t.Errorf("log.Flags() = %d, want %d", got, wantFlags)
	}
}

// doRateLimited adapts the v2 RetryOnRateLimit shape onto Do +
// WithRateLimitOnly: an error-only fn under the rate-limit-only mode. It is
// the same adapter shape consumers of the deleted helper write (see the v3
// design doc's subflux migration).
func doRateLimited(ctx context.Context, maxAttempts int, maxWait time.Duration, fn func(ctx context.Context) error) error {
	_, err := Do(ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	}, WithRateLimitOnly(maxWait), WithMaxAttempts(maxAttempts))
	return err
}

func TestIsTransient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"auth error", &StatusError{Code: http.StatusForbidden}, false},
		{"rate limit error", &StatusError{Code: http.StatusTooManyRequests}, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"canceled", context.Canceled, false},
		{"attempt timeout", AttemptTimeout(context.DeadlineExceeded), true},
		{"attempt timeout wrapped by the caller", fmt.Errorf("delivering: %w", AttemptTimeout(context.DeadlineExceeded)), true},
		{"permanent attempt timeout", Permanent(AttemptTimeout(context.DeadlineExceeded)), false},
		{"permanent", Permanent(errors.New("nope")), false},
		{"permanent transient", Permanent(&StatusError{Code: 503}), false},
		{"http 502", &StatusError{Code: 502}, true},
		{"http 503", &StatusError{Code: 503}, true},
		{"http 504", &StatusError{Code: 504}, true},
		{"http 500", &StatusError{Code: 500}, true},
		{"http 408", &StatusError{Code: 408}, true},
		{"http 400", &StatusError{Code: 400}, false},
		{"net timeout", &fakeNetError{timeout: true}, true},
		{"net non-timeout", &fakeNetError{timeout: false}, false},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"conn reset", syscall.ECONNRESET, true},
		{"conn refused", syscall.ECONNREFUSED, true},
		{"broken pipe", syscall.EPIPE, true},
		// The shape net/http hands back: a write errno arrives inside an
		// *os.SyscallError inside a *net.OpError, and *net.OpError satisfies
		// net.Error, so this case also pins that the net.Error rung above does
		// not claim it first (Errno.Timeout is false for EPIPE).
		{"broken pipe wrapped in a net.OpError", &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}, true},
		{"dns error", &net.DNSError{Err: "no such host"}, true},
		{"plain error", errors.New("misc"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := IsTransient(tt.err)
			if got != tt.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

type fakeNetError struct{ timeout bool }

func (*fakeNetError) Error() string   { return "fake net error" }
func (e *fakeNetError) Timeout() bool { return e.timeout }
func (*fakeNetError) Temporary() bool { return false }

func TestDoRateLimitOnly(t *testing.T) {
	t.Parallel()

	t.Run("success on first call", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := doRateLimited(t.Context(), 3, 5*time.Second, func(_ context.Context) error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
	})

	t.Run("non-rate-limit error returns immediately", func(t *testing.T) {
		t.Parallel()
		calls := 0
		wantErr := errors.New("permanent failure")
		err := doRateLimited(t.Context(), 3, 5*time.Second, func(_ context.Context) error {
			calls++
			return wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected %v, got %v", wantErr, err)
		}
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
	})

	t.Run("transient error returns immediately under rate-limit-only mode", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := doRateLimited(t.Context(), 3, 5*time.Second, func(_ context.Context) error {
			calls++
			return &StatusError{Code: 503}
		})
		if _, ok := errors.AsType[*StatusError](err); !ok {
			t.Fatalf("expected *StatusError, got %v", err)
		}
		if calls != 1 {
			t.Fatalf("expected 1 call (transients are NOT retried in rate-limit-only mode), got %d", calls)
		}
	})

	t.Run("rate-limit error retries", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := doRateLimited(t.Context(), 3, 5*time.Second, func(_ context.Context) error {
			calls++
			if calls < 3 {
				return &StatusError{Code: http.StatusTooManyRequests, RetryAfter: time.Millisecond}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 3 {
			t.Fatalf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("exhausts attempts returns last error", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := doRateLimited(t.Context(), 2, 5*time.Second, func(_ context.Context) error {
			calls++
			return &StatusError{Code: http.StatusTooManyRequests, RetryAfter: time.Millisecond}
		})
		if err == nil {
			t.Fatal("expected error after exhausting attempts")
		}
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("expected a 429, got %T: %v", err, err)
		}
		if calls != 2 {
			t.Fatalf("expected 2 calls, got %d", calls)
		}
	})

	t.Run("context cancellation stops retry", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		err := doRateLimited(ctx, 5, 5*time.Second, func(_ context.Context) error {
			calls++
			if calls == 1 {
				cancel()
			}
			return &StatusError{Code: http.StatusTooManyRequests, RetryAfter: time.Millisecond}
		})
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
		if calls > 2 {
			t.Fatalf("expected at most 2 calls with cancellation, got %d", calls)
		}
	})

	t.Run("context is passed to fn", func(t *testing.T) {
		t.Parallel()
		ctx := context.WithValue(t.Context(), ctxKey{}, "test-value")
		err := doRateLimited(ctx, 1, time.Second, func(fnCtx context.Context) error {
			if fnCtx.Value(ctxKey{}) != "test-value" {
				t.Error("context not propagated to fn")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

type ctxKey struct{}

func TestDo(t *testing.T) {
	t.Parallel()

	t.Run("success on first call", func(t *testing.T) {
		t.Parallel()
		calls := 0
		result, err := Do(t.Context(), func(_ context.Context) (string, error) {
			calls++
			return "ok", nil
		}, WithMaxAttempts(3), WithBaseDelay(time.Millisecond), WithLabel("test"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "ok" {
			t.Fatalf("expected 'ok', got %q", result)
		}
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
	})

	t.Run("retries on error then succeeds", func(t *testing.T) {
		t.Parallel()
		calls := 0
		result, err := Do(t.Context(), func(_ context.Context) (int, error) {
			calls++
			if calls < 3 {
				return 0, &StatusError{Code: 503}
			}
			return 42, nil
		}, WithMaxAttempts(4), WithBaseDelay(time.Millisecond), WithLabel("test"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != 42 {
			t.Fatalf("expected 42, got %d", result)
		}
		if calls != 3 {
			t.Fatalf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("exhausts retries returns last error", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, err := Do(t.Context(), func(_ context.Context) (string, error) {
			calls++
			return "", &StatusError{Code: 502}
		}, WithMaxAttempts(3), WithBaseDelay(time.Millisecond), WithLabel("test"))
		if err == nil {
			t.Fatal("expected error after exhausting retries")
		}
		if calls != 3 {
			t.Fatalf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("non-transient error not retried", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, err := Do(t.Context(), func(_ context.Context) (string, error) {
			calls++
			return "", errors.New("permanent failure")
		}, WithMaxAttempts(3), WithBaseDelay(time.Millisecond), WithLabel("test"))
		if err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Fatalf("expected 1 call, non-transient not retried, got %d", calls)
		}
	})

	t.Run("context cancellation stops retry", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		_, err := Do(ctx, func(_ context.Context) (string, error) {
			calls++
			if calls == 1 {
				cancel()
			}
			return "", &StatusError{Code: 503}
		}, WithMaxAttempts(5), WithBaseDelay(time.Millisecond), WithLabel("test"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("rate limit not retried by default", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, err := Do(t.Context(), func(_ context.Context) (string, error) {
			calls++
			return "", &StatusError{Code: http.StatusTooManyRequests, RetryAfter: time.Millisecond}
		}, WithMaxAttempts(3), WithBaseDelay(time.Millisecond))
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("expected a 429, got %v", err)
		}
		if calls != 1 {
			t.Fatalf("expected 1 call (rate limits non-retryable by default), got %d", calls)
		}
	})
}

// --- Do: WithRateLimitRetry (additive mode) ---

func TestDo_WithRateLimitRetry(t *testing.T) {
	t.Parallel()

	t.Run("retries rate limits alongside transients", func(t *testing.T) {
		t.Parallel()
		calls := 0
		result, err := Do(t.Context(), func(_ context.Context) (string, error) {
			calls++
			switch calls {
			case 1:
				return "", &StatusError{Code: http.StatusTooManyRequests, RetryAfter: time.Millisecond}
			case 2:
				return "", &StatusError{Code: 503} // transient, jittered backoff
			}
			return "ok", nil
		}, WithMaxAttempts(4), WithBaseDelay(time.Millisecond), WithRateLimitRetry(time.Second))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "ok" {
			t.Fatalf("result = %q, want ok", result)
		}
		if calls != 3 {
			t.Fatalf("expected 3 calls (RL retry + transient retry + success), got %d", calls)
		}
	})

	t.Run("non-transient still returns immediately", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, err := Do(t.Context(), func(_ context.Context) (string, error) {
			calls++
			return "", errors.New("permanent")
		}, WithMaxAttempts(3), WithRateLimitRetry(time.Second))
		if err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
	})

	t.Run("hint capped at maxWait", func(t *testing.T) {
		t.Parallel()
		// A 10s hint under a 10ms maxWait must wait only ~10ms; completing
		// well inside the 5s deadline proves the cap applied.
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		calls := 0
		_, err := Do(ctx, func(_ context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", &StatusError{Code: http.StatusTooManyRequests, RetryAfter: 10 * time.Second}
			}
			return "ok", nil
		}, WithMaxAttempts(2), WithRateLimitRetry(10*time.Millisecond))
		if err != nil {
			t.Fatalf("unexpected error: %v (hint must be capped at maxWait)", err)
		}
		if calls != 2 {
			t.Fatalf("expected 2 calls, got %d", calls)
		}
	})

	t.Run("ctx cancellation wins over final rate-limit error", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Do(ctx, func(_ context.Context) (string, error) {
			return "", &StatusError{Code: http.StatusTooManyRequests}
		}, WithMaxAttempts(1), WithRateLimitRetry(time.Second))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Do(WithRateLimitRetry, dead ctx) = %v, want context.Canceled (default-mode ctx contract)", err)
		}
	})
}

// TestDo_rate_limit_modes_last_wins pins C16 for the two rate-limit options:
// they set one mode, and the later option decides it. A 503 is transient, so
// it is retried under WithRateLimitRetry and returned at once under
// WithRateLimitOnly.
func TestDo_rate_limit_modes_last_wins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		opts      []DoOption
		wantCalls int
	}{
		{"retry then only", []DoOption{WithRateLimitRetry(time.Millisecond), WithRateLimitOnly(time.Millisecond)}, 1},
		{"only then retry", []DoOption{WithRateLimitOnly(time.Millisecond), WithRateLimitRetry(time.Millisecond)}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			opts := append([]DoOption{WithMaxAttempts(3), WithBaseDelay(time.Millisecond)}, tc.opts...)
			_, err := Do(t.Context(), func(_ context.Context) (string, error) {
				calls++
				return "", &StatusError{Code: http.StatusServiceUnavailable}
			}, opts...)
			if err == nil {
				t.Fatal("Do = nil error, want the 503")
			}
			if calls != tc.wantCalls {
				t.Errorf("fn calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// TestDo_permanent_rate_limit_not_retried pins that Permanent stops a 429 under
// both rate-limit modes: the mark is checked before the 429 branch.
func TestDo_permanent_rate_limit_not_retried(t *testing.T) {
	t.Parallel()
	for name, opt := range map[string]DoOption{
		"WithRateLimitRetry": WithRateLimitRetry(time.Millisecond),
		"WithRateLimitOnly":  WithRateLimitOnly(time.Millisecond),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			_, err := Do(t.Context(), func(_ context.Context) (string, error) {
				calls++
				return "", Permanent(&StatusError{Code: http.StatusTooManyRequests})
			}, WithMaxAttempts(3), WithBaseDelay(time.Millisecond), opt)
			if !IsPermanent(err) {
				t.Fatalf("Do = %v, want the Permanent error", err)
			}
			if calls != 1 {
				t.Errorf("fn calls = %d, want 1", calls)
			}
		})
	}
}

func TestStatusError_preserves_wire_format(t *testing.T) {
	t.Parallel()
	got := (&StatusError{Code: 503}).Error()
	if got != "HTTP 503" {
		t.Errorf("StatusError{503}.Error() = %q, want %q", got, "HTTP 503")
	}
}

func TestResponseTooLargeError_Error_message(t *testing.T) {
	t.Parallel()
	got := (&ResponseTooLargeError{Limit: 1024}).Error()
	want := "response body exceeds 1024 bytes"
	if got != want {
		t.Errorf("ResponseTooLargeError{Limit:1024}.Error() = %q, want %q", got, want)
	}
}

func TestPermanent(t *testing.T) {
	t.Parallel()

	t.Run("wraps and unwraps", func(t *testing.T) {
		t.Parallel()
		inner := errors.New("bad config")
		pe := Permanent(inner)
		if pe == nil {
			t.Fatal("Permanent(non-nil) returned nil")
		}
		if pe.Error() != "bad config" {
			t.Errorf("Error() = %q, want %q", pe.Error(), "bad config")
		}
		if !errors.Is(pe, inner) {
			t.Error("errors.Is(pe, inner) = false")
		}
		if !IsPermanent(pe) {
			t.Error("IsPermanent(Permanent(inner)) = false")
		}
		if errors.Is(Permanent(errors.New("a")), Permanent(errors.New("b"))) {
			t.Error("errors.Is matched two unrelated Permanent marks")
		}
	})

	t.Run("nil returns nil", func(t *testing.T) {
		t.Parallel()
		if Permanent(nil) != nil {
			t.Error("Permanent(nil) should return nil")
		}
	})

	t.Run("IsPermanent", func(t *testing.T) {
		t.Parallel()
		if IsPermanent(errors.New("normal")) {
			t.Error("normal error should not be permanent")
		}
		if !IsPermanent(Permanent(errors.New("x"))) {
			t.Error("Permanent error should be permanent")
		}
	})

	t.Run("IsTransient returns false for a Permanent mark", func(t *testing.T) {
		t.Parallel()
		pe := Permanent(&StatusError{Code: 503})
		if IsTransient(pe) {
			t.Error("a Permanent mark over a transient error is not transient")
		}
	})

	t.Run("Do does not retry a Permanent mark", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, err := Do(t.Context(), func(_ context.Context) (string, error) {
			calls++
			return "", Permanent(errors.New("stop"))
		}, WithMaxAttempts(5), WithBaseDelay(time.Millisecond), WithLabel("test"))
		if err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (a Permanent mark is not retried)", calls)
		}
	})
}

// TestMarkTransient covers the mirror of Permanent: the mark satisfies the
// Transient interface with a true verdict, keeps the cause visible to
// errors.Is/errors.As, overrides a non-transient verdict already carried by the
// wrapped error, and yields to every rejection IsTransient makes on a question
// the caller is not the authority on.
func TestMarkTransient(t *testing.T) {
	t.Parallel()

	t.Run("wraps and unwraps", func(t *testing.T) {
		t.Parallel()
		inner := errors.New("upstream hiccup")
		te := MarkTransient(inner)
		if te == nil {
			t.Fatal("MarkTransient(non-nil) returned nil")
		}
		if te.Error() != "upstream hiccup" {
			t.Errorf("Error() = %q, want %q", te.Error(), "upstream hiccup")
		}
		if !errors.Is(te, inner) {
			t.Error("errors.Is(te, inner) = false, want true (the cause must stay visible)")
		}
	})

	t.Run("nil returns nil", func(t *testing.T) {
		t.Parallel()
		if MarkTransient(nil) != nil {
			t.Error("MarkTransient(nil) should return nil")
		}
	})

	t.Run("satisfies the Transient interface", func(t *testing.T) {
		t.Parallel()
		var target Transient
		if !errors.As(MarkTransient(errors.New("x")), &target) {
			t.Fatal("errors.As(Transient) = false, want true")
		}
		if !target.IsTransient() {
			t.Error("IsTransient() = false, want true")
		}
	})

	t.Run("IsTransient reports true, including through a caller wrap", func(t *testing.T) {
		t.Parallel()
		if !IsTransient(MarkTransient(errors.New("plain"))) {
			t.Error("IsTransient(MarkTransient(plain)) = false, want true")
		}
		wrapped := fmt.Errorf("fetching titles: %w", MarkTransient(errors.New("plain")))
		if !IsTransient(wrapped) {
			t.Error("IsTransient(wrapped mark) = false, want true")
		}
	})

	t.Run("overrides a non-transient verdict on the wrapped error", func(t *testing.T) {
		t.Parallel()
		// The mark is the outermost verdict, so the caller's knowledge that
		// this upstream's 409 self-heals wins over the status rule.
		plain409 := &StatusError{Code: 409}
		if IsTransient(plain409) {
			t.Fatal("setup: IsTransient(HTTP 409) = true, want false")
		}
		if !IsTransient(MarkTransient(plain409)) {
			t.Error("IsTransient(MarkTransient(HTTP 409)) = false, want true")
		}
		var hse *StatusError
		if !errors.As(MarkTransient(plain409), &hse) || hse.Code != 409 {
			t.Error("errors.As(*StatusError) through the mark failed, want the wrapped status error")
		}
	})

	t.Run("IsTransient rejections outrank the mark", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			err  error
		}{
			{"permanent wins", Permanent(MarkTransient(errors.New("stop")))},
			{"401 stays terminal", MarkTransient(&StatusError{Code: http.StatusUnauthorized})},
			{"403 stays terminal", MarkTransient(&StatusError{Code: http.StatusForbidden})},
			{"429 stays terminal", MarkTransient(&StatusError{Code: http.StatusTooManyRequests})},
			{"caller deadline stays terminal", MarkTransient(context.DeadlineExceeded)},
			{"caller cancellation stays terminal", MarkTransient(context.Canceled)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if IsTransient(tc.err) {
					t.Errorf("IsTransient(%v) = true, want false", tc.err)
				}
			})
		}
	})

	t.Run("Do retries a marked error", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, err := Do(t.Context(), func(_ context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", MarkTransient(&StatusError{Code: 409})
			}
			return "ok", nil
		}, WithMaxAttempts(3), WithBaseDelay(time.Millisecond), WithLabel("test"))
		if err != nil {
			t.Fatalf("Do = %v, want nil", err)
		}
		if calls != 2 {
			t.Errorf("calls = %d, want 2 (a marked error is retried)", calls)
		}
	})
}

func TestDo_context_deadline_during_backoff_sleep(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	calls := 0
	_, err := Do(ctx, func(_ context.Context) (string, error) {
		calls++
		return "", &StatusError{Code: 503}
	}, WithMaxAttempts(3), WithBaseDelay(10*time.Second), WithLabel("test"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do = %v, want context.DeadlineExceeded", err)
	}
	if calls != 1 {
		t.Errorf("fn calls = %d, want 1 (deadline during first backoff sleep)", calls)
	}
}

// --- StatusError classification ---

func TestStatusError_classification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code      int
		transient bool
	}{
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{408, true},
		{429, false},
		{499, false},
		{500, true},
		{501, false},
		{502, true},
		{503, true},
		{504, true},
		{505, false},
		{599, true},
		{600, false},
	}
	for _, tt := range tests {
		if got := (&StatusError{Code: tt.code}).IsTransient(); got != tt.transient {
			t.Errorf("StatusError{%d}.IsTransient() = %v, want %v", tt.code, got, tt.transient)
		}
	}
}

// --- Backoff primitives: concrete edge cases (property coverage in prop_test.go) ---

func TestSafeDouble_concrete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero", 0, 0},
		{"negative passthrough", -3 * time.Second, -3 * time.Second},
		{"doubles", 2 * time.Second, 4 * time.Second},
		{"no overflow near top", 1 << 61, 1 << 62},
		{"overflow caps at max", time.Duration(1<<63 - 1), time.Duration(1<<63 - 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SafeDouble(tt.in); got != tt.want {
				t.Errorf("SafeDouble(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestJitteredBackoff_zero_and_negative_passthrough(t *testing.T) {
	t.Parallel()
	if d := JitteredBackoff(0); d != 0 {
		t.Errorf("JitteredBackoff(0) = %v, want 0", d)
	}
	if d := JitteredBackoff(-time.Second); d != -time.Second {
		t.Errorf("JitteredBackoff(-1s) = %v, want -1s", d)
	}
}

// TestSleepCtx_nonpositive_returns_nil_without_consulting_context pins that a
// non-positive duration short-circuits to nil immediately, never consulting the
// context: the assertion holds even with an already-cancelled context.
func TestSleepCtx_nonpositive_returns_nil_without_consulting_context(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 200 {
		if got := SleepCtx(ctx, 0); got != nil {
			t.Fatalf("SleepCtx(cancelled ctx, 0) = %v, want nil", got)
		}
	}
	if got := SleepCtx(ctx, -time.Second); got != nil {
		t.Fatalf("SleepCtx(cancelled ctx, -1s) = %v, want nil", got)
	}
}

func TestSleepCtx_negative_returns_immediately(t *testing.T) {
	t.Parallel()
	start := time.Now()
	if err := SleepCtx(t.Context(), -time.Second); err != nil {
		t.Errorf("SleepCtx(-1s) = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("SleepCtx(-1s) took %v, want immediate return", elapsed)
	}
}

func TestSleepCtx_cancelled_context_no_goroutine_leak(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 100 {
		_ = SleepCtx(ctx, time.Hour)
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	if after := runtime.NumGoroutine(); after-before > 5 {
		t.Errorf("goroutine leak: before=%d, after=%d", before, after)
	}
}

// --- Do rate-limit-only mode: attempt clamp and Retry-After vs maxWait boundary ---

func TestDoRateLimitOnly_zero_attempts_clamps_to_one(t *testing.T) {
	t.Parallel()
	calls := 0
	sentinel := &StatusError{Code: http.StatusTooManyRequests}
	err := doRateLimited(t.Context(), 0, time.Second, func(_ context.Context) error {
		calls++
		return sentinel
	})
	// maxAttempts<1 clamps to 1: fn runs exactly once and its error is returned.
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("Do(rate-limit-only, maxAttempts=0) = %v, want the single attempt's 429", err)
	}
	if calls != 1 {
		t.Errorf("fn calls = %d, want 1 (maxAttempts<1 clamps to 1)", calls)
	}
}

func TestDoRateLimitOnly_no_sleep_after_final_attempt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// With one attempt the loop must break before sleeping; a mutated break guard
	// would fall through to SleepCtx and return the cancelled-context error.
	// (Under WithRateLimitOnly the final attempt's error wins even on a dead
	// context — the v2 RetryOnRateLimit contract.)
	err := doRateLimited(ctx, 1, time.Minute, func(_ context.Context) error {
		return &StatusError{Code: http.StatusTooManyRequests}
	})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("Do(rate-limit-only, maxAttempts=1) = %v, want a 429 (no trailing sleep)", err)
	}
}

func TestDoRateLimitOnly_zero_retry_after_uses_max_wait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// RetryAfter==0 must NOT override the (large) max wait, so SleepCtx waits an
	// hour against the cancelled context and returns its error.
	err := doRateLimited(ctx, 2, time.Hour, func(_ context.Context) error {
		return &StatusError{Code: http.StatusTooManyRequests, RetryAfter: 0}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Do(rate-limit-only, RetryAfter=0) = %v, want context.Canceled (max wait honored)", err)
	}
}

func TestDoRateLimitOnly_positive_retry_after_caps_below_max_wait(t *testing.T) {
	t.Parallel()
	// Sized against the 10s max wait this must prove was capped, not against the
	// 10ms the capped wait actually takes. Same fragility as the two tests fixed
	// alongside this one: a budget tuned to expected latency fails under load
	// while the code is correct.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	// A positive RetryAfter must shrink the wait to 10ms so the retry completes
	// well within the deadline and returns the rate-limit error.
	err := doRateLimited(ctx, 2, 10*time.Second, func(_ context.Context) error {
		return &StatusError{Code: http.StatusTooManyRequests, RetryAfter: 10 * time.Millisecond}
	})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("Do(rate-limit-only, RetryAfter=10ms) = %v, want a 429 (RetryAfter honored)", err)
	}
}

// TestDoRateLimitOnly_non_positive_max_wait_clamps_to_cap pins the maxWait<=0
// fallback to RetryAfterCap: the inter-attempt wait must stay positive (here
// the clamped 60s against an expiring context), so the loop parks in SleepCtx
// and surfaces the context error. Before the clamp existed, maxWait=0 zeroed
// the wait, SleepCtx returned nil without checking ctx, and the loop hot-spun
// through every attempt in microseconds returning the rate-limit error instead.
func TestDoRateLimitOnly_non_positive_max_wait_clamps_to_cap(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	calls := 0
	err := doRateLimited(ctx, 3, 0, func(_ context.Context) error {
		calls++
		return &StatusError{Code: http.StatusTooManyRequests}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Do(rate-limit-only, maxWait=0) = %v, want context.DeadlineExceeded (clamped wait parked in SleepCtx)", err)
	}
	if calls != 1 {
		t.Errorf("fn calls = %d, want 1 (no hot-spin through attempts)", calls)
	}
}

// --- Do: zero base delay coercion and structured-log content ---

func TestDo_zero_base_delay_defaults_to_base(t *testing.T) {
	t.Parallel()
	// A zero base delay must be coerced to defaultBaseDelay (1s), so the pre-retry
	// sleep blows the 100ms deadline and surfaces the context error.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := Do(ctx, func(_ context.Context) (string, error) {
		return "", &StatusError{Code: 503}
	}, WithMaxAttempts(2), WithBaseDelay(0), WithLabel("test"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Do(baseDelay=0) = %v, want context.DeadlineExceeded (delay defaulted)", err)
	}
}

// These swap slog.Default, so they must NOT run in parallel.

func TestDo_first_try_success_omits_succeeded_log(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	_, err := Do(t.Context(),
		func(_ context.Context) (string, error) { return "ok", nil },
		WithMaxAttempts(3), WithBaseDelay(time.Microsecond), WithLabel("lbl"))
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	if strings.Contains(buf.String(), "succeeded after retry") {
		t.Errorf("first-try success logged a retry-success line:\n%s", buf.String())
	}
}

func TestDo_success_after_one_retry_logs_attempt_counts(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	calls := 0
	_, err := Do(t.Context(),
		func(_ context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, &StatusError{Code: 503}
			}
			return 42, nil
		},
		WithMaxAttempts(3), WithBaseDelay(time.Microsecond), WithLabel("lbl"))
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "attempt=1") {
		t.Errorf("retry debug log = %q, want attribute attempt=1", logged)
	}
	if !strings.Contains(logged, "succeeded after retry") {
		t.Errorf("success-after-retry not logged:\n%s", logged)
	}
	if !strings.Contains(logged, "attempts=2") {
		t.Errorf("success log = %q, want attribute attempts=2", logged)
	}
}

func TestDo_no_retry_log_after_final_attempt(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	calls := 0
	_, err := Do(t.Context(),
		func(_ context.Context) (string, error) {
			calls++
			return "", &StatusError{Code: 503}
		},
		WithMaxAttempts(2), WithBaseDelay(time.Microsecond), WithLabel("lbl"))
	if err == nil {
		t.Fatal("Do = nil, want error after exhaustion")
	}
	if calls != 2 {
		t.Fatalf("fn calls = %d, want 2", calls)
	}
	// Exactly one "failed, retrying" line: the break before the final attempt
	// suppresses the second.
	if got := strings.Count(buf.String(), "failed, retrying"); got != 1 {
		t.Errorf("retry-log count = %d, want 1\n%s", got, buf.String())
	}
}

// TestDo_success_wins_over_context_ended_during_attempt pins that only a
// failed attempt yields to a canceled context: a success returns its value.
func TestDo_success_wins_over_context_ended_during_attempt(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	got, err := Do(ctx, func(context.Context) (string, error) {
		cancel()
		return "ok", nil
	}, WithBaseDelay(time.Microsecond))
	if err != nil || got != "ok" {
		t.Errorf("Do(success, context canceled during the attempt) = %q, %v, want \"ok\", nil", got, err)
	}
}

// TestDo_permanent_error_on_final_attempt_is_not_exhaustion pins that a
// non-retryable error on the last attempt returns as itself with no
// exhaustion line: the budget was not what stopped the loop.
func TestDo_permanent_error_on_final_attempt_is_not_exhaustion(t *testing.T) {
	var buf bytes.Buffer
	permanent := errors.New("permanent")
	calls := 0
	_, err := Do(t.Context(),
		func(_ context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, MarkTransient(errors.New("flaky"))
			}
			return 0, permanent
		},
		WithMaxAttempts(2), WithBaseDelay(time.Microsecond), WithLogger(bufLogger(&buf)))
	if !errors.Is(err, permanent) {
		t.Errorf("Do(transient then permanent) err = %v, want %v", err, permanent)
	}
	if calls != 2 {
		t.Errorf("fn calls = %d, want 2", calls)
	}
	if strings.Contains(buf.String(), "retries exhausted") {
		t.Errorf("log = %q, want no exhaustion line", buf.String())
	}
}

// TestDo_WithLogger_routes_all_lines pins the NEW v3 capability: a per-call
// logger receives the loop's Debug and Warn lines, and slog.Default() stays
// silent. Swaps slog.Default to prove the negative, so not parallel.
func TestDo_WithLogger_routes_all_lines(t *testing.T) {
	var defBuf, callBuf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&defBuf))

	_, err := Do(t.Context(),
		func(_ context.Context) (string, error) {
			return "", &StatusError{Code: 503}
		},
		WithMaxAttempts(2), WithBaseDelay(time.Microsecond), WithLabel("lbl"), WithLogger(bufLogger(&callBuf)))
	if err == nil {
		t.Fatal("Do = nil, want exhaustion error")
	}
	if !strings.Contains(callBuf.String(), "failed, retrying") || !strings.Contains(callBuf.String(), "retries exhausted") {
		t.Errorf("per-call logger missing loop lines:\n%s", callBuf.String())
	}
	if defBuf.Len() != 0 {
		t.Errorf("slog.Default() received lines despite WithLogger:\n%s", defBuf.String())
	}
}

// TestDoRateLimitOnly_retry_debug_log_reports_one_indexed_attempt pins the
// per-attempt Debug line's attempt field to the human (1-indexed) value: the
// first retry logs attempt=1, not the 0-indexed loop counter. Swaps
// slog.Default, so it must not run in parallel.
func TestDoRateLimitOnly_retry_debug_log_reports_one_indexed_attempt(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	calls := 0
	err := doRateLimited(t.Context(), 3, time.Millisecond, func(_ context.Context) error {
		calls++
		if calls == 1 {
			return &StatusError{Code: http.StatusTooManyRequests}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do(rate-limit-only) = %v, want nil", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "failed, retrying") {
		t.Fatalf("expected per-attempt debug log, got:\n%s", logged)
	}
	// The first retry logs the human (1-indexed) attempt number, attempt+1 == 1.
	if !strings.Contains(logged, "attempt=1") {
		t.Errorf("retry debug log = %q, want attribute attempt=1", logged)
	}
}

// TestDoRateLimitOnly_zero_max_wait_still_honors_hint pins that the maxWait
// clamp keeps hint capping intact: with maxWait=0 (clamped to RetryAfterCap) a
// 5ms RetryAfter hint is honored as min(hint, cap) = 5ms. Without the clamp
// the same call computed min(5ms, 0) = 0 and logged delay=0s. Swaps
// slog.Default, so it must not run in parallel.
func TestDoRateLimitOnly_zero_max_wait_still_honors_hint(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	calls := 0
	err := doRateLimited(t.Context(), 2, 0, func(_ context.Context) error {
		calls++
		if calls == 1 {
			return &StatusError{Code: http.StatusTooManyRequests, RetryAfter: 5 * time.Millisecond}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do(rate-limit-only) = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("fn calls = %d, want 2", calls)
	}
	if !strings.Contains(buf.String(), "delay=5ms") {
		t.Errorf("retry debug log = %q, want delay=5ms (hint honored under the clamped cap)", buf.String())
	}
}

// TestDoRateLimitOnly_exhaustion_warn_message pins the terminal Warn text the
// v2 helper emitted ("rate limit retries exhausted"), preserved by the mode.
func TestDoRateLimitOnly_exhaustion_warn_message(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	err := doRateLimited(t.Context(), 2, time.Millisecond, func(_ context.Context) error {
		return &StatusError{Code: http.StatusTooManyRequests, RetryAfter: time.Millisecond}
	})
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if !strings.Contains(buf.String(), "rate limit retries exhausted") {
		t.Errorf("terminal warn = %q, want 'rate limit retries exhausted'", buf.String())
	}
}

func TestLogSlowUpstream_warns_and_redacts_on_slow_response(t *testing.T) {
	t.Parallel()
	// The >10s branch (Warn + redactURL) is otherwise unexercised; only the
	// fast-path negative is tested. A send 11s in the past forces it.
	var buf bytes.Buffer
	logSlowUpstream(bufLogger(&buf), "https://h.example/api?apikey=supersecret", time.Now().Add(-11*time.Second))
	logged := buf.String()
	if !strings.Contains(logged, "slow upstream response") {
		t.Errorf("logSlowUpstream(11s ago) logged %q, want the slow-upstream Warn", logged)
	}
	if strings.Contains(logged, "supersecret") {
		t.Errorf("slow-upstream log leaked the query secret:\n%s", logged)
	}
	if !strings.Contains(logged, "apikey=REDACTED") {
		t.Errorf("slow-upstream log did not redact the query value:\n%s", logged)
	}
}

// TestDo_WithExhaustedLevel_demotes_the_terminal_line pins the option for the
// caller that publishes its own terminal verdict: the per-attempt retry
// diagnostics stay, and only the exhaustion verdict moves off Warn. Both halves
// matter, because the alternative a caller reaches for otherwise is silencing the
// whole logger, which throws the diagnostics away to stop the duplicate line.
func TestDo_WithExhaustedLevel_demotes_the_terminal_line(t *testing.T) {
	var buf bytes.Buffer
	_, err := Do(t.Context(),
		func(_ context.Context) (string, error) {
			return "", &StatusError{Code: 503}
		},
		WithMaxAttempts(3), WithBaseDelay(time.Microsecond), WithLabel("lbl"),
		WithLogger(bufLogger(&buf)), WithExhaustedLevel(slog.LevelDebug))
	if err == nil {
		t.Fatal("Do = nil, want exhaustion error")
	}
	out := buf.String()
	if !strings.Contains(out, "failed, retrying") {
		t.Errorf("per-attempt retry diagnostics lost:\n%s", out)
	}
	if !strings.Contains(out, "lbl retries exhausted") {
		t.Errorf("terminal line missing entirely; it should be demoted, not suppressed:\n%s", out)
	}
	if strings.Contains(out, "level=WARN") {
		t.Errorf("terminal line still at WARN despite WithExhaustedLevel(Debug):\n%s", out)
	}
}

// TestDo_WithExhaustedLevel_overrides_both_default_rules pins that the option
// beats BOTH halves of the default rule, in both directions: a multi-attempt
// budget can be demoted below Warn, and a single-attempt budget (which defaults
// to Debug so a one-attempt door does not double-report) can be raised to Warn
// by a caller that wants the verdict there.
func TestDo_WithExhaustedLevel_overrides_both_default_rules(t *testing.T) {
	fail := func(_ context.Context) (string, error) { return "", &StatusError{Code: 503} }

	var multi bytes.Buffer
	if _, err := Do(t.Context(), fail,
		WithMaxAttempts(2), WithBaseDelay(time.Microsecond), WithLogger(bufLogger(&multi)),
		WithExhaustedLevel(slog.LevelInfo)); err == nil {
		t.Fatal("Do = nil, want exhaustion error")
	}
	if strings.Contains(multi.String(), "level=WARN") {
		t.Errorf("multi-attempt terminal line stayed WARN:\n%s", multi.String())
	}

	var single bytes.Buffer
	if _, err := Do(t.Context(), fail,
		WithMaxAttempts(1), WithLogger(bufLogger(&single)),
		WithExhaustedLevel(slog.LevelWarn)); err == nil {
		t.Fatal("Do = nil, want exhaustion error")
	}
	if !strings.Contains(single.String(), "level=WARN") {
		t.Errorf("single-attempt terminal line was not raised to WARN:\n%s", single.String())
	}
}
