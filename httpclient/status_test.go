package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

func TestCheckHTTPStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		statusCode int
		wantNil    bool
		wantAuth   bool
		wantRate   bool
		wantStatus bool
	}{
		{"200 OK", 200, true, false, false, false},
		{"201 Created", 201, true, false, false, false},
		{"204 No Content", 204, true, false, false, false},
		{"299 boundary top of 2xx", 299, true, false, false, false},
		// v4: success is 2xx ONLY. A 3xx (surfaced by a non-following redirect
		// policy) is an error, no longer a success.
		{"300 boundary first non-success", 300, false, false, false, true},
		{"301 Moved Permanently", 301, false, false, false, true},
		{"302 Found", 302, false, false, false, true},
		{"304 Not Modified", 304, false, false, false, true},
		{"307 Temporary Redirect", 307, false, false, false, true},
		{"399 boundary below 400", 399, false, false, false, true},
		{"400 Bad Request", 400, false, false, false, true},
		{"401 Unauthorized", 401, false, true, false, false},
		{"403 Forbidden", 403, false, true, false, false},
		{"429 Too Many Requests", 429, false, false, true, false},
		{"500 Internal Server Error", 500, false, false, false, true},
		{"502 Bad Gateway", 502, false, false, false, true},
		{"503 Service Unavailable", 503, false, false, false, true},
		{"504 Gateway Timeout", 504, false, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := &http.Response{StatusCode: tt.statusCode, Header: http.Header{}}
			err := httpclient.CheckHTTPStatus(resp)
			if tt.wantNil {
				if err != nil {
					t.Errorf("CheckHTTPStatus(%d) = %v, want nil", tt.statusCode, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckHTTPStatus(%d) = nil, want error", tt.statusCode)
			}
			var authErr *httpclient.AuthError
			var rateErr *httpclient.RateLimitError
			var statusErr *httpclient.HTTPStatusError
			if tt.wantAuth && !errors.As(err, &authErr) {
				t.Errorf("CheckHTTPStatus(%d) = %T, want *AuthError", tt.statusCode, err)
			}
			if tt.wantRate && !errors.As(err, &rateErr) {
				t.Errorf("CheckHTTPStatus(%d) = %T, want *RateLimitError", tt.statusCode, err)
			}
			if tt.wantStatus && !errors.As(err, &statusErr) {
				t.Errorf("CheckHTTPStatus(%d) = %T, want *HTTPStatusError", tt.statusCode, err)
			}
		})
	}
}

func TestCheckHTTPStatus_429_parses_retry_after(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("Retry-After", "30")
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: h}
	err := httpclient.CheckHTTPStatus(resp)
	var rl *httpclient.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("expected *RateLimitError, got %T", err)
	}
	if rl.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", rl.RetryAfter)
	}
}

func TestCheckHTTPStatus_429_parses_http_date(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	h := http.Header{}
	h.Set("Retry-After", future)
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: h}
	err := httpclient.CheckHTTPStatus(resp)
	var rl *httpclient.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("expected *RateLimitError, got %T", err)
	}
	if rl.RetryAfter < 30*time.Second || rl.RetryAfter > 60*time.Second {
		t.Errorf("RetryAfter = %v, want ~45s", rl.RetryAfter)
	}
}

// TestCheckHTTPStatus_non_2xx_is_error_even_below_200 pins the other edge of
// the v4 strict window. Before v4 the classifier returned nil for every status
// below 400, and this test asserted the reverse of what it asserts now ("sub-200
// is not an error"). The window is now "nil means this response carries the
// successful result", and a 1xx does not: net/http resolves a 100-continue
// internally, so the only 1xx a caller can observe is a protocol switch (101),
// which is not a completed REST response. Flipping it also lets GetBytes and a
// caller's own strict band check delegate to this function wholesale instead of
// keeping a hand-rolled `< 200 ||  >= 300` guard beside it.
func TestCheckHTTPStatus_non_2xx_is_error_even_below_200(t *testing.T) {
	t.Parallel()
	for _, code := range []int{http.StatusContinue, http.StatusSwitchingProtocols, 199} {
		err := httpclient.CheckHTTPStatus(&http.Response{StatusCode: code, Header: http.Header{}})
		var se *httpclient.HTTPStatusError
		if !errors.As(err, &se) || se.Code != code {
			t.Errorf("CheckHTTPStatus(%d) = %v, want *HTTPStatusError{%d} (success is 2xx only)", code, err, code)
		}
	}
}

// TestCheckHTTPStatus_3xx_classification pins the type choice for the v4
// breaking change: a surfaced redirect is a *HTTPStatusError, so it reads
// correctly through every existing classifier — not transient, neither a
// server nor a client error — and passes through LogSafeError
// untouched, since it embeds no URL to redact.
func TestCheckHTTPStatus_3xx_classification(t *testing.T) {
	t.Parallel()
	for _, code := range []int{300, 301, 302, 303, 304, 307, 308, 399} {
		err := httpclient.CheckHTTPStatus(&http.Response{StatusCode: code, Header: http.Header{}})
		var se *httpclient.HTTPStatusError
		if !errors.As(err, &se) {
			t.Fatalf("CheckHTTPStatus(%d) = %T, want *HTTPStatusError", code, err)
		}
		if se.Code != code {
			t.Errorf("CheckHTTPStatus(%d).Code = %d, want %d", code, se.Code, code)
		}
		if httpclient.IsTransient(err) {
			t.Errorf("IsTransient(HTTP %d) = true, want false (a redirect is not a retryable server failure)", code)
		}
		if se.IsServerError() {
			t.Errorf("HTTPStatusError{%d}.IsServerError() = true, want false", code)
		}
		if se.IsClientError() {
			t.Errorf("HTTPStatusError{%d}.IsClientError() = true, want false", code)
		}
		if got := httpclient.LogSafeError(err); got != err {
			t.Errorf("LogSafeError(HTTP %d) = %v, want the error unchanged", code, got)
		}
		if got, want := err.Error(), fmt.Sprintf("HTTP %d", code); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	}
}

// TestIsRetryableStatus pins IsRetryableStatus's verdict code by code, invalid codes outside 100-599 included.
func TestIsRetryableStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		want bool
	}{
		{"408 Request Timeout", http.StatusRequestTimeout, true},
		{"429 Too Many Requests", http.StatusTooManyRequests, true},
		{"500 Internal Server Error", http.StatusInternalServerError, true},
		{"502 Bad Gateway", http.StatusBadGateway, true},
		{"503 Service Unavailable", http.StatusServiceUnavailable, true},
		{"504 Gateway Timeout", http.StatusGatewayTimeout, true},
		{"507 Insufficient Storage", http.StatusInsufficientStorage, true},
		{"520 unregistered", 520, true},
		{"599 top of 5xx", 599, true},
		{"501 Not Implemented", http.StatusNotImplemented, false},
		{"505 HTTP Version Not Supported", http.StatusHTTPVersionNotSupported, false},
		{"506 Variant Also Negotiates", http.StatusVariantAlsoNegotiates, false},
		{"508 Loop Detected", http.StatusLoopDetected, false},
		{"510 Not Extended", http.StatusNotExtended, false},
		{"511 Network Authentication Required", http.StatusNetworkAuthenticationRequired, false},
		{"600 invalid", 600, false},
		{"999 invalid", 999, false},
		{"000 invalid", 0, false},
		{"099 invalid", 99, false},
		{"200 OK", http.StatusOK, false},
		{"204 No Content", http.StatusNoContent, false},
		{"301 Moved Permanently", http.StatusMovedPermanently, false},
		{"304 Not Modified", http.StatusNotModified, false},
		{"400 Bad Request", http.StatusBadRequest, false},
		{"401 Unauthorized", http.StatusUnauthorized, false},
		{"403 Forbidden", http.StatusForbidden, false},
		{"404 Not Found", http.StatusNotFound, false},
		{"409 Conflict", http.StatusConflict, false},
		{"499 top of 4xx", 499, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := httpclient.IsRetryableStatus(tt.code); got != tt.want {
				t.Errorf("IsRetryableStatus(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

// TestIsRetryableStatus_agrees_with_the_GetBytes_loop is the delegation pin:
// the exported predicate is the SAME decision the built-in retry makes, so for
// every status code on the wire the door must retry exactly when the predicate
// says retryable. It sweeps every three-digit code net/http parses from a
// status line (000-999) against the door's real behavior — a second attempt is
// made or it is not — rather than against a second copy of the table, so
// re-inlining a divergent guard in getAttempt fails here even though the
// predicate alone would still look right.
func TestIsRetryableStatus_agrees_with_the_GetBytes_loop(t *testing.T) {
	t.Parallel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for code := range 1000 {
		var calls atomic.Int32
		client := &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{
					StatusCode: code,
					Body:       io.NopCloser(strings.NewReader("body")),
					Header:     http.Header{},
				}, nil
			}),
		}
		// Two attempts: a retryable status spends both, a settled one returns
		// after the first, so the observed call count IS the door's verdict.
		_, _ = httpclient.GetBytes(t.Context(), client, "http://example.com/sweep",
			httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Microsecond), httpclient.WithLogger(quiet))
		retried := calls.Load() == 2
		if want := httpclient.IsRetryableStatus(code); retried != want {
			t.Errorf("HTTP %d: GetBytes retried = %v, IsRetryableStatus = %v (the door and the predicate must agree)",
				code, retried, want)
		}
	}
}

func TestStatusError_Error(t *testing.T) {
	err := &httpclient.StatusError{Code: 503, URL: "http://example.com/x"}
	want := "HTTP 503 from http://example.com/x"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestStatusError_IsRateLimited(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{429, true},
		{500, false},
		{503, false},
		{400, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.code), func(t *testing.T) {
			err := &httpclient.StatusError{Code: tt.code}
			if got := errors.Is(err, httpclient.ErrRateLimited); got != tt.want {
				t.Errorf("errors.Is(%d, ErrRateLimited) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestStatusError_IsServerError(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{500, true},
		{502, true},
		{503, true},
		{599, true},
		{600, true},
		{429, false},
		{400, false},
		{499, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.code), func(t *testing.T) {
			err := &httpclient.StatusError{Code: tt.code}
			if got := errors.Is(err, httpclient.ErrServerError); got != tt.want {
				t.Errorf("errors.Is(%d, ErrServerError) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestStatusError_Is_unrelated_target_returns_false(t *testing.T) {
	se := &httpclient.StatusError{Code: http.StatusTooManyRequests, URL: "http://example.com"}
	if errors.Is(se, io.EOF) {
		t.Error("errors.Is(StatusError{429}, io.EOF) = true, want false")
	}
	if errors.Is(se, context.Canceled) {
		t.Error("errors.Is(StatusError{429}, context.Canceled) = true, want false")
	}
}
