package exclusion

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// codeInvalid answers a body whose rule list Validate refuses.
const codeInvalid httpserver.ErrorCode = "exclusion_invalid"

// maxBody is the matches row's body cap. A list at every bound is about 1 MiB
// once JSON escapes each value's characters (a control character is six
// bytes), so the table's default would refuse a list a settings save accepts.
const maxBody = 2 << 20

var codeSpecs = map[httpserver.ErrorCode]httpserver.CodeSpec{
	codeInvalid: {Status: http.StatusUnprocessableEntity, Remediation: "Fix the rules the fields name, then count them again.", NoEffect: true},
}

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
// at the path its name spells under the table's Prefix ("/exclusion/matches"),
// admitting a, the settings read's Access. It needs no store, so the table
// builds without one.
func Endpoint(name string, a httpwire.Access) httpwire.Endpoint {
	return httpwire.Endpoint{
		Name:        name,
		Method:      http.MethodPost,
		Path:        "/" + strings.ReplaceAll(name, ".", "/"),
		Summary:     "Count the matches of a rule list",
		Description: "Answers, for each rule of the list in order, how many of the stored items it matches. The list may be a draft; nothing is saved.",
		Request:     reflect.TypeFor[MatchesRequest](),
		Response:    reflect.TypeFor[MatchesResponse](),
		Codes:       []string{string(codeInvalid)},
		Access:      a,
		MaxBody:     maxBody,
	}
}

// Binding binds the row Endpoint named name: it compiles the body's rules over
// fields, answering exclusion_invalid with Validate's entries under "rules",
// and answers count's counts.
func Binding(name string, fields []Field, count CountFunc) httpwire.Binding {
	return httpwire.Bind(name, matches(fields, count))
}

func matches(fields []Field, count CountFunc) httpwire.Handler[httpwire.None, httpwire.None, MatchesRequest, MatchesResponse] {
	return func(ctx context.Context, c httpwire.Call[httpwire.None, httpwire.None, MatchesRequest]) (MatchesResponse, error) {
		s, errs := Compile("rules", c.Body.Rules, fields)
		if len(errs) != 0 {
			return MatchesResponse{}, refuse(codeInvalid, errs)
		}
		counts, err := count(ctx, s)
		switch {
		case err != nil:
			return MatchesResponse{}, fmt.Errorf("exclusion: count the rules: %w", err)
		case len(counts) != len(c.Body.Rules):
			return MatchesResponse{}, fmt.Errorf("exclusion: the count answered %d counts for %d rules", len(counts), len(c.Body.Rules))
		case counts == nil:
			// The schema's counts is an array, never null.
			counts = []int{}
		}
		return MatchesResponse{Counts: counts}, nil
	}
}

// refuse is the refusal of code naming fields; CheckCodeSpecs sees the code
// through its ErrorCode parameter.
func refuse(code httpserver.ErrorCode, fields []httpwire.FieldError) error {
	return &httpwire.Error{Code: string(code), Message: "the rule list is refused", Fields: fields}
}

// CodeSpecs returns the code the matches row answers, exclusion_invalid, with
// its status and default remediation, for the app's catalog. The map is the
// caller's own copy.
func CodeSpecs() map[httpserver.ErrorCode]httpserver.CodeSpec {
	return maps.Clone(codeSpecs)
}
