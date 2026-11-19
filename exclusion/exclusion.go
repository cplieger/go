package exclusion

import (
	"errors"
	"iter"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/textsafe"
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

// maxMessageBytes bounds a refused pattern's message, which quotes the
// pattern: 200 characters can be 800 bytes.
const maxMessageBytes = 256

// The field codes Validate answers (httpwire's field-code map).
const (
	codeUnknown = "unknown"
	codeEnum    = "enum"
	codePattern = "pattern"
	codeRange   = "range"
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
type Set struct {
	rules [][]condition
}

// condition is one compiled condition: re is set for a Pattern field.
type condition struct {
	re    *regexp.Regexp
	field string
	value string
	kind  Kind
}

func (c *condition) holds(it Item) bool {
	return slices.ContainsFunc(it.Values(c.field), func(v string) bool {
		switch c.kind {
		case Text:
			return strings.EqualFold(v, c.value)
		case Pattern:
			return c.re.MatchString(v)
		default:
			return v == c.value
		}
	})
}

// Compile checks rules as Validate does, with its entries under path, and
// answers the Set that matches and counts them, or nil and the entries.
func Compile(path string, rules []Rule, fields []Field) (*Set, []httpwire.FieldError) {
	compiled, errs := check(path, rules, fields)
	if len(errs) != 0 {
		return nil, errs
	}
	return &Set{rules: compiled}, nil
}

// Validate answers the field errors of rules over fields, each under path
// ("rules[3].conditions[0].field"), for a settings section's validator.
func Validate(path string, rules []Rule, fields []Field) []httpwire.FieldError {
	_, errs := check(path, rules, fields)
	return errs
}

func check(path string, rules []Rule, fields []Field) ([][]condition, []httpwire.FieldError) {
	if len(rules) > MaxRules {
		return nil, []httpwire.FieldError{{Path: path, Code: codeRange, Message: "at most " + strconv.Itoa(MaxRules) + " rules"}}
	}
	declared := make(map[string]*Field, len(fields))
	for i := range fields {
		if _, dup := declared[fields[i].Name]; !dup {
			declared[fields[i].Name] = &fields[i]
		}
	}
	var errs []httpwire.FieldError
	compiled := make([][]condition, 0, len(rules))
	for i, r := range rules {
		at := path + "[" + strconv.Itoa(i) + "].conditions"
		switch n := len(r.Conditions); {
		case n == 0:
			errs = append(errs, httpwire.FieldError{Path: at, Code: codeRange, Message: "at least 1 condition"})
			continue
		case n > MaxConditions:
			errs = append(errs, httpwire.FieldError{Path: at, Code: codeRange, Message: "at most " + strconv.Itoa(MaxConditions) + " conditions"})
			continue
		}
		conds := make([]condition, 0, len(r.Conditions))
		for j, c := range r.Conditions {
			cc, e := compileCondition(at+"["+strconv.Itoa(j)+"]", c, declared[c.Field])
			if e != nil {
				errs = append(errs, *e)
				continue
			}
			conds = append(conds, cc)
		}
		compiled = append(compiled, conds)
	}
	return compiled, errs
}

// compileCondition compiles c, at path at, over f, its field or nil.
func compileCondition(at string, c Condition, f *Field) (condition, *httpwire.FieldError) {
	if f == nil || f.Kind < Text || f.Kind > Choice {
		return condition{}, &httpwire.FieldError{Path: at + ".field", Code: codeUnknown, Message: "not a field of these rules"}
	}
	cc := condition{field: c.Field, value: c.Value, kind: f.Kind}
	code, message := checkValue(c.Value, f)
	if code == "" && f.Kind == Pattern {
		re, err := regexp.Compile(c.Value)
		if err != nil {
			code, message = codePattern, parserMessage(err)
		}
		cc.re = re
	}
	if code != "" {
		return condition{}, &httpwire.FieldError{Path: at + ".value", Code: code, Message: message}
	}
	return cc, nil
}

// checkValue answers the field code and message refusing v as f's value, or
// "" when f takes it; a pattern's syntax is compileCondition's.
func checkValue(v string, f *Field) (code, message string) {
	switch {
	case v == "":
		return codeRange, "required"
	case f.Kind == Pattern && utf8.RuneCountInString(v) > MaxPatternRunes:
		return codeRange, "at most " + strconv.Itoa(MaxPatternRunes) + " characters"
	case f.Kind != Pattern && len(v) > MaxValueBytes:
		return codeRange, "at most " + strconv.Itoa(MaxValueBytes) + " bytes"
	case f.Kind == Bool && v != "true" && v != "false":
		return codeEnum, "true or false"
	case f.Kind == Choice && !slices.Contains(f.Choices, v):
		return codeEnum, "one of " + strings.Join(f.Choices, ", ")
	}
	return "", ""
}

// parserMessage is regexp's refusal without its "error parsing regexp"
// prefix; it quotes the pattern, so it is sanitized and cut.
func parserMessage(err error) string {
	msg := err.Error()
	if se, ok := errors.AsType[*syntax.Error](err); ok {
		msg = se.Code.String() + ": `" + se.Expr + "`"
	}
	text, _ := textsafe.SanitizeSingleLineCapped(msg, maxMessageBytes, "...")
	return text
}

// Match answers the index of the first rule it meets, or -1 and false.
func (s *Set) Match(it Item) (rule int, ok bool) {
	if s == nil {
		return -1, false
	}
	for i := range s.rules {
		if meets(s.rules[i], it) {
			return i, true
		}
	}
	return -1, false
}

// Count answers, for each rule in order, how many of items it matches; an item
// meeting two rules counts in both.
func (s *Set) Count(items iter.Seq[Item]) []int {
	if s == nil {
		return []int{}
	}
	counts := make([]int, len(s.rules))
	if len(s.rules) == 0 {
		return counts
	}
	for it := range items {
		for i := range s.rules {
			if meets(s.rules[i], it) {
				counts[i]++
			}
		}
	}
	return counts
}

func meets(conds []condition, it Item) bool {
	for i := range conds {
		if !conds[i].holds(it) {
			return false
		}
	}
	return true
}
