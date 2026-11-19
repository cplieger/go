package httpwiretest

import (
	"net/http"
	"testing"

	"github.com/cplieger/go/httpwire"
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

// AccessMatrix serves every row of table.Endpoints once per credential.
func AccessMatrix(t testing.TB, table *httpwire.Table, guard httpwire.Guard, creds []Credential, opts ...httpwire.Option) {
	t.Helper()
}
