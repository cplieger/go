package exclusion

import (
	"iter"

	"github.com/cplieger/go/httpwire"
)

// The bounds of one rule list.
const (
	MaxRules      = 100
	MaxConditions = 8
	// MaxValueBytes bounds a Text, Bool or Choice value.
	MaxValueBytes = 200
	// MaxPatternRunes bounds a Pattern value, counted in characters.
	MaxPatternRunes = 200
)

// Kind is how a condition's value is compared with an item's values.
type Kind uint8

// The kinds of field.
const (
	// Text is equal under Unicode simple case folding (strings.EqualFold) to
	// one of the item's values.
	Text Kind = iota + 1
	// Pattern is an RE2 pattern (Go's regexp) found anywhere in one of the
	// item's values.
	Pattern
	// Bool is "true" or "false", equal to one of the item's values.
	Bool
	// Choice is one of the field's Choices, equal to one of the item's values.
	Choice
)

// Field is one property of the app's items that a condition can test.
type Field struct {
	// Name is the wire name, "author".
	Name string
	// Choices are a Choice field's values.
	Choices []string
	Kind    Kind
}

// Condition tests one field of an item.
type Condition struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// Rule leaves out an item that meets every one of its conditions.
type Rule struct {
	Conditions []Condition `json:"conditions"`
}

// Item answers an item's values for one field: a label field answers every
// label, and a field the item has no value for answers none. Values are
// canonical before they reach Item (trimmed, lowercased or normalized as the
// app's source requires), so matching is one comparison.
type Item interface {
	Values(field string) []string
}

// Set is a compiled rule list. The zero Set and a nil *Set hold no rule. It is
// safe for concurrent use.
type Set struct{}

// Compile checks rules as Validate does, with its entries under path, and
// answers the Set that matches and counts them, or nil and the entries.
func Compile(path string, rules []Rule, fields []Field) (*Set, []httpwire.FieldError) {
	return nil, nil
}

// Validate answers the field errors of rules over fields, each under path
// ("rules[3].conditions[0].field"), for a settings section's validator.
func Validate(path string, rules []Rule, fields []Field) []httpwire.FieldError {
	return nil
}

// Match answers the index of the first rule it meets, or -1 and false.
func (s *Set) Match(it Item) (rule int, ok bool) {
	return -1, false
}

// Count answers, for each rule in order, how many of items it matches; an item
// meeting two rules counts in both.
func (s *Set) Count(items iter.Seq[Item]) []int {
	return nil
}
