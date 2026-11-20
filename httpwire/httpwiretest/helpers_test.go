package httpwiretest_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// recorder is a testing.TB whose failures are kept, not reported.
type recorder struct {
	testing.TB
	failures []string
	fatal    bool
	mu       sync.Mutex
}

func (*recorder) Helper() {}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, s)
}

func (r *recorder) Errorf(format string, args ...any) { r.add(fmt.Sprintf(format, args...)) }
func (r *recorder) Error(args ...any)                 { r.add(fmt.Sprint(args...)) }

func (r *recorder) Fatalf(format string, args ...any) {
	r.add(fmt.Sprintf(format, args...))
	r.fatal = true
	runtime.Goexit()
}

func (r *recorder) Fatal(args ...any) {
	r.add(fmt.Sprint(args...))
	r.fatal = true
	runtime.Goexit()
}

// record runs f against a recorder on its own goroutine, so a Fatal ends
// only f.
func record(t *testing.T, f func(testing.TB)) *recorder {
	t.Helper()
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(r)
	}()
	<-done
	return r
}

// failed reports whether one failure holds every fragment.
func (r *recorder) failed(fragments ...string) bool {
	return slices.ContainsFunc(r.failures, func(s string) bool {
		for _, f := range fragments {
			if !strings.Contains(s, f) {
				return false
			}
		}
		return true
	})
}

type job struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
}

type jobsAnswer struct {
	Jobs []job `json:"jobs"`
}

type jobID string

func (jobID) MaxBytes() int { return 16 }

type jobParams struct {
	ID jobID `json:"id"`
}

type jobQuery struct {
	httpwire.Page
}

type shape struct {
	Radius *int   `json:"radius,omitempty"`
	Side   *int   `json:"side,omitempty"`
	Kind   string `json:"kind"`
}

type probe struct {
	At    *time.Time `json:"at,omitempty"`
	Name  string     `json:"name"`
	State string     `json:"state"`
	Tags  []string   `json:"tags"`
	Shape shape      `json:"shape"`
}

type probeParams struct {
	Name string `json:"name"`
}

type runBody struct {
	Kind string `json:"kind"`
}

type (
	none      = httpwire.None
	jobsCall  = httpwire.Call[none, none, none, none]
	pageCall  = httpwire.Call[none, jobQuery, none, none]
	jobCall   = httpwire.Call[jobParams, none, none, none]
	principal = string
)

// script is the answer the fixture's mount gives one request URI.
type script struct {
	header  map[string]string // set last, over the binder's headers
	body    string
	drop    []string             // headers removed
	refuse  httpserver.ErrorCode // a code the catalog writes as the answer
	keptID  string               // the refusal's request_id in place of this request's, as a replay keeps
	want    string               // the request body the script answers; another is a 418
	status  int                  // the status written; zero keeps the refusal's
	noJSONC bool                 // write no application/json Content-Type
}

// fixture is an API over a store of jobs owned by four accounts, and a mount
// whose answers each test scripts.
type fixture struct {
	api     *httpwire.API
	catalog *httpwire.Catalog
	scripts map[string]script
	seen    map[principal]int // jobs.all calls by credential
	jobs    []job
	mu      sync.Mutex
}

const goodProbe = `{"name":"p","state":"on","tags":["x"],"shape":{"kind":"circle","radius":1}}`

func fixtureScripts() map[string]script {
	return map[string]script{
		"GET /api/probe":           {body: goodProbe},
		"GET /api/probe?escaped":   {body: `{"name":"<b>&</b>","state":"off","tags":["<",">"],"shape":{"kind":"square","side":2}}`},
		"GET /api/probe?dated":     {body: strings.Replace(goodProbe, `"name"`, `"at":"2026-11-15T10:00:00.5+01:00","name"`, 1)},
		"GET /api/probe/thing":     {body: goodProbe},
		"GET /api/probe/a%2Fb":     {body: goodProbe},
		"GET /api/probe?missing":   {refuse: "probe_missing"},
		"POST /api/probe":          {status: http.StatusCreated, body: goodProbe, want: `{"any":"body"}`},
		"POST /api/probe?again":    {status: http.StatusOK, body: goodProbe},
		"DELETE /api/probe":        {status: http.StatusNoContent, noJSONC: true},
		"GET /api/probe/file":      {header: map[string]string{"Content-Type": "text/csv"}, body: "a,b\n", noJSONC: true},
		"GET /api/probe/away":      {status: http.StatusFound, header: map[string]string{"Location": "/elsewhere"}, noJSONC: true},
		"HEAD /api/probe":          {},
		"HEAD /api/probe?missing":  {status: http.StatusNotFound},
		"GET /api/probe?forbidden": {refuse: "auth_required"},
		// Imitates a mount's replay: the refusal keeps the first request's id
		// while the header is this request's.
		"POST /api/keyed/run?replay": {refuse: "probe_missing", keptID: "first-id"},
	}
}

func newFixture(t testing.TB, personal bool) *fixture {
	t.Helper()
	f := &fixture{
		scripts: fixtureScripts(), seen: map[principal]int{},
		jobs: []job{{"a1", "alice"}, {"a2", "alice"}, {"b1", "bob"}, {"b2", "bob"}, {"c1", "carol"}, {"d1", "dave"}},
	}
	f.catalog = httpwire.NewCatalog()
	for code, status := range map[httpserver.ErrorCode]int{
		"probe_missing": http.StatusNotFound, "job_missing": http.StatusNotFound,
		"auth_required": http.StatusUnauthorized, "auth_forbidden": http.StatusForbidden,
	} {
		if err := f.catalog.Add(code, httpserver.CodeSpec{Status: status, Remediation: "Fix it."}); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := os.ReadFile("testdata/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	f.api, err = httpwire.New(f.table(personal), f.bindings(),
		httpwire.WithSchema(doc),
		httpwire.WithGuard(fixtureGuard{f.catalog}),
		httpwire.WithCredential(func(r *http.Request) string { c, _ := principalOf(r); return c }),
		httpwire.WithCursorOwner(func(r *http.Request) string { _, o := principalOf(r); return o }),
		httpwire.WithHold(func(next http.Handler) http.Handler { return next }),
		httpwire.WithFeed(fixtureFeed{}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

func (f *fixture) table(personal bool) *httpwire.Table {
	session := httpwire.Access{Class: httpwire.Session}
	public := httpwire.Access{Class: httpwire.Public}
	row := func(name, method, path, summary string, a httpwire.Access) httpwire.Endpoint {
		return httpwire.Endpoint{Name: name, Method: method, Path: path, Summary: summary, Description: summary + ".", Access: a}
	}
	mine := row("jobs.mine", http.MethodGet, "/my/jobs", "List my jobs", session)
	mine.Response, mine.Changes, mine.Personal = reflect.TypeFor[jobsAnswer](), []string{"jobs"}, personal
	own := row("jobs.own", http.MethodGet, "/own/jobs", "List own jobs", session)
	own.Response, own.Personal = reflect.TypeFor[jobsAnswer](), true
	all := row("jobs.all", http.MethodGet, "/jobs", "List jobs", session)
	all.Query, all.Response, all.List, all.Changes = reflect.TypeFor[jobQuery](), reflect.TypeFor[httpwire.List[job]](), true, []string{"jobs"}
	get := row("jobs.get", http.MethodGet, "/jobs/{id}", "Read a job", session)
	get.Params, get.Response, get.Codes, get.Changes = reflect.TypeFor[jobParams](), reflect.TypeFor[job](), []httpserver.ErrorCode{"job_missing"}, []string{"jobs"}
	csv := row("jobs.csv", http.MethodGet, "/jobs.csv", "Export jobs", session)
	csv.Kind, csv.ContentTypes, csv.Changes, csv.Personal = httpwire.Raw, []string{"text/csv"}, []string{"jobs"}, personal
	csv.Successes = []httpwire.Success{{Status: http.StatusNoContent}}
	stream := row("live.stream", http.MethodGet, "/stream", "Stream", session)
	stream.Kind, stream.Events = httpwire.Stream, []httpwire.Event{{Name: "changed", Data: reflect.TypeFor[job]()}}
	run := row("jobs.run", http.MethodPost, "/jobs/run", "Run a job", session)
	run.Request, run.Response, run.Codes, run.Keyed = reflect.TypeFor[runBody](), reflect.TypeFor[job](), []httpserver.ErrorCode{"outcome_unknown"}, true
	krun := row("keyed.run", http.MethodPost, "/keyed/run", "Run a keyed probe", session)
	krun.Codes, krun.Keyed = []httpserver.ErrorCode{"probe_missing"}, true

	pget := row("probe.get", http.MethodGet, "/probe", "Read the probe", public)
	pget.Response, pget.Codes = reflect.TypeFor[probe](), []httpserver.ErrorCode{"probe_missing"}
	pmake := row("probe.make", http.MethodPost, "/probe", "Make a probe", public)
	pmake.Status, pmake.Response = http.StatusCreated, reflect.TypeFor[probe]()
	pmake.Successes = []httpwire.Success{{Status: http.StatusOK, Response: reflect.TypeFor[probe]()}}
	pdrop := row("probe.drop", http.MethodDelete, "/probe", "Drop the probe", public)
	pfile := row("probe.file", http.MethodGet, "/probe/file", "Read the probe file", public)
	pfile.Kind, pfile.ContentTypes = httpwire.Raw, []string{"text/csv"}
	paway := row("probe.away", http.MethodGet, "/probe/away", "Leave the probe", public)
	paway.Kind = httpwire.Redirect
	pitem := row("probe.item", http.MethodGet, "/probe/{name}", "Read a named probe", public)
	pitem.Params, pitem.Response = reflect.TypeFor[probeParams](), reflect.TypeFor[probe]()
	return &httpwire.Table{
		Catalog: f.catalog, Prefix: "/api", OnePerson: personal,
		Endpoints: []httpwire.Endpoint{mine, own, all, get, csv, stream, run},
		Sources:   []httpwire.FeedSource{{Name: "store", Log: true}},
		Kinds:     []httpwire.FeedKind{{Name: "jobs", Source: "store"}},
		Mounts: []httpwire.Mount{
			{Handler: f, Rows: []httpwire.Endpoint{pget, pmake, pdrop, pfile, paway, pitem}},
			{Handler: f, Rows: []httpwire.Endpoint{krun}},
		},
	}
}

func (f *fixture) bindings() []httpwire.Binding {
	return []httpwire.Binding{
		httpwire.Bind("jobs.mine", f.mine),
		httpwire.Bind("jobs.own", f.mine),
		httpwire.Bind("jobs.all", f.all),
		httpwire.Bind("jobs.get", f.get),
		httpwire.BindRaw("jobs.csv", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if account(r.Context()) == "erin" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "text/csv")
			for _, j := range f.jobs {
				if j.Owner == account(r.Context()) {
					_, _ = w.Write([]byte(j.ID + "\n"))
				}
			}
		})),
		httpwire.Bind("jobs.run", func(context.Context, httpwire.Call[none, none, runBody, none]) (job, error) {
			return job{}, &httpwire.Error{Code: "outcome_unknown", Message: "the queue did not answer"}
		}),
		httpwire.BindStream("live.stream", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: changed\ndata: {}\n\n"))
		})),
	}
}

// account is the account behind a request's credential.
func account(ctx context.Context) string {
	_, a, _ := strings.Cut(httpwire.CredentialOf(ctx), ":")
	return a
}

func (f *fixture) mine(ctx context.Context, _ jobsCall) (jobsAnswer, error) {
	var out jobsAnswer
	for _, j := range f.jobs {
		if j.Owner == account(ctx) {
			out.Jobs = append(out.Jobs, j)
		}
	}
	return out, nil
}

// all pages every job forward, the same for every caller.
func (f *fixture) all(ctx context.Context, c pageCall) (httpwire.List[job], error) {
	f.mu.Lock()
	f.seen[httpwire.CredentialOf(ctx)]++
	f.mu.Unlock()
	start := 0
	if c.Position != nil {
		start = slices.IndexFunc(f.jobs, func(j job) bool { return j.ID == string(c.Position) }) + 1
	}
	end := min(start+c.Limit, len(f.jobs))
	l := httpwire.List[job]{Items: f.jobs[start:end], Offset: int64(start), Total: int64(len(f.jobs))}
	var err error
	if end < len(f.jobs) {
		l.NextCursor, err = c.SealNext([]byte(f.jobs[end-1].ID))
	}
	if err == nil && start > 0 {
		l.PrevCursor, err = c.SealPrev([]byte(f.jobs[start].ID))
	}
	return l, err
}

func (f *fixture) get(_ context.Context, c jobCall) (job, error) {
	for _, j := range f.jobs {
		if j.ID == string(c.Params.ID) {
			return j, nil
		}
	}
	return job{}, &httpwire.Error{Code: "job_missing", Message: "no such job"}
}

// ServeHTTP is the mount: it answers the script for the request URI.
func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.RequestURI()
	s, ok := f.scripts[key+" as "+r.Header.Get("Principal")]
	if !ok {
		s, ok = f.scripts[key]
	}
	if !ok {
		http.Error(w, "no script", http.StatusTeapot)
		return
	}
	if got, _ := io.ReadAll(r.Body); string(got) != s.want {
		http.Error(w, "unexpected body", http.StatusTeapot)
		return
	}
	status := http.StatusOK
	body := []byte(s.body)
	if s.refuse != "" {
		rec, kept := httptest.NewRecorder(), r
		if s.keptID != "" {
			kept = r.WithContext(httpserver.WithRequestID(r.Context(), s.keptID))
		}
		f.catalog.WriteError(rec, kept, &httpwire.Error{Code: s.refuse, Message: "refused"})
		rec.Header().Del(httpserver.HeaderRequestID)
		maps.Copy(w.Header(), rec.Header())
		status, body = rec.Code, rec.Body.Bytes()
	} else if !s.noJSONC {
		w.Header().Set("Content-Type", "application/json")
	}
	if s.status != 0 {
		status = s.status
	}
	for _, name := range s.drop {
		w.Header().Del(name)
	}
	for k, v := range s.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// principalOf reads the fixture's Principal header, "session <account>" or
// "key <account> <scope>", into the credential and the cursor owner.
func principalOf(r *http.Request) (credential, owner string) {
	p := strings.Fields(r.Header.Get("Principal"))
	switch {
	case len(p) == 2 && p[0] == "session":
		return "s:" + p[1], "u:" + p[1]
	case len(p) == 3 && p[0] == "key":
		return "k:" + p[1] + "-" + p[2], "k:" + p[1] + "-" + p[2]
	}
	return "", ""
}

// fixtureGuard admits a session on Session rows and anyone on Public ones.
type fixtureGuard struct{ catalog *httpwire.Catalog }

func (g fixtureGuard) Wrap(ep *httpwire.Endpoint, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ep.Access.Class != httpwire.Public && g.Shape(r).Principal != httpwire.ShapeSession {
			g.catalog.WriteError(w, r, &httpwire.Error{Code: "auth_required", Message: "sign in"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (fixtureGuard) Codes(a httpwire.Access) []httpserver.ErrorCode {
	switch a.Class {
	case httpwire.Public:
		return nil
	case httpwire.Key, httpwire.Scoped:
		return []httpserver.ErrorCode{"auth_required", "auth_forbidden"}
	}
	return []httpserver.ErrorCode{"auth_required"}
}

func (fixtureGuard) Shape(r *http.Request) httpwire.Shape {
	p := strings.Fields(r.Header.Get("Principal"))
	switch {
	case len(p) == 2 && p[0] == "session":
		return httpwire.Shape{Principal: httpwire.ShapeSession}
	case len(p) == 3 && p[0] == "key":
		return httpwire.Shape{Principal: httpwire.ShapeKey, Scopes: []string{p[2]}}
	}
	return httpwire.Shape{Principal: httpwire.ShapeAnonymous}
}

func (fixtureGuard) Shapes() []httpwire.Shape {
	return []httpwire.Shape{{Principal: httpwire.ShapeAnonymous}, {Principal: httpwire.ShapeSession}}
}
func (fixtureGuard) Admits(httpwire.Shape, httpwire.Access) bool { return true }
func (fixtureGuard) Person(context.Context) error                { return nil }

type fixtureFeed struct{}

func (fixtureFeed) Position(*http.Request, []string) string { return `store=1;epoch="a"` }

// request builds a request, with a Principal header unless p is "".
func request(t testing.TB, method, target, p string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	if p != "" {
		r.Header.Set("Principal", p)
	}
	return r
}

// as answers a copy of a request carrying principal p.
func as(p string) func(*http.Request) *http.Request {
	return func(r *http.Request) *http.Request {
		c := r.Clone(r.Context())
		c.Header.Set("Principal", p)
		return c
	}
}
