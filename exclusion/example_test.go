package exclusion_test

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/cplieger/go/exclusion"
)

// pull is an app's own item, a pull request or an issue on a forge.
type pull struct {
	kind   string
	author string
	title  string
	labels []string
	draft  bool
}

// Values answers the pull's values for each field the app declares: every
// label for "label", and none for "draft" on an issue, which has no draft
// state.
func (p pull) Values(field string) []string {
	switch field {
	case "kind":
		return []string{p.kind}
	case "author":
		return []string{p.author}
	case "title":
		return []string{p.title}
	case "label":
		return p.labels
	case "draft":
		if p.kind != "pull_request" {
			return nil
		}
		return []string{strconv.FormatBool(p.draft)}
	}
	return nil
}

// This example declares a forge's fields, compiles a rule list from its
// settings, leaves out the items a rule matches and counts each rule.
func Example() {
	fields := []exclusion.Field{
		{Name: "kind", Kind: exclusion.Choice, Choices: []string{"pull_request", "issue"}},
		{Name: "author", Kind: exclusion.Text},
		{Name: "title", Kind: exclusion.Pattern},
		{Name: "label", Kind: exclusion.Text},
		{Name: "draft", Kind: exclusion.Bool},
	}
	rules := []exclusion.Rule{
		// A dependency bump from the bot.
		{Conditions: []exclusion.Condition{
			{Field: "author", Value: "renovate"},
			{Field: "title", Value: `^(chore|fix)\(deps\)`},
		}},
		// A draft pull request.
		{Conditions: []exclusion.Condition{
			{Field: "kind", Value: "pull_request"},
			{Field: "draft", Value: "true"},
		}},
	}
	set, errs := exclusion.Compile("rules", rules, fields)
	if len(errs) != 0 {
		fmt.Println(errs)
		return
	}

	items := []exclusion.Item{
		pull{kind: "pull_request", author: "Renovate", title: "chore(deps): update go"},
		pull{kind: "pull_request", author: "renovate", title: "feat: add a field", draft: true},
		pull{kind: "issue", author: "someone", title: "the page is blank", labels: []string{"bug"}},
	}
	for _, it := range items {
		rule, left := set.Match(it)
		fmt.Println(it.(pull).title, left, rule)
	}
	fmt.Println(set.Count(slices.Values(items)))
	// Output:
	// chore(deps): update go true 0
	// feat: add a field true 1
	// the page is blank false -1
	// [1 1]
}
