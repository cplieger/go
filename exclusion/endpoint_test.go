package exclusion_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/exclusion"
	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
	"github.com/cplieger/go/httpwire"
)

var settingsRead = httpwire.Access{Class: httpwire.Session, Passive: true}

type call = httpwire.Call[httpwire.None, httpwire.None, exclusion.MatchesRequest]

func TestMatchesRowIsPost(t *testing.T) {
	row := exclusion.Endpoint("exclusion.matches", settingsRead)
	t.Run("the row", func(t *testing.T) {
		if row.Name != "exclusion.matches" || row.Method != http.MethodPost || row.Path != "/exclusion/matches" {
			t.Errorf("row is %s %s %s, want exclusion.matches POST /exclusion/matches", row.Name, row.Method, row.Path)
		}
		if row.Keyed || len(row.Changes) != 0 || row.List || row.IfMatch {
			t.Errorf("row is Keyed %v, Changes %v, List %v, IfMatch %v; want a plain read", row.Keyed, row.Changes, row.List, row.IfMatch)
		}
		if row.Access != settingsRead {
			t.Errorf("Access = %+v, want %+v", row.Access, settingsRead)
		}
		if row.Request != reflect.TypeFor[exclusion.MatchesRequest]() || row.Response != reflect.TypeFor[exclusion.MatchesResponse]() {
			t.Errorf("Request %v and Response %v, want MatchesRequest and MatchesResponse", row.Request, row.Response)
		}
		if !slices.Equal(row.Codes, []string{"exclusion_invalid"}) {
			t.Errorf("Codes = %v, want [exclusion_invalid]", row.Codes)
		}
	})
	t.Run("the path follows the name", func(t *testing.T) {
		if got := exclusion.Endpoint("rules.matches", settingsRead).Path; got != "/rules/matches" {
			t.Errorf("Path = %q, want /rules/matches", got)
		}
	})
	t.Run("a list at every bound fits the body cap", func(t *testing.T) {
		// A control character is six bytes once JSON escapes it.
		value := strings.Repeat("\x01", exclusion.MaxValueBytes)
		name := strings.Repeat("f", 64)
		r := exclusion.Rule{}
		for range exclusion.MaxConditions {
			r.Conditions = append(r.Conditions, exclusion.Condition{Field: name, Value: value})
		}
		body, err := json.Marshal(exclusion.MatchesRequest{Rules: slices.Repeat([]exclusion.Rule{r}, exclusion.MaxRules)})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if exclusion.MaxBody < len(body) {
			t.Errorf("MaxBody = %d, under the %d bytes of a list at every bound", exclusion.MaxBody, len(body))
		}
	})
	t.Run("the row's cap is MaxBody, 2 MiB", func(t *testing.T) {
		if exclusion.MaxBody != 2<<20 || row.MaxBody != exclusion.MaxBody {
			t.Errorf("MaxBody = %d and the row's MaxBody = %d, want both 2 MiB", exclusion.MaxBody, row.MaxBody)
		}
	})
	t.Run("the table derives", func(t *testing.T) {
		cat := httpwire.NewCatalog()
		if err := cat.AddOwned(exclusion.CodeSpecs()); err != nil {
			t.Fatalf("AddOwned: %v", err)
		}
		d, err := httpwire.Derive(&httpwire.Table{Catalog: cat, Prefix: "/api", Endpoints: []httpwire.Endpoint{row}}, nil)
		if err != nil {
			t.Fatalf("Derive: %v", err)
		}
		if !slices.Contains(d.Endpoints[0].Errors, "exclusion_invalid") {
			t.Errorf("the row's union %v lacks exclusion_invalid", d.Endpoints[0].Errors)
		}
	})
}

// store counts over a fixed set of items, as an app counts over its store.
func store(items ...exclusion.Item) exclusion.CountFunc {
	return func(_ context.Context, s *exclusion.Set) ([]int, error) {
		return s.Count(seq(items...)), nil
	}
}

func TestMatchesAnswersCountsInOrder(t *testing.T) {
	count := store(
		item{"kind": {"pull_request"}, "author": {"renovate"}},
		item{"kind": {"issue"}, "label": {"wontfix"}},
		item{"kind": {"issue"}, "label": {"wontfix"}, "author": {"renovate"}},
	)
	cases := []struct {
		name  string
		rules []exclusion.Rule
		want  []int
	}{
		{name: "one count per rule", rules: []exclusion.Rule{rule("label", "wontfix"), rule("author", "renovate"), rule("kind", "pull_request", "label", "wontfix")}, want: []int{2, 2, 0}},
		{name: "the order is the list's", rules: []exclusion.Rule{rule("kind", "pull_request", "label", "wontfix"), rule("author", "renovate"), rule("label", "wontfix")}, want: []int{0, 2, 2}},
		{name: "no rule answers an empty list", rules: nil, want: []int{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := exclusion.Matches(fields, count)(t.Context(), call{Body: exclusion.MatchesRequest{Rules: c.rules}})
			if err != nil {
				t.Fatalf("Matches: %v", err)
			}
			if got.Counts == nil || !slices.Equal(got.Counts, c.want) {
				t.Errorf("Counts = %#v, want %v", got.Counts, c.want)
			}
		})
	}
	t.Run("a nil count of no rule answers an empty list", func(t *testing.T) {
		none := func(context.Context, *exclusion.Set) ([]int, error) { return nil, nil }
		got, err := exclusion.Matches(fields, none)(t.Context(), call{})
		if err != nil || got.Counts == nil || len(got.Counts) != 0 {
			t.Errorf("Matches = %#v, %v; want an empty list", got.Counts, err)
		}
	})
}

func TestMatchesCountErrorAnswersNoCounts(t *testing.T) {
	errStore := errors.New("store read failed")
	cases := []struct {
		name  string
		count exclusion.CountFunc
		is    error
	}{
		{name: "the store's error", count: func(context.Context, *exclusion.Set) ([]int, error) { return []int{1}, errStore }, is: errStore},
		{name: "a count per rule missing", count: func(context.Context, *exclusion.Set) ([]int, error) { return []int{}, nil }},
		{name: "a count too many", count: func(context.Context, *exclusion.Set) ([]int, error) { return []int{1, 2}, nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := exclusion.Matches(fields, c.count)(t.Context(), call{Body: exclusion.MatchesRequest{Rules: []exclusion.Rule{rule("author", "renovate")}}})
			if err == nil {
				t.Fatal("Matches answered no error")
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Errorf("error %v does not wrap %v", err, c.is)
			}
			if got.Counts != nil {
				t.Errorf("Counts = %v, want none", got.Counts)
			}
		})
	}
}

func TestSaveAndDraftAgree(t *testing.T) {
	list := []exclusion.Rule{
		rule("author", "renovate"),
		rule("milestone", "v1", "title", "("),
		{},
		rule("kind", "commit", "draft", "maybe"),
	}
	saved := exclusion.Validate("rules", list, fields)
	if len(saved) != 5 {
		t.Fatalf("Validate = %v, want five entries", saved)
	}
	t.Run("Compile", func(t *testing.T) {
		s, errs := exclusion.Compile("rules", list, fields)
		if s != nil || !slices.Equal(errs, saved) {
			t.Errorf("Compile = %v, %v; want nil and %v", s, errs, saved)
		}
	})
	t.Run("the matches row", func(t *testing.T) {
		counted := false
		count := func(context.Context, *exclusion.Set) ([]int, error) { counted = true; return nil, nil }
		got, err := exclusion.Matches(fields, count)(t.Context(), call{Body: exclusion.MatchesRequest{Rules: list}})
		var e *httpwire.Error
		if !errors.As(err, &e) {
			t.Fatalf("Matches error = %v, want an *httpwire.Error", err)
		}
		if e.Code != "exclusion_invalid" || !slices.Equal(e.Fields, saved) {
			t.Errorf("Matches = %s %v, want exclusion_invalid %v", e.Code, e.Fields, saved)
		}
		if counted || got.Counts != nil {
			t.Errorf("a refused list was counted (%v, %v)", counted, got.Counts)
		}
	})
}

func TestCodeSpecs(t *testing.T) {
	httpservertest.CheckCodeSpecs(t, ".", exclusion.CodeSpecs())
	specs := exclusion.CodeSpecs()
	got, ok := specs["exclusion_invalid"]
	if !ok || got.Status != http.StatusUnprocessableEntity || !got.NoEffect || got.Remediation == "" {
		t.Errorf("exclusion_invalid = %+v, %v; want 422, NoEffect and a remediation", got, ok)
	}
	specs["exclusion_invalid"] = httpserver.CodeSpec{}
	if exclusion.CodeSpecs()["exclusion_invalid"] != got {
		t.Error("CodeSpecs answers a map its caller can change")
	}
}
