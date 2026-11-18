package httpwire_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/httpwire"
)

// hPrincipal names the credential a matrix request presents: none, a
// session, a stepped-up session, a key of one scope, a revoked key, or one
// whose account store is down.
const hPrincipal = "Principal"

var matrixPrincipals = []string{"", "session", "recent", "key:read", "key:operate", "key:admin", "revoked", "store-down"}

var scopeRank = map[string]int{"read": 1, "operate": 2, "admin": 3}

// verdict is what a guard answers a principal on a row of Access a: a
// refusal code with its headers, or "" to admit.
type verdict struct {
	header map[string]string
	code   string
	retry  time.Duration
}

func verdictOf(a httpwire.Access, p string) verdict {
	challenge := func(v string) map[string]string {
		return map[string]string{"WWW-Authenticate": v, "Link": `</api/openapi.json>; rel="service-desc"`}
	}
	key, isKey := "", len(p) > 4 && p[:4] == "key:"
	if isKey {
		key = p[4:]
	}
	switch {
	case a.Class == httpwire.Public:
		return verdict{}
	case p == "store-down":
		return verdict{code: "accounts_unavailable", retry: 5 * time.Second}
	case p == "":
		return verdict{code: "auth_required", header: challenge(`Bearer realm="fixture"`)}
	case p == "revoked":
		return verdict{code: "auth_required", header: challenge(`Bearer realm="fixture", error="invalid_token"`)}
	case isKey && (a.Class == httpwire.Session || a.Class == httpwire.Recovery):
		return verdict{code: "auth_required", header: challenge(`Bearer realm="fixture"`)}
	case isKey && scopeRank[key] < scopeRank[a.Scope]:
		return verdict{code: "auth_forbidden", header: challenge(`Bearer realm="fixture", error="insufficient_scope", scope="` + a.Scope + `"`)}
	case a.RecentAuth && p != "recent":
		return verdict{code: "auth_recent_auth_required"}
	}
	return verdict{}
}

type admittedAs struct{}

// matrixGuard decides each request from the Access of the row Wrap was
// given, so a row guarded under another row's Access answers otherwise.
type matrixGuard struct {
	fixtureGuard
	catalog *httpwire.Catalog
	mu      *sync.Mutex
	wrapped map[string]httpwire.Access
}

func (g matrixGuard) Wrap(ep *httpwire.Endpoint, next http.Handler) http.Handler {
	a := ep.Access
	g.mu.Lock()
	g.wrapped[ep.Name] = a
	g.mu.Unlock()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.Header.Get(hPrincipal)
		v := verdictOf(a, p)
		if v.code != "" {
			for k, val := range v.header {
				w.Header().Set(k, val)
			}
			g.catalog.WriteError(w, r, &httpwire.Error{Code: v.code, RetryAfter: v.retry})
			return
		}
		w.Header().Set("Admitted-As", p)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), admittedAs{}, p)))
	})
}

// TestGuardMatrix serves every own row of the fixture table to each
// principal: the guard is handed each row's own Access once, a refusal
// keeps the guard's status and headers with the code in the row's union at
// the catalog's status, and an admitted request reaches its handler with
// the principal the guard admitted. Mount rows guard themselves.
func TestGuardMatrix(t *testing.T) {
	c := newCase(t)
	g := matrixGuard{catalog: c.tb.Catalog, mu: &sync.Mutex{}, wrapped: map[string]httpwire.Access{}}
	c.opts["guard"] = httpwire.WithGuard(g)
	c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(ctx context.Context, _ httpwire.Call[jobParams, noQ, noB]) (job, error) {
		p, _ := ctx.Value(admittedAs{}).(string)
		return job{ID: "as " + p}, nil
	})
	api := c.mustBuild(t)
	derived := api.Table()

	own := map[string]httpwire.Access{}
	for _, ep := range derived.Endpoints {
		own[ep.Name] = ep.Access
	}
	if len(g.wrapped) != len(own) {
		t.Errorf("the guard wrapped %d rows, want the table's %d own rows", len(g.wrapped), len(own))
	}
	for name, a := range own {
		if got, ok := g.wrapped[name]; !ok || got != a {
			t.Errorf("row %s wrapped with %+v (wrapped %v), want its own %+v", name, got, ok, a)
		}
	}
	for _, m := range derived.Mounts {
		for _, ep := range m.Rows {
			if _, ok := g.wrapped[ep.Name]; ok {
				t.Errorf("mount row %s was wrapped by the app's guard", ep.Name)
			}
		}
	}

	for _, ep := range derived.Endpoints {
		for i, p := range matrixPrincipals {
			t.Run(ep.Name+" as "+strconv.Quote(p), func(t *testing.T) {
				tg := driveTargets[ep.Name]
				header := slices.Clone(tg.header)
				if ep.Keyed {
					header = []string{"Idempotency-Key", `"matrix-` + strconv.Itoa(i) + `"`}
				}
				header = append(header, hPrincipal, p)
				rec := answer(api, request(tg.method, tg.path, tg.body, header...))
				if rec.Header().Get("Wire-Version") == "" || rec.Header().Get("X-Request-Id") == "" {
					t.Errorf("answer %d lacks Wire-Version or X-Request-Id", rec.Code)
				}
				want := verdictOf(ep.Access, p)
				if want.code == "" {
					checkMatrixAdmitted(t, &ep, p, rec)
					return
				}
				env := envelopeOf(t, rec)
				spec, _ := derived.Catalog.Lookup(want.code)
				if env.Code != want.code || rec.Code != spec.Status || !slices.Contains(ep.Errors, want.code) {
					t.Errorf("answer %d %s, want %d %s inside the row's union %v", rec.Code, env.Code, spec.Status, want.code, ep.Errors)
				}
				for k, v := range want.header {
					if got := rec.Header().Get(k); got != v {
						t.Errorf("%s = %q, want the guard's %q", k, got, v)
					}
				}
				if want.retry > 0 && (rec.Header().Get("Retry-After") != "5" || env.RetryAfterMS != 5000) {
					t.Errorf("Retry-After %q, retry_after_ms %d; want 5 and 5000", rec.Header().Get("Retry-After"), env.RetryAfterMS)
				}
				if rec.Header().Get("Admitted-As") != "" {
					t.Error("a refused request reached past the guard")
				}
			})
		}
	}
}

func checkMatrixAdmitted(t *testing.T, ep *httpwire.Endpoint, p string, rec *httptest.ResponseRecorder) {
	t.Helper()
	res := rec.Result()
	defer res.Body.Close()
	// The fixture's Redirect handler answers 302; a JSON row with no
	// Response answers 204.
	primary := ep.Status
	switch {
	case primary != 0:
	case ep.Kind == httpwire.Redirect:
		primary = http.StatusFound
	case ep.Kind == httpwire.JSON && ep.Response == nil:
		primary = http.StatusNoContent
	default:
		primary = http.StatusOK
	}
	if res.StatusCode != primary {
		t.Errorf("answer %d, want the row's %d", res.StatusCode, primary)
	}
	if got := res.Header.Get("Admitted-As"); got != p {
		t.Errorf("Admitted-As = %q, want %q", got, p)
	}
	if ep.Name != "jobs.get" {
		return
	}
	var buf [64]byte
	n, _ := res.Body.Read(buf[:])
	if want := `{"id":"as ` + p + `"}`; string(buf[:n]) != want && string(buf[:n]) != want+"\n" {
		t.Errorf("body %q, want the handler to read the admitted %q", buf[:n], p)
	}
}
