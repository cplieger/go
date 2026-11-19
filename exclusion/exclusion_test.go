package exclusion_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/go/exclusion"
	"github.com/cplieger/go/textsafe"
)

func TestValidateFieldCodes(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		rules []exclusion.Rule
		want  []pathCode
	}{
		{name: "a valid list answers nothing", path: "rules", rules: []exclusion.Rule{
			rule("kind", "pull_request", "author", "renovate"),
			rule("connection", "gitlab", "label", "wontfix"),
			rule("title", `^chore\(deps\)`, "draft", "true"),
			rule("repository", "owner/name", "draft", "false"),
		}},
		{
			name: "a field not declared is unknown", path: "rules", rules: []exclusion.Rule{rule("milestone", "v1")},
			want: []pathCode{{"rules[0].conditions[0].field", "unknown"}},
		},
		{
			name: "a choice not listed is enum", path: "rules", rules: []exclusion.Rule{rule("kind", "commit")},
			want: []pathCode{{"rules[0].conditions[0].value", "enum"}},
		},
		{
			name: "a choice is exact", path: "rules", rules: []exclusion.Rule{rule("kind", "Issue")},
			want: []pathCode{{"rules[0].conditions[0].value", "enum"}},
		},
		{
			name: "a bool other than true or false is enum", path: "rules", rules: []exclusion.Rule{rule("draft", "yes"), rule("draft", "True")},
			want: []pathCode{{"rules[0].conditions[0].value", "enum"}, {"rules[1].conditions[0].value", "enum"}},
		},
		{
			name: "a pattern regexp refuses is pattern", path: "rules", rules: []exclusion.Rule{rule("title", "(")},
			want: []pathCode{{"rules[0].conditions[0].value", "pattern"}},
		},
		{
			name: "a rule with no condition is range", path: "rules", rules: []exclusion.Rule{{}, {Conditions: []exclusion.Condition{}}},
			want: []pathCode{{"rules[0].conditions", "range"}, {"rules[1].conditions", "range"}},
		},
		{
			name: "an empty text value is range", path: "rules", rules: []exclusion.Rule{rule("author", "")},
			want: []pathCode{{"rules[0].conditions[0].value", "range"}},
		},
		{
			name: "an empty pattern is range", path: "rules", rules: []exclusion.Rule{rule("title", "")},
			want: []pathCode{{"rules[0].conditions[0].value", "range"}},
		},
		{name: "each entry names its rule and condition", path: "rules", rules: []exclusion.Rule{
			rule("author", "renovate"),
			rule("author", "x", "kind", "commit", "label", "y", "milestone", "z"),
		}, want: []pathCode{{"rules[1].conditions[1].value", "enum"}, {"rules[1].conditions[3].field", "unknown"}}},
		{
			name: "the path is the caller's", path: "filters.exclusions", rules: []exclusion.Rule{rule("milestone", "v1")},
			want: []pathCode{{"filters.exclusions[0].conditions[0].field", "unknown"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := exclusion.Validate(c.path, c.rules, fields)
			if got := pathCodes(errs); !slices.Equal(got, c.want) {
				t.Errorf("Validate = %v, want %v", got, c.want)
			}
			for _, e := range errs {
				if e.Message == "" {
					t.Errorf("entry %s %s has no message", e.Path, e.Code)
				}
			}
		})
	}
}

func TestBoundsAnswerRange(t *testing.T) {
	valueAt := []pathCode{{"rules[0].conditions[0].value", "range"}}
	cases := []struct {
		name  string
		rules []exclusion.Rule
		want  []pathCode
	}{
		{name: "a pattern of 200 characters", rules: []exclusion.Rule{rule("title", strings.Repeat("a", 200))}},
		{name: "a pattern of 201 characters", rules: []exclusion.Rule{rule("title", strings.Repeat("a", 201))}, want: valueAt},
		{name: "a pattern counts characters, not bytes", rules: []exclusion.Rule{rule("title", strings.Repeat("é", 200))}},
		{name: "a value of 200 bytes", rules: []exclusion.Rule{rule("author", strings.Repeat("a", 200))}},
		{name: "a value of 201 bytes", rules: []exclusion.Rule{rule("author", strings.Repeat("a", 201))}, want: valueAt},
		{name: "a value counts bytes, not characters", rules: []exclusion.Rule{rule("author", strings.Repeat("é", 101))}, want: valueAt},
		{name: "8 conditions", rules: []exclusion.Rule{eight()}},
		{
			name: "9 conditions", rules: []exclusion.Rule{{Conditions: append(eight().Conditions, exclusion.Condition{Field: "author", Value: "a"})}},
			want: []pathCode{{"rules[0].conditions", "range"}},
		},
		{name: "100 rules", rules: rules(100)},
		{name: "101 rules", rules: rules(101), want: []pathCode{{"rules", "range"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pathCodes(exclusion.Validate("rules", c.rules, fields)); !slices.Equal(got, c.want) {
				t.Errorf("Validate = %v, want %v", got, c.want)
			}
			s, errs := exclusion.Compile("rules", c.rules, fields)
			if got := pathCodes(errs); !slices.Equal(got, c.want) {
				t.Errorf("Compile = %v, want %v", got, c.want)
			}
			if (s == nil) != (len(c.want) != 0) {
				t.Errorf("Compile's set is %v with %d field errors; want a set exactly when there is none", s, len(c.want))
			}
		})
	}
}

// eight is a rule of 8 conditions.
func eight() exclusion.Rule {
	r := exclusion.Rule{}
	for i := range 8 {
		r.Conditions = append(r.Conditions, exclusion.Condition{Field: "label", Value: "l" + strconv.Itoa(i)})
	}
	return r
}

// rules is a list of n valid rules.
func rules(n int) []exclusion.Rule {
	out := make([]exclusion.Rule, n)
	for i := range out {
		out[i] = rule("author", "a"+strconv.Itoa(i))
	}
	return out
}

func TestPatternMessageThroughTextsafe(t *testing.T) {
	cases := []struct{ name, pattern, names string }{
		{name: "unsafe runes are replaced", pattern: "(\u202e\x1b\u0085\nx", names: "missing closing )"},
		{name: "a long message is cut", pattern: strings.Repeat("\U0001F600", 199) + "(", names: "missing closing )"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := exclusion.Validate("rules", []exclusion.Rule{rule("title", c.pattern)}, fields)
			if len(errs) != 1 || errs[0].Code != "pattern" {
				t.Fatalf("Validate = %v, want one pattern entry", errs)
			}
			msg := errs[0].Message
			if !strings.Contains(msg, c.names) {
				t.Errorf("message %q does not carry the parser's %q", msg, c.names)
			}
			if !utf8.ValidString(msg) || strings.ContainsFunc(msg, textsafe.IsUnsafeSingleLine) {
				t.Errorf("message %q keeps a rune textsafe replaces on one line", msg)
			}
			if len(msg) > 256 {
				t.Errorf("message is %d bytes, want at most 256", len(msg))
			}
		})
	}
}

func TestMatchAndsConditionsOrsRules(t *testing.T) {
	s := compile(t,
		rule("kind", "pull_request", "author", "renovate"),
		rule("label", "wontfix"),
		rule("connection", "gitlab", "title", "chore"),
	)
	cases := []struct {
		name string
		it   item
		rule int
		ok   bool
	}{
		{name: "every condition of a rule", it: item{"kind": {"pull_request"}, "author": {"renovate"}}, rule: 0, ok: true},
		{name: "one condition of a rule is not enough", it: item{"kind": {"issue"}, "author": {"renovate"}}, rule: -1},
		{name: "any one rule", it: item{"kind": {"issue"}, "label": {"wontfix"}}, rule: 1, ok: true},
		{name: "the first rule met is named", it: item{"kind": {"pull_request"}, "author": {"renovate"}, "label": {"wontfix"}}, rule: 0, ok: true},
		{name: "a choice beside a pattern", it: item{"connection": {"gitlab"}, "title": {"release: chore work"}}, rule: 2, ok: true},
		{name: "the choice holds the pattern to its connection", it: item{"connection": {"github"}, "title": {"chore"}}, rule: -1},
		{name: "no rule met", it: item{"kind": {"issue"}, "author": {"someone"}}, rule: -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := s.Match(c.it)
			if r != c.rule || ok != c.ok {
				t.Errorf("Match = %d, %v; want %d, %v", r, ok, c.rule, c.ok)
			}
		})
	}
}

func TestMatchTextIsExactIgnoringCase(t *testing.T) {
	s := compile(t, rule("author", "renovate"), rule("label", "été"))
	cases := []struct {
		name string
		it   item
		ok   bool
	}{
		{name: "equal", it: item{"author": {"renovate"}}, ok: true},
		{name: "case differs", it: item{"author": {"Renovate"}}, ok: true},
		{name: "case folds beyond ASCII", it: item{"label": {"ÉTÉ"}}, ok: true},
		{name: "a longer value", it: item{"author": {"renovate-bot"}}},
		{name: "a shorter value", it: item{"author": {"renov"}}},
		{name: "a padded value is not repaired", it: item{"author": {" renovate "}}},
		{name: "a word inside a label", it: item{"label": {"étéish"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := s.Match(c.it); ok != c.ok {
				t.Errorf("Match ok = %v, want %v", ok, c.ok)
			}
		})
	}
}

func TestMatchNoValueNeverMatches(t *testing.T) {
	cases := []struct {
		name string
		r    exclusion.Rule
		it   item
	}{
		{name: "a label condition on an item with no labels", r: rule("label", "wontfix"), it: item{"kind": {"issue"}}},
		{name: "a draft condition on an issue", r: rule("draft", "false"), it: item{"kind": {"issue"}}},
		{name: "a pattern matching anything on an item with no title", r: rule("title", ".*"), it: item{"kind": {"issue"}}},
		{name: "an empty value list", r: rule("label", "wontfix"), it: item{"label": {}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if r, ok := compile(t, c.r).Match(c.it); ok {
				t.Errorf("Match = %d, true; want no match", r)
			}
		})
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		name, pattern, title string
		ok                   bool
	}{
		{name: "found anywhere", pattern: "fix", title: "a fix here", ok: true},
		{name: "an anchor holds", pattern: "^fix", title: "a fix here"},
		{name: "an anchor met", pattern: "^fix", title: "fix: a thing", ok: true},
		{name: "case-sensitive", pattern: "Fix", title: "fix"},
		{name: "case folded by its flag", pattern: "(?i)fix", title: "FIX", ok: true},
		{name: "the raw value", pattern: "\u202e", title: "evil\u202etxt", ok: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := compile(t, rule("title", c.pattern)).Match(item{"title": {c.title}}); ok != c.ok {
				t.Errorf("Match ok = %v, want %v", ok, c.ok)
			}
		})
	}
}

func TestMatchReadsEveryValue(t *testing.T) {
	labels := make([]string, 0, 65)
	for i := range 64 {
		labels = append(labels, "l"+strconv.Itoa(i))
	}
	labels = append(labels, "broken")
	cases := []struct {
		name string
		r    exclusion.Rule
		it   item
	}{
		{name: "a text value past the 64th", r: rule("label", "broken"), it: item{"label": labels}},
		{name: "a pattern on a later value", r: rule("title", "^b"), it: item{"title": {"alpha", "beta"}}},
		{name: "a choice on a later value", r: rule("kind", "issue"), it: item{"kind": {"pull_request", "issue"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if r, ok := compile(t, c.r).Match(c.it); !ok || r != 0 {
				t.Errorf("Match = %d, %v; want 0, true", r, ok)
			}
		})
	}
}

func TestEmptySetMatchesNothing(t *testing.T) {
	it := item{"kind": {"issue"}, "author": {"renovate"}, "label": {"wontfix"}}
	var zero exclusion.Set
	cases := []struct {
		name string
		s    *exclusion.Set
	}{
		{name: "no rule", s: compile(t)},
		{name: "an empty list", s: compile(t, []exclusion.Rule{}...)},
		{name: "the zero Set", s: &zero},
		{name: "a nil Set", s: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if r, ok := c.s.Match(it); ok || r != -1 {
				t.Errorf("Match = %d, %v; want -1, false", r, ok)
			}
			if got := c.s.Count(seq(it)); got == nil || len(got) != 0 {
				t.Errorf("Count = %#v, want an empty, non-nil list", got)
			}
		})
	}
}

func TestCountPerRule(t *testing.T) {
	s := compile(t,
		rule("author", "renovate"),
		rule("label", "wontfix"),
		rule("label", "nothing-has-this"),
		rule("kind", "issue", "draft", "true"),
	)
	got := s.Count(seq(
		item{"kind": {"pull_request"}, "author": {"renovate"}},
		item{"kind": {"pull_request"}, "author": {"renovate"}, "label": {"wontfix"}},
		item{"kind": {"issue"}, "label": {"wontfix", "bug"}},
		item{"kind": {"issue"}},
	))
	if want := []int{2, 2, 0, 0}; !slices.Equal(got, want) {
		t.Errorf("Count = %v, want %v", got, want)
	}
}
