package httpwire

import "github.com/cplieger/go/httpwire/internal/wirelink"

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
	wirelink.Stub = func(v any) any {
		ep, _ := v.(*Endpoint)
		return stubBinding(ep)
	}
}
