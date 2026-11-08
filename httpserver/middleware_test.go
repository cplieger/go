package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpserver"
)

// recordingMW returns a middleware that appends "<name>-in" before calling next
// and "<name>-out" after, so a Chain's wrapping order is observable.
func recordingMW(log *[]string, name string) httpserver.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*log = append(*log, name+"-in")
			next.ServeHTTP(w, r)
			*log = append(*log, name+"-out")
		})
	}
}

func TestChain_firstListedIsOutermost(t *testing.T) {
	var log []string
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		log = append(log, "handler")
	})

	chained := httpserver.Chain(handler,
		recordingMW(&log, "A"),
		recordingMW(&log, "B"),
		recordingMW(&log, "C"),
	)
	serve(chained, http.MethodGet, "/", nil)

	// A is listed first, so it wraps outermost: first in, last out.
	want := []string{"A-in", "B-in", "C-in", "handler", "C-out", "B-out", "A-out"}
	if !slices.Equal(log, want) {
		t.Errorf("chain order = %v, want %v", log, want)
	}
}

func TestChain_skipsNilMiddleware(t *testing.T) {
	var log []string
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		log = append(log, "handler")
	})

	chained := httpserver.Chain(handler, nil, recordingMW(&log, "A"), nil)
	serve(chained, http.MethodGet, "/", nil)

	want := []string{"A-in", "handler", "A-out"}
	if !slices.Equal(log, want) {
		t.Errorf("chain order = %v, want %v (nil middleware must be skipped)", log, want)
	}
}

func TestChain_emptyRunsHandler(t *testing.T) {
	var called bool
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	serve(httpserver.Chain(handler), http.MethodGet, "/", nil)
	if !called {
		t.Error("Chain with no middlewares did not run the handler")
	}
}

func TestRecoverer_panicWrites500AndLogs(t *testing.T) {
	logCap := &captureHandler{}
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := httpserver.Recoverer(httpserver.WithRecoverLogger(slog.New(logCap)))(panicky)

	rr := serve(h, http.MethodGet, "/x", nil)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Code != "internal_error" || body.Error != "internal server error" {
		t.Errorf("body = %+v, want code=internal_error error='internal server error'", body)
	}

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	if recs[0].Level != slog.LevelError {
		t.Errorf("log level = %v, want Error", recs[0].Level)
	}
	m := attrsOf(recs[0])
	if m["panic"] != "boom" {
		t.Errorf("panic attr = %v, want boom", m["panic"])
	}
	if s, ok := m["stack"].(string); !ok || s == "" {
		t.Errorf("stack attr = %v, want a non-empty string", m["stack"])
	}
}

func TestRecoverer_repanicsErrAbortHandler(t *testing.T) {
	logCap := &captureHandler{}
	aborter := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	h := httpserver.Recoverer(httpserver.WithRecoverLogger(slog.New(logCap)))(aborter)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		serve(h, http.MethodGet, "/x", nil)
	}()

	if recovered != http.ErrAbortHandler {
		t.Errorf("recovered %v, want http.ErrAbortHandler (it must be re-panicked)", recovered)
	}
	if n := len(logCap.snapshot()); n != 0 {
		t.Errorf("ErrAbortHandler was logged %d times, want 0 (silent abort)", n)
	}
}

func TestRecoverer_panicHookFires(t *testing.T) {
	var (
		hookCalls int
		gotVal    any
		gotStack  []byte
	)
	hook := func(v any, stack []byte) {
		hookCalls++
		gotVal, gotStack = v, stack
	}
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	h := httpserver.Recoverer(
		httpserver.WithRecoverLogger(discardLogger()),
		httpserver.WithPanicHook(hook),
	)(panicky)

	rr := serve(h, http.MethodGet, "/x", nil)

	if hookCalls != 1 {
		t.Fatalf("hook called %d times, want 1", hookCalls)
	}
	if gotVal != "kaboom" {
		t.Errorf("hook value = %v, want kaboom", gotVal)
	}
	if len(gotStack) == 0 {
		t.Error("hook received an empty stack")
	}
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (hook does not suppress the response)", rr.Code)
	}
}

func TestRecoverer_nilPanicHookKeepsPriorHook(t *testing.T) {
	var hookCalls int
	hook := func(any, []byte) { hookCalls++ }
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := httpserver.Recoverer(
		httpserver.WithRecoverLogger(discardLogger()),
		httpserver.WithPanicHook(hook),
		httpserver.WithPanicHook(nil),
	)(panicky)

	rr := serve(h, http.MethodGet, "/x", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if hookCalls != 1 {
		t.Errorf("hook called %d times, want 1 (nil hook option must be ignored)", hookCalls)
	}
}

func TestRecoverer_noPanicPassesThrough(t *testing.T) {
	logCap := &captureHandler{}
	var hookCalls int
	h := httpserver.Recoverer(
		httpserver.WithRecoverLogger(slog.New(logCap)),
		httpserver.WithPanicHook(func(any, []byte) { hookCalls++ }),
	)(statusHandler(http.StatusTeapot))

	rr := serve(h, http.MethodGet, "/x", nil)

	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d (passed through)", rr.Code, http.StatusTeapot)
	}
	if n := len(logCap.snapshot()); n != 0 {
		t.Errorf("logged %d lines without a panic, want 0", n)
	}
	if hookCalls != 0 {
		t.Errorf("hook fired %d times without a panic, want 0", hookCalls)
	}
}

func TestRecoverer_nilOptionIgnoredAndDefaultLogger(t *testing.T) {
	swapDefaultLogger(t, discardLogger())

	// A nil option must be skipped; with no WithRecoverLogger the slog.Default()
	// fallback is exercised.
	h := httpserver.Recoverer(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("x")
	}))
	rr := serve(h, http.MethodGet, "/x", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestRecoverer_insideLoggingLogsStatus500(t *testing.T) {
	logCap := &captureHandler{}
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })

	// Logging outermost, Recoverer inside (the documented arrangement): the
	// recovered 500 is recorded before Logging's deferred access line runs,
	// so the request logs as 500 rather than a misleading 200. The recoverer uses
	// a discard logger, so logCap captures only the access line.
	h := httpserver.Chain(panicky,
		httpserver.Logging(httpserver.WithLogger(slog.New(logCap))),
		httpserver.Recoverer(httpserver.WithRecoverLogger(discardLogger())),
	)

	rr := serve(h, http.MethodGet, "/x", nil)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1 (the access line)", len(recs))
	}
	if recs[0].Message != "http" {
		t.Fatalf("captured message = %q, want the access line %q", recs[0].Message, "http")
	}
	if m := attrsOf(recs[0]); m["status"] != int64(http.StatusInternalServerError) {
		t.Errorf("access line status = %v, want 500 (Recoverer inside Logging)", m["status"])
	}

	// The error body carries the request id threaded by Logging.
	echoed := rr.Header().Get(httpserver.HeaderRequestID)
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.RequestID == "" || body.RequestID != echoed {
		t.Errorf("error body request_id = %q, want the echoed id %q", body.RequestID, echoed)
	}
}

func TestRecoverer_committedResponseNotDoubleWritten(t *testing.T) {
	logCap := &captureHandler{}
	// A handler that commits a 200 with a partial body and then panics. The
	// recoverer must not write a second status or body onto the already-committed
	// response (which would corrupt the body and, under an outer Logging, mislog
	// the status), but it must still log the panic.
	panicky := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("boom after commit")
	})
	h := httpserver.Recoverer(httpserver.WithRecoverLogger(slog.New(logCap)))(panicky)

	// Observe the response through a StatusRecorder so the recorded status can be
	// asserted; Recoverer detects its Wrote() accessor and reuses it rather than
	// double-wrapping.
	rec := httpserver.NewStatusRecorder(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	// The panic must not escape Recoverer.
	func() {
		defer func() {
			if v := recover(); v != nil {
				t.Fatalf("panic escaped Recoverer: %v", v)
			}
		}()
		h.ServeHTTP(rec, req)
	}()

	if rec.Status() != http.StatusOK {
		t.Errorf("recorded status = %d, want 200 (a committed response must not be overwritten)", rec.Status())
	}
	inner, ok := rec.Unwrap().(*httptest.ResponseRecorder)
	if !ok {
		t.Fatalf("Unwrap() = %T, want *httptest.ResponseRecorder", rec.Unwrap())
	}
	if inner.Code != http.StatusOK {
		t.Errorf("underlying status = %d, want 200 (no second WriteHeader)", inner.Code)
	}
	if body := inner.Body.String(); body != "partial" {
		t.Errorf("body = %q, want %q (no 500 body appended to a committed response)", body, "partial")
	}

	// The panic is still logged even though the 500 body was skipped.
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1 (the recovered panic)", len(recs))
	}
	if recs[0].Level != slog.LevelError {
		t.Errorf("log level = %v, want Error", recs[0].Level)
	}
	if m := attrsOf(recs[0]); m["panic"] != "boom after commit" {
		t.Errorf("panic attr = %v, want 'boom after commit'", m["panic"])
	}
}

func TestSecurityHeaders_defaults(t *testing.T) {
	h := httpserver.SecurityHeaders()(okHandler())
	rr := serve(h, http.MethodGet, "/", nil)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (next handler must run)", rr.Code)
	}
	defaults := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for k, want := range defaults {
		if got := rr.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{
		"Content-Security-Policy",
		"Permissions-Policy",
		"Cross-Origin-Opener-Policy",
		"Strict-Transport-Security",
	} {
		if got := rr.Header().Get(k); got != "" {
			t.Errorf("%s = %q, want unset by default", k, got)
		}
	}
}

func TestSecurityHeaders_optionsOverride(t *testing.T) {
	h := httpserver.SecurityHeaders(
		httpserver.WithCSP("default-src 'self'"),
		httpserver.WithFrameOptions("SAMEORIGIN"),
		httpserver.WithReferrerPolicy("no-referrer"),
		httpserver.WithPermissionsPolicy("geolocation=(), camera=()"),
		httpserver.WithCOOP("same-origin"),
	)(okHandler())
	rr := serve(h, http.MethodGet, "/", nil)

	want := map[string]string{
		"X-Content-Type-Options":     "nosniff", // set by default (a handler could still override it)
		"Content-Security-Policy":    "default-src 'self'",
		"X-Frame-Options":            "SAMEORIGIN",
		"Referrer-Policy":            "no-referrer",
		"Permissions-Policy":         "geolocation=(), camera=()",
		"Cross-Origin-Opener-Policy": "same-origin",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestSecurityHeaders_emptyValueOmitsHeader(t *testing.T) {
	h := httpserver.SecurityHeaders(
		httpserver.WithFrameOptions(""),
		httpserver.WithReferrerPolicy(""),
	)(okHandler())
	rr := serve(h, http.MethodGet, "/", nil)

	if got := rr.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options = %q, want omitted when cleared", got)
	}
	if got := rr.Header().Get("Referrer-Policy"); got != "" {
		t.Errorf("Referrer-Policy = %q, want omitted when cleared", got)
	}
	// The fixed nosniff default is unaffected.
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestSecurityHeaders_hsts(t *testing.T) {
	cases := []struct {
		name string
		opts []httpserver.SecurityOption
		want string
	}{
		{"off by default", nil, ""},
		{
			"max-age only",
			[]httpserver.SecurityOption{httpserver.WithHSTS(httpserver.HSTS{MaxAge: 24 * time.Hour})},
			"max-age=86400",
		},
		{
			"includeSubDomains",
			[]httpserver.SecurityOption{httpserver.WithHSTS(httpserver.HSTS{MaxAge: 24 * time.Hour, IncludeSubdomains: true})},
			"max-age=86400; includeSubDomains",
		},
		{
			"includeSubDomains and preload",
			[]httpserver.SecurityOption{httpserver.WithHSTS(httpserver.HSTS{MaxAge: 365 * 24 * time.Hour, IncludeSubdomains: true, Preload: true})},
			"max-age=31536000; includeSubDomains; preload",
		},
		{
			// The relational guard: the preload list refuses a submission
			// without includeSubDomains, so the directive is dropped rather
			// than advertising a posture the operator does not get.
			"preload without subdomains drops the directive",
			[]httpserver.SecurityOption{httpserver.WithHSTS(httpserver.HSTS{MaxAge: time.Hour, Preload: true})},
			"max-age=3600",
		},
		{
			"negative max-age clamped to zero",
			[]httpserver.SecurityOption{httpserver.WithHSTS(httpserver.HSTS{MaxAge: -1 * time.Hour})},
			"max-age=0",
		},
		{
			// The zero value is meaningful and is NOT "off": it tells a browser
			// to forget the policy. Leaving the option out is off.
			"zero value forgets the policy",
			[]httpserver.SecurityOption{httpserver.WithHSTS(httpserver.HSTS{})},
			"max-age=0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := httpserver.SecurityHeaders(tc.opts...)(okHandler())
			rr := serve(h, http.MethodGet, "/", nil)
			if got := rr.Header().Get("Strict-Transport-Security"); got != tc.want {
				t.Errorf("Strict-Transport-Security = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHSTS_Validate(t *testing.T) {
	cases := []struct {
		name    string
		policy  httpserver.HSTS
		wantErr error
	}{
		{"zero value", httpserver.HSTS{}, nil},
		{"max-age only", httpserver.HSTS{MaxAge: time.Hour}, nil},
		{"subdomains", httpserver.HSTS{MaxAge: time.Hour, IncludeSubdomains: true}, nil},
		{
			"preload with subdomains",
			httpserver.HSTS{MaxAge: 365 * 24 * time.Hour, IncludeSubdomains: true, Preload: true},
			nil,
		},
		{
			// Relational rule 1: the browsers' preload list refuses a
			// submission without includeSubDomains.
			"preload without subdomains",
			httpserver.HSTS{MaxAge: 365 * 24 * time.Hour, Preload: true},
			httpserver.ErrHSTSPreloadWithoutSubdomains,
		},
		{
			// Relational rule 2: the list also requires a one-year max-age, so
			// a preload policy under it would never be accepted.
			"preload with a short max-age",
			httpserver.HSTS{MaxAge: time.Hour, IncludeSubdomains: true, Preload: true},
			httpserver.ErrHSTSPreloadMaxAgeTooShort,
		},
		{
			// The unit mistake this catches in practice: 31536000 assigned to a
			// Duration field is nanoseconds, so the header would have said
			// max-age=0 ("forget me") beside "preload me".
			"preload with seconds mistaken for a Duration",
			httpserver.HSTS{MaxAge: 31536000, IncludeSubdomains: true, Preload: true},
			httpserver.ErrHSTSPreloadMaxAgeTooShort,
		},
		{
			// Exactly at the floor is accepted.
			"preload at exactly one year",
			httpserver.HSTS{MaxAge: 365 * 24 * time.Hour, IncludeSubdomains: true, Preload: true},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.Validate(); !errors.Is(got, tc.wantErr) {
				t.Errorf("Validate() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

// TestWithHSTS_preloadWithoutSubdomainsWarns pins the lenient half of the guard:
// a SecurityOption cannot return an error, so the contradiction is reported
// through the log while HSTS.Validate stays the strict door for a caller that
// wants to refuse to boot. The header itself is asserted in
// TestSecurityHeaders_hsts (the directive is dropped, not repaired).
func TestWithHSTS_preloadWithoutSubdomainsWarns(t *testing.T) {
	logCap := &captureHandler{}
	swapDefaultLogger(t, slog.New(logCap))

	httpserver.WithHSTS(httpserver.HSTS{MaxAge: 365 * 24 * time.Hour, Preload: true})

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want 1 warn naming the contradiction", len(recs))
	}
	if recs[0].Level != slog.LevelWarn {
		t.Errorf("level = %v, want Warn", recs[0].Level)
	}
	m := attrsOf(recs[0])
	if m["error"] == nil {
		t.Error("missing error attr; the warn must carry the verdict Validate returns")
	}
	if m["fix"] == nil {
		t.Error("missing fix attr; the warn must name the field to set")
	}
}

// TestWithHSTS_validPolicyIsSilent keeps the warn a signal rather than noise: a
// policy Validate accepts logs nothing at wiring time.
func TestWithHSTS_validPolicyIsSilent(t *testing.T) {
	logCap := &captureHandler{}
	swapDefaultLogger(t, slog.New(logCap))

	httpserver.WithHSTS(httpserver.HSTS{MaxAge: 365 * 24 * time.Hour, IncludeSubdomains: true, Preload: true})
	httpserver.WithHSTS(httpserver.HSTS{})

	if recs := logCap.snapshot(); len(recs) != 0 {
		t.Errorf("got %d log records for valid policies, want none", len(recs))
	}
}

func TestSecurityHeaders_nilOptionIgnored(t *testing.T) {
	h := httpserver.SecurityHeaders(nil)(okHandler())
	rr := serve(h, http.MethodGet, "/", nil)
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY (nil option must be skipped)", got)
	}
}

func TestLogging_composesInChain(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Chain(okHandler(), httpserver.Logging(httpserver.WithLogger(slog.New(logCap))))

	rr := serve(h, http.MethodGet, "/composed", nil)

	if !httpserver.ValidRequestID(rr.Header().Get(httpserver.HeaderRequestID)) {
		t.Error("Logging in a Chain did not echo a valid request id")
	}
	recs := logCap.snapshot()
	if len(recs) != 1 || recs[0].Message != "http" {
		t.Fatalf("got %d records (msg of first: %q), want 1 access line", len(recs),
			firstMessage(recs))
	}
	if m := attrsOf(recs[0]); m["path"] != "/composed" {
		t.Errorf("path attr = %v, want /composed", m["path"])
	}
}

func TestLogging_passesOptionsThrough(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Chain(okHandler(), httpserver.Logging(
		httpserver.WithLogger(slog.New(logCap)),
		httpserver.WithSkipPaths("/skip"),
	))

	serve(h, http.MethodGet, "/skip", nil)

	if n := len(logCap.snapshot()); n != 0 {
		t.Errorf("skip path emitted %d lines through Logging(), want 0 (option threaded)", n)
	}
}

func TestClientIP(t *testing.T) {
	trusted := func(cidrs ...string) []netip.Prefix {
		out := make([]netip.Prefix, 0, len(cidrs))
		for _, c := range cidrs {
			out = append(out, netip.MustParsePrefix(c))
		}
		return out
	}

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		xRealIP    string
		trusted    []netip.Prefix
		want       string
	}{
		// (a) No trusted proxies: the socket peer is the only trustworthy source,
		// returned even when an X-Forwarded-For header is present. A client
		// cannot spoof its way past the default.
		{"no trusted set returns peer, XFF ignored", "203.0.113.5:1234", "1.2.3.4", "", nil, "203.0.113.5"},
		// (b) Trusted single-hop proxy: the client sent a spoofed leftmost entry
		// and the proxy appended the real peer on the right. Walking right-to-left,
		// the first untrusted entry (the appended real client) wins and the spoof
		// is ignored.
		{"trusted peer returns real right client not spoofed left", "10.0.0.1:9999", "1.2.3.4, 198.51.100.9", "", trusted("10.0.0.0/8"), "198.51.100.9"},
		// Whitespace around entries is trimmed; the appended trusted hop on the
		// right is skipped and the real client returned.
		{"trusted peer trims XFF whitespace and skips trusted hop", "10.0.0.1:9999", "  1.2.3.4 , 10.0.0.1", "", trusted("10.0.0.0/8"), "1.2.3.4"},
		// (c) Multi-hop trusted chain: the two rightmost entries are our own
		// proxies and are skipped; the leftmost untrusted entry is the client.
		{"trusted multi-hop skips trusted proxies to client", "10.0.0.1:9999", "1.2.3.4, 10.0.0.2, 10.0.0.3", "", trusted("10.0.0.0/8"), "1.2.3.4"},
		// A malformed entry at the right boundary stops the walk: nothing further
		// left can be trusted, so the peer is returned.
		{"trusted peer, malformed XFF boundary, returns peer", "10.0.0.1:9999", "garbage", "", trusted("10.0.0.0/8"), "10.0.0.1"},
		// Trusted peer but no forwarded header: the peer is the client.
		{"trusted peer, no forwarded header, uses peer", "10.0.0.1:9999", "", "", trusted("10.0.0.0/8"), "10.0.0.1"},
		// X-Real-IP is no longer consulted (removed as a client-settable spoof
		// vector): a trusted peer with only X-Real-IP set still returns the peer.
		{"trusted peer ignores removed X-Real-IP", "10.0.0.1:9999", "", "5.6.7.8", trusted("10.0.0.0/8"), "10.0.0.1"},
		// (d) Untrusted direct peer: forwarded headers are attacker-controlled and
		// ignored, including a spoofed XFF and X-Real-IP.
		{"untrusted peer ignores XFF and X-Real-IP", "203.0.113.5:1234", "1.2.3.4", "9.9.9.9", trusted("10.0.0.0/8"), "203.0.113.5"},
		// (e) IPv6 trusted peer parsed from a bracketed host:port; the untrusted
		// XFF entry is the client.
		{"IPv6 trusted peer honors XFF", "[::1]:8080", "2001:db8::1", "", trusted("::1/128"), "2001:db8::1"},
		// (e) An unparseable peer can never be trusted, so XFF is ignored with or
		// without a set, and the zero address's text is returned.
		{"malformed RemoteAddr, no trusted set", "not-an-addr", "1.2.3.4", "", nil, "invalid IP"},
		{"malformed RemoteAddr with trusted set", "weird", "1.2.3.4", "", trusted("10.0.0.0/8"), "invalid IP"},
		// (f) An untrusted IPv4-mapped peer reads as its IPv4 address.
		{"untrusted mapped peer is unmapped", "[::ffff:203.0.113.5]:1234", "1.2.3.4", "", trusted("10.0.0.0/8"), "203.0.113.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xRealIP != "" {
				req.Header.Set("X-Real-IP", tc.xRealIP)
			}
			if got := httpserver.ClientIP(req, tc.trusted...); got != tc.want {
				t.Errorf("ClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientIP_multiLineXFF proves ClientIP treats multiple X-Forwarded-For
// header LINES as the single comma-joined value RFC 7230 defines, rather than
// reading only the first line (the old Header.Get behavior). A client sends a
// spoofed first XFF line; a trusted proxy that appends the peer it observed as
// a SEPARATE header line (instead of comma-appending) must not let the spoofed
// first line win. The right-to-left walk must still select the real appended
// entry, closing the spoof gap.
func TestClientIP_multiLineXFF(t *testing.T) {
	trusted := netip.MustParsePrefix("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:9999" // immediate peer is a trusted proxy
	// Two separate X-Forwarded-For header lines: the client's spoofed value
	// first, then the real client the trusted proxy observed and appended.
	req.Header.Add("X-Forwarded-For", "1.2.3.4")      // spoofed by the client
	req.Header.Add("X-Forwarded-For", "198.51.100.9") // real peer, added by proxy
	if got := httpserver.ClientIP(req, trusted); got != "198.51.100.9" {
		t.Errorf("ClientIP() with multi-line XFF = %q, want 198.51.100.9 (rightmost untrusted entry, not the spoofed first line)", got)
	}
}

func TestRouteTimeout_fastHandlerPasses(t *testing.T) {
	h := httpserver.RouteTimeout(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}), 5*time.Second, "too slow")

	rr := serve(h, http.MethodGet, "/", nil)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if rr.Body.String() != "hello" {
		t.Errorf("body = %q, want hello", rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain (fast handler not relabeled)", ct)
	}
}

func TestRouteTimeout_slowHandlerTimesOut(t *testing.T) {
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // unblocks when TimeoutHandler cancels the request
	})
	h := httpserver.RouteTimeout(slow, 20*time.Millisecond, "server too slow")

	rr := serve(h, http.MethodGet, "/", nil)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if ns := rr.Header().Get("X-Content-Type-Options"); ns != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", ns)
	}
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("timeout body is not JSON: %v (body=%q)", err, rr.Body.String())
	}
	if body.Code != "timeout" || body.Error != "server too slow" {
		t.Errorf("body = %+v, want code=timeout error='server too slow'", body)
	}
}

// TestRouteTimeout_doesNotStopTheHandler pins the godoc's warning: the 503
// goes out while the handler is still running, and the handler goes on with
// its context cancelled, so a store call that ignores ctx would still commit.
func TestRouteTimeout_doesNotStopTheHandler(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan error, 1)
	cancelled := false
	h := httpserver.RouteTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		cancelled = r.Context().Err() != nil
		_, err := w.Write([]byte("ok"))
		finished <- err
	}), 20*time.Millisecond, "")

	got := make(chan *httptest.ResponseRecorder, 1)
	go func() { got <- serve(h, http.MethodPost, "/", nil) }()
	var rr *httptest.ResponseRecorder
	select {
	case rr = <-got:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("RouteTimeout held the response until the handler returned; want the 503 at its deadline")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the 503 before the handler finished", rr.Code)
	}
	close(release)
	if err := <-finished; !errors.Is(err, http.ErrHandlerTimeout) {
		t.Errorf("the handler's write after the 503 = %v, want http.ErrHandlerTimeout", err)
	}
	if !cancelled {
		t.Error("the handler ran on after the 503 with a live context; want it cancelled")
	}
}

// TestRouteDeadline_handlerAnswersFromItsOutcome: the handler sees a context
// deadline of d, and after it passes the client gets the handler's own answer,
// written straight through, never a 503 of the wrapper's.
func TestRouteDeadline_handlerAnswersFromItsOutcome(t *testing.T) {
	const d = 20 * time.Millisecond
	var deadlineIn time.Duration
	var errAtEntry, errAtEnd error
	h := httpserver.RouteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dl, ok := r.Context().Deadline()
		if !ok {
			t.Error("the handler's context has no deadline")
		}
		deadlineIn = time.Until(dl)
		errAtEntry = r.Context().Err()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			t.Error("the handler's context never ended")
		}
		errAtEnd = r.Context().Err()
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusConflict) // the handler rolled back and says so
		_, _ = w.Write([]byte("rolled back"))
	}), d)

	rr := serve(h, http.MethodPost, "/", nil)
	if rr.Code != http.StatusConflict || rr.Body.String() != "rolled back" || rr.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("response = %d %q %q, want the handler's own 409", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	if deadlineIn <= 0 || deadlineIn > d {
		t.Errorf("deadline %v ahead at entry, want within (0, %v]", deadlineIn, d)
	}
	if errAtEntry != nil || !errors.Is(errAtEnd, context.DeadlineExceeded) {
		t.Errorf("context error at entry %v and at the end %v, want nil then DeadlineExceeded", errAtEntry, errAtEnd)
	}
}

type ctxKey struct{}

// TestRouteDeadline_derivesFromTheRequestContext: the deadline is added to the
// request's own context, so its values and a client disconnect reach h.
func TestRouteDeadline_derivesFromTheRequestContext(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "req-1"))
	defer cancelParent()
	var value any
	var endErr error
	h := httpserver.RouteDeadline(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		value = r.Context().Value(ctxKey{})
		cancelParent() // the client went away
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		endErr = r.Context().Err()
	}), time.Hour)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(parent, http.MethodPost, "/", nil))
	if value != "req-1" {
		t.Errorf("request context value = %v, want req-1", value)
	}
	if !errors.Is(endErr, context.Canceled) {
		t.Errorf("after the client went away the handler's context error = %v, want context.Canceled", endErr)
	}
}

// TestRouteDeadline_panicReachesTheCaller: h runs on the caller's goroutine, so
// a panic reaches an outer Recoverer instead of killing the process.
func TestRouteDeadline_panicReachesTheCaller(t *testing.T) {
	h := httpserver.RouteDeadline(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), time.Second)
	defer func() {
		if p := recover(); p != "boom" {
			t.Errorf("recovered %v, want the handler's panic", p)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	t.Error("the handler's panic did not reach the caller")
}

// TestRouteDeadline_writesGoStraightThrough: the handler gets the writer
// RouteDeadline was given, with no writer of its own in between.
func TestRouteDeadline_writesGoStraightThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	h := httpserver.RouteDeadline(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if w != http.ResponseWriter(rec) {
			t.Errorf("the handler got %T, want the server's own writer", w)
		}
	}), time.Second)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
}

func TestRouteDeadline_nonPositiveDurationReturnsHandlerUnwrapped(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		h := httpserver.RouteDeadline(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			if _, ok := r.Context().Deadline(); ok {
				t.Errorf("d=%v: the handler's context has a deadline, want none", d)
			}
		}), d)
		serve(h, http.MethodGet, "/", nil)
	}
}

func TestRouteTimeout_emptyMsgDefaults(t *testing.T) {
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	h := httpserver.RouteTimeout(slow, 10*time.Millisecond, "")

	rr := serve(h, http.MethodGet, "/", nil)

	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Error != "request timed out" {
		t.Errorf("default timeout message = %q, want 'request timed out'", body.Error)
	}
	if body.Code != "timeout" {
		t.Errorf("code = %q, want timeout", body.Code)
	}
}

func TestRouteTimeout_fastHandler503KeepsItsContentType(t *testing.T) {
	// A handler that finishes in time and emits its own 503 with a Content-Type
	// must not be relabeled as JSON: the wrapper only touches an unlabeled 503.
	h := httpserver.RouteTimeout(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html>down</html>"))
	}), 5*time.Second, "too slow")

	rr := serve(h, http.MethodGet, "/", nil)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html" {
		t.Errorf("Content-Type = %q, want text/html (handler's own 503 preserved)", ct)
	}
	if rr.Body.String() != "<html>down</html>" {
		t.Errorf("body = %q, want the handler's own body", rr.Body.String())
	}
}

func TestRouteTimeout_fastHandler503WithoutContentTypeIsRelabeled(t *testing.T) {
	// A handler that finishes in time but emits a 503 with NO Content-Type is
	// indistinguishable from a genuine timeout envelope, so the wrapper relabels
	// it application/json (+ nosniff). This documents and locks in that behavior;
	// a handler that wants to avoid it must set its own Content-Type (see the
	// sibling test that emits text/html).
	h := httpserver.RouteTimeout(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("nope"))
	}), 5*time.Second, "too slow")

	rr := serve(h, http.MethodGet, "/", nil)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (an unlabeled 503 is relabeled)", ct)
	}
	if ns := rr.Header().Get("X-Content-Type-Options"); ns != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", ns)
	}
	// Only the headers are added; the handler's own body bytes are unchanged.
	if rr.Body.String() != "nope" {
		t.Errorf("body = %q, want the handler's own body %q", rr.Body.String(), "nope")
	}
}

func TestRouteTimeout_nonPositiveDurationReturnsHandlerUnwrapped(t *testing.T) {
	// A non-positive timeout disables the wrapper: the original handler is
	// returned unchanged, so its response passes through untouched (no JSON
	// relabel, no 503, no buffering).
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("direct"))
	})
	for _, d := range []time.Duration{0, -time.Second} {
		h := httpserver.RouteTimeout(base, d, "unused")
		rr := serve(h, http.MethodGet, "/", nil)
		if rr.Code != http.StatusOK {
			t.Errorf("d=%v: status = %d, want 200", d, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "text/plain" {
			t.Errorf("d=%v: Content-Type = %q, want text/plain (handler returned unwrapped)", d, ct)
		}
		if rr.Body.String() != "direct" {
			t.Errorf("d=%v: body = %q, want direct", d, rr.Body.String())
		}
	}
}

// firstMessage returns the message of the first record, or "" when there are
// none, for clearer failure output.
func firstMessage(recs []slog.Record) string {
	if len(recs) == 0 {
		return ""
	}
	return recs[0].Message
}

// WriteError is the canonical ErrorResponder and the Recoverer default; this
// assertion locks that the default's signature matches the exported type.
var _ httpserver.ErrorResponder = httpserver.WriteError

func TestRecoverer_customResponderRendersNonJSON(t *testing.T) {
	logCap := &captureHandler{}
	// A non-JSON endpoint supplies an ErrorResponder that writes its own content
	// type and body; Recoverer must use it for the 500 in place of the JSON default.
	responder := func(w http.ResponseWriter, _ *http.Request, status int, code httpserver.ErrorCode, msg string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`<error code="` + string(code) + `">` + msg + `</error>`))
	}
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := httpserver.Recoverer(
		httpserver.WithRecoverLogger(slog.New(logCap)),
		httpserver.WithRecoverResponder(responder),
	)(panicky)

	rr := serve(h, http.MethodGet, "/x", nil)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/xml" {
		t.Errorf("Content-Type = %q, want application/xml (custom responder)", ct)
	}
	if got, want := rr.Body.String(), `<error code="internal_error">internal server error</error>`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	// The panic is still logged regardless of the responder.
	if recs := logCap.snapshot(); len(recs) != 1 || recs[0].Level != slog.LevelError {
		t.Errorf("got %d records (want exactly 1 at Error) for the recovered panic", len(recs))
	}
}

func TestRecoverer_nilResponderKeepsJSONDefault(t *testing.T) {
	// A nil responder is ignored, so the JSON WriteError default stands.
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := httpserver.Recoverer(
		httpserver.WithRecoverLogger(discardLogger()),
		httpserver.WithRecoverResponder(nil),
	)(panicky)

	rr := serve(h, http.MethodGet, "/x", nil)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (nil responder keeps the default)", ct)
	}
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Code != "internal_error" || body.Error != "internal server error" {
		t.Errorf("body = %+v, want the default internal_error envelope", body)
	}
}

func TestRecoverer_customResponderNotCalledOnCommittedResponse(t *testing.T) {
	// The commit gate applies to a custom responder too: a handler that commits
	// then panics must not have the responder write a second status or body.
	var responderCalls int
	responder := func(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
		responderCalls++
		httpserver.WriteError(w, r, status, code, msg)
	}
	panicky := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("boom after commit")
	})
	h := httpserver.Recoverer(
		httpserver.WithRecoverLogger(discardLogger()),
		httpserver.WithRecoverResponder(responder),
	)(panicky)

	rr := serve(h, http.MethodGet, "/x", nil)

	if responderCalls != 0 {
		t.Errorf("responder called %d times on a committed response, want 0", responderCalls)
	}
	if rr.Body.String() != "partial" {
		t.Errorf("body = %q, want %q (committed response left untouched)", rr.Body.String(), "partial")
	}
}

func TestRecoverer_panicHookPanicIsIsolated(t *testing.T) {
	logCap := &captureHandler{}
	// The caller's panic hook itself panics. That secondary failure must be
	// isolated: logged with the original request id, and recovery continues so
	// the client still receives the 500 rather than an aborted connection.
	panicHook := func(any, []byte) { panic("hook boom") }
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })

	// Logging outermost so the deferred access line's recorded status can be
	// asserted; both the recoverer and the logger write into logCap, so records
	// are filtered by message.
	h := httpserver.Chain(panicky,
		httpserver.Logging(httpserver.WithLogger(slog.New(logCap))),
		httpserver.Recoverer(
			httpserver.WithRecoverLogger(slog.New(logCap)),
			httpserver.WithPanicHook(panicHook),
		),
	)

	rr := serve(h, http.MethodGet, "/x", nil)

	// The client still receives the JSON 500 despite the hook panicking.
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (a hook panic must not abort the 500)", rr.Code)
	}
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%q)", err, rr.Body.String())
	}
	if body.Code != "internal_error" || body.Error != "internal server error" {
		t.Errorf("body = %+v, want the default internal_error envelope", body)
	}

	// The secondary hook failure is logged with the original request id, and the
	// outer access line records the 500 the client received.
	var accessStatus any
	var hookFailID, recoveredID string
	sawHookFail := false
	for _, rec := range logCap.snapshot() {
		m := attrsOf(rec)
		switch rec.Message {
		case "http":
			accessStatus = m["status"]
		case "httpserver: panic hook failed":
			sawHookFail = true
			hookFailID, _ = m["request_id"].(string)
			if m["panic"] != "hook boom" {
				t.Errorf("hook-failure panic attr = %v, want 'hook boom'", m["panic"])
			}
		case "httpserver: recovered from panic":
			recoveredID, _ = m["request_id"].(string)
		}
	}
	if !sawHookFail {
		t.Fatal("no 'httpserver: panic hook failed' log record; the hook panic was not isolated")
	}
	if accessStatus != int64(http.StatusInternalServerError) {
		t.Errorf("access line status = %v, want 500 (Recoverer inside Logging)", accessStatus)
	}
	echoed := rr.Header().Get(httpserver.HeaderRequestID)
	if hookFailID == "" || hookFailID != recoveredID || hookFailID != echoed {
		t.Errorf("hook-failure request_id = %q, want the recovered/echoed id (recovered=%q echoed=%q)",
			hookFailID, recoveredID, echoed)
	}
}

func TestRecoverer_responderPanicBeforeCommitFallsBackToJSON500(t *testing.T) {
	logCap := &captureHandler{}
	// A custom responder that panics BEFORE writing any header or body
	// (pre-commit). The response is still uncommitted, so the recovery must fall
	// back to the default JSON 500 and the client still receives a 500.
	responder := func(http.ResponseWriter, *http.Request, int, httpserver.ErrorCode, string) {
		panic("responder boom")
	}
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })

	h := httpserver.Chain(panicky,
		httpserver.Logging(httpserver.WithLogger(slog.New(logCap))),
		httpserver.Recoverer(
			httpserver.WithRecoverLogger(slog.New(logCap)),
			httpserver.WithRecoverResponder(responder),
		),
	)

	rr := serve(h, http.MethodGet, "/x", nil)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (pre-commit responder panic falls back to JSON 500)", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (fallback default responder)", ct)
	}
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%q)", err, rr.Body.String())
	}
	if body.Code != "internal_error" || body.Error != "internal server error" {
		t.Errorf("body = %+v, want the default internal_error envelope", body)
	}

	// The secondary responder failure is logged with the original request id, and
	// the outer access line records the 500 the client received.
	var accessStatus any
	var responderFailID, recoveredID string
	sawResponderFail := false
	for _, rec := range logCap.snapshot() {
		m := attrsOf(rec)
		switch rec.Message {
		case "http":
			accessStatus = m["status"]
		case "httpserver: recover responder failed":
			sawResponderFail = true
			responderFailID, _ = m["request_id"].(string)
			if m["panic"] != "responder boom" {
				t.Errorf("responder-failure panic attr = %v, want 'responder boom'", m["panic"])
			}
		case "httpserver: recovered from panic":
			recoveredID, _ = m["request_id"].(string)
		}
	}
	if !sawResponderFail {
		t.Fatal("no 'httpserver: recover responder failed' log record; the responder panic was not isolated")
	}
	if accessStatus != int64(http.StatusInternalServerError) {
		t.Errorf("access line status = %v, want 500 (Recoverer inside Logging)", accessStatus)
	}
	echoed := rr.Header().Get(httpserver.HeaderRequestID)
	if responderFailID == "" || responderFailID != recoveredID || responderFailID != echoed {
		t.Errorf("responder-failure request_id = %q, want the recovered/echoed id (recovered=%q echoed=%q)",
			responderFailID, recoveredID, echoed)
	}
}

func TestRouteTimeout_envelopeCarriesContextRequestID(t *testing.T) {
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	h := httpserver.RouteTimeout(slow, 20*time.Millisecond, "server too slow")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(httpserver.WithRequestID(req.Context(), "corr-42"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("timeout body is not JSON: %v (body=%q)", err, rr.Body.String())
	}
	if body.RequestID != "corr-42" {
		t.Errorf("timeout envelope request_id = %q, want %q", body.RequestID, "corr-42")
	}
	if body.Code != "timeout" || body.Error != "server too slow" {
		t.Errorf("body = %+v, want code=timeout error='server too slow'", body)
	}
}

func TestRouteTimeout_envelopeOmitsRequestIDWithoutContext(t *testing.T) {
	// Without an id in the request context (no Logging upstream) the
	// envelope omits the field entirely (omitempty wire shape), matching
	// WriteError's behavior for an id-less request.
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	h := httpserver.RouteTimeout(slow, 10*time.Millisecond, "")

	rr := serve(h, http.MethodGet, "/", nil)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "request_id") {
		t.Errorf("id-less timeout envelope contains request_id: %q", rr.Body.String())
	}
}

func TestRouteTimeout_underLoggingCorrelatesHeaderAndBody(t *testing.T) {
	// The universal-correlation proof: composed under Logging, the minted
	// id on the X-Request-ID response header and the request_id in the timeout
	// envelope are the same id.
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	h := httpserver.Logging(httpserver.WithLogger(discardLogger()))(httpserver.RouteTimeout(slow, 20*time.Millisecond, "server too slow"))

	rr := serve(h, http.MethodGet, "/", nil)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	id := rr.Header().Get(httpserver.HeaderRequestID)
	if !httpserver.ValidRequestID(id) {
		t.Fatalf("echoed header id %q is not valid", id)
	}
	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("timeout body is not JSON: %v (body=%q)", err, rr.Body.String())
	}
	if body.RequestID != id {
		t.Errorf("envelope request_id = %q, header id = %q; want them equal", body.RequestID, id)
	}
}

func TestNoStore_setsHeader(t *testing.T) {
	rr := serve(httpserver.NoStore()(okHandler()), http.MethodGet, "/", nil)

	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
}

func TestNoStore_setsHeaderBeforeHandlerRuns(t *testing.T) {
	// The middleware sets the header before next runs, which is what lets a
	// handler both observe it and replace it.
	var seen string
	h := httpserver.NoStore()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen = w.Header().Get("Cache-Control")
		w.WriteHeader(http.StatusOK)
	}))

	serve(h, http.MethodGet, "/", nil)

	if seen != "no-store" {
		t.Errorf("handler saw Cache-Control = %q, want %q (set before next runs)", seen, "no-store")
	}
}

func TestNoStore_handlerCanOverride(t *testing.T) {
	// The load-bearing property: placement and override ordering stay app-owned,
	// so an inner handler with its own cache policy (a long-lived asset, a
	// cacheable preview) wins over the blanket no-store.
	const assetPolicy = "public, max-age=86400"
	h := httpserver.NoStore()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", assetPolicy)
		w.WriteHeader(http.StatusOK)
	}))

	rr := serve(h, http.MethodGet, "/asset.js", nil)

	if got := rr.Header().Get("Cache-Control"); got != assetPolicy {
		t.Errorf("Cache-Control = %q, want %q (the handler's own policy must win)", got, assetPolicy)
	}
	if vals := rr.Header().Values("Cache-Control"); len(vals) != 1 {
		t.Errorf("Cache-Control values = %v, want exactly one (Set replaces, never appends)", vals)
	}
}

func TestNoStore_composesInChainOnEveryResponse(t *testing.T) {
	// Innermost in the Chain: the header lands on an error envelope too, not
	// only on a 200.
	h := httpserver.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "no such thing")
	}), httpserver.SecurityHeaders(), httpserver.NoStore())

	rr := serve(h, http.MethodGet, "/missing", nil)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on a 404 = %q, want %q", got, "no-store")
	}
}

// TestCrossOriginProtectionComposesWithChain pins the package doc's claim that
// net/http.CrossOriginProtection needs no wrapper here: its Handler method
// satisfies Middleware directly, and SetDenyHandler makes the refusal speak this
// package's error envelope with the request-id correlation.
//
// It exists because the alternative — a httpserver.CrossOriginProtection option or
// middleware — is a recurring proposal, and the answer is a compile-time fact
// plus a wire assertion rather than an opinion. If either ever stops holding,
// the doc recipe is wrong and this says so.
func TestCrossOriginProtectionComposesWithChain(t *testing.T) {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpserver.WriteError(w, r, http.StatusForbidden, "cross_origin_denied", "cross-origin request denied")
	}))

	// The load-bearing line: cop.Handler is passed straight into Chain's
	// ...Middleware parameter, so this compiling IS the proof that no adapter
	// and no wrapper option is needed.
	h := httpserver.Chain(okHandler(),
		httpserver.Logging(httpserver.WithLogger(discardLogger())),
		cop.Handler,
		httpserver.SecurityHeaders(),
	)

	cases := []struct {
		name, method, secFetch, origin string
		wantStatus                     int
	}{
		{"safe GET is allowed even cross-site", http.MethodGet, "cross-site", "", http.StatusOK},
		{"same-origin POST is allowed", http.MethodPost, "same-origin", "", http.StatusOK},
		{"cross-site POST is denied", http.MethodPost, "cross-site", "", http.StatusForbidden},
		{"foreign Origin POST is denied", http.MethodPost, "", "https://evil.example", http.StatusForbidden},
		{"headerless POST is treated as non-browser", http.MethodPost, "", "", http.StatusOK},
	}
	for _, tc := range cases {
		// The bubble goes INSIDE the subtest: t.Run panics with
		// "t.Run called inside synctest bubble" the other way round.
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				srv := httptest.NewTestServer(t, h)
				client := srv.Client()

				req, err := http.NewRequestWithContext(t.Context(), tc.method, srv.URL+"/x", nil)
				if err != nil {
					t.Fatalf("build request: %v", err)
				}
				if tc.secFetch != "" {
					req.Header.Set("Sec-Fetch-Site", tc.secFetch)
				}
				if tc.origin != "" {
					req.Header.Set("Origin", tc.origin)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("%s: %v", tc.method, err)
				}
				defer func() { _ = resp.Body.Close() }()

				if resp.StatusCode != tc.wantStatus {
					t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
				}
				// SecurityHeaders sits inside the deny handler's chain, unlike a
				// refusal net/http answers below the handler (see
				// TestMaxHeaderValueCountRefusalIsInvisibleToTheChain).
				if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
					t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
				}
				if tc.wantStatus != http.StatusForbidden {
					return
				}
				var env httpserver.ErrorResponse
				if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
					t.Fatalf("decode refusal envelope: %v", err)
				}
				if env.Code != "cross_origin_denied" {
					t.Errorf("code = %q, want cross_origin_denied", env.Code)
				}
				if env.RequestID == "" {
					t.Error("request_id is empty: the refusal lost its correlation id")
				}
			})
		})
	}
}
