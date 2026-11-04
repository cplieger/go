package httpclient_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// === v3 contract: total-attempts semantics, hard body-overflow, bounded
// === elapsed time, TransportConfig zero-value, NewRetryClient wiring
//
// These tests pin the v3 contract directly (the older files exercise it
// incidentally). They reuse the package-level helper roundTripFunc
// (retry_test.go); nothing here is redeclared.

// TestV3_RoundTripper_MaxAttempts_ExactCounts locks the TransportConfig
// total-attempts model: MaxAttempts in {-1,0,1,2,3} drives EXACTLY {1,3,1,2,3}
// transport calls against an always-retryable upstream. Zero means unset and
// takes DefaultMaxAttempts (3); a negative value means exactly one attempt
// (v2 expressed try-once as WithRTMaxAttempts(0), but a zero struct field
// cannot distinguish absent from zero, so v3 moves try-once to negatives).
func TestV3_RoundTripper_MaxAttempts_ExactCounts(t *testing.T) {
	cases := []struct {
		maxAttempts int
		wantCalls   int32
	}{
		{-1, 1}, // negative: exactly one attempt
		{0, 3},  // zero: unset, takes DefaultMaxAttempts
		{1, 1},
		{2, 2},
		{3, 3},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("maxAttempts=%d", tc.maxAttempts), func(t *testing.T) {
			var calls atomic.Int32
			transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     http.Header{},
				}, nil
			})
			rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{
				BaseDelay:   time.Microsecond,
				MaxAttempts: tc.maxAttempts,
			})
			req, _ := http.NewRequest(http.MethodGet, "http://example.com/counts", http.NoBody)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip = %v, want nil (exhausted retries return the last response)", err)
			}
			resp.Body.Close()
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("TransportConfig{MaxAttempts: %d}: transport calls = %d, want %d", tc.maxAttempts, got, tc.wantCalls)
			}
		})
	}
}

// TestV3_Do_MaxAttempts_ExactCounts is the generic-door twin: WithMaxAttempts
// in {0,1,2,3} drives EXACTLY {1,1,2,3} fn invocations when fn always returns
// a transient error. Unlike the TransportConfig struct field, option absence
// is expressible, so WithMaxAttempts(0) keeps its v2 meaning of "exactly one
// attempt" (the below-1 clamp).
func TestV3_Do_MaxAttempts_ExactCounts(t *testing.T) {
	cases := []struct {
		maxAttempts int
		wantCalls   int32
	}{
		{0, 1}, // clamps to 1
		{1, 1},
		{2, 2},
		{3, 3},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("maxAttempts=%d", tc.maxAttempts), func(t *testing.T) {
			var calls atomic.Int32
			_, err := httpclient.Do(t.Context(),
				func(_ context.Context) (int, error) {
					calls.Add(1)
					return 0, &httpclient.HTTPStatusError{Code: 503} // transient -> retried up to the cap
				}, httpclient.WithMaxAttempts(tc.maxAttempts), httpclient.WithBaseDelay(time.Microsecond), httpclient.WithLabel("counts"))
			if err == nil {
				t.Fatal("Do = nil, want error after exhaustion")
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("Do(WithMaxAttempts(%d)): fn calls = %d, want %d", tc.maxAttempts, got, tc.wantCalls)
			}
		})
	}
}

// TestV3_GetBytes_ResponseTooLargeError verifies the hard body-overflow
// contract: a body over WithMaxBodyBytes yields a nil body and
// *ResponseTooLargeError whose Limit reports the cap (no silent truncation); a
// body exactly at the cap succeeds.
func TestV3_GetBytes_ResponseTooLargeError(t *testing.T) {
	const maxBytes = 16

	overTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, maxBytes+1))),
			Header:     http.Header{},
		}, nil
	})
	body, err := httpclient.GetBytes(t.Context(), &http.Client{Transport: overTransport}, "http://example.com/toolarge",
		httpclient.WithBaseDelay(time.Millisecond), httpclient.WithMaxBodyBytes(maxBytes))
	if body != nil {
		t.Errorf("over-cap body = %d bytes, want nil", len(body))
	}
	var tooLarge *httpclient.ResponseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("over-cap error = %v, want *ResponseTooLargeError", err)
	}
	if tooLarge.Limit != maxBytes {
		t.Errorf("ResponseTooLargeError.Limit = %d, want %d", tooLarge.Limit, int64(maxBytes))
	}

	// A body exactly at the cap must NOT error (the +1 probe read finds no overflow).
	atCapTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, maxBytes))),
			Header:     http.Header{},
		}, nil
	})
	atBody, atErr := httpclient.GetBytes(t.Context(), &http.Client{Transport: atCapTransport}, "http://example.com/atcap",
		httpclient.WithBaseDelay(time.Millisecond), httpclient.WithMaxBodyBytes(maxBytes))
	if atErr != nil {
		t.Fatalf("at-cap body: err = %v, want nil", atErr)
	}
	if int64(len(atBody)) != maxBytes {
		t.Errorf("at-cap body: len = %d, want %d", len(atBody), int64(maxBytes))
	}
}

// TestV3_RoundTripper_MaxElapsedTime_NotOvershot proves the elapsed-time budget
// is a true ceiling even against a large honored Retry-After: the abort happens
// BEFORE sleeping (so the call returns near-instantly instead of sleeping out
// the 60s Retry-After), the message is the clean nil-lastErr form, and only the
// initial attempt runs.
func TestV3_RoundTripper_MaxElapsedTime_NotOvershot(t *testing.T) {
	const budget = 50 * time.Millisecond
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		h := http.Header{}
		h.Set("Retry-After", "60") // capped at RetryAfterCap (60s) — dwarfs the 50ms budget
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     h,
		}, nil
	})
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{
		BaseDelay:      time.Millisecond,
		MaxAttempts:    5,
		MaxElapsedTime: budget,
	})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/budget", http.NoBody)

	start := time.Now()
	_, err := rt.RoundTrip(req) //nolint:bodyclose // budget-abort path returns no usable body
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected max-elapsed-time error")
	}
	if !strings.Contains(err.Error(), "max elapsed time") {
		t.Errorf("error = %v, want containing 'max elapsed time'", err)
	}
	// The honored 60s Retry-After must NOT be slept; aborting on the budget
	// returns immediately. A generous ceiling avoids CI flakes.
	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %v, want << 60s (must not sleep past the budget)", elapsed)
	}
	// lastErr is nil on the 503-response path: the message must be clean, with
	// no fmt.Errorf("...: %w", nil) artifact.
	if strings.Contains(err.Error(), "<nil>") || strings.Contains(err.Error(), "%!w") {
		t.Errorf("error = %v, want clean nil-lastErr message", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("transport calls = %d, want 1 (aborted before the first retry sleep)", got)
	}
}

// TestV3_GetBytes_MaxBodyBytes_MaxInt64_NoSilentLoss guards the probe-size
// overflow fix: a cap of math.MaxInt64 means "effectively unlimited" and must
// return the full body, not wrap maxBodyBytes+1 negative (which would make
// io.LimitReader read zero bytes and silently return an empty body — the exact
// silent-loss class the hard-overflow error was introduced to eliminate).
func TestV3_GetBytes_MaxBodyBytes_MaxInt64_NoSilentLoss(t *testing.T) {
	const payload = "hello world, not truncated"
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(payload)),
			Header:     http.Header{},
		}, nil
	})
	body, err := httpclient.GetBytes(t.Context(), &http.Client{Transport: transport}, "http://example.com/maxint",
		httpclient.WithBaseDelay(time.Millisecond), httpclient.WithMaxBodyBytes(math.MaxInt64))
	if err != nil {
		t.Fatalf("GetBytes with MaxInt64 cap = %v, want nil", err)
	}
	if string(body) != payload {
		t.Errorf("body = %q (len %d), want %q (a MaxInt64 cap must not wrap and silently truncate)", string(body), len(body), payload)
	}
}

// TestV3_TransportConfig_zero_value_works pins that TransportConfig{} is a
// usable default configuration (the stdlib zero-value idiom): a transient
// failure is retried and the request succeeds. The exact default attempt
// count is pinned by the {0,3} row of the ExactCounts table with a fast
// BaseDelay; this test keeps the pure zero value on a succeed-after-one-503
// upstream so it never sleeps the full default backoff more than once.
func TestV3_TransportConfig_zero_value_works(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     http.Header{},
		}, nil
	})
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/zero", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip = %v, want nil", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (zero-value config retries the 503)", resp.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("transport calls = %d, want 2", got)
	}
}

// TestV3_NewRetryClient_nil_policy_panics pins the loud-rejection contract: a
// nil redirect policy is a programmer error (it would silently mean net/http's
// follow-anywhere default — the exact omission the constructor exists to
// prevent), so NewRetryClient panics with a message naming the alternatives.
func TestV3_NewRetryClient_nil_policy_panics(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewRetryClient(nil policy) did not panic")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, "nil redirect policy") {
			t.Errorf("panic = %v, want message naming the nil redirect policy", r)
		}
	}()
	_ = httpclient.NewRetryClient(nil, nil, httpclient.TransportConfig{})
}

// TestV3_NewRetryClient_wiring pins the constructor's assembly: the returned
// client retries through the transport (a 503-then-200 upstream succeeds), the
// supplied redirect policy is installed and enforced (a cross-host redirect is
// refused under DefaultRedirectPolicy), and no Client.Timeout is set (a
// Client.Timeout above a retrying transport would cap the whole retry
// sequence).
func TestV3_NewRetryClient_wiring(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flaky":
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, "ok")
		case "/hop":
			http.Redirect(w, r, "https://evil.example/x", http.StatusFound)
		}
	}))
	t.Cleanup(upstream.Close)

	client := httpclient.NewRetryClient(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
	})

	if client.Timeout != 0 {
		t.Errorf("Client.Timeout = %v, want 0 (total-cap footgun must not be set)", client.Timeout)
	}

	// Retry wiring: the 503 is retried and the second attempt's 200 returned.
	resp, err := client.Get(upstream.URL + "/flaky")
	if err != nil {
		t.Fatalf("GET /flaky = %v, want nil", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok (retry through the transport)", body)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}

	// Policy wiring: a cross-host redirect is refused by DefaultRedirectPolicy.
	respHop, errHop := client.Get(upstream.URL + "/hop")
	if errHop == nil {
		if respHop != nil {
			respHop.Body.Close()
		}
		t.Fatal("GET /hop = nil error, want cross-host redirect refusal")
	}
	if !strings.Contains(errHop.Error(), "refusing redirect") {
		t.Errorf("redirect error = %v, want the DefaultRedirectPolicy refusal", errHop)
	}
}

// TestGetBytesRetriesRequestTimeout pins 408 as a retryable status on the
// httpclient.GetBytes door, beside 429 and 5xx. A 408 is the server reporting that it gave
// up waiting, which is self-healing, and this door only issues idempotent GETs -
// so excluding it forced consumers to lose the retry or re-classify the status
// themselves.
func TestGetBytesRetriesRequestTimeout(t *testing.T) {
	for name, tc := range map[string]struct {
		status     int
		wantCalls  int32
		wantErrSub string
	}{
		"408 retries to exhaustion": {status: http.StatusRequestTimeout, wantCalls: 3, wantErrSub: "retries exhausted"},
		"429 retries to exhaustion": {status: http.StatusTooManyRequests, wantCalls: 3, wantErrSub: "retries exhausted"},
		"503 retries to exhaustion": {status: http.StatusServiceUnavailable, wantCalls: 3, wantErrSub: "retries exhausted"},
		"400 is permanent":          {status: http.StatusBadRequest, wantCalls: 1, wantErrSub: "HTTP 400"},
		"404 is permanent":          {status: http.StatusNotFound, wantCalls: 1, wantErrSub: "HTTP 404"},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
				httpclient.WithMaxAttempts(3), httpclient.WithBaseDelay(time.Millisecond),
				httpclient.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))

			if err == nil {
				t.Fatalf("httpclient.GetBytes() on %d = nil error, want an error", tc.status)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error = %q, want substring %q", err, tc.wantErrSub)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("upstream calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestGetBytesSurfacesRetryAfterHint pins the hint's escape from the loop. A
// caller running httpclient.GetBytes with httpclient.WithMaxAttempts(1) inside its own outer retry
// loop - the sanctioned way to avoid 3x3 attempt amplification - needs the
// upstream-requested wait, which previously died in the loop's internal
// overrideWait and left the outer loop on jittered backoff. The exhaustion
// error now implements httpclient.RetryAfterHint, the same interface Do already honors, so
// the two doors compose. It deliberately does NOT implement Transient: whether
// to make another outer attempt stays the caller's policy.
func TestGetBytesSurfacesRetryAfterHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
		httpclient.WithMaxAttempts(1),
		httpclient.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err == nil {
		t.Fatal("httpclient.GetBytes() = nil error, want an error")
	}

	var hint httpclient.RetryAfterHint
	if !errors.As(err, &hint) {
		t.Fatalf("error %v does not implement httpclient.RetryAfterHint; an outer loop cannot honor the upstream wait", err)
	}
	if got := hint.RetryAfterHint(); got != 7*time.Second {
		t.Errorf("httpclient.RetryAfterHint() = %v, want 7s", got)
	}
	var statusErr *httpclient.StatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusTooManyRequests {
		t.Errorf("error = %v, want it to still unwrap to a 429 *httpclient.StatusError", err)
	}
	if httpclient.IsTransient(err) {
		t.Error("httpclient.IsTransient(exhausted httpclient.GetBytes error) = true, want false: outer-retry policy is the caller's")
	}
}

// TestGetBytesNoHintWithoutRetryAfter pins the negative: an exhausted httpclient.GetBytes
// with no Retry-After header carries no hint, so an enclosing loop keeps its own
// backoff progression instead of receiving a zero-valued wait.
func TestGetBytesNoHintWithoutRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
		httpclient.WithMaxAttempts(1),
		httpclient.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err == nil {
		t.Fatal("httpclient.GetBytes() = nil error, want an error")
	}
	var hint httpclient.RetryAfterHint
	if errors.As(err, &hint) && hint.RetryAfterHint() > 0 {
		t.Errorf("httpclient.RetryAfterHint() = %v, want no hint when the upstream sent no Retry-After", hint.RetryAfterHint())
	}
}

// TestSingleAttemptTerminalLineIsDebug pins the level of the terminal
// "retries exhausted" line against the attempt budget, on BOTH doors. A
// multi-attempt budget that ran out really did exhaust a retry tree the door
// owns, so it Warns; a one-attempt budget retried nothing, so the door is a
// single attempt inside the caller's own loop (the sanctioned
// no-3x3-amplification pattern) and that loop owns the warning. Without the
// split, every failed attempt of every such consumer emits a library Warn its
// operator cannot act on - and, on a non-final outer attempt, one that names a
// degradation which self-heals on the next try.
func TestSingleAttemptTerminalLineIsDebug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// Debug-level handler: the line must be PRESENT either way, so a missing
	// Warn is distinguishable from a swallowed line.
	newLog := func(buf *bytes.Buffer) *slog.Logger {
		return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	for name, tc := range map[string]struct {
		attempts  int
		wantLevel string
	}{
		"one attempt logs Debug":         {attempts: 1, wantLevel: "level=DEBUG"},
		"multi-attempt budget logs Warn": {attempts: 2, wantLevel: "level=WARN"},
	} {
		t.Run("GetBytes/"+name, func(t *testing.T) {
			var buf bytes.Buffer
			_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
				httpclient.WithMaxAttempts(tc.attempts),
				httpclient.WithBaseDelay(time.Millisecond),
				httpclient.WithLogger(newLog(&buf)))
			if err == nil {
				t.Fatal("httpclient.GetBytes() = nil error, want an error")
			}
			assertTerminalLine(t, buf.String(), "http retries exhausted", tc.wantLevel)
		})
		t.Run("Do/"+name, func(t *testing.T) {
			var buf bytes.Buffer
			_, err := httpclient.Do(t.Context(),
				func(context.Context) (struct{}, error) {
					return struct{}{}, &httpclient.HTTPStatusError{Code: http.StatusServiceUnavailable}
				},
				httpclient.WithMaxAttempts(tc.attempts),
				httpclient.WithBaseDelay(time.Millisecond),
				httpclient.WithLogger(newLog(&buf)))
			if err == nil {
				t.Fatal("httpclient.Do() = nil error, want an error")
			}
			assertTerminalLine(t, buf.String(), "retries exhausted", tc.wantLevel)
		})
	}
}

// assertTerminalLine finds the logged line carrying msg and asserts its level.
func assertTerminalLine(t *testing.T, logged, msg, wantLevel string) {
	t.Helper()
	for line := range strings.SplitSeq(logged, "\n") {
		if !strings.Contains(line, msg) {
			continue
		}
		if !strings.Contains(line, wantLevel) {
			t.Errorf("terminal line = %q, want %s", line, wantLevel)
		}
		return
	}
	t.Errorf("no %q line logged; got:\n%s", msg, logged)
}

// === v5 contract: the two capability interfaces are reachable by errors.AsType

// TestCapabilityInterfacesSatisfyError pins the v5 shape of httpclient.Transient and
// httpclient.RetryAfterHint: both embed error, so both satisfy errors.AsType's
// [E error] type parameter.
//
// It is a compile-time contract with a runtime witness. The compile half is the
// two errors.AsType instantiations below: before v5 neither interface declared
// an Error method, so both spellings were rejected with "Transient does not
// satisfy error (missing method Error)" — and Go 1.27's
// `go fix -errorsastype` rewrote httpclient's own errors.As sites (and every
// consumer's) into exactly that non-compiling form, because the fixer does not
// check the constraint before rewriting. Deleting the embedded error from
// either interface re-breaks this file.
//
// The runtime half pins that the wider declared type did not narrow the found
// set: errors.As over an error tree can only ever reach values that are already
// errors, so requiring Error() adds no condition an implementor in a chain can
// fail. Both spellings must therefore agree on the same error.
func TestCapabilityInterfacesSatisfyError(t *testing.T) {
	t.Parallel()

	// Wrapped twice so both spellings have to walk the chain rather than match
	// the top-level value.
	transientErr := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", httpclient.MarkTransient(errors.New("boom"))))

	tr, ok := errors.AsType[httpclient.Transient](transientErr)
	if !ok {
		t.Fatal("errors.AsType[httpclient.Transient] = false, want true")
	}
	if !tr.IsTransient() {
		t.Error("Transient.IsTransient() = false, want true")
	}
	// The embedded error is what makes the recovered value self-describing.
	if got := tr.Error(); got != "boom" {
		t.Errorf("Transient.Error() = %q, want %q", got, "boom")
	}
	var viaAs httpclient.Transient
	if !errors.As(transientErr, &viaAs) || viaAs != tr {
		t.Errorf("errors.As and errors.AsType disagree on httpclient.Transient: As gave %v, AsType gave %v", viaAs, tr)
	}

	hintErr := fmt.Errorf("outer: %w", &fixedHint{d: 3 * time.Second})

	h, ok := errors.AsType[httpclient.RetryAfterHint](hintErr)
	if !ok {
		t.Fatal("errors.AsType[httpclient.RetryAfterHint] = false, want true")
	}
	if got := h.RetryAfterHint(); got != 3*time.Second {
		t.Errorf("RetryAfterHint.RetryAfterHint() = %v, want 3s", got)
	}
	if got := h.Error(); got != "hinted" {
		t.Errorf("RetryAfterHint.Error() = %q, want %q", got, "hinted")
	}
	var hintViaAs httpclient.RetryAfterHint
	if !errors.As(hintErr, &hintViaAs) || hintViaAs != h {
		t.Errorf("errors.As and errors.AsType disagree on httpclient.RetryAfterHint: As gave %v, AsType gave %v", hintViaAs, h)
	}
}

// fixedHint is a minimal httpclient.RetryAfterHint implementor for the contract test
// above. It is the shape a consumer writes, so it doubles as the check that an
// ordinary implementor needs nothing beyond the Error method it already had.
type fixedHint struct{ d time.Duration }

func (*fixedHint) Error() string                   { return "hinted" }
func (e *fixedHint) RetryAfterHint() time.Duration { return e.d }

var _ httpclient.RetryAfterHint = (*fixedHint)(nil)
