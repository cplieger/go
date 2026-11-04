package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// The bounds net/http installs itself — an http.Client.Timeout and a
// Transport.ResponseHeaderTimeout — govern ONE attempt, and the README names
// both as the per-attempt bound to use. net/http reports their expiry through
// an internal timeout error that answers errors.Is(err,
// context.DeadlineExceeded) with true while never carrying the sentinel
// (net/http.timeoutError.Is compares the TARGET and returns true; the value
// unwraps to nothing). Classifying with errors.Is therefore folded both bounds
// in with caller-budget expiry and made them terminal, so a stalled attempt
// was abandoned after one try.
//
// These tests pin the split. The generic shapes come first (they are the
// contract), then each of the three request phases a timeout can fire in.

// stallHandler blocks until the test releases it or the client gives up, which
// is what makes the server-side stall observable as a client-side timeout.
func stallServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// clientTimeoutError produces the error net/http returns when an
// http.Client.Timeout expires while awaiting response headers. Built from a
// real request because the type that carries the Is-without-being behavior is
// unexported and cannot be constructed from outside net/http.
func clientTimeoutError(t *testing.T) error {
	t.Helper()
	srv := stallServer(t)
	resp, err := (&http.Client{Timeout: 40 * time.Millisecond}).Get(srv.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("Get succeeded, want a Client.Timeout expiry")
	}
	return err
}

// TestIsTransient_netHTTPTimeoutIsPerAttempt is the finding: net/http's timeout
// error matches the deadline without being it, and that match alone must not
// make it terminal.
func TestIsTransient_netHTTPTimeoutIsPerAttempt(t *testing.T) {
	t.Parallel()
	err := clientTimeoutError(t)

	// The premise. If either of these ever stops holding, net/http changed and
	// the classification below needs rereading rather than trusting.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("premise gone: errors.Is(%v, context.DeadlineExceeded) = false", err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("premise gone: %v is not a net.Error timeout", err)
	}

	if !httpclient.IsTransient(err) {
		t.Errorf("IsTransient(%v) = false, want true: an http.Client.Timeout bounds ONE attempt", err)
	}
}

// TestIsTransient_callerDeadlineStaysTerminal is the safety property, and the
// reason the classification tests chain IDENTITY rather than errors.Is: a
// caller whose own budget is gone must never look retryable.
func TestIsTransient_callerDeadlineStaysTerminal(t *testing.T) {
	t.Parallel()
	srv := stallServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("Do succeeded, want the caller's deadline")
	}
	if httpclient.IsTransient(err) {
		t.Errorf("IsTransient(%v) = true: a caller out of budget must stay terminal", err)
	}
}

// TestIsTransient_deadlineCarriers tables the cases the split turns on. Each
// row names the CARRIER of the deadline, because that is what decides whether
// the owner of the expired bound is knowable.
func TestIsTransient_deadlineCarriers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"the sentinel itself", context.DeadlineExceeded, false},
		{"a wrapped sentinel", fmt.Errorf("upstream stalled: %w", context.DeadlineExceeded), false},
		{
			"a *url.Error around the sentinel, as net/http builds it",
			&url.Error{Op: "Get", URL: "http://example.test", Err: context.DeadlineExceeded}, false,
		},
		{"cancellation", context.Canceled, false},
		{"a wrapped cancellation", fmt.Errorf("shutting down: %w", context.Canceled), false},
		// The net package maps EVERY expired context onto one shared "i/o
		// timeout" value that matches the deadline without carrying it, so a
		// net-layer error reporting a deadline cannot say whose deadline it
		// was: the caller's, or a net.Dialer.Timeout the transport installed.
		// Unknowable stays terminal.
		{"a *net.OpError reporting a deadline", opErrorWithDeadline(), false},
		{"a *net.DNSError reporting a deadline", dnsErrorWithDeadline(), false},
		// An i/o timeout from a socket deadline (os.ErrDeadlineExceeded) does
		// NOT match context.DeadlineExceeded at all, and was always transient.
		{"a socket read deadline", &net.OpError{Op: "read", Err: fakeIOTimeout{}}, true},
		{"a marked attempt timeout", httpclient.AttemptTimeout(context.DeadlineExceeded), true},
		{"a marked attempt timeout made permanent", httpclient.Permanent(httpclient.AttemptTimeout(context.DeadlineExceeded)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := httpclient.IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsTransient_consumerTypeKeepsItsVerdict: an error type that declares
// itself non-transient keeps that verdict even when it reports a deadline. The
// caller-context rejection no longer decides ahead of the Transient interface
// for a deadline it cannot attribute, so the interface is what speaks — which
// is how a consumer opts a timeout of its own back OUT.
func TestIsTransient_consumerTypeKeepsItsVerdict(t *testing.T) {
	t.Parallel()
	if httpclient.IsTransient(terminalTimeout{}) {
		t.Error("IsTransient(terminalTimeout{}) = true: an explicit IsTransient() false must win")
	}
	if httpclient.IsTransient(httpclient.Permanent(clientTimeoutError(t))) {
		t.Error("IsTransient(Permanent(clientTimeout)) = true: Permanent must still veto")
	}
}

// terminalTimeout is the consumer shape that must not flip: it reports a
// deadline (Is, the way net/http does) and declares itself terminal.
type terminalTimeout struct{}

func (terminalTimeout) Error() string        { return "vendor: request budget exhausted" }
func (terminalTimeout) Is(target error) bool { return target == context.DeadlineExceeded }
func (terminalTimeout) IsTransient() bool    { return false }
func (terminalTimeout) Timeout() bool        { return true }
func (terminalTimeout) Temporary() bool      { return true }

// fakeIOTimeout stands in for os.ErrDeadlineExceeded: a net.Error timeout that
// does not claim to be a context deadline.
type fakeIOTimeout struct{}

func (fakeIOTimeout) Error() string   { return "i/o timeout" }
func (fakeIOTimeout) Timeout() bool   { return true }
func (fakeIOTimeout) Temporary() bool { return true }

// opErrorWithDeadline builds the shape net.Dialer produces when a context
// deadline expires during a dial: an *net.OpError whose cause matches
// context.DeadlineExceeded without carrying it.
func opErrorWithDeadline() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: deadlineMatcher{}}
}

func dnsErrorWithDeadline() error {
	return &net.DNSError{UnwrapErr: deadlineMatcher{}, Err: "i/o timeout", Name: "example.test", IsTimeout: true}
}

// deadlineMatcher mirrors net's own errTimeout: "i/o timeout", a net.Error
// timeout, Is-matching the deadline without holding it.
type deadlineMatcher struct{}

func (deadlineMatcher) Error() string        { return "i/o timeout" }
func (deadlineMatcher) Timeout() bool        { return true }
func (deadlineMatcher) Temporary() bool      { return true }
func (deadlineMatcher) Is(target error) bool { return target == context.DeadlineExceeded }

// --- The three phases, end to end through Do ---

// TestDo_retriesClientTimeoutAwaitingHeaders is the header-wait phase: the
// stall shape a hung upstream produces, and the one plexapi's
// ResponseHeaderTimeout exists for.
func TestDo_retriesClientTimeoutAwaitingHeaders(t *testing.T) {
	t.Parallel()
	srv := stallServer(t)
	client := &http.Client{Timeout: 30 * time.Millisecond}
	attempts := 0
	_, err := httpclient.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
		attempts++
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
		if reqErr != nil {
			return struct{}{}, reqErr
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			return struct{}{}, doErr
		}
		defer resp.Body.Close()
		return struct{}{}, nil
	}, httpclient.WithMaxAttempts(3), httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLogger(discardLogger()))
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (a Client.Timeout bounds one attempt)", attempts)
	}
	if err == nil {
		t.Error("Do = nil error, want the exhausted timeout")
	}
}

// TestDo_retriesResponseHeaderTimeout is the same phase bounded on the
// TRANSPORT instead of the client, which is the plexapi/README-recommended
// placement under a retrying round-tripper.
func TestDo_retriesResponseHeaderTimeout(t *testing.T) {
	t.Parallel()
	srv := stallServer(t)
	base, err := httpclient.CloneDefaultTransport()
	if err != nil {
		t.Fatalf("CloneDefaultTransport: %v", err)
	}
	base.ResponseHeaderTimeout = 30 * time.Millisecond
	client := &http.Client{Transport: base}
	attempts := 0
	_, err = httpclient.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
		attempts++
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
		if reqErr != nil {
			return struct{}{}, reqErr
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			// The redaction a consumer applies to keep the URL out of logs must
			// not defeat the classification.
			return struct{}{}, httpclient.LogSafeError(doErr)
		}
		defer resp.Body.Close()
		return struct{}{}, nil
	}, httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLogger(discardLogger()))
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (a ResponseHeaderTimeout bounds one attempt)", attempts)
	}
	if err == nil {
		t.Error("Do = nil error, want the exhausted timeout")
	}
}

// TestIsTransient_clientTimeoutDuringBodyRead is the body-read phase. net/http
// wraps a read failure in the same timeout error when the client's deadline
// fired, so a partially-read response classifies the same way a header stall
// does — the caller reissues the whole request.
func TestIsTransient_clientTimeoutDuringBodyRead(t *testing.T) {
	t.Parallel()
	stall := make(chan struct{})
	defer close(stall)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "64")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-stall:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	resp, err := (&http.Client{Timeout: 60 * time.Millisecond}).Get(srv.URL)
	if err != nil {
		t.Skipf("headers did not arrive before the client deadline: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	_, err = resp.Body.Read(buf)
	if err == nil {
		t.Fatal("body read succeeded, want the client deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("premise gone: body-read error %v does not report a deadline", err)
	}
	if !httpclient.IsTransient(err) {
		t.Errorf("IsTransient(%v) = false, want true: the client deadline bounded one attempt", err)
	}
}

// TestIsTransient_clientTimeoutDuringDial is the dial phase: net/http replaces
// the dialer's error with its own timeout error when Client.Timeout is what
// expired, so the dial classifies per-attempt too. Uses a listener that
// accepts nothing beyond its backlog is not portable, so this drives the
// timeout through a stalling server's address with a deadline shorter than the
// handshake the server never completes.
func TestIsTransient_clientTimeoutDuringDial(t *testing.T) {
	t.Parallel()
	// A closed listener's address refuses instead of stalling, so use a
	// blackhole address: an RFC 5737 TEST-NET address is not routed, so the
	// connect attempt hangs until the client's own bound fires.
	resp, err := (&http.Client{Timeout: 40 * time.Millisecond}).Get("http://192.0.2.1:81/")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("Get succeeded against a blackhole address")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Skipf("dial failed for another reason in this environment: %v", err)
	}
	if !httpclient.IsTransient(err) {
		t.Errorf("IsTransient(%v) = false, want true: the client deadline bounded one attempt", err)
	}
}

// TestRetryRoundTripper_retriesResponseHeaderTimeout is the live shape this
// split protects: plexapi puts its per-attempt bound on the base transport
// under NewRetryRoundTripper with the DEFAULT policy, which classifies
// through IsTransient. Without the split that bound's expiry would end the
// sequence after one attempt.
func TestRetryRoundTripper_retriesResponseHeaderTimeout(t *testing.T) {
	t.Parallel()
	srv := stallServer(t)
	base, err := httpclient.CloneDefaultTransport()
	if err != nil {
		t.Fatalf("CloneDefaultTransport: %v", err)
	}
	base.ResponseHeaderTimeout = 30 * time.Millisecond
	attempts := 0
	rt := httpclient.NewRetryRoundTripper(base, httpclient.TransportConfig{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
		OnRetry:     func(int, *http.Request, *http.Response, error) { attempts++ },
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("RoundTrip = nil error, want the exhausted header timeout")
	}
	// OnRetry fires once per retry, so 3 total attempts means 2 retries.
	if attempts != 2 {
		t.Errorf("retries = %d, want 2 (3 total attempts)", attempts)
	}
}

// TestRetryRoundTripper_callerDeadlineIsNotRetried keeps the RoundTripper's
// half of the safety property under the same conditions: the caller's own
// deadline ends the sequence at one attempt.
func TestRetryRoundTripper_callerDeadlineIsNotRetried(t *testing.T) {
	t.Parallel()
	srv := stallServer(t)
	base, err := httpclient.CloneDefaultTransport()
	if err != nil {
		t.Fatalf("CloneDefaultTransport: %v", err)
	}
	retries := 0
	rt := httpclient.NewRetryRoundTripper(base, httpclient.TransportConfig{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
		OnRetry:     func(int, *http.Request, *http.Response, error) { retries++ },
	})
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if retries != 0 {
		t.Errorf("retries = %d, want 0 (a caller out of budget must not be retried)", retries)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("RoundTrip = %v, want the caller's deadline", err)
	}
}

// TestGetBytes_callerDeadlineIsNotRetried pins the same property on the door
// whose only caller-context guard is SleepCtx: even with the classification
// reading a timeout as per-attempt, an expired caller context ends the loop.
func TestGetBytes_callerDeadlineIsNotRetried(t *testing.T) {
	t.Parallel()
	srv := stallServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := httpclient.GetBytes(ctx, srv.Client(), srv.URL,
		httpclient.WithMaxAttempts(4), httpclient.WithBaseDelay(time.Second), httpclient.WithLogger(discardLogger()))
	if err == nil {
		t.Fatal("GetBytes = nil error, want the caller's deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("GetBytes = %v, want context.DeadlineExceeded", err)
	}
	// Four attempts at a 1s base delay would take seconds; ending promptly is
	// the observable proof the loop stopped rather than retried.
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("GetBytes took %s: the loop kept retrying past the caller's deadline", elapsed)
	}
}

// TestGetBytes_retriesClientTimeout is the GetBytes half of the fix: the door
// takes the client from its caller, so that client's Timeout is the per-attempt
// bound and its expiry is retried.
func TestGetBytes_retriesClientTimeout(t *testing.T) {
	t.Parallel()
	requests := 0
	var mu sync.Mutex
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	// The handler blocks until cleanup, so Client.Timeout is what ends each
	// attempt and its value only has to be long enough to reach the handler.
	// At 30ms a loaded host could not always complete three dial-and-send
	// cycles inside the budget, so the server saw two requests and the test
	// reported a retry defect that was not there. 500ms keeps the whole test
	// under two seconds and is far above scheduling jitter.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err := httpclient.GetBytes(t.Context(), client, srv.URL,
		httpclient.WithMaxAttempts(3), httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLogger(discardLogger()))
	if err == nil {
		t.Fatal("GetBytes = nil error, want the exhausted timeout")
	}
	mu.Lock()
	got := requests
	mu.Unlock()
	if got != 3 {
		t.Errorf("server saw %d requests, want 3 (a Client.Timeout bounds one attempt)", got)
	}
}
