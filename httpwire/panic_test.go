package httpwire_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// panicking binds name to a handler that panics with v before it writes
// anything, a Raw or Stream one after setting a header.
func panicking(name string, v any) httpwire.Binding {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/api/jobs/half")
		panic(v)
	})
	switch name {
	case "archive.export":
		return httpwire.BindRaw(name, h)
	case "live.stream":
		return httpwire.BindStream(name, h)
	}
	return httpwire.Bind(name, func(context.Context, httpwire.Call[jobParams, noQ, noB]) (job, error) {
		panic(v)
	})
}

func TestPanicAnswersInternalError(t *testing.T) {
	for name, tc := range map[string]struct{ row, method, target string }{
		"a JSON row":   {"jobs.get", http.MethodGet, "/api/jobs/abc"},
		"a Raw write":  {"archive.export", http.MethodPost, "/api/archive"},
		"a Stream row": {"live.stream", http.MethodGet, "/api/stream"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCase(t)
			sink := &logSink{}
			c.opts["logger"] = httpwire.WithLogger(slog.New(sink))
			c.binds[tc.row] = panicking(tc.row, "the handler failed")
			api := c.mustBuild(t)
			var rec *httptest.ResponseRecorder
			func() {
				defer func() {
					if v := recover(); v != nil {
						t.Errorf("the handler's panic reached the caller as %v", v)
					}
				}()
				rec = serve(api, tc.method, tc.target, "")
			}()
			if rec == nil {
				return
			}
			if rec.Code != http.StatusInternalServerError || envelopeOf(t, rec).Code != "internal_error" {
				t.Errorf("answer = %d %q, want 500 internal_error", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("Location"); got != "" {
				t.Errorf("the handler's Location %q is on the refusal", got)
			}
			if rec.Header().Get("Wire-Version") == "" || rec.Header().Get("X-Request-Id") == "" {
				t.Errorf("headers = %v, want Wire-Version and X-Request-Id", rec.Header())
			}
			if !sink.has("ERROR", "panic", "row="+tc.row, "the handler failed") {
				t.Errorf("log = %q, want the panic logged at ERROR with its row", sink.all())
			}
		})
	}
}

func TestPanicAfterTheAnswerStartedAbortsIt(t *testing.T) {
	c := newCase(t)
	sink := &logSink{}
	c.opts["logger"] = httpwire.WithLogger(slog.New(sink))
	c.binds["live.stream"] = httpwire.BindStream("live.stream", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: changed\ndata: {}\n\n"))
		panic("the stream failed")
	}))
	api := c.mustBuild(t)
	rec := httptest.NewRecorder()
	var got any
	func() {
		defer func() { got = recover() }()
		api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream", nil))
	}()
	if got != http.ErrAbortHandler {
		t.Errorf("the caller saw %v, want http.ErrAbortHandler so the cut answer reads as cut", got)
	}
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("answer = %d %q, want the stream as written and nothing after it", rec.Code, rec.Body)
	}
	if !sink.has("ERROR", "panic", "row=live.stream", "the stream failed") {
		t.Errorf("log = %q, want the panic logged at ERROR with its row", sink.all())
	}
}

func TestPanicErrAbortHandlerPassesThrough(t *testing.T) {
	c := newCase(t)
	sink := &logSink{}
	c.opts["logger"] = httpwire.WithLogger(slog.New(sink))
	c.binds["archive.export"] = panicking("archive.export", http.ErrAbortHandler)
	api := c.mustBuild(t)
	rec := httptest.NewRecorder()
	var got any
	func() {
		defer func() { got = recover() }()
		r := httptest.NewRequest(http.MethodPost, "/api/archive", nil)
		api.ServeHTTP(rec, r)
	}()
	if got != http.ErrAbortHandler {
		t.Errorf("the caller saw %v, want http.ErrAbortHandler untouched", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("answer body = %q, want nothing written", rec.Body)
	}
	if sink.has("ERROR", "panic") {
		t.Errorf("log = %q, want an abort logged as no panic", sink.all())
	}
}
