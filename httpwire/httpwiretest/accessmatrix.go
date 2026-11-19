package httpwiretest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/httpwire/internal/wirelink"
)

// CredentialKind is what a Credential presents.
type CredentialKind uint8

// The credential kinds.
const (
	None          CredentialKind = iota + 1 // no credential
	Session                                 // a session whose role grants every scope, past its recent-authentication window
	SessionRecent                           // the same session, authenticated recently
	Key                                     // a valid API key
	KeyInvalid                              // a revoked or expired key
)

// Credential is one principal AccessMatrix serves every row as.
type Credential struct {
	// Apply answers a copy of the request carrying the credential.
	Apply func(*http.Request) *http.Request
	Name  string
	// Scopes are every scope a Key passes, those it implies included.
	Scopes []string
	Kind   CredentialKind
}

// The refusals AccessMatrix expects, as the access module's guard names
// them.
const (
	codeAuthRequired    = "auth_required"
	codeKeyInvalid      = "auth_key_invalid"
	codeSessionRequired = "auth_session_required"
	codeKeyRequired     = "auth_key_required"
	codeScopeRequired   = "auth_scope_required"
	codeRecentRequired  = "auth_recent_auth_required"
	codePersonRequired  = "person_required"
)

// AccessMatrix builds an API of table with guard and opts, every row of
// table.Endpoints bound to a stub, and serves each row once per credential
// over HTTPS. It fails t, naming the row and the credential, wherever the
// answer is not the code the row's Access gives (docs/testing.md lists
// them) or the row does not run as the credential's principal where
// Access admits it; a row the guard answers without running it fails as
// not served. A table New refuses fails t at once.
func AccessMatrix(t testing.TB, table *httpwire.Table, guard httpwire.Guard, creds []Credential, opts ...httpwire.Option) {
	t.Helper()
	switch {
	case table == nil || guard == nil:
		t.Fatal("httpwiretest: AccessMatrix needs a table and a guard")
		return
	case len(creds) == 0:
		t.Fatal("httpwiretest: AccessMatrix needs credentials")
		return
	}
	for _, c := range creds {
		if c.Apply == nil || c.Kind < None || c.Kind > KeyInvalid {
			t.Fatalf("httpwiretest: AccessMatrix: credential %q has no Apply or no Kind", c.Name)
			return
		}
	}
	m := &matrix{t: t}
	bs := make([]httpwire.Binding, 0, len(table.Endpoints))
	for i := range table.Endpoints {
		b, _ := wirelink.Stub(&table.Endpoints[i]).(httpwire.Binding)
		bs = append(bs, b)
	}
	api, err := httpwire.New(table, bs, append(slices.Clone(opts), httpwire.WithGuard(markGuard{Guard: guard, m: m}))...)
	if err != nil {
		t.Fatalf("httpwiretest: AccessMatrix: New: %v", err)
		return
	}
	m.api, m.catalog, m.prefix = api, table.Catalog, table.Prefix
	tb := api.Table()
	for i := range tb.Endpoints {
		for _, c := range creds {
			m.cell(&tb.Endpoints[i], c)
		}
	}
}

type matrix struct {
	t       testing.TB
	api     *httpwire.API
	catalog *httpwire.Catalog
	ran     *ranRow
	prefix  string // set by the stub under the guard, reset before each request
}

type ranRow struct {
	row   string
	shape httpwire.Shape
}

// markGuard puts the matrix's stub directly under the app's guard, so a
// row counts as run the moment its guard admits it, before any decoding.
type markGuard struct {
	httpwire.Guard
	m *matrix
}

func (g markGuard) Wrap(ep *httpwire.Endpoint, _ http.Handler) http.Handler {
	return g.Guard.Wrap(ep, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.m.ran = &ranRow{row: ep.Name, shape: g.Shape(r)}
		w.WriteHeader(http.StatusNoContent)
	}))
}

var wildcard = regexp.MustCompile(`\{[^}]*\}`)

// cell serves ep once as c and checks the answer.
func (m *matrix) cell(ep *httpwire.Endpoint, c Credential) {
	m.t.Helper()
	path := wildcard.ReplaceAllStringFunc(m.prefix+ep.Path, func(w string) string {
		if w == "{$}" {
			return ""
		}
		return "x"
	})
	r := c.Apply(httptest.NewRequestWithContext(m.t.Context(), ep.Method, "https://example.com"+path, nil))
	m.ran = nil
	rec := httptest.NewRecorder()
	m.api.ServeHTTP(rec, r)
	at := fmt.Sprintf("httpwiretest: row %q: %s", ep.Name, c.Name)
	want := expected(ep.Access, c)
	if want == "" {
		m.ranAs(at, ep, c, rec)
		return
	}
	if m.ran != nil {
		m.t.Errorf("%s ran the row, want %s", at, want)
		return
	}
	spec, ok := m.catalog.Lookup(want)
	if !ok {
		m.t.Errorf("%s: the catalog has no %s, the answer the row's Access gives", at, want)
		return
	}
	got := answerCode(r, rec)
	if rec.Code != spec.Status || r.Method != http.MethodHead && got != want {
		m.t.Errorf("%s answered %d %s, want %d %s", at, rec.Code, got, spec.Status, want)
		return
	}
	challenge := rec.Header().Get("WWW-Authenticate")
	switch {
	case rec.Code == http.StatusUnauthorized && challenge == "":
		m.t.Errorf("%s answered 401 with no WWW-Authenticate (RFC 9110 section 15.5.2)", at)
	case want == codeScopeRequired && !strings.Contains(challenge, `error="insufficient_scope"`):
		m.t.Errorf("%s answered %s without error=\"insufficient_scope\" in WWW-Authenticate (RFC 6750 section 3.1)", at, want)
	}
}

// ranAs checks a cell whose credential the row's Access admits.
func (m *matrix) ranAs(at string, ep *httpwire.Endpoint, c Credential, rec *httptest.ResponseRecorder) {
	m.t.Helper()
	switch {
	case m.ran == nil && rec.Code >= http.StatusBadRequest:
		m.t.Errorf("%s answered %d %s, want the row to run", at, rec.Code, answerCode(nil, rec))
	case m.ran == nil || m.ran.row != ep.Name:
		m.t.Errorf("%s was not served: the guard answered %d without running the row", at, rec.Code)
	case !sameShape(m.ran.shape, c):
		m.t.Errorf("%s ran the row as principal %s %q, want the credential's", at, shapeNames[m.ran.shape.Principal], m.ran.shape.Scopes)
	}
}

// expected is the code a's rules give c, "" when the row runs. Each 401
// carries WWW-Authenticate, and auth_scope_required its RFC 6750
// error="insufficient_scope".
func expected(a httpwire.Access, c Credential) string {
	if a.Class == httpwire.Public {
		return ""
	}
	switch c.Kind {
	case None:
		return codeAuthRequired
	case KeyInvalid:
		return codeKeyInvalid
	case Key:
		switch {
		case a.PersonOnly:
			return codePersonRequired
		case a.Class == httpwire.Session:
			return codeSessionRequired
		case a.Scope != "" && !slices.Contains(c.Scopes, a.Scope):
			return codeScopeRequired
		}
	case Session, SessionRecent:
		switch {
		case a.Class == httpwire.Key:
			return codeKeyRequired
		case a.RecentAuth && c.Kind == Session:
			return codeRecentRequired
		}
	}
	return ""
}

// sameShape reports whether s is c's principal: anonymous with no valid
// credential, a session, or a key holding exactly c's scopes.
func sameShape(s httpwire.Shape, c Credential) bool {
	switch c.Kind {
	case Session, SessionRecent:
		return s.Principal == httpwire.ShapeSession
	case Key:
		return s.Principal == httpwire.ShapeKey && slices.Equal(s.Scopes, slices.Sorted(slices.Values(c.Scopes)))
	}
	return s.Principal == httpwire.ShapeAnonymous
}

// answerCode is an error answer's code, "" for one with no envelope (a
// HEAD answer's included).
func answerCode(r *http.Request, rec *httptest.ResponseRecorder) string {
	if r != nil && r.Method == http.MethodHead {
		return ""
	}
	env, err := httpwire.DecodeEnvelope(rec.Body.Bytes())
	if err != nil {
		return ""
	}
	return env.Code
}

var shapeNames = map[httpwire.ShapeKind]string{httpwire.ShapeAnonymous: "anonymous", httpwire.ShapeSession: "session", httpwire.ShapeKey: "key"}
