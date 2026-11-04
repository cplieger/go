package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// discardLogger keeps the retry diagnostics these tests provoke out of the
// test output; what is asserted is the attempt count and the returned error.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// blockUntilDone is the callback shape a real request has when its context
// expires mid-flight: it returns an error WRAPPING the context error, the way
// net/http wraps one in a *url.Error.
func blockUntilDone(attempts *int) func(context.Context) (struct{}, error) {
	return func(ctx context.Context) (struct{}, error) {
		*attempts++
		<-ctx.Done()
		return struct{}{}, fmt.Errorf("upstream stalled: %w", ctx.Err())
	}
}

// TestWithAttemptTimeout_retries_the_attempt_bound is the reason the option
// exists: a deadline that bounded ONE attempt is a retryable failure. Before
// the mark, IsTransient rejected it as a context error (a caller out of
// budget) and Do stopped after the first attempt, so the per-attempt-timeout
// pattern could not be expressed at all — a wrapper implementing Transient did
// not help, because the rejection was reached first.
func TestWithAttemptTimeout_retries_the_attempt_bound(t *testing.T) {
	t.Parallel()
	attempts := 0
	_, err := httpclient.Do(t.Context(), blockUntilDone(&attempts),
		httpclient.WithAttemptTimeout(20*time.Millisecond),
		httpclient.WithMaxAttempts(3),
		httpclient.WithBaseDelay(time.Millisecond),
		httpclient.WithLogger(discardLogger()))
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (the attempt bound must be retried)", attempts)
	}
	if err == nil {
		t.Fatal("Do = nil error, want the exhausted attempt timeout")
	}
	if !httpclient.IsAttemptTimeout(err) {
		t.Errorf("IsAttemptTimeout(%v) = false, want true", err)
	}
	if !httpclient.IsTransient(err) {
		t.Errorf("IsTransient(%v) = false, want true", err)
	}
	// The whole point of marking instead of replacing: the caller's own callers
	// can still recognize a deadline, which a hand-rolled wrapper had to hide
	// (omitting Unwrap) to escape the rejection.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(%v, context.DeadlineExceeded) = false, want true", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() == "" {
		t.Errorf("marked error lost its cause: %q", err)
	}
}

// TestWithAttemptTimeout_caller_deadline_stays_terminal is the safety property:
// once the CALLER is out of budget, nothing is retried. The attempt bound here
// is far larger than the caller's deadline, so the deadline that fires is the
// caller's — inherited by the attempt context, indistinguishable from the
// attempt's own by the error value alone.
func TestWithAttemptTimeout_caller_deadline_stays_terminal(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	attempts := 0
	_, err := httpclient.Do(ctx, blockUntilDone(&attempts),
		httpclient.WithAttemptTimeout(time.Hour),
		httpclient.WithMaxAttempts(3),
		httpclient.WithBaseDelay(time.Millisecond),
		httpclient.WithLogger(discardLogger()))
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (a caller out of budget must not be retried)", attempts)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Do = %v, want context.DeadlineExceeded", err)
	}
	if httpclient.IsAttemptTimeout(err) {
		t.Errorf("IsAttemptTimeout(%v) = true: the caller's deadline was marked as an attempt's", err)
	}
}

// TestWithAttemptTimeout_caller_cancel_stays_terminal is the same property for
// explicit cancellation, which the attempt context also inherits.
func TestWithAttemptTimeout_caller_cancel_stays_terminal(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	attempts := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := httpclient.Do(ctx, blockUntilDone(&attempts),
		httpclient.WithAttemptTimeout(time.Hour),
		httpclient.WithMaxAttempts(3),
		httpclient.WithBaseDelay(time.Millisecond),
		httpclient.WithLogger(discardLogger()))
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (a canceled caller must not be retried)", attempts)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Do = %v, want context.Canceled", err)
	}
	if httpclient.IsAttemptTimeout(err) {
		t.Errorf("IsAttemptTimeout(%v) = true: a cancellation was marked as an attempt timeout", err)
	}
}

// TestWithAttemptTimeout_never_extends_the_caller_budget pins the bound as a
// per-attempt cap and nothing more: context keeps the earlier deadline, so a
// caller deadline nearer than d still governs the attempt.
func TestWithAttemptTimeout_never_extends_the_caller_budget(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	want, _ := ctx.Deadline()
	_, err := httpclient.Do(ctx, func(attemptCtx context.Context) (struct{}, error) {
		got, ok := attemptCtx.Deadline()
		if !ok || !got.Equal(want) {
			t.Errorf("attempt deadline = %v (ok=%v), want the caller's %v", got, ok, want)
		}
		return struct{}{}, nil
	}, httpclient.WithAttemptTimeout(time.Hour), httpclient.WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
}

// TestWithAttemptTimeout_bounds_each_attempt_independently: the bound is
// per-attempt, so every attempt gets the full d rather than sharing one
// deadline across the sequence.
func TestWithAttemptTimeout_bounds_each_attempt_independently(t *testing.T) {
	t.Parallel()
	var deadlines []time.Time
	attempts := 0
	_, _ = httpclient.Do(t.Context(), func(attemptCtx context.Context) (struct{}, error) {
		attempts++
		dl, ok := attemptCtx.Deadline()
		if !ok {
			t.Error("attempt context carried no deadline")
		}
		deadlines = append(deadlines, dl)
		return struct{}{}, &httpclient.StatusError{Code: 503}
	}, httpclient.WithAttemptTimeout(time.Minute),
		httpclient.WithMaxAttempts(2),
		httpclient.WithBaseDelay(2*time.Millisecond),
		httpclient.WithLogger(discardLogger()))
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if !deadlines[1].After(deadlines[0]) {
		t.Errorf("second attempt deadline %v not after the first %v: the bound is not per-attempt",
			deadlines[1], deadlines[0])
	}
}

// TestWithAttemptTimeout_non_positive_is_no_bound: the option's absence and a
// non-positive duration behave identically — fn sees the caller's context.
func TestWithAttemptTimeout_non_positive_is_no_bound(t *testing.T) {
	t.Parallel()
	for _, d := range []time.Duration{0, -time.Second} {
		_, err := httpclient.Do(t.Context(), func(attemptCtx context.Context) (struct{}, error) {
			if _, ok := attemptCtx.Deadline(); ok {
				t.Errorf("d=%v: attempt context carries a deadline, want none", d)
			}
			return struct{}{}, nil
		}, httpclient.WithAttemptTimeout(d), httpclient.WithLogger(discardLogger()))
		if err != nil {
			t.Errorf("d=%v: Do = %v, want nil", d, err)
		}
	}
}

// TestWithAttemptTimeout_over_a_real_request exercises the shape the option
// exists for: a per-attempt bound around a real client call whose upstream
// stalls. net/http reports the expiry as a *url.Error wrapping the context
// error, which is what the mark has to survive.
func TestWithAttemptTimeout_over_a_real_request(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	attempts := 0
	client := srv.Client()
	_, err := httpclient.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
		attempts++
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
		if reqErr != nil {
			return struct{}{}, reqErr
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			// The reduction a consumer applies to keep the URL out of logs
			// must not defeat the classification.
			return struct{}{}, httpclient.LogSafeError(doErr)
		}
		defer resp.Body.Close()
		return struct{}{}, nil
	}, httpclient.WithAttemptTimeout(30*time.Millisecond),
		httpclient.WithMaxAttempts(2),
		httpclient.WithBaseDelay(time.Millisecond),
		httpclient.WithLogger(discardLogger()))
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if !httpclient.IsAttemptTimeout(err) {
		t.Errorf("IsAttemptTimeout(%v) = false, want true", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(%v, context.DeadlineExceeded) = false, want true", err)
	}
}

// TestAttemptTimeout pins the exported mark on its own: nil in, nil out; a
// marked error is transient, still matches the deadline it wraps, still
// unwraps to the cause a caller diagnoses from; and Permanent outranks it.
func TestAttemptTimeout(t *testing.T) {
	t.Parallel()

	if got := httpclient.AttemptTimeout(nil); got != nil {
		t.Errorf("AttemptTimeout(nil) = %v, want nil", got)
	}
	if httpclient.IsAttemptTimeout(nil) {
		t.Error("IsAttemptTimeout(nil) = true, want false")
	}

	cause := fmt.Errorf("dial tcp 10.0.0.1:443: %w", context.DeadlineExceeded)
	marked := httpclient.AttemptTimeout(cause)
	if !httpclient.IsAttemptTimeout(marked) {
		t.Error("IsAttemptTimeout(marked) = false, want true")
	}
	if !httpclient.IsTransient(marked) {
		t.Error("IsTransient(marked) = false, want true")
	}
	if !errors.Is(marked, context.DeadlineExceeded) {
		t.Error("marked error no longer matches context.DeadlineExceeded")
	}
	if !errors.Is(marked, cause) {
		t.Error("marked error lost its cause")
	}
	// The phase that stalled is the diagnostic value of the cause; it must
	// still be readable.
	if !strings.Contains(marked.Error(), "dial tcp") {
		t.Errorf("marked error = %q, want the cause's text", marked.Error())
	}

	// Found through a caller's own wrapping.
	if !httpclient.IsAttemptTimeout(fmt.Errorf("delivering notification: %w", marked)) {
		t.Error("IsAttemptTimeout did not see the mark through a caller's wrapper")
	}

	// Permanent is checked first in IsTransient and keeps its absolute veto.
	if httpclient.IsTransient(httpclient.Permanent(marked)) {
		t.Error("IsTransient(Permanent(marked)) = true: Permanent must still win")
	}

	// A plain context deadline with no mark stays terminal.
	if httpclient.IsTransient(context.DeadlineExceeded) {
		t.Error("IsTransient(context.DeadlineExceeded) = true, want false")
	}
}

// TestWithAttemptTimeout_marks_only_the_attempts_own_deadline pins the three
// conditions of the mark through Do's returned error and attempt count: the
// caller still has budget, the attempt bound expired, and the error reports a
// deadline. WithRateLimitOnly returns the final attempt's error even under an
// ended caller context, which makes the first condition observable.
func TestWithAttemptTimeout_marks_only_the_attempts_own_deadline(t *testing.T) {
	t.Parallel()
	t.Run("caller_out_of_budget", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		attempts := 0
		_, err := httpclient.Do(ctx, blockUntilDone(&attempts),
			httpclient.WithAttemptTimeout(time.Hour),
			httpclient.WithRateLimitOnly(time.Millisecond),
			httpclient.WithLogger(discardLogger()))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Do = %v, want the attempt's context.DeadlineExceeded", err)
		}
		if httpclient.IsAttemptTimeout(err) {
			t.Errorf("IsAttemptTimeout(%v) = true: the caller's deadline was marked as an attempt's", err)
		}
	})
	t.Run("deadline_fn_holds_internally", func(t *testing.T) {
		t.Parallel()
		attempts := 0
		_, err := httpclient.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
			attempts++
			inner, cancel := context.WithTimeout(ctx, time.Millisecond)
			defer cancel()
			<-inner.Done()
			return struct{}{}, inner.Err()
		},
			httpclient.WithAttemptTimeout(time.Hour),
			httpclient.WithMaxAttempts(3),
			httpclient.WithBaseDelay(time.Millisecond),
			httpclient.WithLogger(discardLogger()))
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1 (a deadline fn holds itself stays terminal)", attempts)
		}
		if httpclient.IsAttemptTimeout(err) {
			t.Errorf("IsAttemptTimeout(%v) = true, want false", err)
		}
	})
	t.Run("translated_error_is_not_a_deadline", func(t *testing.T) {
		t.Parallel()
		attempts := 0
		_, err := httpclient.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
			attempts++
			<-ctx.Done()
			return struct{}{}, errors.New("upstream gave up")
		},
			httpclient.WithAttemptTimeout(time.Millisecond),
			httpclient.WithMaxAttempts(3),
			httpclient.WithBaseDelay(time.Millisecond),
			httpclient.WithLogger(discardLogger()))
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1 (an error that is not a deadline gets no mark)", attempts)
		}
		if httpclient.IsAttemptTimeout(err) {
			t.Errorf("IsAttemptTimeout(%v) = true, want false", err)
		}
	})
}
