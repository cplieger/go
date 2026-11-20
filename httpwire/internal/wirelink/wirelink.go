// Package wirelink hands package httpwiretest what an httpwire API was built
// with, which httpwire keeps off its exported API. Package httpwire sets the
// functions when it loads; they pass httpwire's own types as any, since this
// package cannot import httpwire.
package wirelink

import "net/http"

// Built is what New was given: the parsed document (*httpwire.Schema), the
// guard (httpwire.Guard, nil when none) and the wire version.
type Built struct {
	Schema  any
	Guard   any
	Version string
}

var (
	// Inspect answers what an *httpwire.API was built with.
	Inspect func(api any) Built
	// Primary answers an *httpwire.Endpoint's primary success status.
	Primary func(ep any) int
	// OwnerProblems answers every refusal Derive makes of an *httpwire.Owner
	// on its own, the catalog check aside.
	OwnerProblems func(owner any) []error
	// Slot answers the credential and parsed Idempotency-Key an
	// *httpwire.API keeps r's answer under on an *httpwire.Endpoint, ok only
	// on a Keyed row sent a valid key.
	Slot func(api, ep any, r *http.Request) (credential, key string, ok bool)
)
