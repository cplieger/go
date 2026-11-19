package exclusion_test

import (
	"iter"
	"slices"
	"testing"

	"github.com/cplieger/go/exclusion"
	"github.com/cplieger/go/httpwire"
)

// fields is a forge's field set: the kind and the connection as choices,
// repository, author and label as text, the title as a pattern, draft as a
// boolean.
var fields = []exclusion.Field{
	{Name: "kind", Kind: exclusion.Choice, Choices: []string{"pull_request", "issue"}},
	{Name: "connection", Kind: exclusion.Choice, Choices: []string{"github", "gitlab"}},
	{Name: "repository", Kind: exclusion.Text},
	{Name: "author", Kind: exclusion.Text},
	{Name: "label", Kind: exclusion.Text},
	{Name: "title", Kind: exclusion.Pattern},
	{Name: "draft", Kind: exclusion.Bool},
}

// item answers each field's values from a map; a field it lacks has none.
type item map[string][]string

func (i item) Values(field string) []string { return i[field] }

// rule builds a rule from field and value pairs.
func rule(pairs ...string) exclusion.Rule {
	r := exclusion.Rule{}
	for i := 0; i+1 < len(pairs); i += 2 {
		r.Conditions = append(r.Conditions, exclusion.Condition{Field: pairs[i], Value: pairs[i+1]})
	}
	return r
}

// compile compiles rules under "rules", failing t on any field error.
func compile(t *testing.T, rules ...exclusion.Rule) *exclusion.Set {
	t.Helper()
	s, errs := exclusion.Compile("rules", rules, fields)
	if len(errs) != 0 || s == nil {
		t.Fatalf("Compile = %v, %v; want a set and no field error", s, errs)
	}
	return s
}

// pathCode is a field error without its message.
type pathCode struct{ path, code string }

func pathCodes(errs []httpwire.FieldError) []pathCode {
	out := make([]pathCode, 0, len(errs))
	for _, e := range errs {
		out = append(out, pathCode{e.Path, e.Code})
	}
	return out
}

// seq yields items in order.
func seq(items ...exclusion.Item) iter.Seq[exclusion.Item] {
	return slices.Values(items)
}
