package httpwire_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// ownAnswerAPI binds the fixture's Raw, Stream and Redirect rows to h and
// logs to the returned sink.
func ownAnswerAPI(t *testing.T, edit func(*apiCase), h http.HandlerFunc) (*httpwire.API, *logSink) {
	t.Helper()
	c := newCase(t)
	sink := &logSink{}
	c.opts["logger"] = httpwire.WithLogger(slog.New(sink))
	c.binds["archive.export"] = httpwire.BindRaw("archive.export", h)
	c.binds["live.stream"] = httpwire.BindStream("live.stream", h)
	c.binds["auth.sso"] = httpwire.BindRaw("auth.sso", h)
	if edit != nil {
		edit(c)
	}
	return c.mustBuild(t), sink
}

func writesStatus(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, "x")
	}
}

func TestOwnAnswerRowLogsAnUndeclaredSuccessStatus(t *testing.T) {
	declare204 := func(name string) func(*apiCase) {
		return func(c *apiCase) {
			row(c.tb, name).Successes = []httpwire.Success{{Status: http.StatusNoContent}}
		}
	}
	for name, tc := range map[string]struct {
		edit         func(*apiCase)
		method, path string
		status       int
		logged       bool
	}{
		"a Raw row's primary":             {nil, http.MethodPost, "/api/archive", http.StatusOK, false},
		"a Raw row's undeclared 201":      {nil, http.MethodPost, "/api/archive", http.StatusCreated, true},
		"a Raw row's undeclared 204":      {nil, http.MethodPost, "/api/archive", http.StatusNoContent, true},
		"a Raw row's declared 204":        {declare204("archive.export"), http.MethodPost, "/api/archive", http.StatusNoContent, false},
		"a Raw row's refusal":             {nil, http.MethodPost, "/api/archive", http.StatusBadRequest, false},
		"a Stream row's primary":          {nil, http.MethodGet, "/api/stream", http.StatusOK, false},
		"a Stream row's undeclared 202":   {nil, http.MethodGet, "/api/stream", http.StatusAccepted, true},
		"a Stream row's declared 204":     {declare204("live.stream"), http.MethodGet, "/api/stream", http.StatusNoContent, false},
		"a Redirect row's primary":        {nil, http.MethodGet, "/api/sso", http.StatusFound, false},
		"a Redirect row's undeclared 303": {nil, http.MethodGet, "/api/sso", http.StatusSeeOther, true},
		"a Redirect row's 200":            {nil, http.MethodGet, "/api/sso", http.StatusOK, true},
		"a Redirect row's refusal":        {nil, http.MethodGet, "/api/sso", http.StatusForbidden, false},
	} {
		t.Run(name, func(t *testing.T) {
			api, sink := ownAnswerAPI(t, tc.edit, writesStatus(tc.status))
			rec := serve(api, tc.method, tc.path, "")
			if rec.Code != tc.status {
				t.Errorf("answer %d, want the handler's %d written as it is", rec.Code, tc.status)
			}
			logged := sink.has("ERROR", "row=", "status="+strconv.Itoa(tc.status))
			if logged != tc.logged {
				t.Errorf("logged %v, want %v: %v", logged, tc.logged, sink.all())
			}
		})
	}
	t.Run("a body written with no status is a 200", func(t *testing.T) {
		api, sink := ownAnswerAPI(t, nil, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "x") })
		if rec := serve(api, http.MethodGet, "/api/sso", ""); rec.Code != http.StatusOK || !sink.has("ERROR", "row=auth.sso", "status=200") {
			t.Errorf("answer %d, log %v", rec.Code, sink.all())
		}
	})
	t.Run("a handler that writes nothing is a 200", func(t *testing.T) {
		api, sink := ownAnswerAPI(t, nil, func(http.ResponseWriter, *http.Request) {})
		if rec := serve(api, http.MethodGet, "/api/sso", ""); rec.Code != http.StatusOK || !sink.has("ERROR", "row=auth.sso", "status=200") {
			t.Errorf("answer %d, log %v", rec.Code, sink.all())
		}
		if rec := serve(api, http.MethodPost, "/api/archive", ""); rec.Code != http.StatusOK || sink.has("ERROR", "row=archive.export") {
			t.Errorf("answer %d, log %v", rec.Code, sink.all())
		}
	})
	t.Run("a second status is the handler's own bug", func(t *testing.T) {
		api, sink := ownAnswerAPI(t, nil, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.WriteHeader(http.StatusCreated)
		})
		if rec := serve(api, http.MethodPost, "/api/archive", ""); rec.Code != http.StatusOK || len(sink.all()) != 0 {
			t.Errorf("answer %d, log %v", rec.Code, sink.all())
		}
	})
}

func TestStreamRowStillFlushes(t *testing.T) {
	var direct, controlled bool
	var flushErr error
	api, _ := ownAnswerAPI(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, direct = w.(http.Flusher)
		flushErr = http.NewResponseController(w).Flush()
		controlled = true
	})
	rec := serve(api, http.MethodGet, "/api/stream", "")
	if !direct || !controlled || flushErr != nil || !rec.Flushed {
		t.Errorf("http.Flusher %v, Flush error %v, recorder flushed %v", direct, flushErr, rec.Flushed)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Wire-Version") == "" {
		t.Errorf("answer %d with Wire-Version %q", rec.Code, rec.Header().Get("Wire-Version"))
	}
}

// rawWriteAPI caps the fixture's Raw write row at 8 bytes and binds it to
// h, counting its runs.
func rawWriteAPI(t *testing.T, h http.HandlerFunc) (*httpwire.API, *int) {
	t.Helper()
	runs := 0
	api, _ := ownAnswerAPI(t, func(c *apiCase) { row(c.tb, "archive.export").MaxBody = 8 }, func(w http.ResponseWriter, r *http.Request) {
		runs++
		h(w, r)
	})
	return api, &runs
}

// chunked is a request whose body length the server is not told.
func chunked(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/archive", io.MultiReader(strings.NewReader(body)))
	r.ContentLength = -1
	return r
}

func TestRawWriteBodyIsCapped(t *testing.T) {
	readsAll := func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Disposition", "attachment")
		_, _ = w.Write(b)
	}
	t.Run("a declared length over the cap runs no handler", func(t *testing.T) {
		api, runs := rawWriteAPI(t, readsAll)
		rec := serve(api, http.MethodPost, "/api/archive", "123456789")
		if env := envelopeOf(t, rec); rec.Code != http.StatusRequestEntityTooLarge || env.Code != "body_too_large" || *runs != 0 {
			t.Errorf("answer %d %s, handler ran %d times", rec.Code, env.Code, *runs)
		}
	})
	for name, h := range map[string]http.HandlerFunc{
		"a handler answering its own refusal": readsAll,
		"a handler answering a success": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, "ok")
		},
		"a handler writing nothing": func(_ http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) },
	} {
		t.Run("an unannounced body over the cap, "+name, func(t *testing.T) {
			api, runs := rawWriteAPI(t, h)
			rec := answer(api, chunked("123456789"))
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusRequestEntityTooLarge || env.Code != "body_too_large" || *runs != 1 {
				t.Errorf("answer %d %s, handler ran %d times", rec.Code, env.Code, *runs)
			}
			if rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("Wire-Version") == "" {
				t.Errorf("headers %v: want the handler's dropped and the binder's kept", rec.Header())
			}
		})
	}
	t.Run("a read error is the handler's to see", func(t *testing.T) {
		var readErr error
		api, _ := rawWriteAPI(t, func(_ http.ResponseWriter, r *http.Request) { _, readErr = io.ReadAll(r.Body) })
		answer(api, chunked("123456789"))
		if _, ok := errors.AsType[*http.MaxBytesError](readErr); !ok {
			t.Errorf("read error %v, want *http.MaxBytesError", readErr)
		}
	})
	for name, r := range map[string]*http.Request{
		"a body at the cap":              httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader("12345678")),
		"an unannounced body at the cap": chunked("12345678"),
	} {
		t.Run(name, func(t *testing.T) {
			api, _ := rawWriteAPI(t, readsAll)
			if rec := answer(api, r); rec.Code != http.StatusOK || rec.Body.String() != "12345678" {
				t.Errorf("answer %d %q", rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("the table's cap when the row has none", func(t *testing.T) {
		c := newCase(t)
		c.tb.MaxBody = 4
		c.binds["archive.export"] = httpwire.BindRaw("archive.export", http.HandlerFunc(readsAll))
		rec := serve(c.mustBuild(t), http.MethodPost, "/api/archive", "12345")
		if env := envelopeOf(t, rec); env.Code != "body_too_large" {
			t.Errorf("answer %d %s", rec.Code, env.Code)
		}
	})
}
