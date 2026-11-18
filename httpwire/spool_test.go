package httpwire_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/go/httpwire"
)

const spoolMax = 64

// spoolTable holds one Keyed Raw write row, capped at spoolMax bytes.
func spoolTable(t testing.TB) *httpwire.Table {
	t.Helper()
	return &httpwire.Table{
		Prefix: "/api", Catalog: fixtureCatalog(t),
		Endpoints: []httpwire.Endpoint{{
			Name: "archive.import", Method: http.MethodPost, Path: "/api/import", Summary: "Import an archive", Description: "Import an archive.",
			Kind: httpwire.Raw, ContentType: "text/plain", Access: session, Keyed: true, MaxBody: spoolMax,
		}},
	}
}

// spoolFixture serves spoolTable; its handler reads the whole body and
// notes the spool files the directory holds while it runs.
type spoolFixture struct {
	api     *httpwire.API
	log     *logSink
	gate    chan struct{} // when set, each run waits for it to close
	started chan struct{} // receives once per run that reached the gate
	panics  bool
	dir     string
	bodies  []string
	seen    [][]string // the directory's names during each run
	mu      sync.Mutex
}

func (sf *spoolFixture) handler(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	sf.mu.Lock()
	sf.bodies = append(sf.bodies, string(b))
	sf.seen = append(sf.seen, dirNames(sf.dir))
	panics, gate := sf.panics, sf.gate
	sf.mu.Unlock()
	if gate != nil {
		sf.started <- struct{}{}
		<-gate
	}
	if panics {
		panic("the handler failed")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("imported " + string(b)))
}

func (sf *spoolFixture) runs() int {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return len(sf.bodies)
}

func spoolOptions(t testing.TB, dir string, h http.HandlerFunc, l *slog.Logger, hold ...func(http.Handler) http.Handler) (*httpwire.Table, []httpwire.Binding, []httpwire.Option) {
	t.Helper()
	doc, err := os.ReadFile("testdata/openapi/spool.json")
	if err != nil {
		t.Fatal(err)
	}
	held := func(next http.Handler) http.Handler { return next }
	if len(hold) > 0 {
		held = hold[0]
	}
	return spoolTable(t), []httpwire.Binding{httpwire.BindRaw("archive.import", h)}, []httpwire.Option{
		httpwire.WithSchema(doc),
		httpwire.WithGuard(fixtureGuard{}),
		httpwire.WithCredential(func(*http.Request) string { return "s:a" }),
		httpwire.WithCursorOwner(func(*http.Request) string { return "u:a" }),
		httpwire.WithHold(held),
		httpwire.WithSpoolDir(dir),
		httpwire.WithLogger(l),
	}
}

func newSpool(t *testing.T, hold ...func(http.Handler) http.Handler) *spoolFixture {
	t.Helper()
	sf := &spoolFixture{dir: t.TempDir(), log: &logSink{}, started: make(chan struct{}, 2)}
	tb, bs, opts := spoolOptions(t, sf.dir, sf.handler, slog.New(sf.log), hold...)
	api, err := httpwire.New(tb, bs, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sf.api = api
	return sf
}

// send posts body under key; a negative length sends it with no
// Content-Length, as a chunked body arrives.
func (sf *spoolFixture) send(body, key string, length int64) *httptest.ResponseRecorder {
	r := keyedRequest(http.MethodPost, "/api/import", body, key)
	if length < 0 {
		r.Body = io.NopCloser(io.MultiReader(strings.NewReader(body)))
		r.ContentLength = -1
	}
	rec := httptest.NewRecorder()
	sf.api.ServeHTTP(rec, r)
	return rec
}

func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{"error: " + err.Error()}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSpoolCleanupTouchesOnlySpoolFiles(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "httpwire-spool-123"), "left by a crash")
	write(filepath.Join(dir, "httpwire-spool-"), "left by a crash")
	write(filepath.Join(dir, "accounts.db"), "accounts")
	write(filepath.Join(dir, "other"), "other")
	write(filepath.Join(dir, "my-httpwire-spool-1"), "another name")
	write(filepath.Join(outside, "target"), "target")
	if err := os.Mkdir(filepath.Join(dir, "httpwire-spool-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "httpwire-spool-dir", "inner"), "inner")
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(dir, "httpwire-spool-link")); err != nil {
		t.Fatal(err)
	}
	survivors := []string{"accounts.db", "httpwire-spool-dir", "httpwire-spool-link", "my-httpwire-spool-1", "other"}

	t.Run("a refused New removes nothing", func(t *testing.T) {
		tb, bs, opts := spoolOptions(t, dir, http.NotFound, slog.New(&logSink{}))
		tb.Endpoints[0].Summary = ""
		if _, err := httpwire.New(tb, bs, opts...); err == nil {
			t.Fatal("New accepted a row with no Summary")
		}
		if got := dirNames(dir); !slices.Contains(got, "httpwire-spool-123") || !slices.Contains(got, "httpwire-spool-") {
			t.Errorf("directory after a refused New = %v, want the spool files kept", got)
		}
	})
	t.Run("New removes only regular spool files", func(t *testing.T) {
		tb, bs, opts := spoolOptions(t, dir, http.NotFound, slog.New(&logSink{}))
		if _, err := httpwire.New(tb, bs, opts...); err != nil {
			t.Fatalf("New: %v", err)
		}
		if got := dirNames(dir); !slices.Equal(got, survivors) {
			t.Errorf("directory after New = %v, want %v", got, survivors)
		}
		for path, want := range map[string]string{
			filepath.Join(dir, "accounts.db"):                 "accounts",
			filepath.Join(dir, "httpwire-spool-dir", "inner"): "inner",
			filepath.Join(dir, "httpwire-spool-link"):         "target",
			filepath.Join(outside, "target"):                  "target",
		} {
			if b, err := os.ReadFile(path); err != nil || string(b) != want {
				t.Errorf("%s = %q, %v; want %q", path, b, err, want)
			}
		}
	})
	t.Run("a directory that cannot be read refuses New", func(t *testing.T) {
		tb, bs, opts := spoolOptions(t, filepath.Join(dir, "missing"), http.NotFound, slog.New(&logSink{}))
		_, err := httpwire.New(tb, bs, opts...)
		if err == nil || !strings.Contains(err.Error(), "WithSpoolDir") {
			t.Errorf("New = %v, want a WithSpoolDir refusal", err)
		}
	})
}

func TestKeyedRawRowSpoolsItsBody(t *testing.T) {
	sf := newSpool(t)
	rec := sf.send("first archive", `"k1"`, 0)
	if rec.Code != http.StatusOK || rec.Body.String() != "imported first archive" {
		t.Fatalf("first answer %d %q, want 200 with the body read whole", rec.Code, rec.Body)
	}
	if len(sf.seen) != 1 || len(sf.seen[0]) != 1 || !strings.HasPrefix(sf.seen[0][0], "httpwire-spool-") {
		t.Errorf("directory during the run = %v, want one spool file", sf.seen)
	}
	if got := dirNames(sf.dir); len(got) != 0 {
		t.Errorf("directory after the run = %v, want it empty", got)
	}

	t.Run("another body under the key is reused", func(t *testing.T) {
		rec := sf.send("second archive", `"k1"`, 0)
		if rec.Code != http.StatusUnprocessableEntity || codeOf(t, rec) != "idempotency_key_reused" || sf.runs() != 1 {
			t.Errorf("answer %d %s after %d runs, want idempotency_key_reused and one run", rec.Code, rec.Body, sf.runs())
		}
	})
	t.Run("the same body replays", func(t *testing.T) {
		rec := sf.send("first archive", `"k1"`, -1)
		if rec.Code != http.StatusOK || rec.Body.String() != "imported first archive" || rec.Header().Get("Content-Type") != "text/plain" || sf.runs() != 1 {
			t.Errorf("answer %d %q %q after %d runs, want the kept answer and one run", rec.Code, rec.Header().Get("Content-Type"), rec.Body, sf.runs())
		}
	})
	t.Run("the same body under another key runs", func(t *testing.T) {
		if rec := sf.send("first archive", `"k2"`, 0); rec.Code != http.StatusOK || sf.runs() != 2 {
			t.Errorf("answer %d after %d runs, want 200 and a second run", rec.Code, sf.runs())
		}
	})
	t.Run("a body at the cap is read whole", func(t *testing.T) {
		body := strings.Repeat("x", spoolMax)
		if rec := sf.send(body, `"k3"`, -1); rec.Code != http.StatusOK || rec.Body.String() != "imported "+body {
			t.Errorf("answer %d %q, want the whole body", rec.Code, rec.Body)
		}
	})
	if got := dirNames(sf.dir); len(got) != 0 {
		t.Errorf("directory after every request = %v, want it empty", got)
	}
}

func TestKeyedRawBodyOverTheCap(t *testing.T) {
	for name, length := range map[string]int64{"announced": 0, "unannounced": -1} {
		t.Run(name, func(t *testing.T) {
			sf := newSpool(t)
			rec := sf.send(strings.Repeat("x", spoolMax+1), `"k1"`, length)
			if rec.Code != http.StatusRequestEntityTooLarge || codeOf(t, rec) != "body_too_large" || sf.runs() != 0 {
				t.Errorf("answer %d %s after %d runs, want body_too_large and no run", rec.Code, rec.Body, sf.runs())
			}
			if got := dirNames(sf.dir); len(got) != 0 {
				t.Errorf("directory = %v, want it empty", got)
			}
			if rec := sf.send("small", `"k1"`, 0); rec.Code != http.StatusOK || sf.runs() != 1 {
				t.Errorf("a resend under the key answered %d after %d runs, want a run", rec.Code, sf.runs())
			}
		})
	}
}

func TestKeyedRawPanicLeavesNoSpoolFile(t *testing.T) {
	sf := newSpool(t)
	sf.panics = true
	if rec := sf.send("archive", `"k1"`, 0); rec.Code != http.StatusInternalServerError || codeOf(t, rec) != "internal_error" {
		t.Errorf("a panicking run answered %d %q, want 500 internal_error", rec.Code, rec.Body)
	}
	if got := dirNames(sf.dir); len(got) != 0 {
		t.Errorf("directory after a panic = %v, want it empty", got)
	}
	sf.panics = false
	if rec := sf.send("archive", `"k1"`, 0); rec.Code != http.StatusOK || sf.runs() != 2 {
		t.Errorf("a resend after the panic answered %d after %d runs, want a second run", rec.Code, sf.runs())
	}
}

// unwrapWriter is a handler's own wrap of its writer, which keeps
// http.ResponseController's Unwrap convention; opaqueWriter does not.
type unwrapWriter struct{ http.ResponseWriter }

func (w unwrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type opaqueWriter struct{ http.ResponseWriter }

// A Raw handler's refusal is kept by the code of the answer it started:
// the code the catalog wrote through a wrap that unwraps, whatever its
// size, else the code its kept body holds.
func TestKeyedRawRefusalKeptByItsCode(t *testing.T) {
	var catalog *httpwire.Catalog
	cases := map[string]struct {
		write  func(w http.ResponseWriter, r *http.Request)
		status int
		runs   int
	}{
		"past MaxAnswer through a wrap that unwraps": {func(w http.ResponseWriter, r *http.Request) {
			catalog.WriteError(unwrapWriter{w}, r, &httpwire.Error{Code: "held_limit", Message: strings.Repeat("m", 2000)})
		}, http.StatusTooManyRequests, 2},
		"within MaxAnswer through a wrap that does not": {func(w http.ResponseWriter, r *http.Request) {
			catalog.WriteError(opaqueWriter{w}, r, &httpwire.Error{Code: "held_limit", Message: "wait"})
		}, http.StatusTooManyRequests, 2},
		"a refusal of its own, then the catalog's": {func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "the upstream failed", http.StatusBadGateway)
			catalog.WriteError(w, r, &httpwire.Error{Code: "held_limit", Message: "wait"})
		}, http.StatusBadGateway, 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runs := 0
			h := func(w http.ResponseWriter, r *http.Request) {
				runs++
				_, _ = io.Copy(io.Discard, r.Body)
				tc.write(w, r)
			}
			tb, bs, opts := spoolOptions(t, t.TempDir(), h, slog.New(&logSink{}))
			catalog = tb.Catalog
			api, err := httpwire.New(tb, bs, append(opts, httpwire.WithIdempotency(httpwire.IdempotencyOptions{MaxAnswer: kib}))...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for i := range 2 {
				rec := httptest.NewRecorder()
				api.ServeHTTP(rec, keyedRequest(http.MethodPost, "/api/import", "archive", `"k"`))
				if rec.Code != tc.status {
					t.Errorf("answer %d is %d %q, want %d", i, rec.Code, rec.Body, tc.status)
				}
			}
			if runs != tc.runs {
				t.Errorf("%d runs, want %d", runs, tc.runs)
			}
		})
	}
}

func TestKeyedRawRefusalBeforeTheSpool(t *testing.T) {
	sf := newSpool(t)
	rec := sf.send("archive", "", 0)
	if codeOf(t, rec) != "idempotency_key_required" || sf.runs() != 0 {
		t.Errorf("answer %d %s, want idempotency_key_required and no run", rec.Code, rec.Body)
	}
	if got := dirNames(sf.dir); len(got) != 0 {
		t.Errorf("directory = %v, want it empty", got)
	}
}

func TestKeyedRawSpoolFailureIsInternalError(t *testing.T) {
	sf := newSpool(t)
	if err := os.Remove(sf.dir); err != nil {
		t.Fatal(err)
	}
	rec := sf.send("archive", `"k1"`, 0)
	if codeOf(t, rec) != "internal_error" || sf.runs() != 0 {
		t.Errorf("answer %d %s after %d runs, want internal_error and no run", rec.Code, rec.Body, sf.runs())
	}
	if !sf.log.has("ERROR", "spool", "archive.import") {
		t.Errorf("log = %v, want an ERROR line naming the spool and the row", sf.log.all())
	}
}

func TestKeyedRawJoinGetsTheRunsAnswer(t *testing.T) {
	joined := make(chan struct{})
	sf := newSpool(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(joined)
			next.ServeHTTP(w, r)
		})
	})
	sf.gate = make(chan struct{})
	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- sf.send("archive", `"k1"`, 0) }()
	<-sf.started
	second := make(chan *httptest.ResponseRecorder)
	go func() { second <- sf.send("archive", `"k1"`, 0) }()
	<-joined
	if got := dirNames(sf.dir); len(got) != 2 {
		t.Errorf("directory while both requests are in = %v, want two spool files", got)
	}
	close(sf.gate)
	for name, ch := range map[string]chan *httptest.ResponseRecorder{"first": first, "joined": second} {
		if rec := <-ch; rec.Code != http.StatusOK || rec.Body.String() != "imported archive" {
			t.Errorf("%s answer %d %q, want the run's answer", name, rec.Code, rec.Body)
		}
	}
	if sf.runs() != 1 {
		t.Errorf("runs = %d, want one", sf.runs())
	}
	if got := dirNames(sf.dir); len(got) != 0 {
		t.Errorf("directory after both = %v, want it empty", got)
	}
}

// io.Writer's contract lets a handler reuse the slice it wrote once Write
// returns, so the kept answer is the bytes sent, not the handler's buffer.
func TestKeyedRawReplayIsTheBytesSent(t *testing.T) {
	var runs int
	buf := make([]byte, 6)
	h := func(w http.ResponseWriter, r *http.Request) {
		runs++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(append(buf[:0], "first "...))
		_, _ = w.Write(append(buf[:0], "second"...))
		copy(buf, "after ")
	}
	tb, bs, opts := spoolOptions(t, t.TempDir(), h, slog.New(&logSink{}))
	api, err := httpwire.New(tb, bs, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	send := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, keyedRequest(http.MethodPost, "/api/import", "archive", `"k"`))
		return rec
	}
	if first := send(); first.Body.String() != "first second" {
		t.Fatalf("first answer %d %q, want %q", first.Code, first.Body, "first second")
	}
	if again := send(); again.Code != http.StatusOK || again.Body.String() != "first second" || runs != 1 {
		t.Errorf("resend answered %d %q after %d runs, want the bytes first sent and one run", again.Code, again.Body, runs)
	}
}
