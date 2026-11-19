package httpwire_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// apiDoc is the fixture table's document, as wiregen would write it.
func apiDoc(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// editDoc answers the fixture document after f edits its decoded form.
func editDoc(t *testing.T, f func(paths map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(apiDoc(t), &doc); err != nil {
		t.Fatal(err)
	}
	f(doc["paths"].(map[string]any))
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixtureIfMatch is an If-Match the fixture's IfMatch row takes; its
// handler compares no tag.
const fixtureIfMatch = `"0000000000000000.0"`

// fixtureGuard admits everyone; it carries the fixture guard's codes.
type fixtureGuard struct{}

func (fixtureGuard) Wrap(_ *httpwire.Endpoint, next http.Handler) http.Handler { return next }
func (fixtureGuard) Codes(a httpwire.Access) []string                          { return guardCodes(a) }

func (fixtureGuard) Shape(*http.Request) httpwire.Shape {
	return httpwire.Shape{Principal: httpwire.ShapeAnonymous}
}

func (fixtureGuard) Shapes() []httpwire.Shape {
	return []httpwire.Shape{{Principal: httpwire.ShapeAnonymous}}
}
func (fixtureGuard) Admits(httpwire.Shape, httpwire.Access) bool { return true }
func (fixtureGuard) Person(context.Context) error                { return nil }

type fixtureFeed struct{}

func (fixtureFeed) Position(*http.Request, []string) string { return `store=1;epoch="a"` }

type (
	noP = httpwire.None
	noQ = httpwire.None
	noB = httpwire.None
)

// fixtureBindings binds every row of fixtureTable, by name.
func fixtureBindings() map[string]httpwire.Binding {
	return map[string]httpwire.Binding{
		"jobs.list": httpwire.Bind("jobs.list", func(context.Context, httpwire.Call[noP, jobQuery, noB]) ([]job, error) {
			return []job{{ID: "a"}}, nil
		}),
		"jobs.get": httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB]) (job, error) {
			return job{ID: "one"}, nil
		}),
		"jobs.start": httpwire.Bind("jobs.start", func(context.Context, httpwire.Call[noP, noQ, startRequest]) (job, error) {
			return job{ID: "new"}, nil
		}),
		"settings.save": httpwire.Bind("settings.save", func(context.Context, httpwire.Call[noP, noQ, otherBody]) (httpwire.None, error) {
			return httpwire.None{}, nil
		}),
		"archive.export": httpwire.BindRaw("archive.export", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK"))
		})),
		"events.wait": httpwire.Bind("events.wait", func(context.Context, httpwire.Call[noP, noQ, noB]) (httpwire.Status[nudge], error) {
			return httpwire.Answer(http.StatusOK, nudge{At: 7}), nil
		}),
		"live.stream": httpwire.BindStream("live.stream", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: changed\ndata: {}\n\n")
		})),
		"auth.sso": httpwire.BindRaw("auth.sso", http.RedirectHandler("/signed-in", http.StatusFound)),
	}
}

// fixtureOptions are the options fixtureTable needs, by name.
func fixtureOptions(t *testing.T) map[string]httpwire.Option {
	t.Helper()
	limiter := fixtureLimiter(t)
	return map[string]httpwire.Option{
		"schema":      httpwire.WithSchema(apiDoc(t)),
		"guard":       httpwire.WithGuard(fixtureGuard{}),
		"credential":  httpwire.WithCredential(func(*http.Request) string { return "s:fixture" }),
		"cursorOwner": httpwire.WithCursorOwner(func(*http.Request) string { return "u:fixture" }),
		"keyRate":     httpwire.WithKeyRate(limiter, func(*http.Request) (string, bool) { return "", false }),
		"hold":        httpwire.WithHold(func(next http.Handler) http.Handler { return next }),
		"floor": httpwire.WithClientFloor(httpwire.ClientFloor{
			Below:   func(string) bool { return false },
			Message: func(v string) string { return "install a client newer than " + v },
		}),
		"feed": httpwire.WithFeed(fixtureFeed{}),
	}
}

// apiCase is one New call over the fixture, which a test edits first.
type apiCase struct {
	tb    *httpwire.Table
	binds map[string]httpwire.Binding
	opts  map[string]httpwire.Option
	extra []httpwire.Binding
}

func newCase(t *testing.T) *apiCase {
	t.Helper()
	return &apiCase{tb: fixtureTable(t), binds: fixtureBindings(), opts: fixtureOptions(t)}
}

func (c *apiCase) build() (*httpwire.API, error) {
	var bs []httpwire.Binding
	for _, name := range slices.Sorted(maps.Keys(c.binds)) {
		bs = append(bs, c.binds[name])
	}
	var opts []httpwire.Option
	for _, name := range slices.Sorted(maps.Keys(c.opts)) {
		opts = append(opts, c.opts[name])
	}
	return httpwire.New(c.tb, append(bs, c.extra...), opts...)
}

func (c *apiCase) mustBuild(t *testing.T) *httpwire.API {
	t.Helper()
	api, err := c.build()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return api
}

// serve answers one request through api.
func serve(api http.Handler, method, target, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, r)
	return rec
}

// outerHeaders is the app's middleware before the API, setting headers
// every answer must carry, a refusal that replaces a handler's included.
func outerHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		h.ServeHTTP(w, r)
	})
}

func checkOuterHeaders(t *testing.T, h http.Header) {
	t.Helper()
	if h.Get("Cache-Control") != "no-store" || h.Get("Content-Security-Policy") != "default-src 'none'" {
		t.Errorf("headers %v, want the outer Cache-Control and Content-Security-Policy", h)
	}
}

// envelopeOf decodes an error answer, failing when it is not the envelope.
func envelopeOf(t *testing.T, rec *httptest.ResponseRecorder) httpwire.Envelope {
	t.Helper()
	env, err := httpwire.DecodeEnvelope(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("answer %d is not the envelope (%v): %q", rec.Code, err, rec.Body.String())
	}
	return env
}

// logSink keeps every record a logger writes, as one line each.
type logSink struct {
	lines []string
	mu    sync.Mutex
}

func (*logSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *logSink) Handle(_ context.Context, r slog.Record) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s", r.Level, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, b.String())
	return nil
}

func (s *logSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *logSink) WithGroup(string) slog.Handler      { return s }

func (s *logSink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lines)
}

// has reports whether a line starts with level and holds every part.
func (s *logSink) has(level string, parts ...string) bool {
	return slices.ContainsFunc(s.all(), func(l string) bool {
		if !strings.HasPrefix(l, level+" ") {
			return false
		}
		for _, p := range parts {
			if !strings.Contains(l, p) {
				return false
			}
		}
		return true
	})
}
