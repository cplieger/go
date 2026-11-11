package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubSignal struct{ healthy bool }

func (s stubSignal) Healthy() bool { return s.healthy }

func serve(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	return rec
}

// TestCheckHandler_writesTheBody pins the wire contract byte for byte: the
// code, the three headers and the JSON body, with no timestamp.
func TestCheckHandler_writesTheBody(t *testing.T) {
	for name, tc := range map[string]struct {
		h    http.Handler
		code int
		body string
	}{
		"healthy":   {Handler(stubSignal{healthy: true}), http.StatusOK, `{"status":"ok"}`},
		"unhealthy": {Handler(stubSignal{healthy: false}), http.StatusServiceUnavailable, `{"status":"unhealthy","reasons":["the app reports itself unhealthy"]}`},
		"nil":       {Handler(nil), http.StatusServiceUnavailable, `{"status":"unhealthy","reasons":["the app reports itself unhealthy"]}`},
		"no checks": {CheckHandler(), http.StatusOK, `{"status":"ok"}`},
		"empty reason": {
			CheckHandler(Check{Signal: stubSignal{healthy: false}}), http.StatusServiceUnavailable,
			`{"status":"unhealthy","reasons":["the app reports itself unhealthy"]}`,
		},
		"two failing of three": {CheckHandler(
			Check{Signal: stubSignal{healthy: false}, Reason: "the store is not writable"},
			Check{Signal: stubSignal{healthy: true}, Reason: "the listener has not started"},
			Check{Signal: nil, Reason: "the watchdog deadline passed"},
		), http.StatusServiceUnavailable, `{"status":"unhealthy","reasons":["the store is not writable","the watchdog deadline passed"]}`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := serve(t, tc.h)
			if rec.Code != tc.code {
				t.Errorf("code = %d, want %d", rec.Code, tc.code)
			}
			if got := rec.Body.String(); got != tc.body+"\n" {
				t.Errorf("body = %q, want %q", got, tc.body+"\n")
			}
			for key, want := range map[string]string{
				"Content-Type":           "application/json",
				"Cache-Control":          "no-store",
				"X-Content-Type-Options": "nosniff",
			} {
				if got := rec.Header().Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

// TestCheckHandler_readsEachSignalPerRequest pins that the verdict follows the
// signal on every request, and that a caller's later edit of its slice changes
// nothing.
func TestCheckHandler_readsEachSignalPerRequest(t *testing.T) {
	st := NewStatus(WithLogger(discard))
	checks := []Check{{Signal: st, Reason: "the loop stopped"}}
	h := CheckHandler(checks...)
	checks[0].Reason = "edited"
	if rec := serve(t, h); rec.Code != http.StatusServiceUnavailable || rec.Body.String() != `{"status":"unhealthy","reasons":["the loop stopped"]}`+"\n" {
		t.Errorf("unhealthy: %d %q, want 503 with the wired reason", rec.Code, rec.Body.String())
	}
	st.Set(true)
	if rec := serve(t, h); rec.Code != http.StatusOK {
		t.Errorf("healthy: %d, want 200", rec.Code)
	}
}

// TestCheckHandler_onTheMuxRoute pins what "GET /healthz" registration gives:
// HEAD answers with the headers and no body, and other methods get 405.
func TestCheckHandler_onTheMuxRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", Handler(stubSignal{healthy: true}))
	for method, want := range map[string]int{http.MethodGet: 200, http.MethodHead: 200, http.MethodPost: 405, http.MethodPut: 405} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(method, "/healthz", nil))
			if rec.Code != want {
				t.Errorf("%s /healthz = %d, want %d", method, rec.Code, want)
			}
		})
	}
}
