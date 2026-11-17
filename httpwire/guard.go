package httpwire

import (
	"context"
	"net/http"
)

// Guard decides who reaches each row, from the row's Access.
type Guard interface {
	// Wrap guards one row's handler; it runs before any decoding.
	Wrap(ep *Endpoint, next http.Handler) http.Handler
	// Codes are the codes Wrap may answer for a row with this Access.
	Codes(a Access) []string
	// Shape is the request's principal shape, answered without refusing:
	// the admitted principal, else a passive authentication, else anonymous.
	Shape(r *http.Request) Shape
	// Shapes are every shape the guard can admit.
	Shapes() []Shape
	// Admits reports whether a principal of shape s passes a row with this
	// Access with no step-up: false on every RecentAuth row, and false for
	// a key shape on a PersonOnly row.
	Admits(s Shape, a Access) bool
	// Person answers nil for a session that authenticated recently or the
	// admin-socket principal, else the *Error to return.
	Person(ctx context.Context) error
}

// Shape is a principal's kind and the scopes it holds: the unit every
// principal of one view shares.
type Shape struct {
	// Scopes are sorted.
	Scopes    []string
	Principal ShapeKind
}

// ShapeKind is a principal's kind.
type ShapeKind uint8

// The principal kinds.
const (
	ShapeAnonymous ShapeKind = iota + 1
	ShapeSession
	ShapeKey
	ShapeRecoverySession
)
