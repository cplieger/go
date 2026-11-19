package httpwire_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

type principalKey struct{}

// personGuard admits every request, keeping its Principal header for
// Person: a fresh session and the socket are persons, a stale session
// needs a step-up, and a key never is.
type personGuard struct{ fixtureGuard }

func (personGuard) Wrap(_ *httpwire.Endpoint, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, r.Header.Get("Principal"))))
	})
}

func (personGuard) Person(ctx context.Context) error {
	switch p, _ := ctx.Value(principalKey{}).(string); p {
	case "fresh session", "socket":
		return nil
	case "stale session":
		return &httpwire.Error{Code: "auth_recent_auth_required", Message: "sign in again"}
	case "key":
		return &httpwire.Error{Code: "person_required", Message: "an API key cannot do this"}
	case "stray code":
		return &httpwire.Error{Code: "job_busy", Message: "busy"}
	}
	return errors.New("the guard could not read the principal")
}

// personTable holds a Raw row and a JSON row whose handlers decide a
// person action inside, and a whole-row PersonOnly row.
func personTable(t *testing.T) *httpwire.Table {
	t.Helper()
	return &httpwire.Table{
		Prefix: "/api", Catalog: fixtureCatalog(t),
		Endpoints: []httpwire.Endpoint{
			{
				Name: "archive.export", Method: http.MethodPost, Path: "/archive", Summary: "Export an archive", Description: "Export an archive.",
				Kind: httpwire.Raw, ContentType: "application/zip", Access: session, PersonActions: []string{httpwire.PersonAccountsArchive},
			},
			{
				Name: "settings.save", Method: http.MethodPost, Path: "/settings", Summary: "Save settings", Description: "Save settings.",
				Access: session, PersonActions: []string{httpwire.PersonSecretRetarget},
			},
			{
				Name: "keys.rotate", Method: http.MethodPost, Path: "/keys/rotate", Summary: "Rotate a key", Description: "Rotate a key.",
				Access: httpwire.Access{Class: httpwire.Session, PersonOnly: true, RecentAuth: true}, PersonActions: []string{httpwire.PersonKeyRotate},
			},
		},
	}
}

type personCase struct {
	api    *httpwire.API
	action string
	got    []error
}

func newPersonCase(t *testing.T, opts ...httpwire.Option) *personCase {
	t.Helper()
	doc, err := os.ReadFile("testdata/openapi/person.json")
	if err != nil {
		t.Fatal(err)
	}
	pc := &personCase{}
	tb := personTable(t)
	inRow := func(ctx context.Context, _ httpwire.Call[noP, noQ, noB, noH]) (httpwire.None, error) {
		err := httpwire.RequirePerson(ctx, pc.action)
		pc.got = append(pc.got, err)
		return httpwire.None{}, err
	}
	pc.api, err = httpwire.New(tb, []httpwire.Binding{
		httpwire.BindRaw("archive.export", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			err := httpwire.RequirePerson(r.Context(), pc.action)
			pc.got = append(pc.got, err)
			if e, ok := errors.AsType[*httpwire.Error](err); ok {
				tb.Catalog.WriteError(w, r, e)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK"))
		})),
		httpwire.Bind("settings.save", inRow),
		httpwire.Bind("keys.rotate", inRow),
	}, append([]httpwire.Option{
		httpwire.WithSchema(doc), httpwire.WithGuard(personGuard{}),
		httpwire.WithCredential(func(*http.Request) string { return "s:x" }),
		httpwire.WithCursorOwner(func(*http.Request) string { return "u:x" }),
	}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return pc
}

func TestRequirePersonRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		principal, path, action string
		status                  int
		code                    httpserver.ErrorCode
		reason                  string
		logged                  bool
	}{
		"a key":                         {"key", "/api/settings", httpwire.PersonSecretRetarget, 403, "person_required", "secret_retarget", false},
		"a key on a Raw row":            {"key", "/api/archive", httpwire.PersonAccountsArchive, 403, "person_required", "accounts_archive", false},
		"a session past the window":     {"stale session", "/api/settings", httpwire.PersonSecretRetarget, 403, "auth_recent_auth_required", "", false},
		"a stepped-up session":          {"fresh session", "/api/settings", httpwire.PersonSecretRetarget, 204, "", "", false},
		"the admin socket's peer":       {"socket", "/api/settings", httpwire.PersonSecretRetarget, 204, "", "", false},
		"a stepped-up session, Raw row": {"fresh session", "/api/archive", httpwire.PersonAccountsArchive, 200, "", "", false},
		"an action the row lacks":       {"fresh session", "/api/settings", httpwire.PersonAdminAccount, 500, "internal_error", "", true},
		"a whole-row action":            {"fresh session", "/api/keys/rotate", httpwire.PersonKeyRotate, 500, "internal_error", "", true},
		"no action of the six":          {"fresh session", "/api/settings", "secret", 500, "internal_error", "", true},
		"a guard error":                 {"nobody", "/api/settings", httpwire.PersonSecretRetarget, 500, "internal_error", "", true},
		"a guard code off the union":    {"stray code", "/api/settings", httpwire.PersonSecretRetarget, 500, "internal_error", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			sink := &logSink{}
			pc := newPersonCase(t, httpwire.WithLogger(slog.New(sink)))
			pc.action = tc.action
			rec := answer(pc.api, request(http.MethodPost, tc.path, "", "Principal", tc.principal))
			if rec.Code != tc.status {
				t.Fatalf("answered %d %q, want %d", rec.Code, rec.Body.String(), tc.status)
			}
			if tc.code != "" {
				if env := envelopeOf(t, rec); env.Code != tc.code || env.Reason != tc.reason {
					t.Errorf("answered %q reason %q, want %q reason %q", env.Code, env.Reason, tc.code, tc.reason)
				}
			}
			if len(pc.got) != 1 {
				t.Fatalf("RequirePerson ran %d times, want once", len(pc.got))
			}
			if err := pc.got[0]; (err == nil) != (tc.code == "") || err != nil && reflect.TypeOf(err) != reflect.TypeFor[*httpwire.Error]() {
				t.Errorf("RequirePerson = %#v, want nil or an *Error", err)
			}
			if got := sink.has("ERROR", "RequirePerson"); got != tc.logged {
				t.Errorf("logged at ERROR = %v, want %v: %q", got, tc.logged, sink.all())
			}
		})
	}
}

func TestRequirePersonOutsideARequestIsInternalError(t *testing.T) {
	sink := &logSink{}
	old := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(old) })
	err := httpwire.RequirePerson(t.Context(), httpwire.PersonSecretRetarget)
	if e, ok := errors.AsType[*httpwire.Error](err); !ok || e.Code != "internal_error" {
		t.Errorf("RequirePerson = %v, want internal_error", err)
	}
	if !sink.has("ERROR", "RequirePerson") {
		t.Errorf("no ERROR line: %q", sink.all())
	}
}
