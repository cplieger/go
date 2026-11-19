package exclusion

import (
	"context"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// CountFunc answers, for each rule of s in order, how many of the items the
// app's store holds it matches. An error answers the request with no counts.
type CountFunc func(ctx context.Context, s *Set) ([]int, error)

// MatchesRequest is the matches row's body: a draft rule list or the saved one.
type MatchesRequest struct {
	Rules []Rule `json:"rules"`
}

// MatchesResponse is the matches row's answer: one count per rule, in order.
type MatchesResponse struct {
	Counts []int `json:"counts"`
}

// Endpoint answers the matches row: a POST read named name ("exclusion.matches")
// at the path its name spells ("/exclusion/matches"), admitting a, the
// settings read's Access. It needs no store, so the table builds without one.
func Endpoint(name string, a httpwire.Access) httpwire.Endpoint {
	return httpwire.Endpoint{}
}

// Binding binds the row Endpoint named name: it compiles the body's rules over
// fields, answering exclusion_invalid with Validate's entries under "rules",
// and answers count's counts.
func Binding(name string, fields []Field, count CountFunc) httpwire.Binding {
	return httpwire.Bind(name, matches(fields, count))
}

func matches(fields []Field, count CountFunc) httpwire.Handler[httpwire.None, httpwire.None, MatchesRequest, MatchesResponse] {
	return func(ctx context.Context, c httpwire.Call[httpwire.None, httpwire.None, MatchesRequest]) (MatchesResponse, error) {
		return MatchesResponse{}, nil
	}
}

// CodeSpecs returns the code the matches row answers, exclusion_invalid, with
// its status and default remediation, for the app's catalog. The map is the
// caller's own copy.
func CodeSpecs() map[httpserver.ErrorCode]httpserver.CodeSpec {
	return map[httpserver.ErrorCode]httpserver.CodeSpec{}
}
