package exclusion_test

import (
	"fmt"

	"github.com/cplieger/go/exclusion"
)

// A settings section's validator calls Validate with the section's path, so
// each field error names the condition it refuses.
func ExampleValidate() {
	fields := []exclusion.Field{
		{Name: "author", Kind: exclusion.Text},
		{Name: "title", Kind: exclusion.Pattern},
		{Name: "draft", Kind: exclusion.Bool},
	}
	rules := []exclusion.Rule{
		{Conditions: []exclusion.Condition{{Field: "author", Value: "renovate"}}},
		{Conditions: []exclusion.Condition{{Field: "milestone", Value: "v1"}}},
		{Conditions: []exclusion.Condition{{Field: "title", Value: "(wip"}, {Field: "draft", Value: "yes"}}},
	}
	for _, e := range exclusion.Validate("rules", rules, fields) {
		fmt.Println(e.Path, e.Code, e.Message)
	}
	// Output:
	// rules[1].conditions[0].field unknown not a field of these rules
	// rules[2].conditions[0].value pattern missing closing ): `(wip`
	// rules[2].conditions[1].value enum true or false
}
