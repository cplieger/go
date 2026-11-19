// Package wirefixture is exclusion's fixture API: the matches row in a table
// of its own and a guard that admits a session. The wire document test and
// the contract checks build on it.
package wirefixture

import (
	"context"
	"net/http"

	"github.com/cplieger/go/exclusion"
	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// Row is the matches row's name in the fixture table.
const Row = "exclusion.matches"

// Fields is a forge's field set, one field or more of each kind.
var Fields = []exclusion.Field{
	{Name: "kind", Kind: exclusion.Choice, Choices: []string{"pull_request", "issue"}},
	{Name: "repository", Kind: exclusion.Text},
	{Name: "author", Kind: exclusion.Text},
	{Name: "title", Kind: exclusion.Pattern},
	{Name: "draft", Kind: exclusion.Bool},
}

// access is a settings read's: a session, and not session activity.
var access = httpwire.Access{Class: httpwire.Session, Passive: true}

// Table answers the fixture table, with a catalog of its own holding
// exclusion's code and the guard's.
func Table() *httpwire.Table {
	c := httpwire.NewCatalog()
	if err := c.AddOwned(exclusion.CodeSpecs()); err != nil {
		panic(err)
	}
	if err := c.Add(codeAuthRequired, httpserver.CodeSpec{Status: http.StatusUnauthorized, Remediation: "Sign in, then try again."}); err != nil {
		panic(err)
	}
	return &httpwire.Table{Catalog: c, Prefix: "/api", Endpoints: []httpwire.Endpoint{exclusion.Endpoint(Row, access)}}
}

const codeAuthRequired = "auth_required"

// Guard admits a request whose Principal header is "session" on every row
// that is not Public, and refuses any other with auth_required.
type Guard struct{ Catalog *httpwire.Catalog }

var _ httpwire.Guard = Guard{}

// Wrap answers auth_required to a request Admits refuses.
func (g Guard) Wrap(ep *httpwire.Endpoint, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.Admits(g.Shape(r), ep.Access) {
			g.Catalog.WriteError(w, r, &httpwire.Error{Code: codeAuthRequired, Message: "sign in"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Codes answers auth_required for every class but Public.
func (Guard) Codes(a httpwire.Access) []string {
	if a.Class == httpwire.Public {
		return nil
	}
	return []string{codeAuthRequired}
}

// Shape reads the Principal header.
func (Guard) Shape(r *http.Request) httpwire.Shape {
	if r.Header.Get("Principal") == "session" {
		return httpwire.Shape{Principal: httpwire.ShapeSession}
	}
	return httpwire.Shape{Principal: httpwire.ShapeAnonymous}
}

// Shapes are anonymous and a session.
func (Guard) Shapes() []httpwire.Shape {
	return []httpwire.Shape{{Principal: httpwire.ShapeAnonymous}, {Principal: httpwire.ShapeSession}}
}

// Admits a session on every row, and anyone on a Public one.
func (Guard) Admits(s httpwire.Shape, a httpwire.Access) bool {
	return a.Class == httpwire.Public || s.Principal == httpwire.ShapeSession
}

// Person answers nil: the fixture has no step-up.
func (Guard) Person(context.Context) error { return nil }
