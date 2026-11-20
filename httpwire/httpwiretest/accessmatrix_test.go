package httpwiretest_test

import (
	"context"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/httpwire/httpwiretest"
)

// The refusal codes of the matrix's guard, named as access names them.
const (
	codeAuthRequired    httpserver.ErrorCode = "auth_required"
	codeKeyInvalid      httpserver.ErrorCode = "auth_key_invalid"
	codeSessionRequired httpserver.ErrorCode = "auth_session_required"
	codeKeyRequired     httpserver.ErrorCode = "auth_key_required"
	codeScopeRequired   httpserver.ErrorCode = "auth_scope_required"
	codeRecentRequired  httpserver.ErrorCode = "auth_recent_auth_required"
	codePersonRequired  httpserver.ErrorCode = "person_required"
	codeForbidden       httpserver.ErrorCode = "auth_forbidden"
)

// everyScope is every scope the fixture's rows name.
var everyScope = []string{"admin", "operate", "read"}

// matrixCredentials are the nine credentials, presented through the
// fixture's Principal header.
func matrixCredentials() []httpwiretest.Credential {
	as := func(p string) func(*http.Request) *http.Request {
		return func(r *http.Request) *http.Request {
			c := r.Clone(r.Context())
			if p != "" {
				c.Header.Set("Principal", p)
			}
			return c
		}
	}
	key := func(name string, scopes ...string) httpwiretest.Credential {
		return httpwiretest.Credential{Name: name, Apply: as("key " + strings.Join(scopes, ",")), Kind: httpwiretest.Key, Scopes: scopes}
	}
	return []httpwiretest.Credential{
		{Name: "no credential", Apply: as(""), Kind: httpwiretest.None},
		{Name: "the session", Apply: as("session"), Kind: httpwiretest.Session, Scopes: everyScope},
		{Name: "the stepped-up session", Apply: as("recent"), Kind: httpwiretest.SessionRecent, Scopes: everyScope},
		key("a read key", "read"),
		key("an operate key", "read", "operate"),
		key("an admin key", "read", "operate", "admin"),
		key("a key of an unknown scope", "library:read"),
		{Name: "a revoked key", Apply: as("invalid"), Kind: httpwiretest.KeyInvalid},
		{Name: "a member's session", Apply: as("member"), Kind: httpwiretest.Session},
	}
}

// matrixWant is each row's answer to each credential of matrixCredentials,
// in their order, written from the Access rules: "" runs the row.
var matrixWant = func() map[string][]httpserver.ErrorCode {
	const ar, ki, sr, kr, sc, ra, pr, fb = codeAuthRequired, codeKeyInvalid, codeSessionRequired, codeKeyRequired, codeScopeRequired, codeRecentRequired, codePersonRequired, codeForbidden
	session := []httpserver.ErrorCode{ar, "", "", sr, sr, sr, sr, ki, ""}
	scopedRead := []httpserver.ErrorCode{ar, "", "", "", "", "", sc, ki, fb}
	return map[string][]httpserver.ErrorCode{
		"open.read":     {"", "", "", "", "", "", "", "", ""},
		"me.read":       session,
		"me.save":       {ar, ra, "", sr, sr, sr, sr, ki, ra},
		"security.read": {ar, "", "", pr, pr, pr, pr, ki, ""},
		"security.save": {ar, ra, "", pr, pr, pr, pr, ki, ra},
		"roles.read":    {ar, "", "", pr, pr, pr, pr, ki, fb},
		"feed.read":     {ar, kr, kr, "", "", "", "", ki, kr},
		"exports.read":  {ar, kr, kr, sc, "", "", sc, ki, kr},
		"items.list":    scopedRead,
		"items.get":     scopedRead,
		"items.drop":    {ar, "", "", sc, sc, "", sc, ki, fb},
		"keys.permit":   {ar, ra, "", sc, sc, "", sc, ki, fb},
		"secure.read":   session,
		"live.stream":   session,
	}
}()

type itemID string

func (itemID) MaxBytes() int { return 16 }

type itemParams struct {
	ID itemID `json:"id"`
}

type tick struct {
	N int `json:"n"`
}

// matrixTable has a row of every Access shape, on JSON, Raw and Stream rows.
func matrixTable(t testing.TB) *httpwire.Table {
	t.Helper()
	c := httpwire.NewCatalog()
	for code, status := range map[httpserver.ErrorCode]int{
		codeAuthRequired: http.StatusUnauthorized, codeKeyInvalid: http.StatusUnauthorized,
		codeSessionRequired: http.StatusForbidden, codeKeyRequired: http.StatusForbidden,
		codeScopeRequired: http.StatusForbidden, codeRecentRequired: http.StatusForbidden, codeForbidden: http.StatusForbidden,
	} {
		if err := c.Add(code, httpserver.CodeSpec{Status: status, Remediation: "Sign in."}); err != nil {
			t.Fatal(err)
		}
	}
	row := func(name, method, path, summary string, a httpwire.Access) httpwire.Endpoint {
		return httpwire.Endpoint{Name: name, Method: method, Path: path, Summary: summary, Description: summary + ".", Access: a}
	}
	session := httpwire.Access{Class: httpwire.Session}
	feed := row("feed.read", http.MethodGet, "/feed", "Read the feed", httpwire.Access{Class: httpwire.Key})
	feed.Kind, feed.ContentTypes = httpwire.Raw, []string{"text/plain"}
	get := row("items.get", http.MethodGet, "/items/{id}", "Read an item", httpwire.Access{Class: httpwire.Scoped, Scope: "read"})
	get.Params = reflect.TypeFor[itemParams]()
	live := row("live.stream", http.MethodGet, "/live", "Stream the live events", httpwire.Access{Class: httpwire.Session, Passive: true})
	live.Kind, live.Events = httpwire.Stream, []httpwire.Event{{Name: "tick", Data: reflect.TypeFor[tick]()}}
	return &httpwire.Table{Catalog: c, Prefix: "/api", Endpoints: []httpwire.Endpoint{
		row("open.read", http.MethodGet, "/open", "Read the open page", httpwire.Access{Class: httpwire.Public}),
		row("me.read", http.MethodGet, "/me", "Read me", session),
		row("me.save", http.MethodPost, "/me", "Save me", httpwire.Access{Class: httpwire.Session, RecentAuth: true}),
		row("security.read", http.MethodGet, "/security", "Read the security settings", httpwire.Access{Class: httpwire.Session, PersonOnly: true}),
		row("security.save", http.MethodPost, "/security", "Save the security settings", httpwire.Access{Class: httpwire.Session, PersonOnly: true, RecentAuth: true}),
		row("roles.read", http.MethodGet, "/roles", "Read the roles", httpwire.Access{Class: httpwire.Session, PersonOnly: true, Scope: "admin"}),
		feed,
		row("exports.read", http.MethodGet, "/exports", "Read the exports", httpwire.Access{Class: httpwire.Key, Scope: "operate"}),
		row("items.list", http.MethodGet, "/items", "List the items", httpwire.Access{Class: httpwire.Scoped, Scope: "read"}),
		get,
		row("items.drop", http.MethodDelete, "/items", "Drop the items", httpwire.Access{Class: httpwire.Scoped, Scope: "admin"}),
		row("keys.permit", http.MethodPost, "/keys/permit", "Permit a key", httpwire.Access{Class: httpwire.Scoped, Scope: "admin", RecentAuth: true}),
		row("secure.read", http.MethodGet, "/secure", "Read over HTTPS", httpwire.Access{Class: httpwire.Session, HTTPS: true}),
		live,
	}}
}

func matrixOptions(t testing.TB) []httpwire.Option {
	t.Helper()
	doc, err := os.ReadFile("testdata/accessmatrix.json")
	if err != nil {
		t.Fatal(err)
	}
	principal := func(r *http.Request) string {
		if p := r.Header.Get("Principal"); p != "" {
			return "p:" + p
		}
		return ""
	}
	l, err := httpserver.NewKeyedRateLimiter(httpserver.KeyedRate{})
	if err != nil {
		t.Fatal(err)
	}
	keyOf := func(r *http.Request) (string, bool) {
		p := r.Header.Get("Principal")
		return p, strings.HasPrefix(p, "key ")
	}
	return []httpwire.Option{
		httpwire.WithSchema(doc), httpwire.WithCredential(principal), httpwire.WithCursorOwner(principal),
		httpwire.WithKeyRate(l, keyOf),
	}
}

// matrixGuard answers each row from matrixWant, so AccessMatrix passes it
// only when the answers it derives from Access are the ones written there.
// Each other field breaks it one way.
type matrixGuard struct {
	want         map[string][]httpserver.ErrorCode
	served       *atomic.Int64
	swallow      string // a row it answers itself, never running the row
	catalog      *httpwire.Catalog
	noChallenge  bool // no WWW-Authenticate on a 401
	noScopeError bool // no RFC 6750 error="insufficient_scope" on a scope refusal
	anonymous    bool // Shape answers anonymous for every request
	fullRole     bool // Shape answers every scope for every session
}

var principalNames = []string{"", "session", "recent", "key read", "key read,operate", "key read,operate,admin", "key library:read", "invalid", "member"}

func (g matrixGuard) Wrap(ep *httpwire.Endpoint, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.served.Add(1)
		code := g.want[ep.Name][slices.Index(principalNames, r.Header.Get("Principal"))]
		switch {
		case code == "" && ep.Name == g.swallow:
			w.WriteHeader(http.StatusNoContent)
			return
		case code == "":
			next.ServeHTTP(w, r)
			return
		}
		challenge := `Bearer realm="fixture"`
		switch code {
		case codeKeyInvalid:
			challenge += `, error="invalid_token"`
		case codeScopeRequired:
			if !g.noScopeError {
				challenge += `, error="insufficient_scope", scope="` + ep.Access.Scope + `"`
			}
		}
		if code == codeScopeRequired || code == codeAuthRequired || code == codeKeyInvalid {
			if !g.noChallenge || code == codeScopeRequired {
				w.Header().Set("WWW-Authenticate", challenge)
			}
		}
		g.catalog.WriteError(w, r, &httpwire.Error{Code: code, Message: "refused"})
	})
}

func (matrixGuard) Codes(a httpwire.Access) []httpserver.ErrorCode {
	if a.Class == httpwire.Public {
		return nil
	}
	return []httpserver.ErrorCode{codeAuthRequired, codeKeyInvalid, codeSessionRequired, codeKeyRequired, codeScopeRequired, codeRecentRequired, codePersonRequired, codeForbidden}
}

func (g matrixGuard) Shape(r *http.Request) httpwire.Shape {
	p := r.Header.Get("Principal")
	switch {
	case g.anonymous:
	case p == "session" || p == "recent" || p == "member" && g.fullRole:
		return httpwire.Shape{Principal: httpwire.ShapeSession, Scopes: everyScope}
	case p == "member":
		return httpwire.Shape{Principal: httpwire.ShapeSession}
	case strings.HasPrefix(p, "key "):
		s := strings.Split(strings.TrimPrefix(p, "key "), ",")
		slices.Sort(s)
		return httpwire.Shape{Principal: httpwire.ShapeKey, Scopes: s}
	}
	return httpwire.Shape{Principal: httpwire.ShapeAnonymous}
}

func (matrixGuard) Shapes() []httpwire.Shape {
	return []httpwire.Shape{{Principal: httpwire.ShapeAnonymous}, {Principal: httpwire.ShapeSession, Scopes: everyScope}, {Principal: httpwire.ShapeSession}}
}
func (matrixGuard) Admits(httpwire.Shape, httpwire.Access) bool { return true }
func (matrixGuard) Person(context.Context) error                { return nil }

func newMatrixGuard(tb *httpwire.Table) matrixGuard {
	want := map[string][]httpserver.ErrorCode{}
	for name, cells := range matrixWant {
		want[name] = slices.Clone(cells)
	}
	return matrixGuard{want: want, served: &atomic.Int64{}, catalog: tb.Catalog}
}

func TestAccessMatrixPassesAGuardThatFollowsAccess(t *testing.T) {
	tb := matrixTable(t)
	g := newMatrixGuard(tb)
	creds := matrixCredentials()
	r := record(t, func(rt testing.TB) { httpwiretest.AccessMatrix(rt, tb, g, creds, matrixOptions(t)...) })
	for _, s := range r.failures {
		t.Errorf("AccessMatrix failed a guard that follows Access: %s", s)
	}
	if got, want := g.served.Load(), int64(len(tb.Endpoints)*len(creds)); got != want {
		t.Errorf("the guard served %d requests, want every row by every credential, %d", got, want)
	}
}

// cellOf is the index of the credential named name in matrixCredentials.
func cellOf(t *testing.T, name string) int {
	t.Helper()
	i := slices.IndexFunc(matrixCredentials(), func(c httpwiretest.Credential) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("no credential %q", name)
	}
	return i
}

func TestAccessMatrixFailsKeyOnPersonOnly(t *testing.T) {
	tb := matrixTable(t)
	g := newMatrixGuard(tb)
	g.want["security.read"][cellOf(t, "an admin key")] = ""
	r := record(t, func(rt testing.TB) { httpwiretest.AccessMatrix(rt, tb, g, matrixCredentials(), matrixOptions(t)...) })
	if !r.failed(`"security.read"`, "an admin key", string(codePersonRequired)) {
		t.Errorf("AccessMatrix passed a guard that admits a key on a PersonOnly row: %q", r.failures)
	}
	if len(r.failures) != 1 {
		t.Errorf("%d failures, want the one cell: %q", len(r.failures), r.failures)
	}
}

func TestAccessMatrixFailsUnservedRow(t *testing.T) {
	tb := matrixTable(t)
	g := newMatrixGuard(tb)
	g.swallow = "items.get"
	r := record(t, func(rt testing.TB) { httpwiretest.AccessMatrix(rt, tb, g, matrixCredentials(), matrixOptions(t)...) })
	if !r.failed(`"items.get"`, "the session", "not served") {
		t.Errorf("AccessMatrix passed a row its guard answers without running it: %q", r.failures)
	}
	for _, s := range r.failures {
		if !strings.Contains(s, `"items.get"`) {
			t.Errorf("a failure outside the unserved row: %s", s)
		}
	}
}

// TestAccessMatrixFailsAGuardOffItsAccess breaks the guard one cell or one
// header at a time; each must fail naming the row, the credential and the
// answer Access gives.
func TestAccessMatrixFailsAGuardOffItsAccess(t *testing.T) {
	for name, tc := range map[string]struct {
		cell string               // "row/credential"
		code httpserver.ErrorCode // the guard's answer there
		edit func(*matrixGuard)
		want []string
	}{
		"no credential admitted":            {cell: "me.read/no credential", want: []string{`"me.read"`, "no credential", string(codeAuthRequired)}},
		"a revoked key answered as missing": {cell: "me.read/a revoked key", code: codeAuthRequired, want: []string{`"me.read"`, "a revoked key", string(codeKeyInvalid)}},
		"a key admitted on a Session row":   {cell: "me.read/a read key", want: []string{`"me.read"`, "a read key", string(codeSessionRequired)}},
		"a session past its window stepping up": {
			cell: "me.save/the session", want: []string{`"me.save"`, "the session", string(codeRecentRequired)},
		},
		"a session admitted on a Key row": {cell: "feed.read/the session", want: []string{`"feed.read"`, "the session", string(codeKeyRequired)}},
		"a key below the row's scope":     {cell: "exports.read/a read key", want: []string{`"exports.read"`, "a read key", string(codeScopeRequired)}},
		"an unknown scope on a Scoped row": {
			cell: "items.list/a key of an unknown scope", want: []string{`"items.list"`, "a key of an unknown scope", string(codeScopeRequired)},
		},
		"the session refused on a Scoped row": {
			cell: "items.drop/the session", code: codeScopeRequired, want: []string{`"items.drop"`, "the session", "the row to run"},
		},
		"a key on a PersonOnly row answered as a session row": {
			cell: "roles.read/an admin key", code: codeSessionRequired, want: []string{`"roles.read"`, "an admin key", string(codePersonRequired)},
		},
		"a member's session admitted on a Scoped row": {
			cell: "items.drop/a member's session", want: []string{`"items.drop"`, "a member's session", string(codeForbidden)},
		},
		"a member's session admitted on a PersonOnly row with a Scope": {
			cell: "roles.read/a member's session", want: []string{`"roles.read"`, "a member's session", string(codeForbidden)},
		},
		"a member's session asked to step up before its scope is checked": {
			cell: "keys.permit/a member's session", code: codeRecentRequired, want: []string{`"keys.permit"`, "a member's session", string(codeForbidden)},
		},
		"a member's session asked for a key before its scope is checked": {
			cell: "exports.read/a member's session", code: codeForbidden, want: []string{`"exports.read"`, "a member's session", string(codeKeyRequired)},
		},
		"a session holding every scope refused on a Scoped row": {
			cell: "items.drop/the session", code: codeForbidden, want: []string{`"items.drop"`, "the session", "the row to run"},
		},
		"a member's session run as a session of another role": {
			edit: func(g *matrixGuard) { g.fullRole = true }, want: []string{`"me.read"`, "a member's session", "principal"},
		},
		"a public row refusing": {cell: "open.read/a revoked key", code: codeKeyInvalid, want: []string{`"open.read"`, "a revoked key", "the row to run"}},
		"no WWW-Authenticate on a 401": {
			edit: func(g *matrixGuard) { g.noChallenge = true }, want: []string{`"me.read"`, "no credential", "WWW-Authenticate"},
		},
		"no insufficient_scope on a scope refusal": {
			edit: func(g *matrixGuard) { g.noScopeError = true }, want: []string{`"items.drop"`, "a read key", "insufficient_scope"},
		},
		"the row run as another principal": {
			edit: func(g *matrixGuard) { g.anonymous = true }, want: []string{`"items.list"`, "a read key", "principal"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			tb := matrixTable(t)
			g := newMatrixGuard(tb)
			if tc.cell != "" {
				row, cred, _ := strings.Cut(tc.cell, "/")
				g.want[row][cellOf(t, cred)] = tc.code
			}
			if tc.edit != nil {
				tc.edit(&g)
			}
			r := record(t, func(rt testing.TB) { httpwiretest.AccessMatrix(rt, tb, g, matrixCredentials(), matrixOptions(t)...) })
			if !r.failed(tc.want...) {
				t.Errorf("no failure holds %q: %q", tc.want, r.failures)
			}
		})
	}
}

func TestAccessMatrixFailsATableItCannotServe(t *testing.T) {
	tb := matrixTable(t)
	tb.Endpoints[1].Access.Class = 0
	r := record(t, func(rt testing.TB) {
		httpwiretest.AccessMatrix(rt, tb, newMatrixGuard(tb), matrixCredentials(), matrixOptions(t)...)
	})
	if !r.fatal || !r.failed("New") {
		t.Errorf("AccessMatrix over a table New refuses: %q, fatal %v", r.failures, r.fatal)
	}
}
