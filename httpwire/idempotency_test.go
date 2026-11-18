package httpwire_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

type (
	regBody struct {
		Name string `json:"name"`
	}
	account struct {
		Name    string `json:"name"`
		Created bool   `json:"created"`
	}
	jobBody struct {
		Kind string `json:"kind"`
		Note string `json:"note,omitempty"`
		Size int    `json:"size"`
	}
	jobAnswer struct {
		ID  string `json:"id"`
		Pad string `json:"pad"`
	}
	noteText struct {
		Text string `json:"text"`
	}
	retryParams struct {
		ID jobID `json:"id"`
	}
	retryQuery struct {
		Mode jobState `json:"mode,omitempty"`
	}
)

// keyedTable holds Keyed rows of each shape: one with two success
// statuses, one with a Result read and one without, an IfMatch one, and
// rows that are not Keyed.
func keyedTable(t testing.TB) *httpwire.Table {
	t.Helper()
	c := fixtureCatalog(t)
	if err := c.Add("queue_full", httpserver.CodeSpec{Status: http.StatusServiceUnavailable, Remediation: "Retry later.", NoEffect: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.Add("outcome_unknown", httpserver.CodeSpec{Status: http.StatusBadGateway, Remediation: "Read the jobs first."}); err != nil {
		t.Fatal(err)
	}
	jobCodes := []string{"queue_full", "outcome_unknown"}
	return &httpwire.Table{
		Prefix: "/api", Catalog: c,
		Endpoints: []httpwire.Endpoint{
			{
				Name: "users.read", Method: http.MethodGet, Path: "/api/users", Summary: "Read the users", Description: "Read the users.",
				Response: reflect.TypeFor[[]account](), Access: session,
			},
			{
				Name: "users.register", Method: http.MethodPost, Path: "/api/users", Summary: "Register a user", Description: "Register a user.",
				Request: reflect.TypeFor[regBody](), Response: reflect.TypeFor[account](), Status: http.StatusCreated,
				Successes: []httpwire.Success{{Status: http.StatusOK, Response: reflect.TypeFor[account]()}},
				Access:    session, Keyed: true, Result: "users.read",
			},
			{
				Name: "jobs.read", Method: http.MethodGet, Path: "/api/jobs", Summary: "Read the jobs", Description: "Read the jobs.",
				Response: reflect.TypeFor[[]jobAnswer](), Access: session,
			},
			{
				Name: "jobs.start", Method: http.MethodPost, Path: "/api/jobs", Summary: "Start a job", Description: "Start a job.",
				Request: reflect.TypeFor[jobBody](), Response: reflect.TypeFor[jobAnswer](), Status: http.StatusAccepted,
				Access: session, Keyed: true, Result: "jobs.read", Codes: jobCodes,
			},
			{
				Name: "jobs.quick", Method: http.MethodPost, Path: "/api/jobs/quick", Summary: "Run a quick job", Description: "Run a quick job.",
				Request: reflect.TypeFor[jobBody](), Response: reflect.TypeFor[jobAnswer](), Access: session, Keyed: true, Codes: jobCodes,
			},
			{
				Name: "jobs.retry", Method: http.MethodPost, Path: "/api/jobs/{id}/retry", Summary: "Retry a job", Description: "Retry a job.",
				Params: reflect.TypeFor[retryParams](), Query: reflect.TypeFor[retryQuery](), Request: reflect.TypeFor[jobBody](),
				Response: reflect.TypeFor[jobAnswer](), Access: session, Keyed: true,
			},
			{
				Name: "notes.save", Method: http.MethodPut, Path: "/api/notes", Summary: "Save the note", Description: "Save the note.",
				Request: reflect.TypeFor[noteText](), Access: session, Keyed: true, IfMatch: true,
			},
			{
				Name: "notes.touch", Method: http.MethodPost, Path: "/api/notes/touch", Summary: "Touch the note", Description: "Touch the note.",
				Access: session,
			},
			{
				Name: "files.get", Method: http.MethodGet, Path: "/api/files", Summary: "Read a file", Description: "Read a file.",
				Kind: httpwire.Raw, ContentType: "text/plain", Access: session,
			},
		},
	}
}

// keyedFixture serves keyedTable, counting each row's runs and each
// request the hold took. quick answers jobs.quick and jobs.start.
type keyedFixture struct {
	api     *httpwire.API
	catalog *httpwire.Catalog
	log     *logSink
	quick   func(context.Context, httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error)
	runs    map[string]int
	users   map[string]bool
	keys    []httpwire.IdempotencyKey
	events  []httpwire.IdempotencyEvent
	holds   int
	held    int  // requests inside the hold now
	holdCap int  // past it the hold answers held_limit; 0 is no cap
	refuse  bool // the hold answers held_limit
	mu      sync.Mutex
}

func (kf *keyedFixture) ran(row string) {
	kf.mu.Lock()
	defer kf.mu.Unlock()
	kf.runs[row]++
}

func (kf *keyedFixture) runsOf(row string) int {
	kf.mu.Lock()
	defer kf.mu.Unlock()
	return kf.runs[row]
}

func (kf *keyedFixture) outcomes() []httpwire.IdempotencyEvent {
	kf.mu.Lock()
	defer kf.mu.Unlock()
	return append([]httpwire.IdempotencyEvent(nil), kf.events...)
}

func (kf *keyedFixture) holdCount() int {
	kf.mu.Lock()
	defer kf.mu.Unlock()
	return kf.holds
}

func (kf *keyedFixture) hold(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kf.mu.Lock()
		kf.holds++
		refuse := kf.refuse || kf.holdCap > 0 && kf.held >= kf.holdCap
		if !refuse {
			kf.held++
		}
		kf.mu.Unlock()
		if refuse {
			kf.catalog.WriteError(w, r, &httpwire.Error{Code: "held_limit", Message: "too many held requests", RetryAfter: 5 * time.Second})
			return
		}
		defer func() {
			kf.mu.Lock()
			kf.held--
			kf.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func (kf *keyedFixture) jobs(row string) httpwire.Binding {
	return httpwire.Bind(row, func(ctx context.Context, c httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
		kf.ran(row)
		kf.mu.Lock()
		if k, _, ok := httpwire.Idempotency(ctx); ok {
			kf.keys = append(kf.keys, k)
		}
		n, quick := kf.runs[row], kf.quick
		kf.mu.Unlock()
		if quick != nil {
			return quick(ctx, c)
		}
		return jobAnswer{ID: c.Body.Kind + "-" + strconv.Itoa(n)}, nil
	})
}

func (kf *keyedFixture) bindings() []httpwire.Binding {
	return []httpwire.Binding{
		httpwire.Bind("users.read", func(context.Context, httpwire.Call[noP, noQ, noB]) ([]account, error) { return nil, nil }),
		httpwire.Bind("users.register", func(_ context.Context, c httpwire.Call[noP, noQ, regBody]) (httpwire.Status[account], error) {
			kf.ran("users.register")
			kf.mu.Lock()
			defer kf.mu.Unlock()
			if kf.users[c.Body.Name] {
				return httpwire.Answer(http.StatusOK, account{Name: c.Body.Name}), nil
			}
			kf.users[c.Body.Name] = true
			return httpwire.Answer(http.StatusCreated, account{Name: c.Body.Name, Created: true}), nil
		}),
		httpwire.Bind("jobs.read", func(context.Context, httpwire.Call[noP, noQ, noB]) ([]jobAnswer, error) { return nil, nil }),
		kf.jobs("jobs.start"),
		kf.jobs("jobs.quick"),
		httpwire.Bind("jobs.retry", func(_ context.Context, c httpwire.Call[retryParams, retryQuery, jobBody]) (jobAnswer, error) {
			kf.ran("jobs.retry")
			return jobAnswer{ID: string(c.Params.ID) + "-" + string(c.Query.Mode)}, nil
		}),
		httpwire.Bind("notes.save", func(context.Context, httpwire.Call[noP, noQ, noteText]) (httpwire.None, error) {
			kf.ran("notes.save")
			return httpwire.None{}, nil
		}),
		httpwire.Bind("notes.touch", func(context.Context, httpwire.Call[noP, noQ, noB]) (httpwire.None, error) {
			kf.ran("notes.touch")
			return httpwire.None{}, nil
		}),
		httpwire.BindRaw("files.get", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			kf.ran("files.get")
			_, _ = w.Write([]byte("file"))
		})),
	}
}

// newKeyed builds the fixture; edit, when given, changes the table first.
func newKeyed(t testing.TB, io httpwire.IdempotencyOptions, edit func(*httpwire.Table)) *keyedFixture {
	t.Helper()
	api, kf, err := buildKeyed(t, io, edit)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	kf.api = api
	return kf
}

func buildKeyed(t testing.TB, io httpwire.IdempotencyOptions, edit func(*httpwire.Table)) (*httpwire.API, *keyedFixture, error) {
	t.Helper()
	doc, err := os.ReadFile("testdata/openapi/keyed.json")
	if err != nil {
		t.Fatal(err)
	}
	tb := keyedTable(t)
	if edit != nil {
		edit(tb)
	}
	kf := &keyedFixture{catalog: tb.Catalog, log: &logSink{}, runs: map[string]int{}, users: map[string]bool{}}
	observer := io.Observer
	io.Observer = func(e httpwire.IdempotencyEvent) {
		kf.mu.Lock()
		kf.events = append(kf.events, e)
		kf.mu.Unlock()
		if observer != nil {
			observer(e)
		}
	}
	api, err := httpwire.New(tb, kf.bindings(),
		httpwire.WithSchema(doc),
		httpwire.WithGuard(fixtureGuard{}),
		httpwire.WithCredential(func(r *http.Request) string {
			if c := r.Header.Get("Credential"); c != "" {
				return c
			}
			return "s:a"
		}),
		httpwire.WithCursorOwner(func(*http.Request) string { return "u:a" }),
		httpwire.WithHold(kf.hold),
		httpwire.WithIdempotency(io),
		httpwire.WithLogger(slog.New(kf.log)),
	)
	return api, kf, err
}

// keyedRequest is one request; key is the raw Idempotency-Key value, ""
// sending none.
func keyedRequest(method, target, body, key string, header ...string) *http.Request {
	if key != "" {
		header = append([]string{"Idempotency-Key", key}, header...)
	}
	return request(method, target, body, header...)
}

func (kf *keyedFixture) do(method, target, body, key string, header ...string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	kf.api.ServeHTTP(rec, keyedRequest(method, target, body, key, header...))
	return rec
}

func (kf *keyedFixture) quickJob(key string, header ...string) *httptest.ResponseRecorder {
	return kf.do(http.MethodPost, "/api/jobs/quick", `{"kind":"a","size":1}`, key, header...)
}

func codeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code < 400 {
		return ""
	}
	return envelopeOf(t, rec).Code
}

func TestIdempotencyKeyParse(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
	for name, tc := range map[string]struct{ header, key string }{
		"plain":                  {`"k1"`, "k1"},
		"spaces around":          {` "k2" `, "k2"},
		"a tab around":           {"\t\"k3\"", "k3"},
		"an escaped quote":       {`"a\"b"`, `a"b`},
		"an escaped backslash":   {`"a\\b"`, `a\b`},
		"a space inside":         {`"with space"`, "with space"},
		"punctuation":            {`"~!#$%&'()*+,-./:;<=>?@[]^_{|}` + "`" + `"`, "~!#$%&'()*+,-./:;<=>?@[]^_{|}`"},
		"255 bytes":              {`"` + strings.Repeat("x", 255) + `"`, strings.Repeat("x", 255)},
		"255 bytes with escapes": {`"` + strings.Repeat(`\"`, 255) + `"`, strings.Repeat(`"`, 255)},
	} {
		t.Run(name, func(t *testing.T) {
			before := kf.runsOf("jobs.quick")
			rec := kf.quickJob(tc.header)
			if rec.Code != http.StatusOK || kf.runsOf("jobs.quick") != before+1 {
				t.Fatalf("answer %d %s, runs %d; want 200 and one run", rec.Code, rec.Body, kf.runsOf("jobs.quick")-before)
			}
			kf.mu.Lock()
			got := kf.keys[len(kf.keys)-1]
			kf.mu.Unlock()
			if got != (httpwire.IdempotencyKey{Credential: "s:a", Row: "jobs.quick", Key: tc.key}) {
				t.Errorf("Idempotency in the handler = %+v, want key %q", got, tc.key)
			}
		})
	}
	for name, header := range map[string]string{
		"bare token":           "k4",
		"empty string":         `""`,
		"256 bytes":            `"` + strings.Repeat("x", 256) + `"`,
		"no closing quote":     `"open`,
		"no opening quote":     `close"`,
		"escaped letter":       `"a\b"`,
		"escape at the end":    `"a\"`,
		"non-ASCII":            `"é"`,
		"a tab inside":         "\"a\tb\"",
		"a control byte":       "\"a\x01b\"",
		"a DEL byte":           "\"a\x7fb\"",
		"a parameter":          `"a";p=1`,
		"a list":               `"a", "b"`,
		"text after the quote": `"a"b`,
		"single quotes":        `'a'`,
		"a byte sequence":      `:YWJj:`,
		"an integer":           `12`,
		"a boolean":            `?1`,
		"empty":                ` `,
	} {
		t.Run(name, func(t *testing.T) {
			before := kf.runsOf("jobs.quick")
			r := keyedRequest(http.MethodPost, "/api/jobs/quick", `{"kind":"a","size":1}`, "")
			r.Header["Idempotency-Key"] = []string{header}
			rec := httptest.NewRecorder()
			kf.api.ServeHTTP(rec, r)
			if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "idempotency_key_invalid" || kf.runsOf("jobs.quick") != before {
				t.Errorf("answer %d %s, runs %d; want 400 idempotency_key_invalid and none", rec.Code, rec.Body, kf.runsOf("jobs.quick")-before)
			}
		})
	}
	t.Run("two lines", func(t *testing.T) {
		r := keyedRequest(http.MethodPost, "/api/jobs/quick", `{"kind":"a","size":1}`, "")
		r.Header.Add("Idempotency-Key", `"x"`)
		r.Header.Add("Idempotency-Key", `"x"`)
		rec := httptest.NewRecorder()
		kf.api.ServeHTTP(rec, r)
		if codeOf(t, rec) != "idempotency_key_invalid" {
			t.Errorf("answer %d %s, want idempotency_key_invalid", rec.Code, rec.Body)
		}
	})
	t.Run("missing", func(t *testing.T) {
		before := kf.runsOf("jobs.quick")
		rec := kf.quickJob("")
		if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "idempotency_key_required" || kf.runsOf("jobs.quick") != before {
			t.Errorf("answer %d %s, want 400 idempotency_key_required and no run", rec.Code, rec.Body)
		}
	})
}

func TestKeyRefusedOnRowsNotKeyed(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
	for name, tc := range map[string]struct{ method, target, row string }{
		"a write": {http.MethodPost, "/api/notes/touch", "notes.touch"},
		"a read":  {http.MethodGet, "/api/users", "users.read"},
		"a Raw":   {http.MethodGet, "/api/files", "files.get"},
	} {
		for kname, key := range map[string]string{"quoted": `"k"`, "bare": "k", "empty": " "} {
			t.Run(name+" "+kname, func(t *testing.T) {
				r := keyedRequest(tc.method, tc.target, "", "")
				r.Header["Idempotency-Key"] = []string{key}
				rec := httptest.NewRecorder()
				kf.api.ServeHTTP(rec, r)
				if rec.Code != http.StatusBadRequest || codeOf(t, rec) != "idempotency_key_unsupported" || kf.runsOf(tc.row) != 0 {
					t.Errorf("answer %d %s, runs %d; want 400 idempotency_key_unsupported and none", rec.Code, rec.Body, kf.runsOf(tc.row))
				}
			})
		}
	}
	t.Run("without a key", func(t *testing.T) {
		if rec := kf.do(http.MethodPost, "/api/notes/touch", "", ""); rec.Code != http.StatusNoContent {
			t.Errorf("answer %d %s, want 204", rec.Code, rec.Body)
		}
	})
}

func TestRegisterAnswers201Or200Replayed(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
	register := func(key string) *httptest.ResponseRecorder {
		return kf.do(http.MethodPost, "/api/users", `{"name":"ada"}`, key)
	}
	first := register(`"one"`)
	if first.Code != http.StatusCreated || first.Body.String() != "{\"name\":\"ada\",\"created\":true}\n" {
		t.Fatalf("first answer %d %q, want 201 with the new user", first.Code, first.Body)
	}
	replay := register(`"one"`)
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() || kf.runsOf("users.register") != 1 {
		t.Errorf("replay %d %q after %d runs, want the first run's 201 and body, one run", replay.Code, replay.Body, kf.runsOf("users.register"))
	}
	second := register(`"two"`)
	if second.Code != http.StatusOK || second.Body.String() != "{\"name\":\"ada\",\"created\":false}\n" || kf.runsOf("users.register") != 2 {
		t.Fatalf("another key %d %q, want a second run answering 200", second.Code, second.Body)
	}
	if again := register(`"two"`); again.Code != http.StatusOK || again.Body.String() != second.Body.String() || kf.runsOf("users.register") != 2 {
		t.Errorf("its replay %d %q, want the 200 its run answered and no third run", again.Code, again.Body)
	}
	if again := register(`"one"`); again.Code != http.StatusCreated {
		t.Errorf("the first key again %d, want its 201", again.Code)
	}
	want := []httpwire.IdempotencyEvent{{Row: "users.register", Outcome: httpwire.OutcomeReplayed}}
	if got := kf.outcomes(); len(got) != 3 || got[0] != want[0] || got[1] != want[0] || got[2] != want[0] {
		t.Errorf("observer saw %+v, want three replays", got)
	}
}

func TestReplayWritesKeptHeadersAndFreshWireHeaders(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
	kf.quick = func(context.Context, httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
		return jobAnswer{}, &httpwire.Error{Code: "outcome_unknown", Message: "the queue did not answer", RetryAfter: 7 * time.Second}
	}
	id1, id2 := httpserver.NewRequestID(), httpserver.NewRequestID()
	first := kf.quickJob(`"r"`, httpserver.HeaderRequestID, id1)
	replay := kf.quickJob(`"r"`, httpserver.HeaderRequestID, id2)
	for name, got := range map[string][2]string{
		"status":         {strconv.Itoa(replay.Code), strconv.Itoa(http.StatusBadGateway)},
		"body":           {replay.Body.String(), first.Body.String()},
		"Content-Type":   {replay.Header().Get("Content-Type"), "application/json"},
		"nosniff":        {replay.Header().Get("X-Content-Type-Options"), "nosniff"},
		"Retry-After":    {replay.Header().Get("Retry-After"), "7"},
		"Wire-Version":   {replay.Header().Get("Wire-Version"), first.Header().Get("Wire-Version")},
		"X-Request-Id":   {replay.Header().Get(httpserver.HeaderRequestID), id2},
		"first's header": {first.Header().Get(httpserver.HeaderRequestID), id1},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
	if kf.runsOf("jobs.quick") != 1 {
		t.Errorf("%d runs, want the effectful refusal kept and replayed", kf.runsOf("jobs.quick"))
	}
	if first.Header().Get("Wire-Version") == "" {
		t.Error("no Wire-Version on the first answer")
	}
	kf.quick = nil
	ok := kf.quickJob(`"s"`)
	if again := kf.quickJob(`"s"`); again.Header().Get("Retry-After") != "" || again.Header().Get("Content-Type") != "application/json" || again.Body.String() != ok.Body.String() {
		t.Errorf("a replayed success: Retry-After %q, Content-Type %q, body %q", again.Header().Get("Retry-After"), again.Header().Get("Content-Type"), again.Body)
	}
}

func TestWhatIsKept(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
	kf.quick = func(_ context.Context, c httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
		switch c.Body.Kind {
		case "full":
			return jobAnswer{}, &httpwire.Error{Code: "queue_full", Message: "the queue is full"}
		case "lost":
			return jobAnswer{}, &httpwire.Error{Code: "outcome_unknown", Message: "the queue did not answer"}
		}
		return jobAnswer{ID: "done"}, nil
	}
	job := func(kind, key string) *httptest.ResponseRecorder {
		return kf.do(http.MethodPost, "/api/jobs/quick", `{"kind":"`+kind+`","size":1}`, key)
	}
	for _, tc := range []struct {
		kind, code string
		runs       int
	}{
		{"full", "queue_full", 2},
		{"lost", "outcome_unknown", 1},
		{"ok", "", 1},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			before := kf.runsOf("jobs.quick")
			job(tc.kind, `"`+tc.kind+`"`)
			rec := job(tc.kind, `"`+tc.kind+`"`)
			if codeOf(t, rec) != tc.code || kf.runsOf("jobs.quick")-before != tc.runs {
				t.Errorf("resend answered %d %q after %d runs, want %q after %d", rec.Code, codeOf(t, rec), kf.runsOf("jobs.quick")-before, tc.code, tc.runs)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
	post := func(target, body, key string, header ...string) *httptest.ResponseRecorder {
		return kf.do(http.MethodPost, target, body, key, header...)
	}
	first := post("/api/jobs/quick", `{"kind":"a","size":1}`, `"f"`)
	t.Run("another body", func(t *testing.T) {
		rec := post("/api/jobs/quick", `{"kind":"a","size":2}`, `"f"`)
		if rec.Code != http.StatusUnprocessableEntity || codeOf(t, rec) != "idempotency_key_reused" || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("answer %d %s after %d runs, want 422 idempotency_key_reused and one run", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
		if got := kf.outcomes(); len(got) != 1 || got[0] != (httpwire.IdempotencyEvent{Row: "jobs.quick", Outcome: httpwire.OutcomeReused}) {
			t.Errorf("observer saw %+v, want one reuse", got)
		}
	})
	t.Run("the same body spelled otherwise", func(t *testing.T) {
		rec := post("/api/jobs/quick", "{ \"size\" : 1,\n\"kind\":\"\\u0061\" }", `"f"`)
		if rec.Code != http.StatusOK || rec.Body.String() != first.Body.String() || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("answer %d %s after %d runs, want the replay", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
	})
	t.Run("an optional member sent as the value its absence decodes to", func(t *testing.T) {
		rec := post("/api/jobs/quick", `{"kind":"a","size":1,"note":""}`, `"f"`)
		if rec.Code != http.StatusOK || rec.Body.String() != first.Body.String() || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("answer %d %s after %d runs, want the replay", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
	})
	t.Run("another Keyed row", func(t *testing.T) {
		rec := post("/api/jobs", `{"kind":"a","size":1}`, `"f"`)
		if rec.Code != http.StatusAccepted || kf.runsOf("jobs.start") != 1 {
			t.Errorf("answer %d %s, want a run of its own", rec.Code, rec.Body)
		}
	})
	t.Run("another credential", func(t *testing.T) {
		rec := post("/api/jobs/quick", `{"kind":"a","size":2}`, `"f"`, "Credential", "k:2")
		if rec.Code != http.StatusOK || kf.runsOf("jobs.quick") != 2 {
			t.Errorf("answer %d %s, want a run of its own", rec.Code, rec.Body)
		}
	})
	t.Run("the request's path and query", func(t *testing.T) {
		retry := func(target string) *httptest.ResponseRecorder {
			return post(target, `{"kind":"a","size":1}`, `"r"`)
		}
		ran := retry("/api/jobs/ab/retry?mode=fast")
		if ran.Code != http.StatusOK || kf.runsOf("jobs.retry") != 1 {
			t.Fatalf("first retry %d %s", ran.Code, ran.Body)
		}
		for _, target := range []string{"/api/jobs/cd/retry?mode=fast", "/api/jobs/ab/retry?mode=slow", "/api/jobs/ab/retry"} {
			if rec := retry(target); codeOf(t, rec) != "idempotency_key_reused" || kf.runsOf("jobs.retry") != 1 {
				t.Errorf("%s answered %d %s after %d runs, want idempotency_key_reused and one run", target, rec.Code, rec.Body, kf.runsOf("jobs.retry"))
			}
		}
		for _, target := range []string{"/api/jobs/%61b/retry?mode=%66ast", "/api/jobs/ab/retry?mode=fast&"} {
			if rec := retry(target); rec.Code != http.StatusOK || rec.Body.String() != ran.Body.String() || kf.runsOf("jobs.retry") != 1 {
				t.Errorf("%s answered %d %s after %d runs, want the replay", target, rec.Code, rec.Body, kf.runsOf("jobs.retry"))
			}
		}
	})
	t.Run("another If-Match tag", func(t *testing.T) {
		save := func(tag string) *httptest.ResponseRecorder {
			return kf.do(http.MethodPut, "/api/notes", `{"text":"x"}`, `"n"`, "If-Match", tag)
		}
		tagA, tagB := `"00000000000000aa.1"`, `"00000000000000aa.2"`
		if rec := save(tagA); rec.Code != http.StatusNoContent {
			t.Fatalf("first save %d %s", rec.Code, rec.Body)
		}
		if rec := save(tagB); codeOf(t, rec) != "idempotency_key_reused" || kf.runsOf("notes.save") != 1 {
			t.Errorf("another tag answered %d %s after %d runs, want idempotency_key_reused and one run", rec.Code, rec.Body, kf.runsOf("notes.save"))
		}
		if rec := save(tagA); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || kf.runsOf("notes.save") != 1 {
			t.Errorf("the first tag again answered %d %q, want the replayed 204", rec.Code, rec.Body)
		}
	})
}

func TestOversizeAnswerIsACompletion(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{MaxAnswer: 1024}, nil)
	pad := strings.Repeat("p", 2000)
	kf.quick = func(_ context.Context, c httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
		return jobAnswer{ID: c.Body.Kind, Pad: pad}, nil
	}
	for _, tc := range []struct{ target, row, remediation string }{
		{"/api/jobs", "jobs.start", "The request completed; read its result with jobs.read."},
		{"/api/jobs/quick", "jobs.quick", "The request completed; its result is no longer replayable."},
	} {
		t.Run(tc.row, func(t *testing.T) {
			first := kf.do(http.MethodPost, tc.target, `{"kind":"a","size":1}`, `"big"`)
			if first.Code/100 != 2 || !strings.Contains(first.Body.String(), pad) {
				t.Fatalf("first answer %d of %d bytes, want the whole body", first.Code, first.Body.Len())
			}
			rec := kf.do(http.MethodPost, tc.target, `{"kind":"a","size":1}`, `"big"`)
			if rec.Code != http.StatusConflict || codeOf(t, rec) != "idempotency_answer_too_large" || kf.runsOf(tc.row) != 1 {
				t.Fatalf("resend %d %s after %d runs, want 409 idempotency_answer_too_large and no second run", rec.Code, rec.Body, kf.runsOf(tc.row))
			}
			if got := envelopeOf(t, rec).Remediation; got != tc.remediation {
				t.Errorf("remediation %q, want %q", got, tc.remediation)
			}
		})
	}
	kf.do(http.MethodPost, "/api/jobs/quick", `{"kind":"b","size":1}`, `"big2"`)
	lines := 0
	for _, l := range kf.log.all() {
		if strings.HasPrefix(l, "ERROR ") && strings.Contains(l, "row=jobs.quick") {
			lines++
		}
	}
	if lines != 1 || kf.log.has("ERROR", "row=jobs.start") {
		t.Errorf("log %q, want one ERROR line for the row with no Result and none for the other", kf.log.all())
	}
	want := httpwire.IdempotencyEvent{Row: "jobs.start", Outcome: httpwire.OutcomeOversize}
	if got := kf.outcomes(); len(got) != 2 || got[0] != want {
		t.Errorf("observer saw %+v, want an oversize outcome per resend", got)
	}
}
