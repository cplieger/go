package httpwiretest_test

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/httpwire/httpwiretest"
)

// outcomeAPI has a write row of each kind beside outcome_unknown: one not
// Keyed, one Keyed, one that cannot answer it, a read, and a mount row.
func outcomeAPI(t *testing.T) *httpwire.API {
	t.Helper()
	doc, err := os.ReadFile("testdata/outcome.json")
	if err != nil {
		t.Fatal(err)
	}
	session := httpwire.Access{Class: httpwire.Session}
	row := func(name, method, path, summary string, codes ...httpserver.ErrorCode) httpwire.Endpoint {
		return httpwire.Endpoint{Name: name, Method: method, Path: path, Summary: summary, Description: summary + ".", Access: session, Codes: codes}
	}
	keyed := row("act.keyed", http.MethodPost, "/act/keyed", "Act keyed", "outcome_unknown")
	keyed.Keyed = true
	c := httpwire.NewCatalog()
	if err := c.Add("auth_required", httpserver.CodeSpec{Status: http.StatusUnauthorized, Remediation: "Sign in."}); err != nil {
		t.Fatal(err)
	}
	tb := &httpwire.Table{Catalog: c, Prefix: "/api", Endpoints: []httpwire.Endpoint{
		row("act.unkeyed", http.MethodPost, "/act", "Act unkeyed", "outcome_unknown"),
		keyed,
		row("act.plain", http.MethodPost, "/act/plain", "Act plainly"),
		row("act.read", http.MethodGet, "/act", "Read the act", "outcome_unknown"),
	}, Mounts: []httpwire.Mount{{Handler: http.NotFoundHandler(), Rows: []httpwire.Endpoint{
		row("mount.act", http.MethodPut, "/mount/act", "Act through the mount", "outcome_unknown"),
	}}}}
	none := func(context.Context, httpwire.Call[httpwire.None, httpwire.None, httpwire.None, httpwire.None]) (httpwire.None, error) {
		return httpwire.None{}, nil
	}
	api, err := httpwire.New(tb, []httpwire.Binding{
		httpwire.Bind("act.unkeyed", none), httpwire.Bind("act.keyed", none),
		httpwire.Bind("act.plain", none), httpwire.Bind("act.read", none),
	}, httpwire.WithSchema(doc), httpwire.WithGuard(fixtureGuard{c}),
		httpwire.WithCredential(func(*http.Request) string { return "s:a" }),
		httpwire.WithCursorOwner(func(*http.Request) string { return "u:a" }),
		httpwire.WithHold(func(next http.Handler) http.Handler { return next }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return api
}

// TestContractFailsUnkeyedOutcomeUnknown fails each write row that can
// answer outcome_unknown and is not Keyed, since a resend there may send
// the write twice; a Keyed one, a read and a write without it pass.
func TestContractFailsUnkeyedOutcomeUnknown(t *testing.T) {
	api := outcomeAPI(t)
	r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, api, nil) })
	for _, row := range []string{`"act.unkeyed"`, `"mount.act"`} {
		if !r.failed(row, "outcome_unknown", "Keyed") {
			t.Errorf("Contract did not fail row %s: %q", row, r.failures)
		}
	}
	if len(r.failures) != 2 {
		t.Errorf("%d failures, want the two unkeyed write rows: %q", len(r.failures), r.failures)
	}
}
