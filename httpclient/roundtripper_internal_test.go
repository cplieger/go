package httpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// White-box tests for unexported retry helpers whose arms the public doors
// cannot reach deterministically (NewRetryRoundTripper always sets a
// transport).

// TestSchedule_run_surfaces_sleep_error pins that an interrupted backoff
// sleep stops the loop with the context error after the pause hook ran, and
// that the stop is not reported as exhaustion. The context is canceled before
// the call, so SleepCtx returns at once (no scheduler race).
func TestSchedule_run_surfaces_sleep_error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var attempts, pauses, settles int
	exhausted, err := schedule{attempts: 3, baseDelay: time.Second}.run(ctx,
		func(int) (bool, time.Duration) { attempts++; return false, 0 },
		func(retry int, _ time.Duration) {
			pauses++
			if retry != 1 {
				t.Errorf("pause retry = %d, want 1", retry)
			}
		},
		func(time.Duration) { settles++ })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run(canceled ctx) err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if exhausted {
		t.Error("run(canceled ctx) exhausted = true, want false")
	}
	if attempts != 1 || pauses != 1 || settles != 1 {
		t.Errorf("attempts, pauses, settles = %d, %d, %d, want 1, 1, 1", attempts, pauses, settles)
	}
}

// TestRetryRoundTripper_transport_defaults_when_next_nil pins the nil-transport
// fallback in transport(): a zero-value RetryRoundTripper (next == nil) routes
// through http.DefaultTransport.
func TestRetryRoundTripper_transport_defaults_when_next_nil(t *testing.T) {
	rt := &RetryRoundTripper{} // zero value: next == nil
	if got := rt.transport(); got != http.DefaultTransport {
		t.Errorf("(&RetryRoundTripper{}).transport() = %v, want http.DefaultTransport", got)
	}
}
