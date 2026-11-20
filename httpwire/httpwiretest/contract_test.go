package httpwiretest_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/httpwire/httpwiretest"
)

const alice, bob, carol, dave = "session alice", "session bob", "session carol", "session dave"

// passingCases are answers the fixture gives within every row's shapes.
func passingCases(t testing.TB) []httpwiretest.Case {
	t.Helper()
	post := request(t, http.MethodPost, "/api/probe", "")
	post.Body = io.NopCloser(strings.NewReader(`{"any":"body"}`))
	return []httpwiretest.Case{
		{Row: "probe.get", Request: request(t, http.MethodGet, "/api/probe", "")},
		{Row: "probe.get", Request: request(t, http.MethodGet, "/api/probe?escaped", "")},
		{Row: "probe.get", Request: request(t, http.MethodGet, "/api/probe?missing", "")},
		{Row: "probe.get", Request: request(t, http.MethodHead, "/api/probe", "")},
		{Row: "probe.get", Request: request(t, http.MethodHead, "/api/probe?missing", "")},
		{Row: "probe.make", Request: post},
		{Row: "probe.make", Request: request(t, http.MethodPost, "/api/probe?again", "")},
		{Row: "probe.drop", Request: request(t, http.MethodDelete, "/api/probe", "")},
		{Row: "probe.file", Request: request(t, http.MethodGet, "/api/probe/file", "")},
		{Row: "probe.away", Request: request(t, http.MethodGet, "/api/probe/away", "")},
		{Row: "probe.get", Request: request(t, http.MethodGet, "/api/probe?dated", "")},
		{Row: "probe.item", Request: request(t, http.MethodGet, "/api/probe/thing", "")},
		{Row: "probe.item", Request: request(t, http.MethodGet, "/api/probe/a%2Fb", "")},
		{Row: "jobs.get", Request: request(t, http.MethodGet, "/api/jobs/a%2Fb", alice)},
		{Row: "jobs.mine", Request: request(t, http.MethodGet, "/api/my/jobs", alice)},
		{Row: "jobs.mine", Request: request(t, http.MethodGet, "/api/my/jobs", "")},
		{Row: "jobs.own", Request: request(t, http.MethodGet, "/api/own/jobs", alice)},
		{Row: "jobs.all", Request: request(t, http.MethodGet, "/api/jobs?limit=2", alice)},
		{Row: "jobs.get", Request: request(t, http.MethodGet, "/api/jobs/a1", alice)},
		{Row: "jobs.get", Request: request(t, http.MethodGet, "/api/jobs/zz", alice)},
		{Row: "jobs.get", Request: request(t, http.MethodHead, "/api/jobs/a1", alice)},
		{Row: "jobs.csv", Request: request(t, http.MethodGet, "/api/jobs.csv", alice)},
	}
}

func TestContractPassesAnswersInsideTheirShapes(t *testing.T) {
	f := newFixture(t, false)
	r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, passingCases(t)) })
	for _, s := range r.failures {
		t.Errorf("Contract failed a correct answer: %s", s)
	}
}

func TestContractFailsAnAnswerOutsideItsShapes(t *testing.T) {
	const at = "GET /api/probe"
	body := func(old, repl string) script { return script{body: strings.Replace(goodProbe, old, repl, 1)} }
	cases := map[string]struct {
		s     script
		key   string
		row   string
		check string
		want  []string
	}{
		"an extra field":                    {s: body(`"name"`, `"extra":1,"name"`), want: []string{"extra"}},
		"a missing required field":          {s: body(`"state":"on",`, ""), want: []string{`"state"`, "required"}},
		"a value off its enum":              {s: body(`"on"`, `"dim"`), want: []string{`"state"`, "enum"}},
		"a string a byte over its bound":    {s: body(`"p"`, `"123456789"`), want: []string{`"name"`, "range"}},
		"a null under a count of one":       {s: body(`["x"]`, "null"), want: []string{`"tags"`, "required"}},
		"an array under its count":          {s: body(`["x"]`, "[]"), want: []string{`"tags"`, "range"}},
		"a date-time with no offset":        {s: body(`"name"`, `"at":"2026-11-15T10:00:00","name"`), want: []string{"does not decode"}},
		"a date-time off RFC 3339":          {s: body(`"name"`, `"at":"2026-11-15 10:00","name"`), want: []string{"does not decode"}},
		"a body cut short":                  {s: script{body: goodProbe[:20]}, want: []string{`at ""`, "malformed"}},
		"a union case with another's field": {s: body(`"radius":1`, `"radius":1,"side":2`), want: []string{"shape.side", "union"}},
		"a union case with another's tag":   {s: body(`"circle"`, `"square"`), want: []string{"shape", "union"}},
		"an undeclared success status":      {s: script{status: http.StatusAccepted, body: goodProbe}, want: []string{"202", "not declared"}},
		"a code outside the union":          {s: script{refuse: "stale_version"}, want: []string{`"stale_version"`, "union"}},
		"an envelope at another status":     {s: script{refuse: "probe_missing", status: http.StatusConflict}, want: []string{`"probe_missing"`, "404"}},
		"a body that is no envelope":        {s: script{status: http.StatusNotFound, body: `{"error":"gone"}`}, want: []string{"envelope"}},
		"a request_id off the header":       {s: script{refuse: "probe_missing", header: map[string]string{"X-Request-Id": "another-id"}}, want: []string{"request_id"}},
		"no Wire-Version":                   {s: script{body: goodProbe, drop: []string{"Wire-Version"}}, want: []string{"Wire-Version"}},
		"another Wire-Version":              {s: script{body: goodProbe, header: map[string]string{"Wire-Version": "0123456789abcdef"}}, want: []string{"Wire-Version", "0123456789abcdef"}},
		"no X-Request-Id":                   {s: script{body: goodProbe, drop: []string{"X-Request-Id"}}, want: []string{"no X-Request-Id"}},
		"an invalid X-Request-Id":           {s: script{body: goodProbe, header: map[string]string{"X-Request-Id": "a b"}}, want: []string{`X-Request-Id "a b"`}},
		"a field the Response type lacks":   {s: body(`"name"`, `"note":"n","name"`), want: []string{"unknown field", "note"}},
		"an envelope of another media":      {s: script{refuse: "probe_missing", header: map[string]string{"Content-Type": "text/plain"}}, want: []string{"error envelope is"}},
		"a JSON body of another media":      {s: script{body: goodProbe, header: map[string]string{"Content-Type": "text/plain"}}, want: []string{"Content-Type"}},
		"no body":                           {s: script{}, want: []string{"decode"}},
		"trailing data":                     {s: script{body: goodProbe + "{}"}, want: []string{`at ""`, "malformed"}},
		"a Raw answer of another media": {
			key: "GET /api/probe/file", row: "probe.file", check: "/api/probe/file",
			s: script{body: "a,b\n", header: map[string]string{"Content-Type": "text/plain"}}, want: []string{"Content-Type", "text/csv"},
		},
		"a body on a bodiless status": {
			key: "DELETE /api/probe", row: "probe.drop", check: "/api/probe",
			s: script{status: http.StatusNoContent, body: "x", noJSONC: true}, want: []string{"body"},
		},
		"an undeclared redirect status": {
			key: "GET /api/probe/away", row: "probe.away", check: "/api/probe/away",
			s: script{status: http.StatusMovedPermanently, header: map[string]string{"Location": "/x"}, noJSONC: true}, want: []string{"301", "not declared"},
		},
		"a redirect with no Location": {
			key: "GET /api/probe/away", row: "probe.away", check: "/api/probe/away",
			s: script{status: http.StatusFound, noJSONC: true}, want: []string{"Location"},
		},
		"an extra success status's body off its schema": {
			key: "POST /api/probe", row: "probe.make", check: "/api/probe",
			s: script{status: http.StatusOK, body: body(`"on"`, `"dim"`).body}, want: []string{"200", "enum"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false)
			key, row, target := at, "probe.get", "/api/probe"
			if tc.key != "" {
				key, row, target = tc.key, tc.row, tc.check
			}
			f.scripts[key] = tc.s
			method, _, _ := strings.Cut(key, " ")
			r := record(t, func(tb testing.TB) {
				httpwiretest.Contract(tb, f.api, []httpwiretest.Case{{Row: row, Request: request(t, method, target, "")}})
			})
			if !r.failed(append([]string{`row "` + row + `"`}, tc.want...)...) {
				t.Errorf("failures %q; want one naming row %q with %q", r.failures, row, tc.want)
			}
		})
	}
}

func TestContractFailsACaseItCannotCheck(t *testing.T) {
	f := newFixture(t, false)
	cases := map[string]struct {
		c    httpwiretest.Case
		want []string
	}{
		"a row the table lacks":       {httpwiretest.Case{Row: "probe.nope", Request: request(t, http.MethodGet, "/api/probe", "")}, []string{`"probe.nope"`, "no row"}},
		"a Stream row":                {httpwiretest.Case{Row: "live.stream", Request: request(t, http.MethodGet, "/api/stream", alice)}, []string{`row "live.stream"`, "Stream"}},
		"a request for another row":   {httpwiretest.Case{Row: "probe.get", Request: request(t, http.MethodGet, "/api/probe/file", "")}, []string{`row "probe.get"`, "does not reach"}},
		"a request of another method": {httpwiretest.Case{Row: "probe.get", Request: request(t, http.MethodPost, "/api/probe", "")}, []string{`row "probe.get"`, "does not reach"}},
		"a literal path another row takes": {
			httpwiretest.Case{Row: "probe.item", Request: request(t, http.MethodGet, "/api/probe/file", "")},
			[]string{`row "probe.item"`, "does not reach"},
		},
		"no request": {httpwiretest.Case{Row: "probe.get"}, []string{`row "probe.get"`, "no Request"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, []httpwiretest.Case{tc.c}) })
			if !r.failed(tc.want...) {
				t.Errorf("failures %q; want one with %q", r.failures, tc.want)
			}
		})
	}
	t.Run("a nil API", func(t *testing.T) {
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, nil, nil) })
		if !r.fatal || !r.failed("nil") {
			t.Errorf("failures %q, fatal %v; want a fatal naming the nil API", r.failures, r.fatal)
		}
	})
}

// keyedCase is a POST of row sent as alice, with Idempotency-Key key and the
// inbound X-Request-Id id, each "" sending none.
func keyedCase(t testing.TB, row, target, body, key, id string) httpwiretest.Case {
	t.Helper()
	r := request(t, http.MethodPost, target, alice)
	if body != "" {
		r.Body = io.NopCloser(strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if id != "" {
		r.Header.Set("X-Request-Id", id)
	}
	return httpwiretest.Case{Row: row, Request: r}
}

func runJob(t testing.TB, key, id string) httpwiretest.Case {
	return keyedCase(t, "jobs.run", "/api/jobs/run", `{"kind":"a"}`, key, id)
}

// A replayed refusal is the first run's body, so its request_id names the
// run whose logs hold the failure while its X-Request-Id is the resend's.
func TestContractPassesAReplayedRefusal(t *testing.T) {
	t.Run("the API's own replay", func(t *testing.T) {
		f := newFixture(t, false)
		first, again := httptest.NewRecorder(), httptest.NewRecorder()
		f.api.ServeHTTP(first, runJob(t, `"k0"`, "").Request)
		f.api.ServeHTTP(again, runJob(t, `"k0"`, "").Request)
		if again.Body.String() != first.Body.String() || again.Header().Get("X-Request-Id") == first.Header().Get("X-Request-Id") {
			t.Fatalf("a resend answered %q with id %q after %q with id %q; want the kept body under a fresh id",
				again.Body, again.Header().Get("X-Request-Id"), first.Body, first.Header().Get("X-Request-Id"))
		}
		r := record(t, func(tb testing.TB) {
			httpwiretest.Contract(tb, f.api, []httpwiretest.Case{runJob(t, `"k1"`, ""), runJob(t, `"k1"`, ""), runJob(t, `"k2"`, ""), runJob(t, `"k1"`, "")})
		})
		for _, s := range r.failures {
			t.Errorf("Contract failed a replayed refusal: %s", s)
		}
	})
	t.Run("the API's own replay under a key sent with a trailing space", func(t *testing.T) {
		f := newFixture(t, false)
		r := record(t, func(tb testing.TB) {
			httpwiretest.Contract(tb, f.api, []httpwiretest.Case{runJob(t, `"k1"`, ""), runJob(t, `"k1" `, "")})
		})
		for _, s := range r.failures {
			t.Errorf("Contract failed a replayed refusal: %s", s)
		}
	})
	t.Run("a mount's imitated replay", func(t *testing.T) {
		f := newFixture(t, false)
		r := record(t, func(tb testing.TB) {
			httpwiretest.Contract(tb, f.api, []httpwiretest.Case{
				keyedCase(t, "keyed.run", "/api/keyed/run", "", `"k1"`, "first-id"),
				keyedCase(t, "keyed.run", "/api/keyed/run?replay", "", `"k1"`, "first-id"),
			})
		})
		for _, s := range r.failures {
			t.Errorf("Contract failed a replayed refusal: %s", s)
		}
	})
}

// The replay rule admits only an id an earlier case with the same
// credential, Keyed row and Idempotency-Key was answered with.
func TestContractFailsARequestIDNoReplayKept(t *testing.T) {
	first := func(t testing.TB, key string) httpwiretest.Case {
		return keyedCase(t, "keyed.run", "/api/keyed/run", "", key, "first-id")
	}
	resend := func(t testing.TB, key string) httpwiretest.Case {
		return keyedCase(t, "keyed.run", "/api/keyed/run?replay", "", key, "first-id")
	}
	probe := func(t testing.TB, target string) httpwiretest.Case {
		r := request(t, http.MethodGet, target, "")
		r.Header.Set("Idempotency-Key", `"k1"`)
		r.Header.Set("X-Request-Id", "first-id")
		return httpwiretest.Case{Row: "probe.get", Request: r}
	}
	const keyed, unkeyed = "nor an earlier case's with this credential and Idempotency-Key", "is not the X-Request-Id"
	cases := map[string]struct {
		cases func(testing.TB) []httpwiretest.Case
		want  string
	}{
		"no earlier case": {func(tb testing.TB) []httpwiretest.Case { return []httpwiretest.Case{resend(tb, `"k1"`)} }, keyed},
		"an earlier case under another key": {func(tb testing.TB) []httpwiretest.Case {
			return []httpwiretest.Case{first(tb, `"k2"`), resend(tb, `"k1"`)}
		}, keyed},
		"an earlier case under another key sent with a trailing space": {func(tb testing.TB) []httpwiretest.Case {
			return []httpwiretest.Case{first(tb, `"k2" `), resend(tb, `"k1"`)}
		}, keyed},
		"an earlier case with no key": {func(tb testing.TB) []httpwiretest.Case {
			return []httpwiretest.Case{first(tb, ""), resend(tb, `"k1"`)}
		}, keyed},
		"an earlier case of another row": {func(tb testing.TB) []httpwiretest.Case {
			return []httpwiretest.Case{runJob(tb, `"k1"`, "first-id"), resend(tb, `"k1"`)}
		}, keyed},
		"an earlier case of another credential": {func(tb testing.TB) []httpwiretest.Case {
			earlier := first(tb, `"k1"`)
			return []httpwiretest.Case{{Row: earlier.Row, Request: as(bob)(earlier.Request)}, resend(tb, `"k1"`)}
		}, keyed},
		"two cases with no key on a Keyed row": {func(tb testing.TB) []httpwiretest.Case {
			return []httpwiretest.Case{first(tb, ""), resend(tb, "")}
		}, unkeyed},
		"two cases with one key on a row that is not Keyed": {func(tb testing.TB) []httpwiretest.Case {
			return []httpwiretest.Case{probe(tb, "/api/probe?missing"), probe(tb, "/api/probe?replay")}
		}, unkeyed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false)
			cs := tc.cases(t)
			row := cs[len(cs)-1].Row
			r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, cs) })
			if len(r.failures) != 1 || !r.failed(`row "`+row+`"`, "?replay", `request_id "first-id"`, `"second-id"`, tc.want) {
				t.Errorf("failures %q; want only the resend's, naming its request_id", r.failures)
			}
		})
	}
}

// sameShapeCases cover every read row with Changes: jobs.mine, jobs.all with
// a page cursor, and jobs.get both found and refused.
func sameShapeCases(t testing.TB) []httpwiretest.Case {
	t.Helper()
	return []httpwiretest.Case{
		{Row: "jobs.mine", Request: request(t, http.MethodGet, "/api/my/jobs", "")},
		{Row: "jobs.all", Request: request(t, http.MethodGet, "/api/jobs?limit=2", "")},
		{Row: "jobs.get", Request: request(t, http.MethodGet, "/api/jobs/a1", "")},
		{Row: "jobs.get", Request: request(t, http.MethodGet, "/api/jobs/zz", "")},
		{Row: "jobs.csv", Request: request(t, http.MethodGet, "/api/jobs.csv", "")},
		{Row: "probe.get", Request: request(t, http.MethodGet, "/api/probe", "")},
	}
}

var users = httpwiretest.WithSameShape(httpwiretest.SameShape{A: as(alice), B: as(bob)})

func TestSameShapeFailsUnmarkedPerUserRow(t *testing.T) {
	t.Run("an unmarked per-user row fails", func(t *testing.T) {
		f := newFixture(t, false)
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t), users) })
		for _, row := range []string{"jobs.mine", "jobs.csv"} {
			if !r.failed(`row "`+row+`"`, "differ") {
				t.Errorf("failures %q; want one naming %s", r.failures, row)
			}
		}
		if len(r.failures) != 2 {
			t.Errorf("failures %q; want only jobs.mine's and jobs.csv's", r.failures)
		}
	})
	t.Run("the row marked Personal is skipped", func(t *testing.T) {
		f := newFixture(t, true)
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t), users) })
		if len(r.failures) != 0 {
			t.Errorf("failures %q; want none", r.failures)
		}
	})
	t.Run("shared rows pass, cursors and refusals included", func(t *testing.T) {
		f := newFixture(t, true)
		cases := sameShapeCases(t)[1:]
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, cases, users) })
		if len(r.failures) != 0 {
			t.Errorf("failures %q; want none", r.failures)
		}
		a, b := httptest.NewRecorder(), httptest.NewRecorder()
		f.api.ServeHTTP(a, request(t, http.MethodGet, "/api/jobs?limit=2", alice))
		f.api.ServeHTTP(b, request(t, http.MethodGet, "/api/jobs?limit=2", bob))
		if a.Body.String() == b.Body.String() || !strings.Contains(a.Body.String(), "next_cursor") {
			t.Errorf("the two pages %q and %q should differ only by their cursors", a.Body, b.Body)
		}
	})
	t.Run("a covered row with no case fails", func(t *testing.T) {
		f := newFixture(t, false)
		cases := sameShapeCases(t)[1:2]
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, cases, users) })
		for _, row := range []string{"jobs.mine", "jobs.get", "jobs.csv"} {
			if !r.failed(`row "`+row+`"`, "no case") {
				t.Errorf("failures %q; want one naming %s with no case", r.failures, row)
			}
		}
		if r.failed(`row "jobs.own"`) || r.failed(`row "jobs.all"`) || r.failed(`row "probe.get"`) {
			t.Errorf("failures %q name a row that needs no case or has one", r.failures)
		}
	})
	t.Run("two principals of different shapes fail", func(t *testing.T) {
		f := newFixture(t, true)
		pair := httpwiretest.WithSameShape(httpwiretest.SameShape{A: as(alice), B: as("key alice read")})
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t)[2:3], pair) })
		if !r.failed(`row "jobs.get"`, "different shapes") {
			t.Errorf("failures %q; want one naming jobs.get's two shapes", r.failures)
		}
	})
	t.Run("principals of one kind and other scopes fail", func(t *testing.T) {
		f := newFixture(t, true)
		pair := httpwiretest.WithSameShape(httpwiretest.SameShape{A: as("key alice read"), B: as("key alice write")})
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t)[2:3], pair) })
		if !r.failed(`row "jobs.get"`, "different shapes") {
			t.Errorf("failures %q; want one naming jobs.get's two shapes", r.failures)
		}
	})
	t.Run("principals of one scope and other kinds fail", func(t *testing.T) {
		f := newFixture(t, true)
		pair := httpwiretest.WithSameShape(httpwiretest.SameShape{A: as(alice), B: as("")})
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t)[2:3], pair) })
		if !r.failed(`row "jobs.get"`, "different shapes") {
			t.Errorf("failures %q; want one naming jobs.get's two shapes", r.failures)
		}
	})
	t.Run("answers differing only in status fail", func(t *testing.T) {
		f := newFixture(t, false)
		pair := httpwiretest.WithSameShape(httpwiretest.SameShape{A: as("session erin"), B: as("session frank")})
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t)[4:5], pair) })
		if !r.failed(`row "jobs.csv"`, "differ", "204", "200") {
			t.Errorf("failures %q; want one naming jobs.csv's two statuses", r.failures)
		}
	})
	t.Run("a later page's cursors are left out too", func(t *testing.T) {
		f := newFixture(t, true)
		second := func(p string) func(*http.Request) *http.Request {
			rec := httptest.NewRecorder()
			f.api.ServeHTTP(rec, request(t, http.MethodGet, "/api/jobs?limit=2", p))
			var page struct {
				Next string `json:"next_cursor"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.Next == "" {
				t.Fatalf("page one as %s: %q (%v)", p, rec.Body, err)
			}
			return func(r *http.Request) *http.Request {
				c := as(p)(r)
				c.URL.RawQuery = "limit=2&cursor=" + page.Next
				return c
			}
		}
		pair := httpwiretest.WithSameShape(httpwiretest.SameShape{A: second(alice), B: second(bob)})
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t)[1:2], pair) })
		if r.failed(`row "jobs.all"`) {
			t.Errorf("failures %q; want none naming jobs.all", r.failures)
		}
		rec := httptest.NewRecorder()
		f.api.ServeHTTP(rec, second(alice)(request(t, http.MethodGet, "/api/jobs?limit=2", "")))
		if !strings.Contains(rec.Body.String(), "prev_cursor") {
			t.Errorf("page two %q carries no prev_cursor to leave out", rec.Body)
		}
	})
	t.Run("a nil principal function fails", func(t *testing.T) {
		f := newFixture(t, true)
		pair := httpwiretest.WithSameShape(httpwiretest.SameShape{A: as(alice)})
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t)[2:3], pair) })
		if !r.failed(`row "jobs.get"`, "nil") {
			t.Errorf("failures %q; want one naming the nil function", r.failures)
		}
	})
	t.Run("a principal function answering nil fails", func(t *testing.T) {
		f := newFixture(t, true)
		pair := httpwiretest.WithSameShape(httpwiretest.SameShape{A: as(alice), B: func(*http.Request) *http.Request { return nil }})
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t)[2:3], pair) })
		if !r.failed(`row "jobs.get"`, "nil") {
			t.Errorf("failures %q; want one naming the nil request", r.failures)
		}
	})
	t.Run("each pair runs over every case", func(t *testing.T) {
		f := newFixture(t, true)
		pairs := []httpwiretest.Option{users, httpwiretest.WithSameShape(httpwiretest.SameShape{A: as(carol), B: as(dave)})}
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t), pairs...) })
		if len(r.failures) != 0 {
			t.Errorf("failures %q; want none", r.failures)
		}
		for _, who := range []string{"s:alice", "s:bob", "s:carol", "s:dave"} {
			if f.seen[who] != 1 {
				t.Errorf("jobs.all served %s %d times; want once", who, f.seen[who])
			}
		}
	})
	t.Run("without the option nothing more is checked", func(t *testing.T) {
		f := newFixture(t, false)
		r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, f.api, sameShapeCases(t)[1:2]) })
		if len(r.failures) != 0 {
			t.Errorf("failures %q; want none", r.failures)
		}
	})
}

func TestSameShapeWithNoGuard(t *testing.T) {
	f := newFixture(t, false)
	tb := f.table(false)
	tb.Endpoints = nil
	tb.Mounts = tb.Mounts[:1]
	tb.Mounts[0].Rows[0].Changes = []string{"jobs"}
	var doc map[string]any
	raw, err := os.ReadFile("testdata/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	paths, _ := doc["paths"].(map[string]any)
	for p := range paths {
		if !strings.HasPrefix(p, "/api/probe") {
			delete(paths, p)
		}
	}
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	api, err := httpwire.New(tb, nil, httpwire.WithSchema(raw), httpwire.WithFeed(fixtureFeed{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.api = api
	r := record(t, func(tb testing.TB) {
		httpwiretest.Contract(tb, api, []httpwiretest.Case{{Row: "probe.get", Request: request(t, http.MethodGet, "/api/probe", "")}}, users)
	})
	if len(r.failures) != 0 || r.fatal {
		t.Errorf("failures %q; want none from an API with no guard", r.failures)
	}
	cursor := func(c string) script {
		return script{body: strings.Replace(goodProbe, "{", `{"next_cursor":"`+c+`",`, 1)}
	}
	f.scripts["GET /api/probe as "+alice], f.scripts["GET /api/probe as "+bob] = cursor("x"), cursor("y")
	r = record(t, func(tb testing.TB) {
		httpwiretest.Contract(tb, api, []httpwiretest.Case{{Row: "probe.get", Request: request(t, http.MethodGet, "/api/probe", "")}}, users)
	})
	if !r.failed(`row "probe.get"`, "differ") {
		t.Errorf("failures %q; want the two next_cursor members of a row that is no List compared", r.failures)
	}
}
