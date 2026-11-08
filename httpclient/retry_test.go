package httpclient_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpclient"
)

// bufLogger returns a debug-level text logger writing into buf.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// swapDefaultLogger is this package's copy of the three-global restore; the
// reason log's writer and flags travel with slog's is on httpclient_test.go's
// swapDefaultLogger.
func swapDefaultLogger(t *testing.T, logger *slog.Logger) {
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
}

func shortOpts() []httpclient.GetOption {
	return []httpclient.GetOption{httpclient.WithBaseDelay(time.Millisecond), httpclient.WithMaxBodyBytes(10 << 20)}
}

func TestGetBytes_success_first_try(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %s", body)
	}
}

func TestGetBytes_error_status_fails_fast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...); err == nil {
		t.Error("expected error for 404")
	}
}

func TestGetBytes_all_2xx_are_success(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"200 OK", http.StatusOK, "hello"},
		{"201 Created", http.StatusCreated, "created"},
		{"204 No Content", http.StatusNoContent, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != http.StatusOK {
					w.WriteHeader(tt.status)
				}
				if tt.body != "" {
					_, _ = w.Write([]byte(tt.body))
				}
			}))
			defer srv.Close()

			body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
			if err != nil {
				t.Fatalf("Retry(%d) = %v, want nil", tt.status, err)
			}
			if string(body) != tt.body {
				t.Errorf("Retry(%d) body = %q, want %q", tt.status, body, tt.body)
			}
		})
	}
}

func TestGetBytes_non_followed_3xx_is_status_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://example.com/moved")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer srv.Close()

	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	_, err := httpclient.GetBytes(t.Context(), client, srv.URL, shortOpts()...)
	if err == nil {
		t.Fatal("expected *StatusError for a non-followed 3xx")
	}
	var se *httpclient.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T, want *httpclient.StatusError", err)
	}
	if se.Code != http.StatusMovedPermanently {
		t.Errorf("StatusError.Code = %d, want 301", se.Code)
	}
}

// TestGetBytes_sub_200_is_status_error guards the v4 delegation: getAttempt no
// longer restates the 2xx band, it asks CheckStatus, so this pins that a
// status BELOW 200 is still a permanent *StatusError and not a body read. Only a
// fake transport can produce a bare 1xx response (net/http resolves a real
// 100-continue itself), and without this test a future loosening of the
// classifier's low edge would silently make GetBytes return an informational
// response's bytes as content.
func TestGetBytes_sub_200_is_status_error(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusContinue,
			Body:       io.NopCloser(strings.NewReader("not a result")),
			Header:     http.Header{},
		}, nil
	})}

	body, err := httpclient.GetBytes(t.Context(), client, "http://example.test/informational", shortOpts()...)
	if body != nil {
		t.Errorf("body = %q, want nil (a 1xx carries no result)", body)
	}
	var se *httpclient.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusContinue {
		t.Fatalf("err = %v (%T), want *httpclient.StatusError{100}", err, err)
	}
}

func TestGetBytes_recovers_after_retryable_status(t *testing.T) {
	tests := []struct {
		name       string
		failStatus int
	}{
		{"429 then 200", http.StatusTooManyRequests},
		{"500 then 200", http.StatusInternalServerError},
		{"503 then 200", http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(tt.failStatus)
					_, _ = w.Write([]byte("transient"))
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok-body"))
			}))
			defer srv.Close()

			body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
			if err != nil {
				t.Fatalf("Retry after retry = %v, want nil", err)
			}
			if string(body) != "ok-body" {
				t.Errorf("body = %q, want %q", body, "ok-body")
			}
			if got := calls.Load(); got != 2 {
				t.Errorf("server call count = %d, want 2", got)
			}
		})
	}
}

func TestGetBytes_exhausts_on_persistent_failure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
	if err == nil {
		t.Fatalf("Retry after all retries = nil, want error")
	}
	if body != nil {
		t.Errorf("body = %v, want nil", body)
	}
	if !strings.Contains(err.Error(), "retries exhausted") {
		t.Errorf("error = %v, want containing %q", err, "retries exhausted")
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("error = %v, want wrapped HTTP 503 cause", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("server call count = %d, want 3", got)
	}
}

// TestGetBytes_permanent_status_on_final_attempt_is_not_exhaustion pins that a
// settled answer on the last attempt is returned as itself: the budget was not
// what stopped the loop.
func TestGetBytes_permanent_status_on_final_attempt_is_not_exhaustion(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
		httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLogger(bufLogger(&buf)))
	statusErr, ok := errors.AsType[*httpclient.StatusError](err)
	if !ok || statusErr.Code != http.StatusNotFound {
		t.Fatalf("GetBytes(503 then 404) err = %v, want a *StatusError with Code 404", err)
	}
	if strings.Contains(err.Error(), "retries exhausted") {
		t.Errorf("GetBytes(503 then 404) err = %q, want no exhaustion wrap", err)
	}
	if strings.Contains(buf.String(), "retries exhausted") {
		t.Errorf("log = %q, want no exhaustion line", buf.String())
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("server call count = %d, want 2", got)
	}
}

// TestGetBytes_aborts_on_context_cancellation pins that a cancel landing during
// a backoff sleep aborts the loop instead of spending another attempt.
//
// It runs in a synctest bubble so the cancel lands at a KNOWN point in the
// progression rather than a raced one. On a real clock the goroutine's 50ms
// against the loop's 100ms base delay was a timing bet: a stall long enough to
// push the cancel past the second inter-attempt sleep would let a third attempt
// through and fail the call-count assertion for a reason the code did not
// cause. Under the fake clock the cancel is ordered, so the counts are exact —
// and synctest.Sleep (Go 1.27) adds the settle that time.Sleep alone does not,
// so the main goroutine is provably parked in SleepCtx when the cancel arrives.
func TestGetBytes_aborts_on_context_cancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
		}))

		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			synctest.Sleep(50 * time.Millisecond)
			cancel()
		}()

		_, err := httpclient.GetBytes(ctx, srv.Client(), srv.URL, httpclient.WithBaseDelay(100*time.Millisecond), httpclient.WithMaxBodyBytes(10<<20))
		if err == nil {
			t.Fatalf("Retry after ctx cancel = nil, want error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled wrapped", err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("server call count = %d, want exactly 1 (the cancel lands inside the first backoff sleep)", got)
		}
	})
}

func TestGetBytes_body_size_limit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		buf := make([]byte, 1024)
		for range 11 * 1024 {
			_, _ = w.Write(buf)
		}
	}))
	defer srv.Close()

	// The 11MB body exceeds the 10MB cap. v2 fails loud with
	// *ResponseTooLargeError (no truncated body) and reports the cap via Limit.
	body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, httpclient.WithBaseDelay(time.Millisecond), httpclient.WithMaxBodyBytes(10<<20))
	if body != nil {
		t.Errorf("body = %d bytes, want nil (oversize body must not be truncated and returned)", len(body))
	}
	var tooLarge *httpclient.ResponseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("Retry(oversize) error = %v, want *ResponseTooLargeError", err)
	}
	if tooLarge.Limit != 10<<20 {
		t.Errorf("ResponseTooLargeError.Limit = %d, want %d", tooLarge.Limit, int64(10<<20))
	}
	if err.Error() != tooLarge.Error() {
		t.Errorf("Retry(oversize) error = %q, want the *ResponseTooLargeError itself, unwrapped", err)
	}
}

// TestGetBytes_honors_retry_after asserts the honored Retry-After wait EXACTLY,
// in synthetic time. See TestRetryRoundTripper_retries_on_429_with_retry_after
// for why the bubble needs httptest.NewTestServer rather than a real listener.
func TestGetBytes_honors_retry_after(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))

		start := time.Now()
		body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Retry = %v, want nil", err)
		}
		if string(body) != "ok" {
			t.Errorf("body = %q, want ok", body)
		}
		if elapsed != time.Second {
			t.Errorf("elapsed = %v, want exactly 1s (the Retry-After, not the 1ms BaseDelay)", elapsed)
		}
	})
}

// TestGetBytes_honors_retry_after_on_503 asserts the Retry helper waits the
// server-requested Retry-After on a 5xx, not just on 429. Runs in synthetic
// time.
func TestGetBytes_honors_retry_after_on_503(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if calls.Add(1) == 1 {
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(status)
						return
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("ok"))
				}))

				start := time.Now()
				body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
				elapsed := time.Since(start)
				if err != nil {
					t.Fatalf("Retry = %v, want nil", err)
				}
				if string(body) != "ok" {
					t.Errorf("body = %q, want ok", body)
				}
				// Retry-After: 1 must be honored on the 5xx, not just on 429.
				if elapsed != time.Second {
					t.Errorf("elapsed = %v, want exactly 1s (Retry-After honored on %d)", elapsed, status)
				}
			})
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty", "", 0},
		{"delta seconds", "5", 5 * time.Second},
		{"delta seconds with whitespace", "  10  ", 10 * time.Second},
		{"delta seconds just under cap", "59", 59 * time.Second},
		{"delta seconds exactly at cap", "60", 60 * time.Second},
		{"delta seconds one over cap", "61", 60 * time.Second},
		{"delta seconds capped at 60", "120", 60 * time.Second},
		{"huge value capped not overflowed", "10000000000", 60 * time.Second},
		{"max int64 seconds capped", "9223372036854775807", 60 * time.Second},
		{"above int64 range treated as malformed", "99999999999999999999", 0},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"malformed", "soon", 0},
		{"http-date future capped", now.Add(5 * time.Minute).UTC().Format(http.TimeFormat), 60 * time.Second},
		{"http-date past", now.Add(-time.Hour).UTC().Format(http.TimeFormat), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := httpclient.ParseRetryAfter(tt.in)
			if strings.Contains(tt.name, "http-date future") {
				if got < 55*time.Second || got > 60*time.Second {
					t.Errorf("ParseRetryAfter(%q) = %v, want ~60s", tt.in, got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("ParseRetryAfter(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestDrain_small_body swaps slog.Default; not parallel.
func TestDrain_small_body(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	// A sub-limit body drains via io.CopyN returning io.EOF, which Drain must
	// treat as a clean drain (the !errors.Is(err, io.EOF) guard), never a failure.
	httpclient.Drain(io.NopCloser(strings.NewReader("small")))
	if strings.Contains(buf.String(), "failed to drain") {
		t.Errorf("clean small-body drain logged a failure:\n%s", buf.String())
	}
}

func TestGetBytes_returns_typed_StatusError_on_exhaustion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
	if err == nil {
		t.Fatal("want error")
	}
	var target *httpclient.StatusError
	if !errors.As(err, &target) {
		t.Fatalf("errors.As(*StatusError) = false; err = %v", err)
	}
	if target.Code != 503 {
		t.Errorf("Code = %d, want 503", target.Code)
	}
	if got := httpclient.StatusErrorURL(target); got != srv.URL {
		t.Errorf("URL = %q, want %q", got, srv.URL)
	}
	if !errors.Is(err, httpclient.ErrServerError) {
		t.Error("want errors.Is(ErrServerError)")
	}
}

func TestGetBytes_returns_typed_StatusError_rate_limited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, httpclient.ErrRateLimited) {
		t.Errorf("want errors.Is(ErrRateLimited); err = %v", err)
	}
}

func TestGetBytes_4xx_returns_typed_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "retries exhausted") {
		t.Error("4xx should fast-fail without retries exhausted wrap")
	}
	var target *httpclient.StatusError
	if !errors.As(err, &target) {
		t.Fatalf("errors.As(*StatusError) = false; err = %v", err)
	}
	if target.Code != 404 {
		t.Errorf("Code = %d, want 404", target.Code)
	}
	if got := httpclient.StatusErrorURL(target); got != srv.URL {
		t.Errorf("URL = %q, want %q", got, srv.URL)
	}
}

type trackingCloser struct {
	io.Reader
	onClose func()
}

func (tc *trackingCloser) Close() error {
	tc.onClose()
	return nil
}

func TestReadLimitedBody(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		limit        int64
		want         string
		wantTooLarge bool
	}{
		{"under limit", "hello", 100, "hello", false},
		{"exactly at limit", "hello", 5, "hello", false},
		{"one byte over limit", "hello!", 5, "", true},
		{"empty body", "", 5, "", false},
		{"maxint64 is unlimited", "hello world", math.MaxInt64, "hello world", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			closed := false
			body := &trackingCloser{Reader: strings.NewReader(tc.body), onClose: func() { closed = true }}
			data, err := httpclient.ReadLimitedBody(body, tc.limit)
			if !closed {
				t.Error("ReadLimitedBody did not close the body")
			}
			if tc.wantTooLarge {
				var tooLarge *httpclient.ResponseTooLargeError
				if !errors.As(err, &tooLarge) {
					t.Fatalf("err = %v, want *ResponseTooLargeError", err)
				}
				if tooLarge.Limit != tc.limit {
					t.Errorf("ResponseTooLargeError.Limit = %d, want %d", tooLarge.Limit, tc.limit)
				}
				if data != nil {
					t.Errorf("data = %q, want nil bytes on overflow", data)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(data) != tc.want {
				t.Errorf("data = %q, want %q", data, tc.want)
			}
		})
	}
}

// TestReadLimitedBody_readErrorPropagates pins that a mid-read error is returned
// as-is (not masked as an overflow) and the body is still closed.
func TestReadLimitedBody_readErrorPropagates(t *testing.T) {
	errBoom := errors.New("boom")
	closed := false
	body := &trackingCloser{Reader: iotest.ErrReader(errBoom), onClose: func() { closed = true }}
	data, err := httpclient.ReadLimitedBody(body, 100)
	if !closed {
		t.Error("ReadLimitedBody did not close the body on read error")
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if data != nil {
		t.Errorf("data = %q, want nil bytes on read error", data)
	}
}

func TestGetBytes_non200_statusCodes(t *testing.T) {
	tests := []struct {
		status int
	}{
		{400}, {403}, {404}, {500}, {503},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, shortOpts()...)
			if err == nil {
				t.Fatalf("nil error for status %d", tt.status)
			}
			if body != nil {
				t.Errorf("non-nil body for error status %d", tt.status)
			}
			wantMsg := fmt.Sprintf("HTTP %d", tt.status)
			if !strings.Contains(err.Error(), wantMsg) {
				t.Errorf("error = %v, want containing %q", err, wantMsg)
			}
		})
	}
}

func TestGetBytes_non_transient_transport_error_fails_fast(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("permanent transport failure")
		}),
	}
	_, err := httpclient.GetBytes(t.Context(), client, "http://example.com/test", shortOpts()...)
	if err == nil {
		t.Fatal("expected error for non-transient transport error")
	}
	if strings.Contains(err.Error(), "retries exhausted") {
		t.Error("non-transient transport error should fail fast, not exhaust retries")
	}
}

// TestGetBytes_non_retryable_transport_error_is_redacted pins GetBytes's
// contract that every returned error is redacted: net/http's *url.Error
// carries the request URL, query secret included.
func TestGetBytes_non_retryable_transport_error_is_redacted(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("permanent transport failure")
		}),
	}
	_, err := httpclient.GetBytes(t.Context(), client, "http://example.com/feed?api_key=hunter2", shortOpts()...)
	if err == nil {
		t.Fatal("GetBytes err = nil, want the transport failure")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("GetBytes err = %q, want the URL's secret absent", err)
	}
}

// TestGetBytes_exhausted_transport_error_is_redacted pins the same contract on
// the exhaustion path, which wraps the last attempt's *url.Error.
func TestGetBytes_exhausted_transport_error_is_redacted(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		}),
	}
	_, err := httpclient.GetBytes(t.Context(), client, "http://example.com/feed?api_key=hunter2",
		httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Millisecond))
	if err == nil || !strings.Contains(err.Error(), "retries exhausted") {
		t.Fatalf("GetBytes err = %v, want the exhaustion error", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("GetBytes err = %q, want the URL's secret absent", err)
	}
}

// TestGetBytes_closes_every_discarded_body pins that a retried status and a
// permanent one both release their response, so no connection is leaked.
func TestGetBytes_closes_every_discarded_body(t *testing.T) {
	var closed atomic.Int32
	var calls atomic.Int32
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			code := http.StatusServiceUnavailable
			if calls.Add(1) == 2 {
				code = http.StatusNotFound
			}
			return &http.Response{
				StatusCode: code,
				Header:     http.Header{},
				Body:       &trackingCloser{Reader: strings.NewReader("discarded"), onClose: func() { closed.Add(1) }},
			}, nil
		}),
	}
	_, err := httpclient.GetBytes(t.Context(), client, "http://example.com/feed", shortOpts()...)
	if _, ok := errors.AsType[*httpclient.StatusError](err); !ok {
		t.Fatalf("GetBytes(503 then 404) err = %v, want a *StatusError", err)
	}
	if got := closed.Load(); got != 2 {
		t.Errorf("body closes = %d, want 2 (the 503 and the 404)", got)
	}
}

// TestGetBytes_logs_slow_successful_attempt pins that GetBytes reports an
// attempt slower than 10s; the fake clock makes the handler's delay instant.
func TestGetBytes_logs_slow_successful_attempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(11 * time.Second)
			_, _ = w.Write([]byte("ok"))
		}))
		var buf bytes.Buffer
		body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL, httpclient.WithLogger(bufLogger(&buf)))
		if err != nil || string(body) != "ok" {
			t.Fatalf("GetBytes = %q, %v, want \"ok\", nil", body, err)
		}
		if !strings.Contains(buf.String(), "slow upstream response") {
			t.Errorf("log = %q, want the slow-upstream line", buf.String())
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGetBytes_WithHeaders_appliesHeadersToRequest(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
		httpclient.WithBaseDelay(time.Millisecond),
		httpclient.WithHeaders(func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer token123")
		}),
	)
	if err != nil {
		t.Fatalf("Retry = %v, want nil", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
	if gotAuth != "Bearer token123" {
		t.Errorf("server saw Authorization = %q, want Bearer token123", gotAuth)
	}
}

func TestGetBytes_retries_on_transient_transport_error(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     http.Header{},
			}, nil
		}),
	}
	body, err := httpclient.GetBytes(t.Context(), client, "http://example.com/x",
		httpclient.WithBaseDelay(time.Millisecond), httpclient.WithMaxAttempts(3))
	if err != nil {
		t.Fatalf("Retry = %v, want nil after transient transport error", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("transport calls = %d, want 2", got)
	}
}

func TestGetBytes_create_request_error_fails_fast(t *testing.T) {
	// DEL control character (0x7f) makes http.NewRequestWithContext fail;
	// LogSafeError drops the url.Error wrapper so the url-parse cause is returned.
	_, err := httpclient.GetBytes(t.Context(), http.DefaultClient, "http://example.com/\x7f",
		httpclient.WithBaseDelay(time.Millisecond))
	if err == nil {
		t.Fatal("Retry with malformed URL = nil, want error")
	}
	if strings.Contains(err.Error(), "retries exhausted") {
		t.Errorf("error = %v, want fast-fail (not retries-exhausted wrap)", err)
	}
	if !strings.Contains(err.Error(), "invalid control character") {
		t.Errorf("error = %v, want URL-parse failure cause", err)
	}
}

func TestGetBytes_read_response_body_error(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errReadCloser{err: errors.New("read boom")},
				Header:     http.Header{},
			}, nil
		}),
	}
	_, err := httpclient.GetBytes(t.Context(), client, "http://example.com/x",
		httpclient.WithBaseDelay(time.Millisecond))
	if err == nil {
		t.Fatal("Retry with erroring body = nil, want error")
	}
	if !strings.Contains(err.Error(), "read response") {
		t.Errorf("error = %v, want containing read response", err)
	}
}

// errReadCloser fails every Read and records whether Close was called.
type errReadCloser struct {
	err    error
	closed bool
}

func (e *errReadCloser) Read(_ []byte) (int, error) { return 0, e.err }
func (e *errReadCloser) Close() error               { e.closed = true; return nil }

// errDrainBody fails every Read and is a no-op on Close (for Drain logging).
type errDrainBody struct{}

func (errDrainBody) Read([]byte) (int, error) { return 0, errors.New("read boom") }
func (errDrainBody) Close() error             { return nil }

//nolint:bodyclose // synthetic responses passed to the pure header parser; bodies never read
func TestParseRetryAfterResponse(t *testing.T) {
	t.Parallel()
	resp := func(retryAfter string) *http.Response {
		h := http.Header{}
		if retryAfter != "" {
			h.Set("Retry-After", retryAfter)
		}
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: h}
	}

	if d := httpclient.ParseRetryAfterResponse(nil); d != 0 {
		t.Errorf("ParseRetryAfterResponse(nil) = %v, want 0", d)
	}
	if d := httpclient.ParseRetryAfterResponse(resp("")); d != 0 {
		t.Errorf("ParseRetryAfterResponse(no header) = %v, want 0", d)
	}
	if d := httpclient.ParseRetryAfterResponse(resp("0")); d != 0 {
		t.Errorf("ParseRetryAfterResponse(\"0\") = %v, want 0", d)
	}
	if d := httpclient.ParseRetryAfterResponse(resp("-1")); d != 0 {
		t.Errorf("ParseRetryAfterResponse(\"-1\") = %v, want 0", d)
	}
	if d := httpclient.ParseRetryAfterResponse(resp("9")); d != 9*time.Second {
		t.Errorf("ParseRetryAfterResponse(\"9\") = %v, want 9s", d)
	}
	// Shared parseRetryAfterValue trims whitespace (delegation, not a per-copy Atoi).
	if d := httpclient.ParseRetryAfterResponse(resp("  30  ")); d != 30*time.Second {
		t.Errorf("ParseRetryAfterResponse(\"  30  \") = %v, want 30s", d)
	}
	// Uncapped (unlike ParseRetryAfter): an above-max value caps only at the
	// int64-seconds overflow boundary, computed as maxSecs*time.Second.
	const maxSecs = 9223372036
	if d := httpclient.ParseRetryAfterResponse(resp("9999999999")); d != time.Duration(maxSecs)*time.Second {
		t.Errorf("ParseRetryAfterResponse(overflow) = %v, want %v", d, time.Duration(maxSecs)*time.Second)
	}
	future := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	if d := httpclient.ParseRetryAfterResponse(resp(future)); d <= 0 {
		t.Errorf("ParseRetryAfterResponse(future date) = %v, want > 0", d)
	}
	past := time.Now().Add(-45 * time.Second).UTC().Format(http.TimeFormat)
	if d := httpclient.ParseRetryAfterResponse(resp(past)); d != 0 {
		t.Errorf("ParseRetryAfterResponse(past date) = %v, want 0", d)
	}
}

func TestGetBytes_zero_base_delay_defaults_to_base(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	// A zero base delay must be coerced to the 1s default, so the pre-retry
	// sleep blows the 100ms deadline and surfaces the context error.
	_, err := httpclient.GetBytes(ctx, srv.Client(), srv.URL, httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(0))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Retry(baseDelay=0) = %v, want context.DeadlineExceeded (delay defaulted)", err)
	}
}

func TestGetBytes_zero_max_body_bytes_defaults_to_full_body(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	// Zero must be coerced to the 10 MB default, returning the whole body.
	body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
		httpclient.WithBaseDelay(time.Millisecond), httpclient.WithMaxBodyBytes(0))
	if err != nil {
		t.Fatalf("Retry = %v, want nil", err)
	}
	if string(body) != "hello world" {
		t.Errorf("Retry(maxBodyBytes=0) body = %q, want %q", body, "hello world")
	}
}

func TestGetBytes_does_not_sleep_before_first_attempt(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// The deadline is sized against the behaviour it must EXCLUDE, not against
	// how fast a local round trip usually is. A pre-first-attempt sleep would
	// last the 10s base delay, so 3s falsifies it with room to spare, while a
	// no-sleep call returns in about a millisecond and so passes even when the
	// rest of the suite is saturating the machine. At 100ms this test failed
	// intermittently on a loaded host with nothing wrong in the code.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	// The first attempt must NOT sleep, so a fast response returns before the
	// deadline despite a huge base delay.
	body, err := httpclient.GetBytes(ctx, srv.Client(), srv.URL, httpclient.WithBaseDelay(10*time.Second))
	if err != nil {
		t.Fatalf("Retry(first attempt) = %v, want nil (no pre-first-attempt sleep)", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestGetBytes_fast_response_does_not_log_slow_upstream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	body, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
		httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLogger(bufLogger(&buf)))
	if err != nil {
		t.Fatalf("Retry = %v, want nil", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
	// A sub-second response must NOT trigger the >10s "slow upstream" warning.
	if strings.Contains(buf.String(), "slow upstream response") {
		t.Errorf("fast response logged slow-upstream warning:\n%s", buf.String())
	}
}

func TestGetBytes_retry_debug_log_reports_one_indexed_attempt(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	if _, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL,
		httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLogger(bufLogger(&buf))); err != nil {
		t.Fatalf("Retry = %v, want nil", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "http request failed, will retry") {
		t.Fatalf("expected retry debug log, got:\n%s", logged)
	}
	// The first failure logs the human (1-indexed) attempt number, attempt+1 == 1.
	if !strings.Contains(logged, "attempt=1") {
		t.Errorf("retry debug log = %q, want attribute attempt=1", logged)
	}
}

// TestDrain_clean_drain_does_not_log_failure swaps slog.Default; not parallel.
func TestDrain_clean_drain_does_not_log_failure(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	// A body larger than the 64KB drain limit: CopyN returns nil (no EOF).
	httpclient.Drain(io.NopCloser(strings.NewReader(strings.Repeat("y", 128<<10))))
	if strings.Contains(buf.String(), "failed to drain") {
		t.Errorf("clean drain logged a failure:\n%s", buf.String())
	}
}

// TestDrain_logs_on_non_eof_read_error swaps slog.Default; not parallel.
func TestDrain_logs_on_non_eof_read_error(t *testing.T) {
	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	httpclient.Drain(errDrainBody{})
	if !strings.Contains(buf.String(), "failed to drain response body") {
		t.Errorf("Drain(erroring body) logged %q, want drain-failure debug line", buf.String())
	}
}

func TestDrainClose_closes_body_on_read_error(t *testing.T) {
	errReader := &errReadCloser{err: errors.New("read fail")}
	httpclient.DrainClose(errReader)
	if !errReader.closed {
		t.Error("DrainClose did not close body on read error")
	}
}

// TestGetBytes_maxAttempts_nonpositive_clamps_to_one verifies a degenerate attempt
// count runs fn exactly once (try once), never the old coerce-to-default-3 and
// never a silent zero-attempt no-op.
func TestGetBytes_maxAttempts_nonpositive_clamps_to_one(t *testing.T) {
	t.Parallel()
	for _, maxAttempts := range []int{0, -1, -100} {
		var calls atomic.Int32
		client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
			}, nil
		})}
		_, _ = httpclient.GetBytes(t.Context(), client, "http://example.com/retry-edge",
			httpclient.WithBaseDelay(time.Millisecond), httpclient.WithMaxAttempts(maxAttempts))
		if got := calls.Load(); got != 1 {
			t.Errorf("WithMaxAttempts(%d): calls = %d, want 1 (clamped to a single attempt)", maxAttempts, got)
		}
	}
}

// TestGetBytes_cancel_during_backoff_aborts_before_next_attempt verifies that a
// context cancelled while Retry is sleeping between attempts aborts the loop
// immediately: the interrupted-sleep error propagates and no further attempt is
// made. The transport cancels the context on the first call so the subsequent
// backoff sleep observes a dead context; the WithHeaders hook counts attempts
// independently of the http.Client's transport-invocation details.
func TestGetBytes_cancel_during_backoff_aborts_before_next_attempt(t *testing.T) {
	var attempts atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	client := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			cancel() // cancel during the post-response backoff window
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
			}, nil
		}),
	}

	// A one-hour base delay would dominate the run if the sleep were not
	// interrupted; because the context is already cancelled, SleepCtx returns at
	// once and the test stays fast.
	_, err := httpclient.GetBytes(ctx, client, "http://example.com/cancel-during-backoff",
		httpclient.WithBaseDelay(time.Hour),
		httpclient.WithMaxAttempts(3),
		httpclient.WithHeaders(func(*http.Request) { attempts.Add(1) }),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Retry = %v, want context.Canceled (a cancelled backoff sleep must abort)", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (cancellation during backoff must abort before a second attempt)", got)
	}
}

// retryAfterHintErr is a transient error carrying an explicit Retry-After hint,
// exercising Do's RetryAfterHint honoring.
type retryAfterHintErr struct{ d time.Duration }

func (*retryAfterHintErr) Error() string                   { return "rate limited" }
func (*retryAfterHintErr) IsTransient() bool               { return true }
func (e *retryAfterHintErr) RetryAfterHint() time.Duration { return e.d }

// TestDo_honorsRetryAfterHint proves a transient error that implements
// RetryAfterHint drives the wait: the base delay is set very high, so a fast
// retry proves the small hint was used instead of the 10s backoff.
func TestDo_honorsRetryAfterHint(t *testing.T) {
	var calls int
	start := time.Now()
	got, err := httpclient.Do(t.Context(),
		func(context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, &retryAfterHintErr{d: 200 * time.Millisecond}
			}
			return 42, nil
		}, httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(10*time.Second), httpclient.WithLabel("hint-test"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got != 42 || calls != 2 {
		t.Fatalf("got=%d calls=%d, want 42 and 2", got, calls)
	}
	if elapsed > 3*time.Second {
		t.Errorf("waited %v; the 200ms Retry-After hint was not honored over the 10s base delay", elapsed)
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("waited %v; expected to honor the ~200ms hint", elapsed)
	}
}

func TestDo_hintOverridesLargerBackoff(t *testing.T) {
	var calls int
	start := time.Now()
	got, err := httpclient.Do(t.Context(),
		func(context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, &retryAfterHintErr{d: 250 * time.Millisecond}
			}
			return 9, nil
		}, httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLabel("hint-over-backoff"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got != 9 || calls != 2 {
		t.Fatalf("got=%d calls=%d, want 9 and 2", got, calls)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("waited %v; a hint larger than the base delay must override it (not min(hint, backoff))", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("waited %v; far longer than the 250ms hint", elapsed)
	}
}

// TestDo_ignoresNonPositiveHint confirms a hint of zero leaves the jittered
// exponential backoff in charge (no override, no panic).
func TestDo_ignoresNonPositiveHint(t *testing.T) {
	var calls int
	got, err := httpclient.Do(t.Context(),
		func(context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, &retryAfterHintErr{d: 0}
			}
			return 7, nil
		}, httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLabel("hint-test"))
	if err != nil || got != 7 || calls != 2 {
		t.Fatalf("err=%v got=%d calls=%d, want nil/7/2", err, got, calls)
	}
}

// TestOneRetryRule pins one status rule behind every door: over every code
// net/http parses from a status line (000-999), the transport's default policy
// retries it exactly when IsRetryableStatus says so, and a status error reports
// itself transient on the same rule less 429, which Do leaves to its rate-limit
// options; every transport-error class gets one verdict from IsTransient and
// the transport alike.
func TestOneRetryRule(t *testing.T) {
	t.Parallel()
	transportRetries := func(t *testing.T, answer func() (*http.Response, error)) bool {
		t.Helper()
		var calls atomic.Int32
		rt := httpclient.NewRetryRoundTripper(roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return answer()
		}), httpclient.TransportConfig{MaxAttempts: 2, BaseDelay: time.Nanosecond})
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/rule", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := rt.RoundTrip(req); err == nil {
			resp.Body.Close()
		}
		return calls.Load() == 2
	}
	for code := range 1000 {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()
			want := httpclient.IsRetryableStatus(code)
			wantTransient := want && code != http.StatusTooManyRequests
			if got := (&httpclient.StatusError{Code: code}).IsTransient(); got != wantTransient {
				t.Errorf("StatusError{%d}.IsTransient() = %v, want %v", code, got, wantTransient)
			}
			got := transportRetries(t, func() (*http.Response, error) {
				return &http.Response{StatusCode: code, Body: http.NoBody, Header: http.Header{}}, nil
			})
			if got != want {
				t.Errorf("transport default retried HTTP %d = %v, IsRetryableStatus = %v", code, got, want)
			}
		})
	}
	classes := []struct {
		name string
		err  error
		want bool
	}{
		{"timeout", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, true},
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"broken_pipe", &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}, true},
		{"unexpected_eof", io.ErrUnexpectedEOF, true},
		{"dns", &net.DNSError{Err: "no such host", Name: "example.invalid", IsNotFound: true}, true},
		{"permanent", httpclient.Permanent(syscall.ECONNRESET), false},
		{"caller_canceled", context.Canceled, false},
		{"plain", errors.New("plain"), false},
	}
	for _, tc := range classes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := httpclient.IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if got := transportRetries(t, func() (*http.Response, error) { return nil, tc.err }); got != tc.want {
				t.Errorf("transport default retried %v = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
	t.Run("server_closed", func(t *testing.T) {
		t.Parallel()
		addr, accepts := hangUpServer(t, false)
		err := get(t, &http.Client{}, "http://"+addr+"/")
		if !httpclient.IsTransient(err) {
			t.Errorf("IsTransient(%v) = false, want true", err)
		}
		client := &http.Client{Transport: httpclient.NewRetryRoundTripper(nil, httpclient.TransportConfig{MaxAttempts: 2, BaseDelay: time.Nanosecond})}
		_ = get(t, client, "http://"+addr+"/")
		if got := accepts.Load(); got != 3 {
			t.Errorf("connections = %d, want 3: one for the plain client, two for the transport's retry", got)
		}
	})
	t.Run("server_closed_post_not_retried", func(t *testing.T) {
		t.Parallel()
		addr, accepts := hangUpServer(t, false)
		client := &http.Client{Transport: httpclient.NewRetryRoundTripper(nil, httpclient.TransportConfig{MaxAttempts: 2, BaseDelay: time.Nanosecond})}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatal("POST to a hang-up server succeeded, want a transport error")
		}
		if got := accepts.Load(); got != 1 {
			t.Errorf("connections = %d, want 1: a POST is sent once", got)
		}
		if !errors.Is(err, io.EOF) {
			t.Errorf("errors.Is(%v, io.EOF) = false, want true", err)
		}
		if !strings.Contains(err.Error(), "server closed the connection before responding") {
			t.Errorf("err = %q, want the hang-up named", err)
		}
	})
}

// TestRetryRoundTripper_empty_method_is_GET pins that a request with no method,
// which net/http sends as a GET, is retried as one.
func TestRetryRoundTripper_empty_method_is_GET(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rt := httpclient.NewRetryRoundTripper(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Header: http.Header{}}, nil
	}), httpclient.TransportConfig{BaseDelay: time.Nanosecond})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.invalid/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Method = ""
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip err = %v, want the 503", err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (the default)", got)
	}
}

// TestRetryRoundTripper_body_without_GetBody_is_sent_once pins that a request
// whose body cannot be rewound is never replayed, whatever its method: a retry
// would send the drained body.
func TestRetryRoundTripper_body_without_GetBody_is_sent_once(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			rt := httpclient.NewRetryRoundTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Header: http.Header{}}, nil
			}), httpclient.TransportConfig{BaseDelay: time.Nanosecond, RetryNonIdempotent: true})
			req, err := http.NewRequestWithContext(t.Context(), method, "http://example.invalid/", io.NopCloser(strings.NewReader("payload")))
			if err != nil {
				t.Fatal(err)
			}
			if req.GetBody != nil {
				t.Fatal("setup: GetBody set, want a body that cannot be rewound")
			}
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip err = %v, want the 503", err)
			}
			resp.Body.Close()
			if got := calls.Load(); got != 1 {
				t.Errorf("calls = %d, want 1", got)
			}
		})
	}
}
