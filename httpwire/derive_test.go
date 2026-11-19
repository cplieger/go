package httpwire_test

import (
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

type (
	jobID    string
	jobState string
	jobQuery struct {
		State jobState `json:"state,omitempty"`
	}
	jobParams struct {
		ID jobID `json:"id"`
	}
	job struct {
		ID string `json:"id"`
	}
	startRequest struct {
		Kind string `json:"kind"`
	}
	nudge struct {
		At int64 `json:"at"`
	}
	otherBody struct {
		Name string `json:"name"`
	}
	changed struct {
		Kinds []string `json:"kinds"`
	}
)

func (jobID) MaxBytes() int    { return 64 }
func (jobState) MaxBytes() int { return 16 }

var (
	errJobMissing = errors.New("job missing")
	errRunMissing = errors.New("run missing")
	errLocked     = errors.New("locked")
	errStray      = errors.New("stray")
)

// jobOwner answers two sentinels with one code and keeps a third inside.
func jobOwner() *httpwire.Owner {
	return &httpwire.Owner{
		Name: "jobs",
		Codes: []httpwire.OwnerCode{
			{Err: errJobMissing, Code: "job_missing"},
			{Err: errRunMissing, Code: "job_missing"},
		},
		Sentinels: map[string]error{"ErrJobMissing": errJobMissing, "ErrRunMissing": errRunMissing, "ErrLocked": errLocked},
		Outside:   map[string]string{"ErrLocked": "retried inside the package"},
	}
}

func fixtureCatalog(t testing.TB) *httpwire.Catalog {
	t.Helper()
	c := httpwire.NewCatalog()
	for code, status := range map[httpserver.ErrorCode]int{
		"job_missing":               http.StatusNotFound,
		"job_busy":                  http.StatusConflict,
		"auth_required":             http.StatusUnauthorized,
		"auth_forbidden":            http.StatusForbidden,
		"auth_recent_auth_required": http.StatusForbidden,
		"auth_bad_request":          http.StatusBadRequest,
	} {
		if err := c.Add(code, httpserver.CodeSpec{Status: status, Remediation: "Fix it."}); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// guardCodes is a fixture guard's codes: every authenticated class may be
// unauthenticated, a key may lack its scope, and a step-up may be due.
func guardCodes(a httpwire.Access) []httpserver.ErrorCode {
	var codes []httpserver.ErrorCode
	switch a.Class {
	case httpwire.Public:
		return nil
	case httpwire.Session:
		codes = []httpserver.ErrorCode{"auth_required"}
	case httpwire.Key, httpwire.Scoped:
		codes = []httpserver.ErrorCode{"auth_required", "auth_forbidden"}
	}
	if a.RecentAuth {
		codes = append(codes, "auth_recent_auth_required")
	}
	return codes
}

var (
	session = httpwire.Access{Class: httpwire.Session}
	public  = httpwire.Access{Class: httpwire.Public}
)

// fixtureTable holds one row per feature and a mount; Derive accepts it.
func fixtureTable(t *testing.T) *httpwire.Table {
	t.Helper()
	return &httpwire.Table{
		Prefix:       "/api",
		Catalog:      fixtureCatalog(t),
		ClientHeader: "Fixture-Version",
		Sources:      []httpwire.FeedSource{{Name: "store", Log: true}, {Name: "accounts", Log: true}, {Name: "live"}},
		Kinds:        []httpwire.FeedKind{{Name: "jobs", Source: "store"}, {Name: "keys", Source: "accounts"}, {Name: "status", Source: "live"}},
		Endpoints: []httpwire.Endpoint{
			{
				Name: "jobs.list", Method: http.MethodGet, Path: "/jobs", Summary: "List jobs", Description: "Lists the jobs.",
				Query: reflect.TypeFor[jobQuery](), Response: reflect.TypeFor[[]job](), Access: session, Changes: []string{"jobs", "status"},
			},
			{
				Name: "jobs.get", Method: http.MethodGet, Path: "/jobs/{id}", Summary: "Read a job", Description: "Reads one job.",
				Params: reflect.TypeFor[jobParams](), Response: reflect.TypeFor[job](), Access: session, Owners: []*httpwire.Owner{jobOwner()},
				Changes: []string{"jobs"},
			},
			{
				Name: "jobs.start", Method: http.MethodPost, Path: "/jobs", Summary: "Start a job", Description: "Starts a job.",
				Request: reflect.TypeFor[startRequest](), Response: reflect.TypeFor[job](), Status: http.StatusAccepted,
				Access: httpwire.Access{Class: httpwire.Scoped, Scope: "operate"}, Codes: []httpserver.ErrorCode{"job_busy"},
				Keyed: true, Window: 24 * time.Hour, MaxKept: 1000, Result: "jobs.list",
				Reasons: map[httpserver.ErrorCode]httpwire.ReasonSet{"job_busy": {Listed: []string{"running"}}},
			},
			{
				Name: "settings.save", Method: http.MethodPatch, Path: "/settings", Summary: "Save sign-in", Description: "Saves the sign-in methods.",
				Request: reflect.TypeFor[otherBody](), Access: httpwire.Access{Class: httpwire.Session, PersonOnly: true, RecentAuth: true},
				PersonActions: []string{httpwire.PersonSignInMethods}, IfMatch: true,
			},
			{
				Name: "archive.export", Method: http.MethodPost, Path: "/archive", Summary: "Export", Description: "Exports an archive.",
				Kind: httpwire.Raw, ContentType: "application/zip", Access: session, PersonActions: []string{httpwire.PersonAccountsArchive},
			},
			{
				Name: "events.wait", Method: http.MethodGet, Path: "/events/wait", Summary: "Wait", Description: "Waits for a nudge.",
				Response: reflect.TypeFor[nudge](), Successes: []httpwire.Success{{Status: http.StatusNoContent}}, Access: session, Held: true,
			},
			{
				Name: "live.stream", Method: http.MethodGet, Path: "/stream", Summary: "Stream", Description: "Streams changes.",
				Kind: httpwire.Stream, Access: session, Events: []httpwire.Event{{Name: "changed", Data: reflect.TypeFor[changed]()}},
			},
			{
				Name: "auth.sso", Method: http.MethodGet, Path: "/sso", Summary: "Sign in", Description: "Redirects to the provider.",
				Kind: httpwire.Redirect, Access: public, DuringOutage: true,
			},
		},
		Mounts: []httpwire.Mount{{
			Handler: http.NotFoundHandler(),
			Rows: []httpwire.Endpoint{
				{
					Name: "auth.keys", Method: http.MethodGet, Path: "/auth/keys", Summary: "List keys", Description: "Lists the API keys.",
					Response: reflect.TypeFor[[]job](), Access: httpwire.Access{Class: httpwire.Key, Scope: "admin"},
					Codes: []httpserver.ErrorCode{"auth_required", "auth_forbidden"}, Changes: []string{"keys"},
				},
				{
					Name: "auth.keyCreate", Method: http.MethodPost, Path: "/auth/keys", Summary: "Create a key", Description: "Creates an API key.",
					Request: reflect.TypeFor[otherBody](), Response: reflect.TypeFor[job](), Status: http.StatusCreated, Access: session,
					Codes:   []httpserver.ErrorCode{"auth_required", "auth_bad_request"},
					Reasons: map[httpserver.ErrorCode]httpwire.ReasonSet{"auth_bad_request": {Listed: []string{"name"}, Open: true}},
				},
			},
		}},
	}
}

func row(tb *httpwire.Table, name string) *httpwire.Endpoint {
	for i := range tb.Endpoints {
		if tb.Endpoints[i].Name == name {
			return &tb.Endpoints[i]
		}
	}
	for m := range tb.Mounts {
		for i := range tb.Mounts[m].Rows {
			if tb.Mounts[m].Rows[i].Name == name {
				return &tb.Mounts[m].Rows[i]
			}
		}
	}
	panic("no fixture row " + name)
}

func TestDeriveAcceptsFixtureTable(t *testing.T) {
	got, err := httpwire.Derive(fixtureTable(t), guardCodes)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got.MaxBody != 64<<10 {
		t.Errorf("MaxBody = %d, want the 64 KiB default", got.MaxBody)
	}
	if len(got.Endpoints) != 8 || len(got.Mounts) != 1 || len(got.Mounts[0].Rows) != 2 {
		t.Errorf("Derive changed the row count: %d rows, %d mounts", len(got.Endpoints), len(got.Mounts))
	}
}

func TestDeriveKeepsAnExplicitMaxBody(t *testing.T) {
	tb := fixtureTable(t)
	tb.MaxBody = 1 << 20
	got, err := httpwire.Derive(tb, guardCodes)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxBody != 1<<20 {
		t.Errorf("MaxBody = %d, want 1 MiB", got.MaxBody)
	}
}

func TestDeriveLeavesItsInputAlone(t *testing.T) {
	tb := fixtureTable(t)
	got, err := httpwire.Derive(tb, guardCodes)
	if err != nil {
		t.Fatal(err)
	}
	if tb.MaxBody != 0 {
		t.Errorf("input MaxBody = %d, want 0", tb.MaxBody)
	}
	for _, name := range []string{"jobs.list", "auth.keys"} {
		if e := row(tb, name).Errors; e != nil {
			t.Errorf("input row %s Errors = %v, want nil", name, e)
		}
	}
	row(got, "jobs.start").Codes[0] = "changed"
	row(got, "jobs.start").Reasons["job_busy"].Listed[0] = "changed"
	row(got, "auth.keys").Changes[0] = "changed"
	if c := row(tb, "jobs.start").Codes[0]; c != "job_busy" {
		t.Errorf("an edit of the derived Codes reached the input: %q", c)
	}
	if r := row(tb, "jobs.start").Reasons["job_busy"].Listed[0]; r != "running" {
		t.Errorf("an edit of the derived Reasons reached the input: %q", r)
	}
	if c := row(tb, "auth.keys").Changes[0]; c != "keys" {
		t.Errorf("an edit of a derived mount row reached the input: %q", c)
	}
}

var everyRow = []httpserver.ErrorCode{"accounts_unavailable", "maintenance", "host_not_allowed", "internal_error", "not_found", "method_not_allowed"}

var keyedCodes = []httpserver.ErrorCode{
	"idempotency_key_invalid", "idempotency_key_required", "idempotency_key_reused", "idempotency_in_flight",
	"idempotency_store_full", "idempotency_answer_too_large", "held_limit",
}

func join(parts ...[]httpserver.ErrorCode) []httpserver.ErrorCode { return slices.Concat(parts...) }

func TestUnionOfEachRow(t *testing.T) {
	want := map[string][]httpserver.ErrorCode{
		"jobs.list": join([]httpserver.ErrorCode{"bad_query", "idempotency_key_unsupported", "upgrade_required", "store_unavailable", "auth_required"}, everyRow),
		"jobs.get":  join([]httpserver.ErrorCode{"job_missing", "idempotency_key_unsupported", "upgrade_required", "store_unavailable", "auth_required"}, everyRow),
		"jobs.start": join([]httpserver.ErrorCode{"job_busy", "unsupported_media_type", "bad_body", "body_too_large"}, keyedCodes,
			[]httpserver.ErrorCode{"wire_mismatch", "cross_origin_refused", "rate_limited", "upgrade_required", "store_unavailable", "auth_required", "auth_forbidden"}, everyRow),
		"settings.save": join([]httpserver.ErrorCode{
			"unsupported_media_type", "bad_body", "body_too_large", "if_match_required", "if_match_invalid", "stale_version", "idempotency_key_unsupported",
			"wire_mismatch", "cross_origin_refused", "person_required", "upgrade_required", "store_unavailable", "auth_required", "auth_recent_auth_required",
		}, everyRow),
		"archive.export": join([]httpserver.ErrorCode{
			"body_too_large", "idempotency_key_unsupported", "wire_mismatch", "cross_origin_refused", "person_required",
			"auth_required", "auth_recent_auth_required", "upgrade_required", "store_unavailable",
		}, everyRow),
		"events.wait":    join([]httpserver.ErrorCode{"idempotency_key_unsupported", "held_limit", "upgrade_required", "store_unavailable", "auth_required"}, everyRow),
		"live.stream":    join([]httpserver.ErrorCode{"idempotency_key_unsupported", "held_limit", "upgrade_required", "store_unavailable", "auth_required"}, everyRow),
		"auth.sso":       join([]httpserver.ErrorCode{"idempotency_key_unsupported", "upgrade_required"}, everyRow),
		"auth.keys":      join([]httpserver.ErrorCode{"auth_required", "auth_forbidden", "upgrade_required", "rate_limited"}, everyRow),
		"auth.keyCreate": join([]httpserver.ErrorCode{"auth_required", "auth_bad_request", "upgrade_required", "wire_mismatch", "cross_origin_refused"}, everyRow),
	}
	got, err := httpwire.Derive(fixtureTable(t), guardCodes)
	if err != nil {
		t.Fatal(err)
	}
	for name, w := range want {
		t.Run(name, func(t *testing.T) {
			ep := row(got, name)
			if !slices.Equal(ep.Errors, w) {
				t.Errorf("Errors =\n %v\nwant\n %v", ep.Errors, w)
			}
			if u := httpwire.Union(got, ep, guardCodes); !slices.Equal(u, ep.Errors) {
				t.Errorf("Union = %v, want Derive's Errors %v", u, ep.Errors)
			}
		})
	}
}

func TestUnionWithoutClientHeaderOrGuard(t *testing.T) {
	tb := fixtureTable(t)
	tb.ClientHeader = ""
	got, err := httpwire.Derive(tb, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := join([]httpserver.ErrorCode{"bad_query", "idempotency_key_unsupported", "store_unavailable"}, everyRow)
	if e := row(got, "jobs.list").Errors; !slices.Equal(e, want) {
		t.Errorf("Errors = %v, want %v", e, want)
	}
	want = join([]httpserver.ErrorCode{"auth_required", "auth_forbidden", "rate_limited"}, everyRow)
	if e := row(got, "auth.keys").Errors; !slices.Equal(e, want) {
		t.Errorf("mount Errors = %v, want %v", e, want)
	}
}

func TestUnionListRowCarriesCursorInvalid(t *testing.T) {
	tb := fixtureTable(t)
	row(tb, "jobs.list").List = true
	u := httpwire.Union(tb, row(tb, "jobs.list"), guardCodes)
	want := join([]httpserver.ErrorCode{"bad_query", "cursor_invalid", "idempotency_key_unsupported", "upgrade_required", "store_unavailable", "auth_required"}, everyRow)
	if !slices.Equal(u, want) {
		t.Errorf("Union = %v, want %v", u, want)
	}
}

func TestDeriveRefusesPersonalChangesWithoutOnePerson(t *testing.T) {
	tb := fixtureTable(t)
	row(tb, "jobs.list").Personal = true
	_, err := httpwire.Derive(tb, guardCodes)
	if err == nil || !strings.Contains(err.Error(), `row "jobs.list"`) || !strings.Contains(err.Error(), "Personal") {
		t.Errorf("Derive = %v, want a refusal naming jobs.list as Personal", err)
	}
	t.Run("OnePerson", func(t *testing.T) {
		tb.OnePerson = true
		if _, err := httpwire.Derive(tb, guardCodes); err != nil {
			t.Errorf("Derive in a OnePerson table = %v, want nil", err)
		}
	})
	t.Run("no Changes", func(t *testing.T) {
		tb := fixtureTable(t)
		ep := row(tb, "jobs.get")
		ep.Personal, ep.Changes = true, nil
		if _, err := httpwire.Derive(tb, guardCodes); err != nil {
			t.Errorf("Derive of a Personal row with no Changes = %v, want nil", err)
		}
	})
	t.Run("mount row", func(t *testing.T) {
		tb := fixtureTable(t)
		row(tb, "auth.keys").Personal = true
		_, err := httpwire.Derive(tb, guardCodes)
		if err == nil || !strings.Contains(err.Error(), `row "auth.keys"`) {
			t.Errorf("Derive = %v, want a refusal naming auth.keys", err)
		}
	})
}

func TestDeriveListsEveryCaseInOneError(t *testing.T) {
	tb := fixtureTable(t)
	row(tb, "jobs.list").Summary = ""
	row(tb, "auth.keyCreate").Description = ""
	tb.Kinds = append(tb.Kinds, httpwire.FeedKind{Name: "orphan", Source: "nowhere"})
	_, err := httpwire.Derive(tb, guardCodes)
	if err == nil {
		t.Fatal("Derive accepted three broken rows")
	}
	for _, want := range []string{`row "jobs.list"`, `row "auth.keyCreate"`, `kind "orphan"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestDeriveRefusesNilTable(t *testing.T) {
	if _, err := httpwire.Derive(nil, guardCodes); err == nil {
		t.Error("Derive(nil) = nil error")
	}
}

type refusal struct {
	mutate func(*httpwire.Table)
	want   []string // fragments the error must hold
}

func edit(name string, f func(*httpwire.Endpoint)) func(*httpwire.Table) {
	return func(tb *httpwire.Table) { f(row(tb, name)) }
}

func addRow(ep httpwire.Endpoint) func(*httpwire.Table) {
	return func(tb *httpwire.Table) {
		if ep.Summary == "" {
			ep.Summary, ep.Description = "Extra", "An extra row."
		}
		if ep.Access.Class == 0 {
			ep.Access = session
		}
		tb.Endpoints = append(tb.Endpoints, ep)
	}
}

func runRefusals(t *testing.T, cases map[string]refusal) {
	t.Helper()
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tb := fixtureTable(t)
			c.mutate(tb)
			_, err := httpwire.Derive(tb, guardCodes)
			if err == nil {
				t.Fatal("Derive accepted the table")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not hold %q", err, w)
				}
			}
		})
	}
}

func TestDeriveRefusesTableShape(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"no catalog":            {func(tb *httpwire.Table) { tb.Catalog = nil }, []string{"Catalog"}},
		"empty prefix":          {func(tb *httpwire.Table) { tb.Prefix = "" }, []string{"Prefix"}},
		"relative prefix":       {func(tb *httpwire.Table) { tb.Prefix = "api" }, []string{"Prefix"}},
		"prefix trailing slash": {func(tb *httpwire.Table) { tb.Prefix = "/api/" }, []string{"Prefix"}},
		"prefix wildcard":       {func(tb *httpwire.Table) { tb.Prefix = "/{v}" }, []string{"Prefix"}},
		"negative MaxBody":      {func(tb *httpwire.Table) { tb.MaxBody = -1 }, []string{"MaxBody"}},
		"window under minute":   {func(tb *httpwire.Table) { tb.Window = 59 * time.Second }, []string{"the table's Window"}},
		"window over 7 days":    {func(tb *httpwire.Table) { tb.Window = 7*24*time.Hour + time.Second }, []string{"the table's Window"}},
		"negative window":       {func(tb *httpwire.Table) { tb.Window = -time.Minute }, []string{"the table's Window"}},
		"client header token":   {func(tb *httpwire.Table) { tb.ClientHeader = "Fixture Version" }, []string{"ClientHeader"}},
		"mount with no handler": {func(tb *httpwire.Table) { tb.Mounts[0].Handler = nil }, []string{"mount", "Handler"}},
		"source name uppercase": {func(tb *httpwire.Table) { tb.Sources[0].Name = "Store" }, []string{`source "Store"`}},
		"source name digit":     {func(tb *httpwire.Table) { tb.Sources = append(tb.Sources, httpwire.FeedSource{Name: "1x"}) }, []string{`source "1x"`}},
		"source name too long": {func(tb *httpwire.Table) {
			tb.Sources = append(tb.Sources, httpwire.FeedSource{Name: strings.Repeat("s", 33)})
		}, []string{"source"}},
		"source twice":           {func(tb *httpwire.Table) { tb.Sources = append(tb.Sources, httpwire.FeedSource{Name: "live"}) }, []string{`source "live"`, "twice"}},
		"kind of unknown source": {func(tb *httpwire.Table) { tb.Kinds[0].Source = "nowhere" }, []string{`kind "jobs"`, `"nowhere"`}},
		"kind twice":             {func(tb *httpwire.Table) { tb.Kinds = append(tb.Kinds, httpwire.FeedKind{Name: "jobs", Source: "live"}) }, []string{`kind "jobs"`, "twice"}},
		"kind name grammar":      {func(tb *httpwire.Table) { tb.Kinds = append(tb.Kinds, httpwire.FeedKind{Name: "Jobs", Source: "live"}) }, []string{`kind "Jobs"`}},
		"kind name too long": {func(tb *httpwire.Table) {
			tb.Kinds = append(tb.Kinds, httpwire.FeedKind{Name: strings.Repeat("k", 33), Source: "live"})
		}, []string{"kind"}},
	})
}

func TestDeriveRefusesNamesMethodsAndPaths(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"empty name":           {edit("jobs.list", func(e *httpwire.Endpoint) { e.Name = "" }), []string{`row ""`, "name"}},
		"name with no area":    {edit("jobs.list", func(e *httpwire.Endpoint) { e.Name = "list" }), []string{`row "list"`, "name"}},
		"name uppercase start": {edit("jobs.list", func(e *httpwire.Endpoint) { e.Name = "Jobs.list" }), []string{`row "Jobs.list"`, "name"}},
		"name empty segment":   {edit("jobs.list", func(e *httpwire.Endpoint) { e.Name = "jobs..list" }), []string{`row "jobs..list"`, "name"}},
		"name punctuation":     {edit("jobs.list", func(e *httpwire.Endpoint) { e.Name = "jobs.list-all" }), []string{`row "jobs.list-all"`, "name"}},
		"name twice":           {edit("jobs.get", func(e *httpwire.Endpoint) { e.Name = "jobs.list" }), []string{`row "jobs.list"`, "twice"}},
		"name of a mount row":  {edit("jobs.get", func(e *httpwire.Endpoint) { e.Name = "auth.keys" }), []string{`row "auth.keys"`, "twice"}},
		"method HEAD":          {edit("jobs.list", func(e *httpwire.Endpoint) { e.Method = http.MethodHead }), []string{`row "jobs.list"`, "method"}},
		"method lowercase":     {edit("jobs.list", func(e *httpwire.Endpoint) { e.Method = "get" }), []string{`row "jobs.list"`, "method"}},
		"mount method":         {edit("auth.keys", func(e *httpwire.Endpoint) { e.Method = "OPTIONS" }), []string{`row "auth.keys"`, "method"}},
		"path with no slash":   {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "jobs" }), []string{`row "jobs.list"`, "path"}},
		"empty path":           {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "" }), []string{`row "jobs.list"`, "path"}},
		"path only a slash":    {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "/" }), []string{`row "jobs.list"`, "path"}},
		"mount path no slash":  {edit("auth.keys", func(e *httpwire.Endpoint) { e.Path = "auth/keys" }), []string{`row "auth.keys"`, "path"}},
		"trailing slash":       {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "/jobs/" }), []string{`row "jobs.list"`, "path"}},
		"empty segment":        {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "//jobs" }), []string{`row "jobs.list"`, "path"}},
		"partial wildcard":     {edit("jobs.get", func(e *httpwire.Endpoint) { e.Path = "/jobs/x{id}" }), []string{`row "jobs.get"`, "path"}},
		"end anchor":           {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "/jobs/{$}" }), []string{`row "jobs.list"`, "path"}},
		"rest wildcard":        {edit("jobs.get", func(e *httpwire.Endpoint) { e.Path = "/jobs/{id...}" }), []string{`row "jobs.get"`, "path"}},
		"wildcard twice":       {edit("jobs.get", func(e *httpwire.Endpoint) { e.Path = "/{id}/jobs/{id}" }), []string{`row "jobs.get"`, "path"}},
		"wildcard name":        {edit("jobs.get", func(e *httpwire.Endpoint) { e.Path = "/jobs/{1d}" }), []string{`row "jobs.get"`, "path"}},
		"query in path":        {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "/jobs?all" }), []string{`row "jobs.list"`, "path"}},
		"space in path":        {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "/my jobs" }), []string{`row "jobs.list"`, "path"}},
		"bad escape":           {edit("jobs.list", func(e *httpwire.Endpoint) { e.Path = "/jobs%2" }), []string{`row "jobs.list"`, "path"}},
		"one method and pattern": {
			edit("jobs.get", func(e *httpwire.Endpoint) { e.Path, e.Params = "/jobs", nil }), []string{`"jobs.get"`, `"jobs.list"`},
		},
		"table row on a mount pattern": {
			edit("jobs.get", func(e *httpwire.Endpoint) { e.Path, e.Params = "/auth/keys", nil }), []string{`"jobs.get"`, `"auth.keys"`},
		},
		"ambiguous patterns": {
			func(tb *httpwire.Table) {
				addRow(httpwire.Endpoint{Name: "a.one", Method: http.MethodGet, Path: "/x/{b}", Params: reflect.TypeFor[jobParams]()})(tb)
				addRow(httpwire.Endpoint{Name: "a.two", Method: http.MethodGet, Path: "/{a}/y", Params: reflect.TypeFor[jobParams]()})(tb)
			},
			[]string{`"a.one"`, `"a.two"`},
		},
		"params with no wildcard": {edit("jobs.list", func(e *httpwire.Endpoint) { e.Params = reflect.TypeFor[jobParams]() }), []string{`row "jobs.list"`, "Params"}},
		"wildcard with no params": {edit("jobs.get", func(e *httpwire.Endpoint) { e.Params = nil }), []string{`row "jobs.get"`, "Params"}},
		"request on a GET":        {edit("jobs.list", func(e *httpwire.Endpoint) { e.Request = reflect.TypeFor[startRequest]() }), []string{`row "jobs.list"`, "Request"}},
		"empty summary":           {edit("jobs.list", func(e *httpwire.Endpoint) { e.Summary = "" }), []string{`row "jobs.list"`, "Summary"}},
		"empty description":       {edit("jobs.list", func(e *httpwire.Endpoint) { e.Description = " " }), []string{`row "jobs.list"`, "Description"}},
		"mount empty summary":     {edit("auth.keys", func(e *httpwire.Endpoint) { e.Summary = "" }), []string{`row "auth.keys"`, "Summary"}},
		"mount empty description": {edit("auth.keyCreate", func(e *httpwire.Endpoint) { e.Description = "" }), []string{`row "auth.keyCreate"`, "Description"}},
		"negative row MaxBody":    {edit("jobs.start", func(e *httpwire.Endpoint) { e.MaxBody = -1 }), []string{`row "jobs.start"`, "MaxBody"}},
	})
}

func TestDeriveRefusesKindsAndStatuses(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"unknown kind":           {edit("jobs.list", func(e *httpwire.Endpoint) { e.Kind = 9 }), []string{`row "jobs.list"`, "Kind"}},
		"content type on JSON":   {edit("jobs.list", func(e *httpwire.Endpoint) { e.ContentType = "text/csv" }), []string{`row "jobs.list"`, "ContentType"}},
		"raw with no type":       {edit("archive.export", func(e *httpwire.Endpoint) { e.ContentType = "" }), []string{`row "archive.export"`, "ContentType"}},
		"response on raw":        {edit("archive.export", func(e *httpwire.Endpoint) { e.Response = reflect.TypeFor[job]() }), []string{`row "archive.export"`, "Response"}},
		"response on stream":     {edit("live.stream", func(e *httpwire.Endpoint) { e.Response = reflect.TypeFor[job]() }), []string{`row "live.stream"`, "Response"}},
		"response on redirect":   {edit("auth.sso", func(e *httpwire.Endpoint) { e.Response = reflect.TypeFor[job]() }), []string{`row "auth.sso"`, "Response"}},
		"stream on a POST":       {edit("live.stream", func(e *httpwire.Endpoint) { e.Method = http.MethodPost }), []string{`row "live.stream"`, "GET"}},
		"redirect on a POST":     {edit("auth.sso", func(e *httpwire.Endpoint) { e.Method = http.MethodPost }), []string{`row "auth.sso"`, "GET"}},
		"IfMatch on raw":         {edit("archive.export", func(e *httpwire.Endpoint) { e.IfMatch = true }), []string{`row "archive.export"`, "IfMatch"}},
		"IfMatch on stream":      {edit("live.stream", func(e *httpwire.Endpoint) { e.IfMatch = true }), []string{`row "live.stream"`, "IfMatch"}},
		"JSON 205":               {edit("jobs.start", func(e *httpwire.Endpoint) { e.Status = http.StatusResetContent }), []string{`row "jobs.start"`, "205"}},
		"JSON 300":               {edit("jobs.start", func(e *httpwire.Endpoint) { e.Status = http.StatusMultipleChoices }), []string{`row "jobs.start"`, "300"}},
		"JSON 204 with a body":   {edit("jobs.start", func(e *httpwire.Endpoint) { e.Status = http.StatusNoContent }), []string{`row "jobs.start"`, "204"}},
		"JSON 200 with no body":  {edit("settings.save", func(e *httpwire.Endpoint) { e.Status = http.StatusOK }), []string{`row "settings.save"`, "200"}},
		"redirect 200":           {edit("auth.sso", func(e *httpwire.Endpoint) { e.Status = http.StatusOK }), []string{`row "auth.sso"`, "200"}},
		"redirect 304":           {edit("auth.sso", func(e *httpwire.Endpoint) { e.Status = http.StatusNotModified }), []string{`row "auth.sso"`, "304"}},
		"raw 204":                {edit("archive.export", func(e *httpwire.Endpoint) { e.Status = http.StatusNoContent }), []string{`row "archive.export"`, "204"}},
		"stream not 200":         {edit("live.stream", func(e *httpwire.Endpoint) { e.Status = 299 }), []string{`row "live.stream"`, "200"}},
		"extra 204 with a body":  {edit("events.wait", func(e *httpwire.Endpoint) { e.Successes[0].Response = reflect.TypeFor[nudge]() }), []string{`row "events.wait"`, "204"}},
		"extra 201 with no body": {edit("events.wait", func(e *httpwire.Endpoint) { e.Successes[0].Status = http.StatusCreated }), []string{`row "events.wait"`, "201"}},
		"extra 205":              {edit("events.wait", func(e *httpwire.Endpoint) { e.Successes[0].Status = http.StatusResetContent }), []string{`row "events.wait"`, "205"}},
		"extra error status":     {edit("events.wait", func(e *httpwire.Endpoint) { e.Successes[0].Status = http.StatusNotFound }), []string{`row "events.wait"`, "404"}},
		"extra is the primary": {edit("events.wait", func(e *httpwire.Endpoint) {
			e.Successes[0] = httpwire.Success{Status: 200, Response: reflect.TypeFor[nudge]()}
		}), []string{`row "events.wait"`, "200"}},
		"extra is the default": {edit("settings.save", func(e *httpwire.Endpoint) { e.Successes = []httpwire.Success{{Status: http.StatusNoContent}} }), []string{`row "settings.save"`, "204"}},
		"extra twice":          {edit("jobs.get", func(e *httpwire.Endpoint) { e.Successes = []httpwire.Success{{Status: 204}, {Status: 204}} }), []string{`row "jobs.get"`, "twice"}},
		"raw extra 200":        {edit("archive.export", func(e *httpwire.Endpoint) { e.Successes = []httpwire.Success{{Status: 201}} }), []string{`row "archive.export"`, "201"}},
		"stream extra with body": {edit("live.stream", func(e *httpwire.Endpoint) {
			e.Successes = []httpwire.Success{{Status: 204, Response: reflect.TypeFor[job]()}}
		}), []string{`row "live.stream"`, "Response"}},
		"redirect extra 200": {edit("auth.sso", func(e *httpwire.Endpoint) { e.Successes = []httpwire.Success{{Status: 200}} }), []string{`row "auth.sso"`, "200"}},
		"redirect extra with body": {edit("auth.sso", func(e *httpwire.Endpoint) {
			e.Successes = []httpwire.Success{{Status: 303, Response: reflect.TypeFor[job]()}}
		}), []string{`row "auth.sso"`, "Response"}},
		"two body types": {
			edit("jobs.get", func(e *httpwire.Endpoint) {
				e.Successes = []httpwire.Success{{Status: http.StatusCreated, Response: reflect.TypeFor[otherBody]()}}
			}),
			[]string{`row "jobs.get"`, "httpwire_test.job", "httpwire_test.otherBody"},
		},
	})
}

func TestDeriveAcceptsSuccessesUnderEachKind(t *testing.T) {
	tb := fixtureTable(t)
	row(tb, "jobs.get").Successes = []httpwire.Success{{Status: http.StatusCreated, Response: reflect.TypeFor[job]()}, {Status: http.StatusNoContent}}
	row(tb, "archive.export").Successes = []httpwire.Success{{Status: http.StatusNoContent}}
	row(tb, "live.stream").Successes = []httpwire.Success{{Status: http.StatusNoContent}}
	row(tb, "auth.sso").Successes = []httpwire.Success{{Status: http.StatusSeeOther}, {Status: http.StatusTemporaryRedirect}}
	row(tb, "settings.save").Successes = []httpwire.Success{{Status: http.StatusOK, Response: reflect.TypeFor[otherBody]()}}
	if _, err := httpwire.Derive(tb, guardCodes); err != nil {
		t.Errorf("Derive = %v, want nil", err)
	}
}

func TestDeriveRefusesAccess(t *testing.T) {
	with := func(name string, a httpwire.Access) func(*httpwire.Table) {
		return edit(name, func(e *httpwire.Endpoint) { e.Access = a })
	}
	runRefusals(t, map[string]refusal{
		"zero class":               {with("jobs.list", httpwire.Access{}), []string{`row "jobs.list"`, "Class"}},
		"unknown class":            {with("jobs.list", httpwire.Access{Class: 9}), []string{`row "jobs.list"`, "Class"}},
		"scope on public":          {with("auth.sso", httpwire.Access{Class: httpwire.Public, Scope: "read"}), []string{`row "auth.sso"`, "Scope"}},
		"scope on session":         {with("jobs.list", httpwire.Access{Class: httpwire.Session, Scope: "read"}), []string{`row "jobs.list"`, "Scope"}},
		"the class after Scoped":   {with("jobs.list", httpwire.Access{Class: httpwire.Scoped + 1}), []string{`row "jobs.list"`, "not Public, Session, Key or Scoped"}},
		"scoped with no scope":     {with("jobs.start", httpwire.Access{Class: httpwire.Scoped}), []string{`row "jobs.start"`, "Scope"}},
		"scope outside grammar":    {with("jobs.start", httpwire.Access{Class: httpwire.Scoped, Scope: "Read All"}), []string{`row "jobs.start"`, "Scope"}},
		"passive on public":        {with("auth.sso", httpwire.Access{Class: httpwire.Public, Passive: true}), []string{`row "auth.sso"`, "Passive"}},
		"HTTPS on public":          {with("auth.sso", httpwire.Access{Class: httpwire.Public, HTTPS: true}), []string{`row "auth.sso"`, "HTTPS"}},
		"recent auth on public":    {with("auth.sso", httpwire.Access{Class: httpwire.Public, RecentAuth: true}), []string{`row "auth.sso"`, "RecentAuth"}},
		"person only on key":       {with("jobs.start", httpwire.Access{Class: httpwire.Key, PersonOnly: true}), []string{`row "jobs.start"`, "PersonOnly"}},
		"person only on public":    {with("auth.sso", httpwire.Access{Class: httpwire.Public, PersonOnly: true}), []string{`row "auth.sso"`, "PersonOnly"}},
		"mount row access":         {with("auth.keys", httpwire.Access{}), []string{`row "auth.keys"`, "Class"}},
		"event readers malformed":  {edit("live.stream", func(e *httpwire.Endpoint) { e.Events[0].Readers = httpwire.Access{Class: httpwire.Public, Scope: "x"} }), []string{`row "live.stream"`, `event "changed"`, "Scope"}},
		"event readers class zero": {edit("live.stream", func(e *httpwire.Endpoint) { e.Events[0].Readers = httpwire.Access{Passive: true} }), []string{`row "live.stream"`, `event "changed"`}},
	})
}

func TestDeriveAcceptsAccessForms(t *testing.T) {
	tb := fixtureTable(t)
	addRow(httpwire.Endpoint{Name: "key.any", Method: http.MethodGet, Path: "/k1", Access: httpwire.Access{Class: httpwire.Key}})(tb)
	addRow(httpwire.Endpoint{Name: "key.scope", Method: http.MethodGet, Path: "/k2", Access: httpwire.Access{Class: httpwire.Key, Scope: "read"}})(tb)
	addRow(httpwire.Endpoint{Name: "person.scope", Method: http.MethodGet, Path: "/p", Access: httpwire.Access{Class: httpwire.Session, PersonOnly: true, Scope: "admin"}})(tb)
	addRow(httpwire.Endpoint{Name: "scoped.step", Method: http.MethodPost, Path: "/s", Access: httpwire.Access{Class: httpwire.Scoped, Scope: "admin", RecentAuth: true, HTTPS: true}})(tb)
	addRow(httpwire.Endpoint{Name: "session.passive", Method: http.MethodGet, Path: "/r", Access: httpwire.Access{Class: httpwire.Session, Passive: true}})(tb)
	row(tb, "live.stream").Events[0].Readers = httpwire.Access{Class: httpwire.Scoped, Scope: "read"}
	if _, err := httpwire.Derive(tb, guardCodes); err != nil {
		t.Errorf("Derive = %v, want nil", err)
	}
}

func TestDeriveRefusesKeyedAndHeldRows(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"keyed public":         {edit("auth.sso", func(e *httpwire.Endpoint) { e.Keyed = true }), []string{`row "auth.sso"`, "Keyed", "Public"}},
		"keyed stream":         {edit("live.stream", func(e *httpwire.Endpoint) { e.Keyed = true }), []string{`row "live.stream"`, "Keyed", "Stream"}},
		"keyed shown once":     {edit("jobs.start", func(e *httpwire.Endpoint) { e.ShownOnce = true }), []string{`row "jobs.start"`, "ShownOnce"}},
		"window not keyed":     {edit("settings.save", func(e *httpwire.Endpoint) { e.Window = time.Hour }), []string{`row "settings.save"`, "Window"}},
		"window under minute":  {edit("jobs.start", func(e *httpwire.Endpoint) { e.Window = 59 * time.Second }), []string{`row "jobs.start"`, "Window"}},
		"window over 7 days":   {edit("jobs.start", func(e *httpwire.Endpoint) { e.Window = 7*24*time.Hour + time.Second }), []string{`row "jobs.start"`, "Window"}},
		"negative window":      {edit("jobs.start", func(e *httpwire.Endpoint) { e.Window = -time.Hour }), []string{`row "jobs.start"`, "Window"}},
		"max kept not keyed":   {edit("settings.save", func(e *httpwire.Endpoint) { e.MaxKept = 10 }), []string{`row "settings.save"`, "MaxKept"}},
		"negative max kept":    {edit("jobs.start", func(e *httpwire.Endpoint) { e.MaxKept = -1 }), []string{`row "jobs.start"`, "MaxKept"}},
		"result not keyed":     {edit("settings.save", func(e *httpwire.Endpoint) { e.Result = "jobs.list" }), []string{`row "settings.save"`, "Result"}},
		"result names nothing": {edit("jobs.start", func(e *httpwire.Endpoint) { e.Result = "jobs.gone" }), []string{`row "jobs.start"`, `"jobs.gone"`}},
		"result is a write":    {edit("jobs.start", func(e *httpwire.Endpoint) { e.Result = "settings.save" }), []string{`row "jobs.start"`, `"settings.save"`}},
		"keyed read":           {edit("jobs.get", func(e *httpwire.Endpoint) { e.Keyed = true }), []string{`row "jobs.get"`, "Keyed", "read"}},
		"keyed text one way": {edit("jobs.start", func(e *httpwire.Endpoint) { e.Request = reflect.TypeFor[oneWayBody]() }), []string{
			`row "jobs.start"`, "httpwire_test.oneWayText", "MarshalText",
		}},
		"keyed JSON one way": {edit("jobs.start", func(e *httpwire.Endpoint) { e.Request = reflect.TypeFor[[]map[string]*oneWayJSON]() }), []string{
			`row "jobs.start"`, "httpwire_test.oneWayJSON", "MarshalJSON",
		}},
		"keyed query one way": {edit("jobs.start", func(e *httpwire.Endpoint) { e.Query = reflect.TypeFor[oneWayQuery]() }), []string{
			`row "jobs.start"`, "httpwire_test.oneWayText",
		}},
		"held raw":    {edit("archive.export", func(e *httpwire.Endpoint) { e.Held = true }), []string{`row "archive.export"`, "Held"}},
		"held stream": {edit("live.stream", func(e *httpwire.Endpoint) { e.Held = true }), []string{`row "live.stream"`, "Held"}},
	})
}

// oneWayText and oneWayJSON decode into a field the fingerprint's
// json.Marshal cannot see.
type (
	oneWayText struct{ v string }
	oneWayJSON struct{ v string }
	oneWayBody struct {
		V oneWayText `json:"v"`
	}
	oneWayQuery struct {
		V oneWayText `json:"v"`
	}
)

func (o *oneWayText) UnmarshalText(b []byte) error { o.v = string(b); return nil }
func (o *oneWayJSON) UnmarshalJSON(b []byte) error { o.v = string(b); return nil }

// The window a client may resend within is the table's, so the client
// generator, which reads only the table, publishes each Keyed row's.
func TestDeriveFillsEachKeyedRowsWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		table, own, want time.Duration
	}{
		"the default":    {0, 0, 10 * time.Minute},
		"the table's":    {time.Hour, 0, time.Hour},
		"the row's own":  {time.Hour, 24 * time.Hour, 24 * time.Hour},
		"the low bound":  {time.Minute, 0, time.Minute},
		"the high bound": {7 * 24 * time.Hour, 0, 7 * 24 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			tb := fixtureTable(t)
			tb.Window = tc.table
			row(tb, "jobs.start").Window = tc.own
			d, err := httpwire.Derive(tb, guardCodes)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if got := row(d, "jobs.start").Window; got != tc.want {
				t.Errorf("the Keyed row's Window = %v, want %v", got, tc.want)
			}
			if got := row(d, "settings.save").Window; got != 0 {
				t.Errorf("a row that is not Keyed has Window %v, want none", got)
			}
		})
	}
}

func TestDeriveAcceptsKeyedBounds(t *testing.T) {
	for name, w := range map[string]time.Duration{"one minute": time.Minute, "seven days": 7 * 24 * time.Hour} {
		t.Run(name, func(t *testing.T) {
			tb := fixtureTable(t)
			row(tb, "jobs.start").Window = w
			row(tb, "jobs.start").Result = "auth.keys"
			if _, err := httpwire.Derive(tb, guardCodes); err != nil {
				t.Errorf("Derive = %v, want nil", err)
			}
		})
	}
}

func TestDeriveRefusesChangesAndEvents(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"changes on a write":     {edit("jobs.start", func(e *httpwire.Endpoint) { e.Changes = []string{"jobs"} }), []string{`row "jobs.start"`, "write"}},
		"changes on mount write": {edit("auth.keyCreate", func(e *httpwire.Endpoint) { e.Changes = []string{"keys"} }), []string{`row "auth.keyCreate"`, "write"}},
		"changes outside kinds":  {edit("jobs.list", func(e *httpwire.Endpoint) { e.Changes = []string{"runs"} }), []string{`row "jobs.list"`, `"runs"`}},
		"changes twice":          {edit("jobs.list", func(e *httpwire.Endpoint) { e.Changes = []string{"jobs", "jobs"} }), []string{`row "jobs.list"`, `"jobs"`, "twice"}},
		"events off a stream":    {edit("jobs.list", func(e *httpwire.Endpoint) { e.Events = []httpwire.Event{{Name: "x", Data: reflect.TypeFor[job]()}} }), []string{`row "jobs.list"`, "Events"}},
		"event name grammar":     {edit("live.stream", func(e *httpwire.Endpoint) { e.Events[0].Name = "Changed" }), []string{`row "live.stream"`, `event "Changed"`}},
		"event name empty":       {edit("live.stream", func(e *httpwire.Endpoint) { e.Events[0].Name = "" }), []string{`row "live.stream"`, `event ""`}},
		"event with no data":     {edit("live.stream", func(e *httpwire.Endpoint) { e.Events[0].Data = nil }), []string{`row "live.stream"`, `event "changed"`, "Data"}},
		"event twice": {
			edit("live.stream", func(e *httpwire.Endpoint) { e.Events = append(e.Events, e.Events[0]) }), []string{`row "live.stream"`, `event "changed"`, "twice"},
		},
	})
}

func TestDeriveRefusesPersonActions(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"unknown action": {edit("archive.export", func(e *httpwire.Endpoint) { e.PersonActions = []string{"wipe_disk"} }), []string{`row "archive.export"`, `"wipe_disk"`}},
		"action on public": {
			edit("auth.sso", func(e *httpwire.Endpoint) { e.PersonActions = []string{httpwire.PersonAdminAccount} }), []string{`row "auth.sso"`, "Public"},
		},
		"action twice": {
			edit("archive.export", func(e *httpwire.Endpoint) {
				e.PersonActions = []string{httpwire.PersonAccountsArchive, httpwire.PersonAccountsArchive}
			}),
			[]string{`row "archive.export"`, "twice"},
		},
	})
	for _, action := range []string{httpwire.PersonSignInMethods, httpwire.PersonTrustedProxies, httpwire.PersonKeyRotate} {
		runRefusals(t, map[string]refusal{
			"whole-row " + action + " not person only": {
				edit("archive.export", func(e *httpwire.Endpoint) { e.PersonActions = []string{action} }), []string{`row "archive.export"`, action, "PersonOnly"},
			},
		})
	}
}

func TestPersonActionSpellings(t *testing.T) {
	got := []string{
		httpwire.PersonSignInMethods, httpwire.PersonTrustedProxies, httpwire.PersonKeyRotate,
		httpwire.PersonAdminAccount, httpwire.PersonSecretRetarget, httpwire.PersonAccountsArchive,
	}
	want := []string{"sign_in_methods", "trusted_proxies", "key_rotate", "admin_account", "secret_retarget", "accounts_archive"}
	if !slices.Equal(got, want) {
		t.Errorf("person actions = %v, want %v", got, want)
	}
}

func TestDeriveRefusesReasons(t *testing.T) {
	reasons := func(name string, r map[httpserver.ErrorCode]httpwire.ReasonSet) func(*httpwire.Table) {
		return edit(name, func(e *httpwire.Endpoint) { e.Reasons = r })
	}
	runRefusals(t, map[string]refusal{
		"code outside union":   {reasons("jobs.list", map[httpserver.ErrorCode]httpwire.ReasonSet{"job_busy": {Listed: []string{"running"}}}), []string{`row "jobs.list"`, `"job_busy"`, "union"}},
		"reason grammar":       {reasons("jobs.start", map[httpserver.ErrorCode]httpwire.ReasonSet{"job_busy": {Listed: []string{"Running"}}}), []string{`row "jobs.start"`, `"Running"`}},
		"reason too long":      {reasons("jobs.start", map[httpserver.ErrorCode]httpwire.ReasonSet{"job_busy": {Listed: []string{strings.Repeat("r", 65)}}}), []string{`row "jobs.start"`, "reason"}},
		"reason twice":         {reasons("jobs.start", map[httpserver.ErrorCode]httpwire.ReasonSet{"job_busy": {Listed: []string{"running", "running"}}}), []string{`row "jobs.start"`, `"running"`, "twice"}},
		"empty closed set":     {reasons("jobs.start", map[httpserver.ErrorCode]httpwire.ReasonSet{"job_busy": {}}), []string{`row "jobs.start"`, `"job_busy"`}},
		"mount code off union": {reasons("auth.keys", map[httpserver.ErrorCode]httpwire.ReasonSet{"auth_bad_request": {Open: true}}), []string{`row "auth.keys"`, `"auth_bad_request"`}},
	})
}

func TestDeriveAcceptsAnOpenReasonSetWithNoList(t *testing.T) {
	tb := fixtureTable(t)
	row(tb, "auth.keyCreate").Reasons = map[httpserver.ErrorCode]httpwire.ReasonSet{"auth_bad_request": {Open: true}}
	got, err := httpwire.Derive(tb, guardCodes)
	if err != nil {
		t.Fatalf("Derive = %v, want nil", err)
	}
	if rs := row(got, "auth.keyCreate").Reasons["auth_bad_request"]; !rs.Open || len(rs.Listed) != 0 {
		t.Errorf("derived reason set = %+v, want open with no list", rs)
	}
}

func TestDeriveRefusesCodesOutsideTheCatalog(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"own code":   {edit("jobs.start", func(e *httpwire.Endpoint) { e.Codes = []httpserver.ErrorCode{"job_gone"} }), []string{`row "jobs.start"`, `"job_gone"`, "catalog"}},
		"mount code": {edit("auth.keys", func(e *httpwire.Endpoint) { e.Codes = []httpserver.ErrorCode{"auth_gone"} }), []string{`row "auth.keys"`, `"auth_gone"`, "catalog"}},
		"guard code": {
			func(tb *httpwire.Table) { tb.Catalog = httpwire.NewCatalog() },
			[]string{`row "jobs.list"`, `"auth_required"`, "catalog"},
		},
	})
}

func TestDeriveRefusesOwners(t *testing.T) {
	owner := func(f func(o *httpwire.Owner)) func(*httpwire.Table) {
		return edit("jobs.get", func(e *httpwire.Endpoint) {
			o := jobOwner()
			f(o)
			e.Owners = []*httpwire.Owner{o}
		})
	}
	runRefusals(t, map[string]refusal{
		"nil owner":     {edit("jobs.get", func(e *httpwire.Endpoint) { e.Owners = []*httpwire.Owner{nil} }), []string{`row "jobs.get"`, "nil"}},
		"unnamed owner": {owner(func(o *httpwire.Owner) { o.Name = "" }), []string{`owner ""`, "Name"}},
		"nil err":       {owner(func(o *httpwire.Owner) { o.Codes[0].Err = nil }), []string{`owner "jobs"`, "nil"}},
		"empty code":    {owner(func(o *httpwire.Owner) { o.Codes[0].Code = "" }), []string{`owner "jobs"`, "ErrJobMissing", "code"}},
		"not a sentinel": {owner(func(o *httpwire.Owner) {
			o.Codes = append(o.Codes, httpwire.OwnerCode{Err: errStray, Code: "job_missing"})
		}), []string{`owner "jobs"`, "stray", "Sentinels"}},
		"unlisted sentinel": {
			owner(func(o *httpwire.Owner) { o.Codes = o.Codes[:1] }), []string{`owner "jobs"`, "ErrRunMissing", "neither"},
		},
		"sentinel in both": {
			owner(func(o *httpwire.Owner) { o.Outside["ErrJobMissing"] = "kept inside" }), []string{`owner "jobs"`, "ErrJobMissing", "both"},
		},
		"sentinel under two codes": {
			owner(func(o *httpwire.Owner) {
				o.Codes = append(o.Codes, httpwire.OwnerCode{Err: errJobMissing, Code: "job_busy"})
			}), []string{`owner "jobs"`, "ErrJobMissing", "twice"},
		},
		"outside name no sentinel": {
			owner(func(o *httpwire.Owner) { o.Outside["ErrGone"] = "never declared" }), []string{`owner "jobs"`, "ErrGone"},
		},
		"outside with no reason": {
			owner(func(o *httpwire.Owner) { o.Outside["ErrLocked"] = "" }), []string{`owner "jobs"`, "ErrLocked", "reason"},
		},
		"nil sentinel value": {
			owner(func(o *httpwire.Owner) { o.Sentinels["ErrNil"] = nil; o.Outside["ErrNil"] = "none" }), []string{`owner "jobs"`, "ErrNil", "nil"},
		},
		"code outside catalog": {
			owner(func(o *httpwire.Owner) { o.Codes[1].Code = "run_missing" }), []string{`owner "jobs"`, `"run_missing"`, "catalog"},
		},
		"owners on a mount row": {
			edit("auth.keys", func(e *httpwire.Endpoint) { e.Owners = []*httpwire.Owner{jobOwner()} }), []string{`row "auth.keys"`, "Owners"},
		},
	})
}

// listErr is an error type == cannot compare.
type listErr struct{ items []string }

func (e listErr) Error() string { return strings.Join(e.items, ",") }

func TestDeriveRefusesAnIncomparableSentinel(t *testing.T) {
	tb := fixtureTable(t)
	o := jobOwner()
	o.Sentinels["ErrList"] = listErr{items: []string{"a"}}
	o.Codes = append(o.Codes, httpwire.OwnerCode{Err: listErr{items: []string{"a"}}, Code: "job_missing"})
	row(tb, "jobs.get").Owners = []*httpwire.Owner{o}
	_, err := httpwire.Derive(tb, guardCodes)
	if err == nil || !strings.Contains(err.Error(), "Codes[2]") || !strings.Contains(err.Error(), "Sentinels") {
		t.Errorf("Derive = %v, want Codes[2] refused as no sentinel errors.Is can match", err)
	}
}

func TestUnionOfARawRead(t *testing.T) {
	tb := fixtureTable(t)
	addRow(httpwire.Endpoint{Name: "files.get", Method: http.MethodGet, Path: "/file", Kind: httpwire.Raw, ContentType: "text/csv", DuringOutage: true})(tb)
	got, err := httpwire.Derive(tb, guardCodes)
	if err != nil {
		t.Fatal(err)
	}
	want := join([]httpserver.ErrorCode{"idempotency_key_unsupported", "upgrade_required", "auth_required"}, everyRow)
	if e := row(got, "files.get").Errors; !slices.Equal(e, want) {
		t.Errorf("Errors = %v, want %v", e, want)
	}
}

func TestDeriveChecksASharedOwnerOnce(t *testing.T) {
	tb := fixtureTable(t)
	o := jobOwner()
	o.Codes[0].Err = nil
	row(tb, "jobs.get").Owners = []*httpwire.Owner{o}
	row(tb, "jobs.list").Owners = []*httpwire.Owner{o}
	_, err := httpwire.Derive(tb, guardCodes)
	if err == nil {
		t.Fatal("Derive accepted an owner with a nil Err")
	}
	if n := strings.Count(err.Error(), "nil Err"); n != 1 {
		t.Errorf("the nil Err is reported %d times, want once: %v", n, err)
	}
}

func TestDeriveRefusesNoneAsADeclaredType(t *testing.T) {
	none := reflect.TypeFor[httpwire.None]()
	runRefusals(t, map[string]refusal{
		"Params":           {edit("jobs.get", func(e *httpwire.Endpoint) { e.Params = none }), []string{`row "jobs.get": Params is httpwire.None`}},
		"Query":            {edit("jobs.list", func(e *httpwire.Endpoint) { e.Query = none }), []string{`row "jobs.list": Query is httpwire.None`}},
		"Request":          {edit("jobs.start", func(e *httpwire.Endpoint) { e.Request = none }), []string{`row "jobs.start": Request is httpwire.None`}},
		"Response":         {edit("jobs.get", func(e *httpwire.Endpoint) { e.Response = none }), []string{`row "jobs.get": Response is httpwire.None`}},
		"a mount Response": {edit("auth.keys", func(e *httpwire.Endpoint) { e.Response = none }), []string{`row "auth.keys": Response is httpwire.None`}},
		"a Success Response": {edit("events.wait", func(e *httpwire.Endpoint) {
			e.Successes = []httpwire.Success{{Status: http.StatusCreated, Response: none}}
		}), []string{`row "events.wait": extra success 201 sets Response to httpwire.None`}},
		"an event's Data": {edit("live.stream", func(e *httpwire.Endpoint) { e.Events[0].Data = none }), []string{`row "live.stream"`, `event "changed"`, "Data"}},
	})
}
