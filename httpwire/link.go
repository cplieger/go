package httpwire

import (
	"net/http"

	"github.com/cplieger/go/httpwire/internal/wirelink"
)

func init() {
	wirelink.Inspect = func(v any) wirelink.Built {
		a, _ := v.(*API)
		return wirelink.Built{Schema: a.schema, Guard: a.o.guard, Version: a.version}
	}
	wirelink.Primary = func(v any) int {
		ep, _ := v.(*Endpoint)
		return primaryStatus(ep)
	}
	wirelink.OwnerProblems = func(v any) []error {
		o, _ := v.(*Owner)
		c := &tableChecker{t: &Table{}, owners: map[*Owner]bool{}}
		c.owner(o)
		return c.errs
	}
	wirelink.Slot = func(api, ep any, r *http.Request) (credential, key string, ok bool) {
		a, _ := api.(*API)
		e, _ := ep.(*Endpoint)
		if !e.Keyed {
			return "", "", false
		}
		key, err := idempotencyKeyOf(r, e)
		if err != nil {
			return "", "", false
		}
		// WithCredential, not CredentialOf: a mount row gets no scope. It is
		// nil only when every endpoint row is Public, mount rows aside.
		if a.o.credential != nil {
			credential = a.o.credential(r)
		}
		return credential, key, true
	}
}
