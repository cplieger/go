package httpserver_test

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cplieger/go/httpserver"
)

func TestValidRequestID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"single", "a", true},
		{"len 64", strings.Repeat("a", 64), true},
		{"len 65", strings.Repeat("a", 65), false},
		{"typical hex", "0123456789abcdef0123456789abcdef", true},
		{"underscore and hyphen", "req_id-123", true},
		{"all classes", "Aa0_-", true},
		{"class edges", "azAZ09", true},
		{"space", "bad id", false},
		{"dot", "bad.id", false},
		{"slash", "a/b", false},
		{"newline", "a\nb", false},
		{"tab", "a\tb", false},
		{"colon", "a:b", false},
		{"unicode", "café", false},
		{"crlf injection", "abc\r\nX-Evil: 1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpserver.ValidRequestID(tc.in); got != tc.want {
				t.Errorf("ValidRequestID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewRequestID_isValidAndHex(t *testing.T) {
	id := httpserver.NewRequestID()
	if !httpserver.ValidRequestID(id) {
		t.Errorf("NewRequestID() = %q, which is not a valid request id", id)
	}
	if len(id) != 32 {
		t.Errorf("NewRequestID() length = %d, want 32 hex chars", len(id))
	}
}

func TestNewRequestID_unique(t *testing.T) {
	a, b := httpserver.NewRequestID(), httpserver.NewRequestID()
	if a == b {
		t.Errorf("two successive ids are equal: %q", a)
	}
}

func TestRequestID_contextRoundTrip(t *testing.T) {
	ctx := httpserver.WithRequestID(t.Context(), "abc")
	if got := httpserver.RequestIDFromContext(ctx); got != "abc" {
		t.Errorf("RequestIDFromContext = %q, want %q", got, "abc")
	}
}

func TestRequestIDFromContext_absent(t *testing.T) {
	if got := httpserver.RequestIDFromContext(t.Context()); got != "" {
		t.Errorf("RequestIDFromContext = %q, want empty", got)
	}
}

// serve runs one request through h and returns the response recorder.
func serve(h http.Handler, method, target string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestLogging_mintsIDWhenAbsent(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpserver.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := httpserver.Logging(httpserver.WithLogger(discardLogger()))(next)

	rr := serve(h, http.MethodGet, "/x", nil)

	echoed := rr.Header().Get(httpserver.HeaderRequestID)
	if !httpserver.ValidRequestID(echoed) {
		t.Errorf("echoed id %q is not valid", echoed)
	}
	if seen != echoed {
		t.Errorf("context id %q != echoed header %q", seen, echoed)
	}
}

func TestLogging_reusesValidInboundID(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpserver.RequestIDFromContext(r.Context())
	})
	h := httpserver.Logging(httpserver.WithLogger(discardLogger()))(next)

	hdr := http.Header{}
	hdr.Set(httpserver.HeaderRequestID, "inbound-123")
	rr := serve(h, http.MethodGet, "/x", hdr)

	if got := rr.Header().Get(httpserver.HeaderRequestID); got != "inbound-123" {
		t.Errorf("echoed id = %q, want inbound-123", got)
	}
	if seen != "inbound-123" {
		t.Errorf("context id = %q, want inbound-123", seen)
	}
}

func TestLogging_replacesInvalidInboundID(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpserver.RequestIDFromContext(r.Context())
	})
	h := httpserver.Logging(httpserver.WithLogger(discardLogger()))(next)

	hdr := http.Header{}
	hdr.Set(httpserver.HeaderRequestID, "bad id!!")
	rr := serve(h, http.MethodGet, "/x", hdr)

	echoed := rr.Header().Get(httpserver.HeaderRequestID)
	if echoed == "bad id!!" {
		t.Error("invalid inbound id was reused instead of replaced")
	}
	if !httpserver.ValidRequestID(echoed) {
		t.Errorf("replacement id %q is not valid", echoed)
	}
	if seen != echoed {
		t.Errorf("context id %q != echoed header %q", seen, echoed)
	}
}

func TestLogging_emitsOneInfoLine(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)))(statusHandler(http.StatusCreated))

	serve(h, http.MethodPost, "/api/thing", nil)

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	r := recs[0]
	if r.Message != "http" {
		t.Errorf("log message = %q, want %q", r.Message, "http")
	}
	if r.Level != slog.LevelInfo {
		t.Errorf("log level = %v, want Info", r.Level)
	}
	m := attrsOf(r)
	if m["method"] != http.MethodPost {
		t.Errorf("method attr = %v, want POST", m["method"])
	}
	if m["path"] != "/api/thing" {
		t.Errorf("path attr = %v, want /api/thing", m["path"])
	}
	if m["status"] != int64(http.StatusCreated) {
		t.Errorf("status attr = %v, want %d", m["status"], http.StatusCreated)
	}
	if id, ok := m["request_id"].(string); !ok || !httpserver.ValidRequestID(id) {
		t.Errorf("request_id attr = %v, want a valid id", m["request_id"])
	}
	if d, ok := m["duration_ms"].(int64); !ok || d < 0 {
		t.Errorf("duration_ms attr = %v, want a non-negative int64", m["duration_ms"])
	}
}

func TestLogging_skipPathOmitsLogLineButEchoesID(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithSkipPaths("/stream"))(okHandler())

	rr := serve(h, http.MethodGet, "/stream", nil)

	if n := len(logCap.snapshot()); n != 0 {
		t.Errorf("skip path emitted %d log lines, want 0", n)
	}
	if !httpserver.ValidRequestID(rr.Header().Get(httpserver.HeaderRequestID)) {
		t.Error("skip path did not echo a valid request id")
	}
}

func TestLogging_nonSkipPathStillLogs(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithSkipPaths("/stream"))(okHandler())

	serve(h, http.MethodGet, "/other", nil)

	if n := len(logCap.snapshot()); n != 1 {
		t.Errorf("non-skip path emitted %d log lines, want 1", n)
	}
}

func TestLogging_recorderCapturesHandlerStatus(t *testing.T) {
	logCap := &captureHandler{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	})
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)))(next)

	rr := serve(h, http.MethodGet, "/x", nil)

	if rr.Code != http.StatusForbidden {
		t.Errorf("response code = %d, want %d", rr.Code, http.StatusForbidden)
	}
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(recs))
	}
	if m := attrsOf(recs[0]); m["status"] != int64(http.StatusForbidden) {
		t.Errorf("logged status = %v, want %d", m["status"], http.StatusForbidden)
	}
}

func TestLogging_defaultLoggerWhenUnset(t *testing.T) {
	swapDefaultLogger(t, discardLogger())

	// No WithLogger option: exercises the slog.Default() fallback.
	h := httpserver.Logging()(okHandler())
	rr := serve(h, http.MethodGet, "/x", nil)
	if !httpserver.ValidRequestID(rr.Header().Get(httpserver.HeaderRequestID)) {
		t.Error("default-logger path did not echo a valid request id")
	}
}

func TestLogging_nilOptionIgnored(t *testing.T) {
	// A nil LogOption must be skipped rather than panic.
	h := httpserver.Logging(nil, httpserver.WithLogger(discardLogger()))(okHandler())
	rr := serve(h, http.MethodGet, "/x", nil)
	if !httpserver.ValidRequestID(rr.Header().Get(httpserver.HeaderRequestID)) {
		t.Error("did not echo a valid request id")
	}
}

func TestLogging_panicStillEmitsAccessLine(t *testing.T) {
	logCap := &captureHandler{}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)))(next)

	// Logging does not recover; the panic propagates out of ServeHTTP.
	// Recover it here so the test can assert the deferred access line still ran.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("handler panic did not propagate through Logging")
			}
		}()
		serve(h, http.MethodGet, "/boom", nil)
	}()

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records after panic, want exactly 1", len(recs))
	}
	if recs[0].Message != "http" {
		t.Errorf("log message = %q, want %q", recs[0].Message, "http")
	}
	if m := attrsOf(recs[0]); m["status"] != int64(http.StatusOK) {
		t.Errorf("panic access line status = %v, want 200 (recorded default)", m["status"])
	}
}

func TestLogging_skipFuncSuppressesLogAndMetricButEchoesID(t *testing.T) {
	logCap := &captureHandler{}
	var metricCalls int
	// A path parameter (/ws/{id}) that an exact WithSkipPaths match cannot cover.
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithSkipFunc(func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/ws/")
	}), httpserver.WithRecordRouteMetric(func(httpserver.RequestMetric) { metricCalls++ }))(okHandler())

	rr := serve(h, http.MethodGet, "/ws/room-42", nil)

	if n := len(logCap.snapshot()); n != 0 {
		t.Errorf("skip-func path emitted %d log lines, want 0", n)
	}
	if metricCalls != 0 {
		t.Errorf("skip-func path called the metric hook %d times, want 0", metricCalls)
	}
	if !httpserver.ValidRequestID(rr.Header().Get(httpserver.HeaderRequestID)) {
		t.Error("skip-func path did not echo a valid request id")
	}
}

func TestLogging_skipFuncFalseStillLogs(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithSkipFunc(func(*http.Request) bool { return false }))(okHandler())

	serve(h, http.MethodGet, "/normal", nil)

	if n := len(logCap.snapshot()); n != 1 {
		t.Errorf("skip-func returning false emitted %d log lines, want 1", n)
	}
}

// hijackableRecorder is an httptest.ResponseRecorder that also implements
// http.Hijacker — the one capability a WebSocket upgrade needs and the plain
// recorder lacks. Hijack succeeds with a nil connection: what is under test is
// what the access LOG does once a handler has taken the connection over, not the
// bytes that then travel on it (recorder_test.go's hijackOnlyWriter is the same
// stand-in for the recorder's own tests).
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

// hijackThroughWriter takes the connection over through the writer the handler
// was handed, using the direct http.Hijacker assertion both real WebSocket
// libraries make (coder/websocket's hijacker helper type-switches on it first,
// gorilla/websocket asserts it outright). Going through the writer is the point:
// that is how the hijack reaches httpserver's StatusRecorder at all.
func hijackThroughWriter(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("writer handed to the handler does not implement http.Hijacker")
	}
	if _, _, err := hj.Hijack(); err != nil {
		t.Fatalf("Hijack: %v", err)
	}
}

// wsUpgradeHandler answers a handshake the way coder/websocket's Accept does on
// success: WriteHeader(101) through the writer it was handed, then a hijack
// through that same writer. A real handler returns from here only when the
// socket CLOSES, which is why the record cannot be decided by "what the status
// was when the handler returned" without inventing a session-length duration.
func wsUpgradeHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
		hijackThroughWriter(t, w)
	})
}

// wsBareHijackHandler is the OTHER upgrade shape: hijack first and write the 101
// status line onto the connection directly (gorilla/websocket's Upgrade), so no
// status ever reaches the recorder and its 200 default describes nothing.
func wsBareHijackHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijackThroughWriter(t, w)
	})
}

// statusThenHijackHandler writes an explicit status BEFORE hijacking, the HTTP
// CONNECT-tunnel shape. It told us what it answered, so its record is kept.
func statusThenHijackHandler(code int) func(*testing.T) http.Handler {
	return func(t *testing.T) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			hijackThroughWriter(t, w)
		})
	}
}

// fixedHandler adapts a plain handler to the table's per-subtest factory shape.
func fixedHandler(h http.Handler) func(*testing.T) http.Handler {
	return func(*testing.T) http.Handler { return h }
}

// TestWithSkipUpgrades pins the whole contract on ONE route: only a response
// that actually switched protocols loses its record, and every refusal the same
// route can answer keeps a complete one. The 400 and the 403 are the two
// outcomes a request-inspecting predicate cannot model (coder/websocket
// base64-decodes Sec-WebSocket-Key and requires 16 bytes; the origin check runs
// inside Accept), which is why they are asserted field by field rather than by
// count.
func TestWithSkipUpgrades(t *testing.T) {
	const wsRoute = "/ws"

	cases := []struct {
		name        string
		target      string
		handler     func(*testing.T) http.Handler
		extraOpts   []httpserver.LogOption
		wantRecords int
		wantStatus  int
	}{
		{
			name:    "completed upgrade through the writer is suppressed",
			target:  wsRoute,
			handler: wsUpgradeHandler,
		},
		{
			name:    "hijack without a status is suppressed",
			target:  wsRoute,
			handler: wsBareHijackHandler,
		},
		{
			name:        "malformed Sec-WebSocket-Key 400 on the same route is logged",
			target:      wsRoute,
			handler:     fixedHandler(statusHandler(http.StatusBadRequest)),
			wantRecords: 1,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "disallowed origin 403 on the same route is logged",
			target:      wsRoute,
			handler:     fixedHandler(statusHandler(http.StatusForbidden)),
			wantRecords: 1,
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "missing upgrade headers 426 on the same route is logged",
			target:      wsRoute,
			handler:     fixedHandler(statusHandler(http.StatusUpgradeRequired)),
			wantRecords: 1,
			wantStatus:  http.StatusUpgradeRequired,
		},
		{
			name:        "explicit status before a hijack keeps its record",
			target:      wsRoute,
			handler:     statusThenHijackHandler(http.StatusOK),
			wantRecords: 1,
			wantStatus:  http.StatusOK,
		},
		{
			name:        "ordinary 200 is unaffected",
			target:      "/api/thing",
			handler:     fixedHandler(okHandler()),
			wantRecords: 1,
			wantStatus:  http.StatusOK,
		},
		{
			// No WriteHeader at all: net/http sends the implicit 200 the recorder
			// defaults to, which is not 101, so the line is emitted as ever.
			name:   "implicit 200 without WriteHeader is unaffected",
			target: "/api/thing",
			handler: fixedHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("hello"))
			})),
			wantRecords: 1,
			wantStatus:  http.StatusOK,
		},
		{
			// A skip predicate is evaluated before the handler and bypasses the
			// recorder, so it wins over any status: the 400 that WithSkipUpgrades
			// alone would have logged stays silent.
			name:      "a skip predicate still wins over the status rule",
			target:    wsRoute,
			handler:   fixedHandler(statusHandler(http.StatusBadRequest)),
			extraOpts: []httpserver.LogOption{httpserver.WithSkipFunc(func(*http.Request) bool { return true })},
		},
		{
			name:      "a skip path still wins over the status rule",
			target:    wsRoute,
			handler:   fixedHandler(statusHandler(http.StatusForbidden)),
			extraOpts: []httpserver.LogOption{httpserver.WithSkipPaths(wsRoute)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logCap := &captureHandler{}
			var metricCalls int
			opts := []httpserver.LogOption{
				httpserver.WithLogger(slog.New(logCap)),
				httpserver.WithSkipUpgrades(true),
				httpserver.WithClientIP(),
				httpserver.WithRecordRouteMetric(func(httpserver.RequestMetric) { metricCalls++ }),
			}
			h := httpserver.Logging(append(opts, tc.extraOpts...)...)(tc.handler(t))

			w := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.target, nil))

			recs := logCap.snapshot()
			if len(recs) != tc.wantRecords {
				t.Fatalf("emitted %d access lines, want %d", len(recs), tc.wantRecords)
			}
			// A suppressed record takes the metric hook with it, the same pairing
			// WithSkipPaths has and for the same reason.
			if metricCalls != tc.wantRecords {
				t.Errorf("metric hook called %d times, want %d", metricCalls, tc.wantRecords)
			}
			// Every path through the middleware still mints and echoes an id.
			if echoed := w.Header().Get(httpserver.HeaderRequestID); !httpserver.ValidRequestID(echoed) {
				t.Errorf("echoed request id %q is not valid", echoed)
			}
			if tc.wantRecords == 0 {
				return
			}
			m := attrsOf(recs[0])
			if m["status"] != int64(tc.wantStatus) {
				t.Errorf("logged status = %v, want %d", m["status"], tc.wantStatus)
			}
			if m["path"] != tc.target {
				t.Errorf("logged path = %v, want %q", m["path"], tc.target)
			}
			if d, ok := m["duration_ms"].(int64); !ok || d < 0 {
				t.Errorf("duration_ms = %v, want a non-negative int64", m["duration_ms"])
			}
			if id, _ := m["request_id"].(string); !httpserver.ValidRequestID(id) {
				t.Errorf("request_id = %v, want a valid id", m["request_id"])
			}
			// httptest.NewRequest's peer, resolved by the spoof-proof ClientIP with
			// no trusted proxies.
			if m["client_ip"] != "192.0.2.1" {
				t.Errorf("client_ip = %v, want 192.0.2.1", m["client_ip"])
			}
		})
	}
}

// looksLikeUpgrade is the shape of predicate a consumer must write to skip
// upgrades from the REQUEST: the RFC 6455 header signal plus GET, HTTP/1.1,
// version 13, and exactly one Sec-WebSocket-Key field. Every condition is
// necessary for a handshake to succeed, and they are still not sufficient.
func looksLikeUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		r.Method == http.MethodGet &&
		r.ProtoAtLeast(1, 1) &&
		r.Header.Get("Sec-WebSocket-Version") == "13" &&
		len(r.Header.Values("Sec-WebSocket-Key")) == 1
}

// upgradeRequest builds a handshake that satisfies every condition a predicate
// can check while carrying a Sec-WebSocket-Key that is valid base64 of the WRONG
// length (12 bytes, not 16) — the exact input coder/websocket answers 400 for
// and a predicate calls an upgrade.
func upgradeRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "AAAAAAAAAAAAAAAA")
	return req
}

// TestWithSkipUpgrades_recordsTheRefusalAPredicateSuppresses is the regression
// for the bug the option exists to fix, asserted with both wirings side by side
// on the same request and the same response.
func TestWithSkipUpgrades_recordsTheRefusalAPredicateSuppresses(t *testing.T) {
	refused := statusHandler(http.StatusBadRequest) // what Accept answers for that key

	predicting := &captureHandler{}
	httpserver.Logging(httpserver.WithLogger(slog.New(predicting)), httpserver.WithSkipFunc(looksLikeUpgrade))(refused).ServeHTTP(httptest.NewRecorder(), upgradeRequest())

	if n := len(predicting.snapshot()); n != 0 {
		t.Fatalf("the predicting wiring emitted %d lines for the refused handshake, want 0; "+
			"this test no longer models the bug WithSkipUpgrades fixes", n)
	}

	observing := &captureHandler{}
	httpserver.Logging(httpserver.WithLogger(slog.New(observing)), httpserver.WithSkipUpgrades(true))(refused).ServeHTTP(httptest.NewRecorder(), upgradeRequest())

	recs := observing.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d access lines for the refused handshake, want exactly 1", len(recs))
	}
	if m := attrsOf(recs[0]); m["status"] != int64(http.StatusBadRequest) {
		t.Errorf("logged status = %v, want %d", m["status"], http.StatusBadRequest)
	}
}

// TestWithSkipUpgrades_absentLeavesUpgradesLogged pins that the option is
// additive: without it, a 101 keeps the line every existing consumer gets today.
func TestWithSkipUpgrades_absentLeavesUpgradesLogged(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)))(wsUpgradeHandler(t))

	w := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws", nil))

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d access lines without the option, want exactly 1", len(recs))
	}
	if m := attrsOf(recs[0]); m["status"] != int64(http.StatusSwitchingProtocols) {
		t.Errorf("logged status = %v, want %d", m["status"], http.StatusSwitchingProtocols)
	}
}

// TestWithSkipUpgrades_falseMatchesAbsentAndLastWins pins the parameter form's
// contract: false is exactly what leaving the option out means (so a caller can
// pass its own computed flag without branching), and options resolve last-wins
// in both directions.
func TestWithSkipUpgrades_falseMatchesAbsentAndLastWins(t *testing.T) {
	cases := []struct {
		name      string
		opts      []httpserver.LogOption
		wantLines int
	}{
		{"false", []httpserver.LogOption{httpserver.WithSkipUpgrades(false)}, 1},
		{"true", []httpserver.LogOption{httpserver.WithSkipUpgrades(true)}, 0},
		{
			"true then false restores the record",
			[]httpserver.LogOption{httpserver.WithSkipUpgrades(true), httpserver.WithSkipUpgrades(false)},
			1,
		},
		{
			"false then true suppresses it",
			[]httpserver.LogOption{httpserver.WithSkipUpgrades(false), httpserver.WithSkipUpgrades(true)},
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logCap := &captureHandler{}
			opts := append([]httpserver.LogOption{httpserver.WithLogger(slog.New(logCap))}, tc.opts...)
			h := httpserver.Logging(opts...)(wsUpgradeHandler(t))

			w := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws", nil))

			if got := len(logCap.snapshot()); got != tc.wantLines {
				t.Errorf("got %d access lines for the completed upgrade, want %d", got, tc.wantLines)
			}
		})
	}
}

// TestWithSkipUpgrades_panicAfterTheSwitchIsStillLogged pins the godoc claim
// that suppressing the record does not hide a crash mid-session: Recoverer logs
// the panic and its stack from its own line, which is where a failure after the
// handshake belongs — the access line it would have paired with says 101, since
// the status was decided at the handshake, not at the crash.
func TestWithSkipUpgrades_panicAfterTheSwitchIsStillLogged(t *testing.T) {
	logCap := &captureHandler{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
		hijackThroughWriter(t, w)
		panic("session boom")
	})
	h := httpserver.Chain(next,
		httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithSkipUpgrades(true)),
		httpserver.Recoverer(httpserver.WithRecoverLogger(slog.New(logCap))),
	)

	w := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws", nil))

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1 (Recoverer's panic line, no access line)", len(recs))
	}
	if recs[0].Message == "http" {
		t.Errorf("the one line emitted is the access line; a suppressed upgrade must not emit one")
	}
	if m := attrsOf(recs[0]); m["panic"] != "session boom" {
		t.Errorf("recovered panic attr = %v, want %q", m["panic"], "session boom")
	}
}

func TestLogging_rejectsCRLFInjectionInboundID(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = httpserver.RequestIDFromContext(r.Context())
	})
	h := httpserver.Logging(httpserver.WithLogger(discardLogger()))(next)

	hdr := http.Header{}
	// A header-splitting / log-forging inbound id must be rejected, not echoed.
	hdr.Set(httpserver.HeaderRequestID, "abc\r\nX-Evil: 1")
	rr := serve(h, http.MethodGet, "/x", hdr)

	echoed := rr.Header().Get(httpserver.HeaderRequestID)
	if strings.ContainsAny(echoed, "\r\n") {
		t.Errorf("echoed id %q contains CR/LF; injection content was not rejected", echoed)
	}
	if !httpserver.ValidRequestID(echoed) {
		t.Errorf("echoed id %q is not a freshly minted valid id", echoed)
	}
	if seen != echoed {
		t.Errorf("context id %q != echoed header %q", seen, echoed)
	}
}

// serveWithPeer drives h with a request whose RemoteAddr and optional
// X-Forwarded-For are set, so the client-IP resolution can be exercised.
func serveWithPeer(h http.Handler, remoteAddr, xff string) {
	req := httptest.NewRequest(http.MethodGet, "/api/thing", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
}

// Without WithClientIP the access line carries no client_ip attribute (the
// default output is unchanged).
func TestLogging_noClientIPAttrByDefault(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)))(okHandler())

	serveWithPeer(h, "192.0.2.1:1234", "203.0.113.5")

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(recs))
	}
	if _, ok := attrsOf(recs[0])["client_ip"]; ok {
		t.Error("client_ip attr present without WithClientIP; want absent")
	}
}

// With WithClientIP and no trusted ranges, the socket peer host is logged and
// an X-Forwarded-For is IGNORED (the spoof-proof default).
func TestLogging_withClientIPLogsPeerByDefault(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithClientIP())(okHandler())

	// A spoofed XFF must not be honored when no proxy range is trusted.
	serveWithPeer(h, "192.0.2.1:1234", "203.0.113.5")

	m := attrsOf(logCap.snapshot()[0])
	if got := m["client_ip"]; got != "192.0.2.1" {
		t.Errorf("client_ip = %v, want the socket peer 192.0.2.1 (XFF ignored)", got)
	}
}

// With WithClientIP and the peer inside a trusted proxy range, the real client
// is resolved from X-Forwarded-For (right-to-left, skipping trusted hops).
func TestLogging_withClientIPResolvesTrustedXFF(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithClientIP(netip.MustParsePrefix("192.0.2.0/24")))(okHandler())

	// Peer 192.0.2.1 is a trusted proxy; it appended the client it saw.
	serveWithPeer(h, "192.0.2.1:1234", "203.0.113.5")

	m := attrsOf(logCap.snapshot()[0])
	if got := m["client_ip"]; got != "203.0.113.5" {
		t.Errorf("client_ip = %v, want the forwarded client 203.0.113.5", got)
	}
}

// WithClientIPFunc logs the result of the caller-supplied resolver verbatim
// (for a dynamic/hot-reloaded trusted set), instead of the fixed-set ClientIP.
func TestLogging_withClientIPFunc(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithClientIPFunc(func(*http.Request) string { return "resolved-by-func" }))(okHandler())

	serveWithPeer(h, "192.0.2.1:1234", "203.0.113.5")

	if got := attrsOf(logCap.snapshot()[0])["client_ip"]; got != "resolved-by-func" {
		t.Errorf("client_ip = %v, want the func result %q", got, "resolved-by-func")
	}
}

// WithClientIP and WithClientIPFunc are mutually exclusive; the last one applied
// wins (the earlier one's state is cleared).
func TestLogging_clientIPOptionsMutuallyExclusive(t *testing.T) {
	// Func applied last → func wins.
	cap1 := &captureHandler{}
	h1 := httpserver.Logging(httpserver.WithLogger(slog.New(cap1)), httpserver.WithClientIP(netip.MustParsePrefix("192.0.2.0/24")), httpserver.WithClientIPFunc(func(*http.Request) string { return "func-wins" }))(okHandler())
	serveWithPeer(h1, "192.0.2.1:1234", "203.0.113.5")
	if got := attrsOf(cap1.snapshot()[0])["client_ip"]; got != "func-wins" {
		t.Errorf("client_ip = %v, want func-wins", got)
	}

	// WithClientIP applied last → trusted-set path wins (func cleared).
	cap2 := &captureHandler{}
	h2 := httpserver.Logging(httpserver.WithLogger(slog.New(cap2)), httpserver.WithClientIPFunc(func(*http.Request) string { return "func-loses" }), httpserver.WithClientIP(netip.MustParsePrefix("192.0.2.0/24")))(okHandler())
	serveWithPeer(h2, "192.0.2.1:1234", "203.0.113.5")
	if got := attrsOf(cap2.snapshot()[0])["client_ip"]; got != "203.0.113.5" {
		t.Errorf("client_ip = %v, want the trusted-XFF client 203.0.113.5", got)
	}
}

// WithClientIPFunc(nil) is a no-op (matching the package's nil-option
// convention), so no client_ip attribute is emitted.
func TestLogging_withClientIPFuncNilIsNoOp(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithClientIPFunc(nil))(okHandler())

	serveWithPeer(h, "192.0.2.1:1234", "203.0.113.5")

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(recs))
	}
	if _, ok := attrsOf(recs[0])["client_ip"]; ok {
		t.Error("client_ip attr present after WithClientIPFunc(nil); want absent")
	}
}

// A nil WithClientIPFunc applied after WithClientIP does not clear the prior
// trusted-set resolver: the nil callback is ignored, not last-wins.
func TestLogging_withClientIPFuncNilKeepsPriorTrustedSet(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithClientIP(netip.MustParsePrefix("192.0.2.0/24")), httpserver.WithClientIPFunc(nil))(okHandler())

	serveWithPeer(h, "192.0.2.1:1234", "203.0.113.5")

	if got := attrsOf(logCap.snapshot()[0])["client_ip"]; got != "203.0.113.5" {
		t.Errorf("client_ip = %v, want the trusted-XFF client 203.0.113.5 (nil func ignored)", got)
	}
}

// A panicking WithClientIPFunc resolver must not escape the outer Logging defer
// (which sits outside Recoverer): the request still completes, the access line
// is still emitted, and only the client_ip attribute is omitted.
func TestLogging_panickingClientIPResolverStillEmitsAccessLine(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithClientIPFunc(func(*http.Request) string { panic("resolver boom") }))(okHandler())

	// A panic in the resolver must be contained, not propagated out of ServeHTTP.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("client-IP resolver panic escaped Logging: %v", r)
			}
		}()
		serveWithPeer(h, "192.0.2.1:1234", "203.0.113.5")
	}()

	recs := logCap.snapshot()
	// Expect the resolver-failure log AND the access line.
	var access *slog.Record
	var sawFailure bool
	for i := range recs {
		switch recs[i].Message {
		case "http":
			access = &recs[i]
		case "httpserver: client_ip resolver failed":
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Error("expected a 'client_ip resolver failed' log record, got none")
	}
	if access == nil {
		t.Fatal("access line was not emitted after resolver panic")
	}
	if _, ok := attrsOf(*access)["client_ip"]; ok {
		t.Error("client_ip attr present after resolver panic; want omitted")
	}
	if m := attrsOf(*access); m["status"] != int64(http.StatusOK) {
		t.Errorf("access line status = %v, want 200", m["status"])
	}
}

func TestLogging_withLogLevelMapsStatus(t *testing.T) {
	// The canonical scrape-quiet policy: 2xx/3xx at Debug, 4xx Warn, 5xx Error.
	policy := func(_ *http.Request, status int) slog.Level {
		switch {
		case status >= 500:
			return slog.LevelError
		case status >= 400:
			return slog.LevelWarn
		}
		return slog.LevelDebug
	}
	cases := []struct {
		status int
		want   slog.Level
	}{
		{http.StatusAccepted, slog.LevelDebug},
		{http.StatusNotFound, slog.LevelWarn},
		{http.StatusInternalServerError, slog.LevelError},
	}
	for _, tc := range cases {
		logCap := &captureHandler{}
		h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithLogLevel(policy))(statusHandler(tc.status))

		serve(h, http.MethodGet, "/x", nil)

		recs := logCap.snapshot()
		if len(recs) != 1 {
			t.Fatalf("status %d: got %d records, want 1", tc.status, len(recs))
		}
		if recs[0].Level != tc.want {
			t.Errorf("status %d: line level = %v, want %v", tc.status, recs[0].Level, tc.want)
		}
	}
}

func TestLogging_defaultLineLevelIsInfo(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)))(okHandler())

	serve(h, http.MethodGet, "/x", nil)

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0].Level != slog.LevelInfo {
		t.Errorf("default line level = %v, want Info", recs[0].Level)
	}
}

func TestLogging_withLogLevelNilIsNoOp(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithLogLevel(nil))(okHandler())

	serve(h, http.MethodGet, "/x", nil)

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0].Level != slog.LevelInfo {
		t.Errorf("line level with nil policy = %v, want Info", recs[0].Level)
	}
}

// A panicking WithLogLevel policy must not lose the access line or escape the
// outer Logging defer: the line falls back to Info and the failure is logged.
func TestLogging_panickingLogLevelHookStillEmitsAccessLine(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithLogLevel(func(*http.Request, int) slog.Level { panic("level boom") }))(statusHandler(http.StatusAccepted))

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("log level hook panic escaped Logging: %v", r)
			}
		}()
		serve(h, http.MethodPut, "/x", nil)
	}()

	recs := logCap.snapshot()
	var access *slog.Record
	var sawFailure bool
	for i := range recs {
		switch recs[i].Message {
		case "http":
			access = &recs[i]
		case "httpserver: log level hook failed":
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Error("expected a 'log level hook failed' record, got none")
	}
	if access == nil {
		t.Fatal("access line was not emitted after log level hook panic")
	}
	if access.Level != slog.LevelInfo {
		t.Errorf("access line level after hook panic = %v, want Info fallback", access.Level)
	}
}

func TestLogging_withLogLevelComposesWithRequestMetricHook(t *testing.T) {
	// The level policy and the request-aware metric hook ride the same
	// deferred emission: both must fire for one request.
	var metricCalls int
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithLogLevel(func(_ *http.Request, status int) slog.Level {
		if status >= 400 {
			return slog.LevelWarn
		}
		return slog.LevelDebug
	}), httpserver.WithRecordRouteMetric(func(httpserver.RequestMetric) { metricCalls++ }))(statusHandler(http.StatusNotFound))

	serve(h, http.MethodGet, "/missing", nil)

	if metricCalls != 1 {
		t.Errorf("metric hook fired %d times, want 1", metricCalls)
	}
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0].Level != slog.LevelWarn {
		t.Errorf("line level = %v, want Warn", recs[0].Level)
	}
}

func TestLogging_withPathFuncRewritesLoggedPath(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithPathFunc(func(r *http.Request) string {
		if strings.HasPrefix(r.URL.Path, "/api/sessions/") {
			return "/api/sessions/{id}"
		}
		return r.URL.Path
	}))(okHandler())

	serve(h, http.MethodDelete, "/api/sessions/tok-secret-123", nil)

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	m := attrsOf(recs[0])
	if m["path"] != "/api/sessions/{id}" {
		t.Errorf("path attr = %v, want the transformed template", m["path"])
	}
	for _, rec := range recs {
		mm := attrsOf(rec)
		for k, v := range mm {
			if s, ok := v.(string); ok && strings.Contains(s, "tok-secret-123") {
				t.Errorf("raw path leaked through attr %q = %q", k, s)
			}
		}
	}
}

func TestLogging_withPathFuncPanicFallsBackToPlaceholder(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithPathFunc(func(*http.Request) string { panic("boom") }))(okHandler())

	rr := serve(h, http.MethodGet, "/api/sessions/tok-secret-456", nil)

	if rr.Code != http.StatusOK {
		t.Errorf("response code = %d, want 200 (panicking transform must not break the request)", rr.Code)
	}
	recs := logCap.snapshot()
	var accessLine bool
	for _, rec := range recs {
		m := attrsOf(rec)
		if rec.Message == "http" {
			accessLine = true
			if m["path"] != "(path-redaction-failed)" {
				t.Errorf("path attr = %v, want the fail-closed placeholder", m["path"])
			}
		}
		for k, v := range m {
			if s, ok := v.(string); ok && strings.Contains(s, "tok-secret-456") {
				t.Errorf("raw path leaked through %q attr %q = %q", rec.Message, k, s)
			}
		}
	}
	if !accessLine {
		t.Error("no access line emitted; the line must still emit when the transform panics")
	}
}

func TestLogging_withPathFuncEmptyReturnFallsBackToPlaceholder(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithPathFunc(func(*http.Request) string { return "" }))(okHandler())

	serve(h, http.MethodGet, "/api/sessions/tok-secret-789", nil)

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	if m := attrsOf(recs[0]); m["path"] != "(path-redaction-failed)" {
		t.Errorf("path attr = %v, want the fail-closed placeholder", m["path"])
	}
}

func TestLogging_withPathFuncNotCalledOnSkippedPath(t *testing.T) {
	var calls int
	h := httpserver.Logging(httpserver.WithLogger(discardLogger()), httpserver.WithSkipFunc(func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/api/sessions/")
	}), httpserver.WithPathFunc(func(*http.Request) string { calls++; return "/x" }))(okHandler())

	serve(h, http.MethodGet, "/api/sessions/tok", nil)

	if calls != 0 {
		t.Errorf("transform called %d times on a skipped request, want 0", calls)
	}
}

func TestLogging_withPathFuncNilIgnored(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithPathFunc(nil))(okHandler())

	serve(h, http.MethodGet, "/plain", nil)

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	if m := attrsOf(recs[0]); m["path"] != "/plain" {
		t.Errorf("path attr = %v, want the raw path when fn is nil", m["path"])
	}
}

func TestLogging_withPathFuncSeesPopulatedPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /api/sessions/{id}", okHandler())
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithPathFunc(func(r *http.Request) string {
		if r.Pattern != "" {
			return r.Pattern
		}
		return "(unmatched)"
	}))(mux)

	serve(h, http.MethodGet, "/api/sessions/tok-abc", nil)

	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	if m := attrsOf(recs[0]); m["path"] != "GET /api/sessions/{id}" {
		t.Errorf("path attr = %v, want the mux pattern (transform runs after routing)", m["path"])
	}
}

func TestLogging_levelHookPanicDiagnosticCarriesTransformedPath(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithPathFunc(func(*http.Request) string { return "/tmpl/{id}" }), httpserver.WithLogLevel(func(*http.Request, int) slog.Level { panic("level boom") }))(okHandler())

	serve(h, http.MethodGet, "/tmpl/tok-secret-lvl", nil)

	recs := logCap.snapshot()
	if len(recs) < 2 {
		t.Fatalf("got %d log records, want the hook-failure diagnostic plus the access line", len(recs))
	}
	for _, rec := range recs {
		m := attrsOf(rec)
		for k, v := range m {
			if s, ok := v.(string); ok && strings.Contains(s, "tok-secret-lvl") {
				t.Errorf("raw path leaked through %q attr %q = %q", rec.Message, k, s)
			}
		}
		if rec.Message != "http" {
			if m["path"] != "/tmpl/{id}" {
				t.Errorf("diagnostic path attr = %v, want the transformed path", m["path"])
			}
		}
	}
}

// The metric hook's failure diagnostic carries the recorded path, never the
// raw one, so a panicking hook cannot leak a token WithPathFunc redacted.
func TestWithRecordRouteMetric_panicDiagnosticCarriesTransformedPath(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)),
		httpserver.WithPathFunc(func(*http.Request) string { return "/redacted" }),
		httpserver.WithRecordRouteMetric(func(httpserver.RequestMetric) { panic("metric boom") }),
	)(okHandler())

	serve(h, http.MethodGet, "/t/secret-token", nil)

	var found bool
	for _, rec := range logCap.snapshot() {
		m := attrsOf(rec)
		for k, v := range m {
			if s, ok := v.(string); ok && strings.Contains(s, "secret-token") {
				t.Errorf("raw path leaked through %q attr %q = %q", rec.Message, k, s)
			}
		}
		if rec.Message == "httpserver: metric hook failed" {
			found = true
			if m["path"] != "/redacted" {
				t.Errorf("diagnostic path attr = %v, want the transformed path", m["path"])
			}
		}
	}
	if !found {
		t.Error("no metric hook failure diagnostic was logged")
	}
}

// probeLevelOf serves one request through a WithProbeLogLevel-configured logger
// and returns the level of the emitted access line.
func probeLevelOf(t *testing.T, target string, status int, probePaths ...string) slog.Level {
	t.Helper()
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithProbeLogLevel(probePaths...))(statusHandler(status))
	serve(h, http.MethodGet, target, nil)
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	return recs[0].Level
}

func TestWithProbeLogLevel_mapsProbeStatusesToLevels(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   slog.Level
	}{
		{"healthy probe", http.StatusOK, slog.LevelDebug},
		{"redirect", http.StatusNotModified, slog.LevelDebug},
		{"refused host", http.StatusForbidden, slog.LevelDebug},
		{"client error", http.StatusNotFound, slog.LevelDebug},
		{"server error", http.StatusInternalServerError, slog.LevelDebug},
		{"unhealthy verdict", http.StatusServiceUnavailable, slog.LevelDebug},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeLevelOf(t, "/api/health", tc.status, "/api/health"); got != tc.want {
				t.Errorf("probe %d level = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestWithProbeLogLevel_nonProbePathStaysInfo(t *testing.T) {
	if got := probeLevelOf(t, "/api/things", http.StatusInternalServerError, "/api/health"); got != slog.LevelInfo {
		t.Errorf("non-probe 500 level = %v, want Info (the preset must not touch real traffic)", got)
	}
}

func TestWithProbeLogLevel_emptySetLogsEverythingAtInfo(t *testing.T) {
	if got := probeLevelOf(t, "/api/health", http.StatusOK); got != slog.LevelInfo {
		t.Errorf("empty-set probe level = %v, want Info", got)
	}
}

func TestWithProbeLogLevel_skippedPathStillEmitsNothing(t *testing.T) {
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithSkipPaths("/ws"), httpserver.WithProbeLogLevel("/ws"))(okHandler())
	serve(h, http.MethodGet, "/ws", nil)
	if n := len(logCap.snapshot()); n != 0 {
		t.Errorf("skipped path emitted %d lines, want 0 (skip wins over the probe policy)", n)
	}
}

// --- WithTemplatePathsUnder -------------------------------------------------
//
// The policy exists because two apps hand-rolled the same WithPathFunc over the
// same upstream route table and diverged on the unmatched case. These tests pin
// the three outcomes it decides once, plus the stdlib property the whole thing
// rests on (a nested ServeMux overwrites r.Pattern in place), because that
// property is what makes the template visible to a middleware sitting OUTSIDE
// both muxes.

// nestedSessionMux mirrors the real consumer shape: an outer mux that mounts a
// subtree prefix, and an inner mux (the route-owning package's own handler)
// registering the concrete templates under it. Both are plain http.ServeMux, so
// this is the stdlib behaviour the option depends on, not a stand-in for it.
func nestedSessionMux() http.Handler {
	inner := http.NewServeMux()
	inner.HandleFunc("DELETE /api/sessions/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	inner.HandleFunc("PUT /api/sessions/{id}/title", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	outer := http.NewServeMux()
	outer.Handle("/api/sessions/", inner)
	outer.HandleFunc("/api/sessions/events", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	outer.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return outer
}

// loggedPathFor serves one request through the logger wrapped around the nested
// mux and returns the recorded path attribute.
func loggedPathFor(t *testing.T, method, target string, opts ...httpserver.LogOption) string {
	t.Helper()
	logCap := &captureHandler{}
	opts = append([]httpserver.LogOption{httpserver.WithLogger(slog.New(logCap))}, opts...)
	h := httpserver.Logging(opts...)(nestedSessionMux())
	serve(h, method, target, nil)
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	p, _ := attrsOf(recs[0])["path"].(string)
	return p
}

// A stand-in for the credential a session path embeds. Deliberately
// sequential rather than random-looking: the assertions only need a value
// that must not appear in a log line, and a high-entropy hex literal beside an
// identifier like this one is what secret scanners exist to flag.
const fakeSessionID = "0123456789abcdef"

func TestWithTemplatePathsUnder_recordsTheMatchedTemplate(t *testing.T) {
	cases := []struct {
		name, method, target, want string
	}{
		{"session itself", http.MethodDelete, "/api/sessions/" + fakeSessionID, "/api/sessions/{id}"},
		{"subresource", http.MethodPut, "/api/sessions/" + fakeSessionID + "/title", "/api/sessions/{id}/title"},
		// An exact path under the prefix is its own registered pattern, so it
		// records as itself with no special-casing in the option.
		{"exact path member", http.MethodGet, "/api/sessions/events", "/api/sessions/events"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := loggedPathFor(t, tc.method, tc.target,
				httpserver.WithTemplatePathsUnder("/api/sessions/"))
			if got != tc.want {
				t.Errorf("logged path = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, fakeSessionID) {
				t.Errorf("logged path %q leaked the session token", got)
			}
		})
	}
}

func TestWithTemplatePathsUnder_unmatchedUnderPrefixWithholdsThePath(t *testing.T) {
	// The case the two hand-rolled copies disagreed on, and the one that matters
	// most: an unrouted request under a credential-bearing prefix STILL has the
	// credential in it, so the raw path must never be recorded. It is marked
	// unmatched rather than mislabelled onto a route it is not, so a new upstream
	// subroute shows up as something to wire.
	got := loggedPathFor(t, http.MethodGet, "/api/sessions/"+fakeSessionID+"/not-a-route",
		httpserver.WithTemplatePathsUnder("/api/sessions/"))
	if strings.Contains(got, fakeSessionID) {
		t.Fatalf("logged path %q leaked the session token on an unmatched route", got)
	}
	if got != "/api/sessions/(unmatched)" {
		t.Errorf("logged path = %q, want %q", got, "/api/sessions/(unmatched)")
	}
	// Distinct from the broken-policy placeholder: conflating a routine 404 with
	// "the path policy itself failed" would hide the latter.
	if got == "(path-redaction-failed)" {
		t.Error("an unmatched route must not report as a failed path policy")
	}
}

func TestWithTemplatePathsUnder_leavesPathsOutsideThePrefixAlone(t *testing.T) {
	// Deliberately NOT the template. The static mount's pattern is "/", so
	// recording templates everywhere would collapse every asset onto one line and
	// lose which asset was requested. Credential-bearing routes are a
	// per-route-family fact, which is why the option takes prefixes.
	for _, target := range []string{"/assets/app.js", "/does-not-exist.png"} {
		got := loggedPathFor(t, http.MethodGet, target,
			httpserver.WithTemplatePathsUnder("/api/sessions/"))
		if got != target {
			t.Errorf("logged path for %q = %q, want it unchanged", target, got)
		}
	}
}

func TestWithTemplatePathsUnder_longestPrefixWins(t *testing.T) {
	// Nested declarations must not depend on argument order, so a reader can add
	// a prefix without auditing the ones already there.
	//
	// Asserted on an UNMATCHED path deliberately: when a route matches, the
	// recorded value is r.Pattern and which prefix was selected cannot be
	// observed. The selected prefix is only visible in the unmatched marker, so
	// that is the only case where this rule has an effect to pin.
	for _, order := range [][]string{
		{"/api/", "/api/sessions/"},
		{"/api/sessions/", "/api/"},
	} {
		got := loggedPathFor(t, http.MethodGet, "/api/sessions/"+fakeSessionID+"/not-a-route",
			httpserver.WithTemplatePathsUnder(order...))
		if got != "/api/sessions/(unmatched)" {
			t.Errorf("with prefixes %v: logged path = %q, want the LONGEST prefix's marker", order, got)
		}
		// ...and a matched route is unaffected by the ordering either way.
		if p := loggedPathFor(t, http.MethodDelete, "/api/sessions/"+fakeSessionID,
			httpserver.WithTemplatePathsUnder(order...)); p != "/api/sessions/{id}" {
			t.Errorf("with prefixes %v: logged path = %q, want the matched template", order, p)
		}
	}
}

func TestWithTemplatePathsUnder_ignoresEmptyPrefixes(t *testing.T) {
	// An empty prefix would declare the whole surface credential-bearing. It is
	// dropped rather than honoured, and — the observable half — an option left
	// with nothing to declare installs NO path policy at all, so it cannot
	// silently displace one that was already set. Without the filter this reads
	// as "the last policy applied wins" and quietly discards the transform.
	got := loggedPathFor(t, http.MethodDelete, "/api/sessions/"+fakeSessionID,
		httpserver.WithPathFunc(func(*http.Request) string { return "/kept" }),
		httpserver.WithTemplatePathsUnder("", ""))
	if got != "/kept" {
		t.Errorf("logged path = %q, want the earlier transform kept (an all-empty option is inert)", got)
	}

	// A real prefix alongside an empty one still works, and the empty one does
	// not widen it.
	got = loggedPathFor(t, http.MethodDelete, "/api/sessions/"+fakeSessionID,
		httpserver.WithTemplatePathsUnder("", "/api/sessions/"))
	if got != "/api/sessions/{id}" {
		t.Errorf("logged path = %q, want the template alongside a dropped empty prefix", got)
	}
	if p := loggedPathFor(t, http.MethodGet, "/assets/app.js",
		httpserver.WithTemplatePathsUnder("", "/api/sessions/")); p != "/assets/app.js" {
		t.Errorf("logged path = %q, want the raw path (an empty prefix must not match everything)", p)
	}
}

func TestWithTemplatePathsUnder_lastPathPolicyWins(t *testing.T) {
	// There is one recorded path, so the two path policies cannot both apply.
	// Pinned in both directions so the precedence is a contract rather than an
	// accident of which field the options happen to write.
	got := loggedPathFor(t, http.MethodDelete, "/api/sessions/"+fakeSessionID,
		httpserver.WithPathFunc(func(*http.Request) string { return "/from-pathfunc" }),
		httpserver.WithTemplatePathsUnder("/api/sessions/"))
	if got != "/api/sessions/{id}" {
		t.Errorf("logged path = %q, want the template (applied last)", got)
	}

	got = loggedPathFor(t, http.MethodDelete, "/api/sessions/"+fakeSessionID,
		httpserver.WithTemplatePathsUnder("/api/sessions/"),
		httpserver.WithPathFunc(func(*http.Request) string { return "/from-pathfunc" }))
	if got != "/from-pathfunc" {
		t.Errorf("logged path = %q, want the transform's value (applied last)", got)
	}
}

func TestWithTemplatePathsUnder_nestedMuxOverwritesPatternInPlace(t *testing.T) {
	// The stdlib property the whole option rests on, asserted directly rather
	// than inferred from the logged output: the OUTER mux sets r.Pattern to its
	// subtree prefix, then the INNER mux overwrites it with the concrete template
	// on the SAME request pointer. That is what lets a middleware wrapped around
	// both see the innermost match — and what makes an inner 404 clear it to "",
	// which is the unmatched case above.
	var seen string
	inner := http.NewServeMux()
	inner.HandleFunc("DELETE /api/sessions/{id}", func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Pattern
	})
	outer := http.NewServeMux()
	outer.Handle("/api/sessions/", inner)

	var afterRouting string
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outer.ServeHTTP(w, r)
		// Same *http.Request the wrapper handed down: if ServeMux cloned instead
		// of assigning in place, this would still read the outer prefix.
		afterRouting = r.Pattern
	})
	serve(probe, http.MethodDelete, "/api/sessions/"+fakeSessionID, nil)

	if seen != "DELETE /api/sessions/{id}" {
		t.Errorf("inner handler saw pattern %q, want the concrete template", seen)
	}
	if afterRouting != "DELETE /api/sessions/{id}" {
		t.Errorf("pattern after routing = %q, want the inner template (assigned in place)", afterRouting)
	}
}

// The recorded-path cap and its marker as the package DOCUMENTS them, spelled
// out here rather than read from the package: they are the public contract, so a
// test that imported them could not notice either one changing.
const (
	defaultLoggedPathCap = 512
	truncatedMarker      = "...(truncated)"
	overlongMethod       = "(overlong)"
)

// requestWithPath builds a request carrying path verbatim as r.URL.Path,
// bypassing the target parsing httptest.NewRequest does. A real client sends
// %-escapes that decode to bytes url.Parse refuses in a target string, and the
// recorded path is read from URL.Path either way, so this is the honest shape
// for path-boundary cases (empty, invalid UTF-8, very long).
func requestWithPath(method, path string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, "http://x", nil)
	req.URL.Path = path
	return req
}

// loggedAttrs serves req through Logging wrapped around next and returns
// the attributes of the single access line it emitted.
func loggedAttrs(t *testing.T, next http.Handler, req *http.Request, opts ...httpserver.LogOption) map[string]any {
	t.Helper()
	logCap := &captureHandler{}
	opts = append([]httpserver.LogOption{httpserver.WithLogger(slog.New(logCap))}, opts...)
	httpserver.Logging(opts...)(next).ServeHTTP(httptest.NewRecorder(), req)
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	return attrsOf(recs[0])
}

// loggedPathOf returns the recorded path for a request whose URL.Path is set
// verbatim to path.
func loggedPathOf(t *testing.T, path string, opts ...httpserver.LogOption) string {
	t.Helper()
	got, _ := loggedAttrs(t, okHandler(), requestWithPath(http.MethodGet, path), opts...)["path"].(string)
	return got
}

// asciiPath returns a request path of exactly n bytes.
func asciiPath(n int) string {
	return "/" + strings.Repeat("a", n-1)
}

func TestLogging_boundsTheLoggedPathByDefault(t *testing.T) {
	// The cap is a floor, not an opt-in: no option is passed here, which is the
	// whole point — the eight consumers that never bounded their path get it on a
	// version bump without touching their code.
	cases := []struct {
		name, path, want string
	}{
		{"short path unchanged", "/api/thing", "/api/thing"},
		{"exactly at the cap unchanged", asciiPath(defaultLoggedPathCap), asciiPath(defaultLoggedPathCap)},
		{"one byte over is cut", asciiPath(defaultLoggedPathCap + 1), asciiPath(defaultLoggedPathCap) + truncatedMarker},
		{"a megabyte URL costs half a kilobyte", asciiPath(1 << 20), asciiPath(defaultLoggedPathCap) + truncatedMarker},
		// r.URL.Path can be empty (a client sending "OPTIONS *", a synthetic
		// request). Unchanged: only a path POLICY's empty return means "the
		// policy failed", and coercing this one would report a failure that did
		// not happen.
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := loggedPathOf(t, tc.path)
			if got != tc.want {
				t.Errorf("logged path (len %d) = %.80q… (len %d), want %.80q… (len %d)",
					len(tc.path), got, len(got), tc.want, len(tc.want))
			}
			if len(got) > defaultLoggedPathCap+len(truncatedMarker) {
				t.Errorf("logged path is %d bytes, want at most %d",
					len(got), defaultLoggedPathCap+len(truncatedMarker))
			}
		})
	}
}

func TestLogging_cutLoggedPathCarriesTheMarker(t *testing.T) {
	// A silently shortened path reads as a real request for a route that does not
	// exist, which sends an operator hunting for a missing handler instead of a
	// client sending a megabyte URL. The marker is the difference.
	got := loggedPathOf(t, asciiPath(4096))
	if !strings.HasSuffix(got, truncatedMarker) {
		t.Errorf("logged path = %.80q…, want it to end in %q", got, truncatedMarker)
	}
	if in := loggedPathOf(t, "/api/thing"); strings.Contains(in, truncatedMarker) {
		t.Errorf("within-cap path logged as %q, want no truncation marker", in)
	}
}

func TestLogging_cutsTheLoggedPathOnARuneBoundary(t *testing.T) {
	// Each case places a multi-byte rune so the cap falls INSIDE it. The whole
	// rune must go: half a rune is rewritten as U+FFFD by every encoder between
	// here and the log store, so the tail of the value is corrupt for the reader
	// and two distinct paths can render identically.
	cases := []struct {
		name string
		r    rune
		// lead is the byte count before the rune, chosen so the rune spans byte
		// index defaultLoggedPathCap.
		lead int
	}{
		{"two-byte rune", 'é', defaultLoggedPathCap - 1},
		{"three-byte rune", '€', defaultLoggedPathCap - 2},
		{"four-byte rune", '𝄞', defaultLoggedPathCap - 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := asciiPath(tc.lead) + string(tc.r) + strings.Repeat("z", 32)
			if size := utf8.RuneLen(tc.r); tc.lead+size <= defaultLoggedPathCap {
				t.Fatalf("case does not straddle the cap: lead %d + rune %d bytes", tc.lead, size)
			}
			got := loggedPathOf(t, path)

			if want := asciiPath(tc.lead) + truncatedMarker; got != want {
				t.Errorf("logged path = %q…, want the cut at the rune boundary (%d bytes kept)", got[max(0, len(got)-8):], tc.lead)
			}
			if !utf8.ValidString(got) {
				t.Errorf("logged path is not valid UTF-8: %q", got[max(0, len(got)-8):])
			}
			if strings.ContainsRune(got, utf8.RuneError) {
				t.Errorf("logged path contains U+FFFD, so a rune was split: %q", got[max(0, len(got)-8):])
			}
		})
	}
}

func TestWithMaxLoggedPath_tightensAndWidensTheCap(t *testing.T) {
	// knell's ask: a route table of /healthz, /metrics, and /beat/{id} with a
	// 64-char id has no use for 512 bytes of log line.
	if got, want := loggedPathOf(t, asciiPath(300), httpserver.WithMaxLoggedPath(128)),
		asciiPath(128)+truncatedMarker; got != want {
		t.Errorf("with a 128-byte cap: logged path = %.40q… (len %d), want the cut at 128", got, len(got))
	}
	// Widening keeps a long path whole for an app that wants it.
	if got := loggedPathOf(t, asciiPath(2000), httpserver.WithMaxLoggedPath(4096)); got != asciiPath(2000) {
		t.Errorf("with a 4096-byte cap: logged path is %d bytes, want the 2000-byte path unchanged", len(got))
	}
	// Within either cap, byte-identical.
	if got := loggedPathOf(t, "/api/thing", httpserver.WithMaxLoggedPath(16)); got != "/api/thing" {
		t.Errorf("logged path = %q, want it unchanged", got)
	}
}

func TestWithMaxLoggedPath_nonPositiveKeepsTheDefault(t *testing.T) {
	// There is no way to switch the bound off, and a config-driven 0 must not
	// find one: eight of the nine access-log consumers had no path bound of their
	// own, so an option that could zero the cap would reopen exactly the hole it
	// closes.
	for _, n := range []int{0, -1, -512} {
		got := loggedPathOf(t, asciiPath(1000), httpserver.WithMaxLoggedPath(n))
		if want := asciiPath(defaultLoggedPathCap) + truncatedMarker; got != want {
			t.Errorf("WithMaxLoggedPath(%d): logged path is %d bytes, want the default cap to stand (%d)",
				n, len(got), len(want))
		}
	}
	// Non-positive means IGNORED, which is stronger than "the default stands":
	// options are otherwise last-wins, so a 0 arriving after an explicit cap must
	// leave that cap in place rather than widening the bound back to the default.
	for _, n := range []int{0, -1} {
		got := loggedPathOf(t, asciiPath(1000), httpserver.WithMaxLoggedPath(128), httpserver.WithMaxLoggedPath(n))
		if want := asciiPath(128) + truncatedMarker; got != want {
			t.Errorf("WithMaxLoggedPath(128) then WithMaxLoggedPath(%d): logged path is %d bytes, want the 128-byte cap to stand (%d)",
				n, len(got), len(want))
		}
	}
}

func TestLogging_cutsALoggedPathWithNoRuneStartToKeep(t *testing.T) {
	// The value the cap receives is not always a well-formed path: a redaction
	// transform that byte-slices one hands back the TAIL of a rune, and %-escapes
	// decode to the same shape. When every byte before the cut is a continuation
	// byte there is no rune boundary to back off to, so the whole prefix goes and
	// the marker is all that is left — the cap has to stay total over such a
	// value rather than walking past the front of the string.
	const runeTail = "\x9f\x99\x82" // the trailing 3 bytes of U+1F642
	if got := loggedPathOf(t, runeTail, httpserver.WithMaxLoggedPath(2)); got != truncatedMarker {
		t.Errorf("logged path = %q, want %q", got, truncatedMarker)
	}
}

func TestWithMaxLoggedPath_boundsACallerPathFuncReturn(t *testing.T) {
	// A transform is a REDACTION policy; nothing about redacting a token also
	// bounds the path it sat in. So the cap applies to fn's return, not only to
	// the raw default — the caller cannot opt out of the floor by installing a
	// policy.
	long := "/rewritten/" + strings.Repeat("b", 2000)
	got := loggedPathOf(t, "/short",
		httpserver.WithPathFunc(func(*http.Request) string { return long }))
	if want := long[:defaultLoggedPathCap] + truncatedMarker; got != want {
		t.Errorf("logged path is %d bytes, want the transform's return cut at %d", len(got), defaultLoggedPathCap)
	}
}

func TestWithMaxLoggedPath_keepsTheFailClosedPlaceholder(t *testing.T) {
	// The cap runs after the fail-closed coercion, and the placeholder is well
	// under it, so a broken policy still reports as a broken policy rather than
	// as a truncated one.
	got := loggedPathOf(t, "/secret/"+fakeSessionID,
		httpserver.WithPathFunc(func(*http.Request) string { return "" }))
	if got != "(path-redaction-failed)" {
		t.Errorf("logged path = %q, want the fail-closed placeholder", got)
	}
}

func TestWithMaxLoggedPath_boundsTheTemplatePolicyOutput(t *testing.T) {
	// WithTemplatePathsUnder returns a path OUTSIDE every declared prefix
	// unchanged — deliberately, so a static 404 stays diagnosable — which leaves
	// the raw client bytes as the recorded value. That is the branch the cap has
	// to cover.
	outside := asciiPath(1000)
	got := loggedPathOf(t, outside, httpserver.WithTemplatePathsUnder("/api/sessions/"))
	if want := asciiPath(defaultLoggedPathCap) + truncatedMarker; got != want {
		t.Errorf("path outside the prefix logged at %d bytes, want the cut at %d", len(got), defaultLoggedPathCap)
	}
	// A matched template is a string the server registered, so it is far under
	// the cap and comes through untouched.
	if p := loggedPathFor(t, http.MethodDelete, "/api/sessions/"+fakeSessionID,
		httpserver.WithTemplatePathsUnder("/api/sessions/")); p != "/api/sessions/{id}" {
		t.Errorf("logged path = %q, want the matched template unchanged", p)
	}
}

func TestLogging_boundsTheLoggedMethod(t *testing.T) {
	// net/http validates the method's CHARSET before a handler runs, so LENGTH is
	// the axis left open — and it is bounded only by the request line, a megabyte
	// at this package's own MaxHeaderBytes default.
	cases := []struct {
		name, method, want string
	}{
		{"GET", http.MethodGet, http.MethodGet},
		// The longest method in IANA's registry (RFC 4437 §7). A real method must
		// log as itself, which is what rules out a tighter bound.
		{"longest registered method", "UPDATEREDIRECTREF", "UPDATEREDIRECTREF"},
		{"exactly at the cap", strings.Repeat("M", 24), strings.Repeat("M", 24)},
		// One byte over: a fixed placeholder, never a prefix. "MMMM…M" cut to 24
		// reads as a method somebody tried, and grepping the upstream for it finds
		// nothing.
		{"one byte over the cap", strings.Repeat("M", 25), overlongMethod},
		{"absurd method", strings.Repeat("Z", 4096), overlongMethod},
		// A token of punctuation is a legal method (RFC 9110 §5.6.2) and reaches a
		// handler, so the bound cannot assume methods look like words.
		{"punctuation token", "M!#$%&'*+-.^_`|~", "M!#$%&'*+-.^_`|~"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := loggedAttrs(t, okHandler(), requestWithPath(tc.method, "/x"))["method"].(string)
			if got != tc.want {
				t.Errorf("logged method (len %d) = %.40q, want %.40q", len(tc.method), got, tc.want)
			}
		})
	}
}

func TestLogging_overlongMethodDoesNotAlterRouting(t *testing.T) {
	// Only the LOGGED value changes. The mux still refuses the method the client
	// actually sent, with the status and the Allow header it would have produced
	// without this package in the chain.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	logCap := &captureHandler{}
	rr := httptest.NewRecorder()
	httpserver.Logging(httpserver.WithLogger(slog.New(logCap)))(mux).
		ServeHTTP(rr, requestWithPath(strings.Repeat("M", 100), "/x"))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 (routing must be untouched)", rr.Code)
	}
	// "GET, HEAD" is what ServeMux advertises for a GET-only pattern, unchanged
	// by anything here.
	if allow := rr.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	m := attrsOf(recs[0])
	if m["method"] != overlongMethod {
		t.Errorf("logged method = %v, want the placeholder", m["method"])
	}
	if m["status"] != int64(http.StatusMethodNotAllowed) {
		t.Errorf("logged status = %v, want 405", m["status"])
	}
}

func TestLogging_boundsTheMethodInAHookFailureDiagnostic(t *testing.T) {
	// The hook-failure diagnostics log a method too, and they land in the same
	// stream as the access line. A bound that covered only the access line would
	// leave a panicking hook as the way to get an unbounded method into the log.
	logCap := &captureHandler{}
	httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithLogLevel(func(*http.Request, int) slog.Level { panic("level boom") }))(okHandler()).ServeHTTP(httptest.NewRecorder(), requestWithPath(strings.Repeat("M", 300), "/x"))

	recs := logCap.snapshot()
	if len(recs) != 2 {
		t.Fatalf("got %d log records, want the diagnostic plus the access line", len(recs))
	}
	for _, rec := range recs {
		if got := attrsOf(rec)["method"]; got != overlongMethod {
			t.Errorf("%q record logged method %v, want the placeholder", rec.Message, got)
		}
	}
}

// The ten values the metric's method label can take, spelled out here rather
// than read from the package: the closed set IS the public ceiling, so a test
// that imported the constants could not notice the set changing.
var permittedMetricMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodDelete, http.MethodConnect, http.MethodOptions,
	http.MethodTrace, http.MethodPatch, metricMethodBucket,
}

const (
	metricMethodBucket = "other"
	metricUnmatched    = "unmatched"
	// A method that is a legal RFC 9110 §5.6.2 token of pure punctuation. Sent
	// over a real socket it reaches a handler, so the bucket cannot assume a
	// method looks like a word.
	hostileMethodToken = "M!#$%&'*+-.^_`|~"
)

// routeLabelRequest builds the minimal request routeMetricLabels reads — the
// method, the matched pattern, a path — as a struct literal.
// httptest.NewRequest cannot express two of the cases this derivation must
// bound: it substitutes GET for an empty method, and it panics on a method
// carrying a space (a real socket refuses that one at parse time, but the
// derivation is a pure function and must not depend on who calls it).
func routeLabelRequest(method, pattern string) *http.Request {
	return &http.Request{
		Method:  method,
		Pattern: pattern,
		URL:     &url.URL{Path: "/beat/x"},
	}
}

func TestRouteMetricLabels_labelPairs(t *testing.T) {
	// The pattern is set directly so every shape is covered including ones a
	// single mux cannot produce at once; the mux-driven tests below pin that
	// http.ServeMux really does assign these patterns.
	cases := []struct {
		name, method, pattern, wantMethod, wantPath string
	}{
		// The nine standard methods record VERBATIM, against a method-agnostic
		// pattern (which http.ServeMux hands every method to, and whose path
		// label is the pattern itself).
		{"GET", http.MethodGet, "/beat/{id}", http.MethodGet, "/beat/{id}"},
		{"HEAD", http.MethodHead, "/beat/{id}", http.MethodHead, "/beat/{id}"},
		{"POST", http.MethodPost, "/beat/{id}", http.MethodPost, "/beat/{id}"},
		{"PUT", http.MethodPut, "/beat/{id}", http.MethodPut, "/beat/{id}"},
		{"DELETE", http.MethodDelete, "/beat/{id}", http.MethodDelete, "/beat/{id}"},
		{"CONNECT", http.MethodConnect, "/beat/{id}", http.MethodConnect, "/beat/{id}"},
		{"OPTIONS", http.MethodOptions, "/beat/{id}", http.MethodOptions, "/beat/{id}"},
		{"TRACE", http.MethodTrace, "/beat/{id}", http.MethodTrace, "/beat/{id}"},
		{"PATCH", http.MethodPatch, "/beat/{id}", http.MethodPatch, "/beat/{id}"},

		// Everything outside that set collapses onto ONE bucket, whatever it
		// looks like. PROPFIND is a real registered method and still buckets:
		// the set is the standard nine, not the IANA registry, because the
		// registry is open-ended and a label domain must not be.
		{"registered but non-standard method", "PROPFIND", "/", metricMethodBucket, "/"},
		{"vendor method", "PURGE", "/", metricMethodBucket, "/"},
		{"attacker-shaped token", hostileMethodToken, "/", metricMethodBucket, "/"},
		{"absurdly long token", strings.Repeat("M", 4096), "/", metricMethodBucket, "/"},
		// HTTP methods are case-SENSITIVE (RFC 9110 §9.1), so "get" is not GET.
		// Folding it in would hand a caller a second spelling of a real series;
		// bucketing it is the only reading that keeps the domain closed.
		{"lowercase get is not GET", "get", "/", metricMethodBucket, "/"},
		{"mixed-case Get is not GET", "Get", "/", metricMethodBucket, "/"},
		{"empty method", "", "/", metricMethodBucket, "/"},

		// A method-BEARING pattern: the path label drops the method prefix,
		// which the method label carries in its own right.
		{"method-bearing pattern", http.MethodGet, "GET /beat/{id}", http.MethodGet, "/beat/{id}"},
		{"method-bearing exact path", http.MethodPost, "POST /api/sessions", http.MethodPost, "/api/sessions"},
		// The case that motivated reading the request instead of the pattern:
		// http.ServeMux routes HEAD to a GET-only pattern, so a pattern-derived
		// method label would record GET here while the access line for the same
		// request_id records HEAD.
		{"HEAD against a GET-only pattern", http.MethodHead, "GET /beat/{id}", http.MethodHead, "/beat/{id}"},
		// Still a string the server registered, so still bounded; the host is
		// kept rather than stripped because it is part of what matched.
		{"host-qualified pattern", http.MethodGet, "GET example.com/beat/{id}", http.MethodGet, "example.com/beat/{id}"},

		// Nothing matched: the PATH collapses, so every scanner probe lands in
		// one series. The METHOD does not collapse with it — it is already
		// bounded to ten values with no help from the route table, so there is
		// nothing left for a collapse to protect, and keeping it means a 404
		// flood is still visible per method.
		{"nothing matched", http.MethodGet, "", http.MethodGet, metricUnmatched},
		{"nothing matched, hostile method", hostileMethodToken, "", metricMethodBucket, metricUnmatched},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method, path := httpserver.RouteMetricLabelsForTest(routeLabelRequest(tc.method, tc.pattern))
			if method != tc.wantMethod || path != tc.wantPath {
				t.Errorf("routeMetricLabels(method %.40q, pattern %q) = (%.40q, %q), want (%q, %q)",
					tc.method, tc.pattern, method, path, tc.wantMethod, tc.wantPath)
			}
		})
	}
}

func TestRouteMetricLabels_methodLabelIsBoundedByConstruction(t *testing.T) {
	// The bug this derivation exists to end. An app registering a "/" catch-all
	// (an SPA fallback) has a NEVER-empty r.Pattern, so an "unmatched" collapse
	// keyed on the empty pattern never fires — and a derivation that trusts
	// r.Method verbatim there hands an unauthenticated caller the label: one
	// permanent series per token, in this process and in every observer
	// scraping it. The bound must therefore hold for every pattern, including
	// none at all.
	permitted := make(map[string]bool, len(permittedMetricMethods))
	for _, m := range permittedMetricMethods {
		permitted[m] = true
	}
	hostile := []string{
		strings.Repeat("Z", 300), hostileMethodToken, "PROPFIND", "get", "",
		"GET ", " GET", "GET\tHEAD", "gEt",
	}
	for _, pattern := range []string{"", "/", "/api/sessions/", "GET /beat/{id}"} {
		for _, m := range hostile {
			method, _ := httpserver.RouteMetricLabelsForTest(routeLabelRequest(m, pattern))
			if !permitted[method] {
				t.Errorf("pattern %q, method %.40q: label %.40q is outside the ten permitted values",
					pattern, m, method)
			}
		}
	}
}

// routeMetricMux is a route table with the three shapes that decide a path
// label: a method-bearing template, a method-agnostic subtree, and the "/"
// catch-all that makes r.Pattern never empty.
func routeMetricMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /beat/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/api/sessions/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func TestRouteMetricLabels_againstARealMux(t *testing.T) {
	// The patterns asserted above are only worth asserting if http.ServeMux
	// really assigns them, so this drives the labels through a real route table
	// — including the catch-all that makes r.Pattern never empty.
	cases := []struct {
		name, method, path, wantMethod, wantPath string
	}{
		{"matched template", http.MethodGet, "/beat/abc123", http.MethodGet, "/beat/{id}"},
		// ServeMux routes HEAD to the GET-only pattern, so the PATH is that
		// pattern's template while the METHOD stays the verb that arrived —
		// which is what keeps this line's metric and its access line agreeing.
		{"HEAD against a GET pattern", http.MethodHead, "/beat/abc123", http.MethodHead, "/beat/{id}"},
		{"method-agnostic subtree", http.MethodDelete, "/api/sessions/abc", http.MethodDelete, "/api/sessions/"},
		// Falls through to the SPA catch-all, where an app's own "unmatched"
		// collapse could never fire. The token buckets anyway.
		{"scanner probe onto the catch-all", "PROPFIND", "/wp-login.php", metricMethodBucket, "/"},
		{"hostile token onto the catch-all", hostileMethodToken, "/", metricMethodBucket, "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			httpserver.Logging(httpserver.WithLogger(discardLogger()), httpserver.WithRecordRouteMetric(func(m httpserver.RequestMetric) {
				gotMethod, gotPath = m.Method, m.Path
			}))(routeMetricMux()).ServeHTTP(httptest.NewRecorder(), requestWithPath(tc.method, tc.path))

			if gotMethod != tc.wantMethod || gotPath != tc.wantPath {
				t.Errorf("%.40q %s: labels = (%.40q, %q), want (%q, %q)",
					tc.method, tc.path, gotMethod, gotPath, tc.wantMethod, tc.wantPath)
			}
		})
	}
}

func TestRouteMetricLabels_unmatchedCollapsesThePathLabel(t *testing.T) {
	// Without a catch-all, a 404 clears the pattern and every probe lands on the
	// one path label, so a scanner cannot mint a series per URL. The method
	// label stays the caller's method when it is standard and buckets otherwise
	// — bounded either way, so the pair is at most ten series for all 404s.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /beat/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	cases := []struct {
		method, target, wantMethod string
	}{
		{http.MethodGet, "/nope", http.MethodGet},
		{http.MethodGet, "/wp-login.php", http.MethodGet},
		{http.MethodGet, "/beat", http.MethodGet},
		{http.MethodDelete, "/nope", http.MethodDelete},
		{"PROPFIND", "/.git/config", metricMethodBucket},
		{hostileMethodToken, "/nope", metricMethodBucket},
	}
	for _, tc := range cases {
		var gotMethod, gotPath string
		httpserver.Logging(httpserver.WithLogger(discardLogger()), httpserver.WithRecordRouteMetric(func(m httpserver.RequestMetric) {
			gotMethod, gotPath = m.Method, m.Path
		}))(mux).ServeHTTP(httptest.NewRecorder(), requestWithPath(tc.method, tc.target))

		if gotMethod != tc.wantMethod || gotPath != metricUnmatched {
			t.Errorf("%.40q %s: labels = (%.40q, %q), want (%q, %q)",
				tc.method, tc.target, gotMethod, gotPath, tc.wantMethod, metricUnmatched)
		}
	}
}

// routeMetricCall is one WithRecordRouteMetric invocation, as the application
// sees it.
type routeMetricCall struct {
	method, path string
	status       int
	d            time.Duration
}

func TestWithRecordRouteMetric_handsTheAppBoundedLabels(t *testing.T) {
	// The end-to-end shape a consumer actually wires: a real ServeMux behind
	// Chain + Logging. The app's hook is a plain recorder that does NOT derive
	// anything — the point of the option is that there is nothing left to
	// derive, so a consumer cannot repeat the mistake of trusting r.Method.
	var calls []routeMetricCall
	handler := httpserver.Chain(routeMetricMux(),
		httpserver.Logging(
			httpserver.WithLogger(discardLogger()),
			httpserver.WithRecordRouteMetric(func(m httpserver.RequestMetric) {
				calls = append(calls, routeMetricCall{method: m.Method, path: m.Path, status: m.Status, d: m.Latency})
			}),
		),
		httpserver.Recoverer(),
	)

	cases := []struct {
		name, method, target string
		want                 routeMetricCall
	}{
		{
			"matched template", http.MethodGet, "/beat/abc123",
			routeMetricCall{method: http.MethodGet, path: "/beat/{id}", status: http.StatusOK},
		},
		{
			"HEAD against the GET-only pattern", http.MethodHead, "/beat/abc123",
			routeMetricCall{method: http.MethodHead, path: "/beat/{id}", status: http.StatusOK},
		},
		{
			"method-agnostic subtree", http.MethodDelete, "/api/sessions/" + fakeSessionID,
			routeMetricCall{method: http.MethodDelete, path: "/api/sessions/", status: http.StatusOK},
		},
		{
			// The subflux shape: a "/" catch-all, an attacker-chosen method and
			// an attacker-chosen path. Both labels are fixed strings.
			"hostile probe onto the catch-all", hostileMethodToken, "/" + strings.Repeat("z", 2000),
			routeMetricCall{method: metricMethodBucket, path: "/", status: http.StatusOK},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls = nil

			handler.ServeHTTP(httptest.NewRecorder(), requestWithPath(tc.method, tc.target))

			if len(calls) != 1 {
				t.Fatalf("got %d hook calls, want exactly 1", len(calls))
			}
			got := calls[0]
			if got.method != tc.want.method || got.path != tc.want.path || got.status != tc.want.status {
				t.Errorf("hook got (%.40q, %q, %d), want (%q, %q, %d)",
					got.method, got.path, got.status, tc.want.method, tc.want.path, tc.want.status)
			}
			if got.d < 0 {
				t.Errorf("hook duration = %v, want a non-negative latency", got.d)
			}
		})
	}
}

func TestWithRecordRouteMetric_pathLabelIgnoresTheLogPathPolicy(t *testing.T) {
	// The label is the matched ROUTE, so a recorded-path policy (which exists to
	// redact a credential out of the LOG) neither feeds nor weakens it. The two
	// are complementary: one bounds a log line, the other bounds a label domain.
	var gotPath string
	h := httpserver.Logging(httpserver.WithLogger(discardLogger()), httpserver.WithPathFunc(func(*http.Request) string { return "/redacted" }), httpserver.WithRecordRouteMetric(func(m httpserver.RequestMetric) { gotPath = m.Path }))(routeMetricMux())
	h.ServeHTTP(httptest.NewRecorder(), requestWithPath(http.MethodGet, "/beat/abc123"))

	if gotPath != "/beat/{id}" {
		t.Errorf("hook path = %q, want the matched route template", gotPath)
	}
}

func TestWithRecordRouteMetric_firesOnPanicAndSkipsSkippedPaths(t *testing.T) {
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	mux := http.NewServeMux()
	mux.Handle("GET /boom", panicking)
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	var calls []routeMetricCall
	// Recoverer INSIDE the logger, the documented order: the panic is answered
	// 500 and the metric still records, from the same deferred emit as the
	// access line.
	h := httpserver.Chain(mux,
		httpserver.Logging(
			httpserver.WithLogger(discardLogger()),
			httpserver.WithSkipPaths("/events"),
			httpserver.WithRecordRouteMetric(func(m httpserver.RequestMetric) {
				calls = append(calls, routeMetricCall{method: m.Method, path: m.Path, status: m.Status})
			}),
		),
		httpserver.Recoverer(httpserver.WithRecoverLogger(discardLogger())),
	)

	h.ServeHTTP(httptest.NewRecorder(), requestWithPath(http.MethodGet, "/boom"))
	if len(calls) != 1 {
		t.Fatalf("got %d hook calls for the panicking route, want exactly 1", len(calls))
	}
	if want := (routeMetricCall{method: http.MethodGet, path: "/boom", status: http.StatusInternalServerError}); calls[0] != want {
		t.Errorf("hook got %+v, want %+v", calls[0], want)
	}

	// A skipped path emits no access line, so it records no metric either: a
	// stream's open-to-close duration paired with a synthetic status misleads.
	calls = nil
	h.ServeHTTP(httptest.NewRecorder(), requestWithPath(http.MethodGet, "/events"))
	if len(calls) != 0 {
		t.Errorf("got %d hook calls for a skipped path, want 0", len(calls))
	}
}

func TestWithRecordRouteMetric_panickingHookStillEmitsAccessLine(t *testing.T) {
	// The hook runs in the outer Logging defer, outside Recoverer, so an
	// unisolated panic here would escape to net/http and close the connection on
	// an otherwise completed request.
	logCap := &captureHandler{}
	h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithRecordRouteMetric(func(httpserver.RequestMetric) { panic("metric boom") }))(okHandler())

	func() {
		defer func() {
			if v := recover(); v != nil {
				t.Errorf("route metric hook panic escaped Logging: %v", v)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), requestWithPath(http.MethodGet, "/x"))
	}()

	recs := logCap.snapshot()
	var sawAccess, sawFailure bool
	for _, rec := range recs {
		switch rec.Message {
		case "http":
			sawAccess = true
		case "httpserver: metric hook failed":
			sawFailure = true
		}
	}
	if !sawAccess {
		t.Error("access line was not emitted after the route metric hook panicked")
	}
	if !sawFailure {
		t.Error("expected a 'metric hook failed' diagnostic, got none")
	}
}

func TestWithRecordRouteMetric_nilIsNoOp(t *testing.T) {
	// The package's skip-nil convention: a trailing nil neither enables the hook
	// nor clears the one already applied.
	var got string
	httpserver.Logging(httpserver.WithLogger(discardLogger()), httpserver.WithRecordRouteMetric(func(m httpserver.RequestMetric) { got = m.Method }), httpserver.WithRecordRouteMetric(nil))(routeMetricMux()).ServeHTTP(httptest.NewRecorder(), requestWithPath(http.MethodDelete, "/api/sessions/x"))

	if got != http.MethodDelete {
		t.Errorf("hook method = %q, want the prior hook still installed and firing", got)
	}
}
