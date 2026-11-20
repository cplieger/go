package httpwire_test

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// The refusal drive scripts every collaborator through request headers:
// Guard-Answer is the code the guard's Wrap answers, Person-Answer the one
// Guard.Person answers, Hold-Answer the hold's, Mount-Answer the mount
// handler's, Handler-Answer what a handler returns (a code, a sentinel's
// name, "stray" or "block"), Credential the request's credential ("-" for
// none) and Key-Id the admitted key, if any.
const (
	hGuard   = "Guard-Answer"
	hPerson  = "Person-Answer"
	hHold    = "Hold-Answer"
	hMount   = "Mount-Answer"
	hHandler = "Handler-Answer"
	hCred    = "Credential"
	hKey     = "Key-Id"
)

// reasonOf is the reason a scripted refusal carries, from its row's set.
var reasonOf = map[httpserver.ErrorCode]string{"job_busy": "running", "auth_bad_request": "name"}

// driveFixture serves fixtureTable with every collaborator scripted.
type driveFixture struct {
	api     *httpwire.API
	catalog *httpwire.Catalog
	release chan struct{}
	outage  string
	mu      sync.Mutex
}

func (f *driveFixture) refuse(w http.ResponseWriter, r *http.Request, code httpserver.ErrorCode) {
	f.catalog.WriteError(w, r, &httpwire.Error{Code: code, Reason: reasonOf[code], RetryAfter: retryOf(code)})
}

func retryOf(code httpserver.ErrorCode) time.Duration {
	if code == "held_limit" || code == "rate_limited" {
		return 5 * time.Second
	}
	return 0
}

type driveGuard struct{ f *driveFixture }

func (g driveGuard) Wrap(_ *httpwire.Endpoint, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := r.Header.Get(hGuard); code != "" {
			g.f.refuse(w, r, httpserver.ErrorCode(code))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (driveGuard) Codes(a httpwire.Access) []httpserver.ErrorCode { return guardCodes(a) }
func (driveGuard) Shape(*http.Request) httpwire.Shape {
	return httpwire.Shape{Principal: httpwire.ShapeSession}
}

func (driveGuard) Shapes() []httpwire.Shape {
	return []httpwire.Shape{{Principal: httpwire.ShapeSession}}
}
func (driveGuard) Admits(httpwire.Shape, httpwire.Access) bool { return true }

func (driveGuard) Person(ctx context.Context) error {
	if code, _ := ctx.Value(personAnswer{}).(string); code != "" {
		return &httpwire.Error{Code: httpserver.ErrorCode(code)}
	}
	return nil
}

type personAnswer struct{}

// handlerError is what a JSON handler returns for its Handler-Answer.
func (f *driveFixture) handlerError(r string) error {
	switch r {
	case "":
		return nil
	case "block":
		<-f.release
		return nil
	case "stray":
		return errStray
	case "ErrJobMissing":
		return errJobMissing
	case "ErrRunMissing":
		return errRunMissing
	}
	return &httpwire.Error{Code: httpserver.ErrorCode(r), Reason: reasonOf[httpserver.ErrorCode(r)]}
}

type handlerAnswer struct{}

func answerOf(ctx context.Context) string { s, _ := ctx.Value(handlerAnswer{}).(string); return s }

func newDrive(t *testing.T) *driveFixture {
	t.Helper()
	f := &driveFixture{release: make(chan struct{})}
	c := newCase(t)
	f.catalog = c.tb.Catalog
	c.tb.Mounts[0].Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := r.Header.Get(hMount); code != "" {
			f.refuse(w, r, httpserver.ErrorCode(code))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	big := strings.Repeat("x", 2<<10)
	c.binds["jobs.list"] = httpwire.Bind("jobs.list", func(ctx context.Context, _ httpwire.Call[noP, jobQuery, noB, noH]) ([]job, error) {
		return []job{}, f.handlerError(answerOf(ctx))
	})
	c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(ctx context.Context, _ httpwire.Call[jobParams, noQ, noB, noH]) (job, error) {
		return job{ID: "one"}, f.handlerError(answerOf(ctx))
	})
	c.binds["jobs.start"] = httpwire.Bind("jobs.start", func(ctx context.Context, c httpwire.Call[noP, noQ, startRequest, noH]) (job, error) {
		if c.Body.Kind == "big" {
			return job{ID: big}, nil
		}
		return job{ID: "new"}, f.handlerError(answerOf(ctx))
	})
	c.binds["settings.save"] = httpwire.Bind("settings.save", func(ctx context.Context, _ httpwire.Call[noP, noQ, otherBody, noH]) (httpwire.None, error) {
		return httpwire.None{}, f.handlerError(answerOf(ctx))
	})
	c.binds["archive.export"] = httpwire.BindRaw("archive.export", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := httpwire.RequirePerson(r.Context(), httpwire.PersonAccountsArchive); err != nil {
			e, _ := err.(*httpwire.Error)
			f.catalog.WriteError(w, r, e)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK"))
	}))
	c.binds["events.wait"] = httpwire.Bind("events.wait", func(ctx context.Context, _ httpwire.Call[noP, noQ, noB, noH]) (httpwire.Status[nudge], error) {
		return httpwire.Answer(http.StatusNoContent, nudge{}), f.handlerError(answerOf(ctx))
	})
	limiter, err := httpserver.NewKeyedRateLimiter(httpserver.KeyedRate{Burst: 1, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	c.opts["guard"] = httpwire.WithGuard(driveGuard{f})
	c.opts["credential"] = httpwire.WithCredential(func(r *http.Request) string {
		switch v := r.Header.Get(hCred); v {
		case "-":
			return ""
		case "":
			return "s:fixture"
		default:
			return v
		}
	})
	c.opts["keyRate"] = httpwire.WithKeyRate(limiter, func(r *http.Request) (string, bool) {
		id := r.Header.Get(hKey)
		return id, id != ""
	})
	c.opts["hold"] = httpwire.WithHold(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if code := r.Header.Get(hHold); code != "" {
				f.refuse(w, r, httpserver.ErrorCode(code))
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	c.opts["floor"] = httpwire.WithClientFloor(httpwire.ClientFloor{
		Below:   func(v string) bool { return v == "0" },
		Message: func(v string) string { return "install a client newer than " + v },
	})
	c.opts["outage"] = httpwire.WithOutage(func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.outage
	})
	c.opts["idempotency"] = httpwire.WithIdempotency(httpwire.IdempotencyOptions{MaxPerCredential: 1, MaxAnswer: 1 << 10})
	c.opts["logger"] = httpwire.WithLogger(slog.New(&logSink{}))
	api := c.mustBuild(t)
	f.api = api
	return f
}

func (f *driveFixture) setOutage(file string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outage = file
}

// ServeHTTP hands the scripted Person-Answer and Handler-Answer to the
// guard and the handlers through the request's context.
func (f *driveFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), personAnswer{}, r.Header.Get(hPerson))
	ctx = context.WithValue(ctx, handlerAnswer{}, r.Header.Get(hHandler))
	f.api.ServeHTTP(w, r.WithContext(ctx))
}

// rowTarget is a request a row's pattern routes, with the body its decode
// accepts.
type rowTarget struct {
	method, path, body string
	header             []string
}

var driveTargets = map[string]rowTarget{
	"jobs.list":      {http.MethodGet, "/api/jobs", "", nil},
	"jobs.get":       {http.MethodGet, "/api/jobs/a", "", nil},
	"jobs.start":     {http.MethodPost, "/api/jobs", `{"kind":"a"}`, []string{"Idempotency-Key", `"k"`}},
	"settings.save":  {http.MethodPatch, "/api/settings", `{"name":"x"}`, []string{"If-Match", fixtureIfMatch}},
	"archive.export": {http.MethodPost, "/api/archive", "", nil},
	"events.wait":    {http.MethodGet, "/api/events/wait", "", nil},
	"live.stream":    {http.MethodGet, "/api/stream", "", nil},
	"auth.sso":       {http.MethodGet, "/api/sso", "", nil},
	"auth.keys":      {http.MethodGet, "/api/auth/keys", "", nil},
	"auth.keyCreate": {http.MethodPost, "/api/auth/keys", `{"name":"x"}`, nil},
}

// notDriven are the codes of a row's union no request to the binder can
// draw, each with why the union still holds it.
var notDriven = map[string]map[httpserver.ErrorCode]string{
	"*": {"host_not_allowed": "httpserver's host check answers it in front of the API"},
	"auth.sso": {
		"accounts_unavailable": "a DuringOutage Public row passes the outage and asks for no account; the code is every row's",
		"internal_error":       "a Public Redirect row's handler writes its own answer and no credential is read; the code is every row's",
	},
}

// drove records the codes each row answered.
type drove struct {
	t     *testing.T
	f     *driveFixture
	union map[string][]httpserver.ErrorCode
	seen  map[string]map[httpserver.ErrorCode]bool
}

// send answers one request to row and checks the refusal is want, that
// want is in the row's union and that the envelope carries the catalog's
// status for it.
func (d *drove) send(row string, want httpserver.ErrorCode, edit func(*rowTarget)) {
	d.t.Helper()
	tg := driveTargets[row]
	tg.header = slices.Clone(tg.header)
	if edit != nil {
		edit(&tg)
	}
	rec := answer(d.f, request(tg.method, tg.path, tg.body, tg.header...))
	d.check(row, want, rec)
}

func (d *drove) check(row string, want httpserver.ErrorCode, rec *httptest.ResponseRecorder) {
	d.t.Helper()
	env, err := httpwire.DecodeEnvelope(rec.Body.Bytes())
	if err != nil {
		d.t.Errorf("%s, want %s: answer %d is not the envelope (%v): %q", row, want, rec.Code, err, rec.Body)
		return
	}
	spec, ok := d.f.catalog.Lookup(env.Code)
	if env.Code != want || !ok || rec.Code != spec.Status {
		d.t.Errorf("%s answered %d %s, want %s at the catalog's status", row, rec.Code, env.Code, want)
	}
	if !slices.Contains(d.union[row], env.Code) {
		d.t.Errorf("%s answered %s, which is not in its union %v", row, env.Code, d.union[row])
	}
	if d.seen[row] == nil {
		d.seen[row] = map[httpserver.ErrorCode]bool{}
	}
	d.seen[row][env.Code] = true
}

func header(kv ...string) func(*rowTarget) {
	return func(tg *rowTarget) { tg.header = append(tg.header, kv...) }
}

func TestEveryRefusalPathAnswersInsideTheRowsUnion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newDrive(t)
		tb := f.api.Table()
		d := &drove{t: t, f: f, union: map[string][]httpserver.ErrorCode{}, seen: map[string]map[httpserver.ErrorCode]bool{}}
		var rows []httpwire.Endpoint
		rows = append(rows, tb.Endpoints...)
		mount := map[string]bool{}
		for _, m := range tb.Mounts {
			for _, ep := range m.Rows {
				rows = append(rows, ep)
				mount[ep.Name] = true
			}
		}
		for _, ep := range rows {
			d.union[ep.Name] = ep.Errors
		}
		if len(rows) != len(driveTargets) {
			t.Fatalf("the drive has %d targets for %d rows", len(driveTargets), len(rows))
		}
		for _, ep := range rows {
			driveEveryRow(d, ep, mount[ep.Name])
		}
		driveRowFeatures(d)
		for _, ep := range rows {
			skip := maps.Clone(notDriven["*"])
			maps.Copy(skip, notDriven[ep.Name])
			for _, code := range ep.Errors {
				_, skipped := skip[code]
				switch {
				case skipped && d.seen[ep.Name][code]:
					t.Errorf("%s answered %s, which the drive lists as not reachable", ep.Name, code)
				case !skipped && !d.seen[ep.Name][code]:
					t.Errorf("%s never answered %s of its union", ep.Name, code)
				}
			}
			for code := range skip {
				if !slices.Contains(ep.Errors, code) {
					t.Errorf("%s lists %s as not reachable, but its union lacks it", ep.Name, code)
				}
			}
		}
	})
}

// driveEveryRow draws the codes every row may answer, from the router, the
// floor, maintenance, the outage, the guard and the binder's write checks.
func driveEveryRow(d *drove, ep httpwire.Endpoint, mount bool) {
	tg := driveTargets[ep.Name]
	write := tg.method != http.MethodGet
	d.send(ep.Name, "method_not_allowed", func(tg *rowTarget) { tg.method = http.MethodDelete })
	d.send(ep.Name, "not_found", func(tg *rowTarget) { tg.path += "/x/y" })
	d.send(ep.Name, "upgrade_required", header("Fixture-Version", "0"))
	driveQuiet(d, ep.Name, write, tg.method == http.MethodGet && ep.Kind == httpwire.Stream)
	if !ep.DuringOutage {
		for file, code := range map[string]httpserver.ErrorCode{"accounts": "accounts_unavailable", "store": "store_unavailable", "bogus": "internal_error"} {
			if mount && file == "store" {
				continue
			}
			d.f.setOutage(file)
			d.send(ep.Name, code, nil)
		}
		d.f.setOutage("")
	}
	if write {
		d.send(ep.Name, "wire_mismatch", header("Wire-Version", "other"))
	}
	if mount {
		for _, code := range ep.Codes {
			d.send(ep.Name, code, header(hMount, string(code)))
		}
		if ep.Access.Class == httpwire.Key || ep.Access.Class == httpwire.Scoped {
			d.send(ep.Name, "rate_limited", header(hMount, "rate_limited"))
		}
		if write {
			d.send(ep.Name, "cross_origin_refused", header(hMount, "cross_origin_refused"))
		}
		return
	}
	if write {
		d.send(ep.Name, "cross_origin_refused", header("Sec-Fetch-Site", "cross-site"))
	}
	for _, code := range guardCodes(ep.Access) {
		d.send(ep.Name, code, header(hGuard, string(code)))
	}
	if !ep.Keyed {
		d.send(ep.Name, "idempotency_key_unsupported", header("Idempotency-Key", `"k"`))
	}
	if ep.Access.Class != httpwire.Public {
		d.send(ep.Name, "internal_error", header(hCred, "-"))
	}
}

// driveQuiet draws maintenance: a write under QuietWrites, the stream under
// QuietWrites, and a read held past 5 s under QuietAll.
func driveQuiet(d *drove, row string, write, stream bool) {
	q := httpwire.QuietAll
	if write || stream {
		q = httpwire.QuietWrites
	}
	resume, err := d.f.api.Quiesce(d.t.Context(), q)
	if err != nil {
		d.t.Fatal(err)
	}
	defer resume()
	d.send(row, "maintenance", nil)
}

// driveRowFeatures draws the codes each row's own features bring.
func driveRowFeatures(d *drove) {
	d.send("jobs.list", "bad_query", func(tg *rowTarget) { tg.path += "?state=" + strings.Repeat("s", 17) })
	d.send("jobs.get", "not_found", func(tg *rowTarget) { tg.path = "/api/jobs/" + strings.Repeat("i", 65) })
	d.send("jobs.get", "job_missing", header(hHandler, "ErrJobMissing"))
	d.send("jobs.get", "job_missing", header(hHandler, "ErrRunMissing"))
	d.send("jobs.get", "internal_error", header(hHandler, "stray"))

	d.send("settings.save", "unsupported_media_type", func(tg *rowTarget) { tg.header = append(tg.header, "Content-Type", "text/plain") })
	d.send("settings.save", "bad_body", func(tg *rowTarget) { tg.body = `{"name":1}` })
	d.send("settings.save", "body_too_large", func(tg *rowTarget) { tg.body = `{"name":"` + strings.Repeat("n", 64<<10) + `"}` })
	d.send("settings.save", "if_match_required", func(tg *rowTarget) { tg.header = nil })
	d.send("settings.save", "if_match_invalid", func(tg *rowTarget) { tg.header = []string{"If-Match", `"7"`} })
	d.send("settings.save", "stale_version", header(hHandler, "stale_version"))
	d.send("settings.save", "person_required", header(hGuard, "person_required"))

	d.send("archive.export", "body_too_large", func(tg *rowTarget) { tg.body = strings.Repeat("z", 64<<10+1) })
	d.send("archive.export", "person_required", header(hPerson, "person_required"))
	d.send("archive.export", "auth_recent_auth_required", header(hPerson, "auth_recent_auth_required"))

	d.send("events.wait", "held_limit", header(hHold, "held_limit"))
	d.send("live.stream", "held_limit", header(hGuard, "held_limit"))

	driveKeyed(d)
}

// driveKeyed draws a Keyed write's codes; the store keeps one answer per
// credential and at most 1 KiB of it.
func driveKeyed(d *drove) {
	const row = "jobs.start"
	d.send(row, "unsupported_media_type", func(tg *rowTarget) {
		tg.header = append(tg.header, "Content-Type", "application/x-www-form-urlencoded")
	})
	d.send(row, "bad_body", func(tg *rowTarget) { tg.body = `{"kind":1}` })
	d.send(row, "body_too_large", func(tg *rowTarget) { tg.body = `{"kind":"` + strings.Repeat("k", 64<<10) + `"}` })
	d.send(row, "job_busy", header(hHandler, "job_busy", hCred, "s:busy"))
	d.send(row, "idempotency_key_required", func(tg *rowTarget) { tg.header = nil })
	d.send(row, "idempotency_key_invalid", func(tg *rowTarget) { tg.header = []string{"Idempotency-Key", "k"} })

	answer(d.f, request(http.MethodPost, "/api/jobs", `{"kind":"a"}`, "Idempotency-Key", `"r"`, hCred, "s:reused"))
	d.send(row, "idempotency_key_reused", func(tg *rowTarget) {
		tg.body, tg.header = `{"kind":"b"}`, []string{"Idempotency-Key", `"r"`, hCred, "s:reused"}
	})

	answer(d.f, request(http.MethodPost, "/api/jobs", `{"kind":"a"}`, "Idempotency-Key", `"1"`, hCred, "s:full"))
	d.send(row, "idempotency_store_full", func(tg *rowTarget) { tg.header = []string{"Idempotency-Key", `"2"`, hCred, "s:full"} })

	big := func(tg *rowTarget) {
		tg.body, tg.header = `{"kind":"big"}`, []string{"Idempotency-Key", `"b"`, hCred, "s:big"}
	}
	if rec := answer(d.f, request(http.MethodPost, "/api/jobs", `{"kind":"big"}`, "Idempotency-Key", `"b"`, hCred, "s:big")); rec.Code != http.StatusAccepted {
		d.t.Errorf("the oversize first run answered %d, want it sent whole", rec.Code)
	}
	d.send(row, "idempotency_answer_too_large", big)

	answer(d.f, request(http.MethodPost, "/api/jobs", `{"kind":"a"}`, "Idempotency-Key", `"k"`, hKey, "key-1", hCred, "k:1"))
	d.send(row, "rate_limited", header(hKey, "key-1", hCred, "k:1"))

	claim := request(http.MethodPost, "/api/jobs", `{"kind":"a"}`, "Idempotency-Key", `"w"`, hCred, "s:wait", hHandler, "block")
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- answer(d.f, claim) }()
	synctest.Wait()
	wait := func(extra ...string) func(*rowTarget) {
		return func(tg *rowTarget) {
			tg.header = append([]string{"Idempotency-Key", `"w"`, hCred, "s:wait", hHandler, "block"}, extra...)
		}
	}
	d.send(row, "held_limit", wait(hHold, "held_limit"))
	d.send(row, "idempotency_in_flight", wait())
	close(d.f.release)
	if rec := <-done; rec.Code != http.StatusAccepted {
		d.t.Errorf("the blocked claimant answered %d %s, want its run's answer", rec.Code, rec.Body)
	}
}

func TestListRowRefusalsAreInItsUnion(t *testing.T) {
	s := newPageStore(t, 3)
	var union []httpserver.ErrorCode
	for _, ep := range s.api.Table().Endpoints {
		if ep.Name == "items.list" {
			union = ep.Errors
		}
	}
	for target, want := range map[string]httpserver.ErrorCode{
		withCursor("/api/items", "AAAA"):             "cursor_invalid",
		"/api/items?kind=" + strings.Repeat("k", 17): "bad_query",
		"/api/items?limit=0":                         "bad_query",
	} {
		rec := s.get(target, "session a 1")
		env := envelopeOf(t, rec)
		if env.Code != want || !slices.Contains(union, env.Code) {
			t.Errorf("%s answered %s, want %s inside the union %v", target, env.Code, want, union)
		}
	}
}
