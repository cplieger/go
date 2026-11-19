package httpfield

import (
	"net/http"
	"slices"
	"testing"
)

func TestList(t *testing.T) {
	cases := []struct {
		name   string
		lines  []string
		syntax Syntax
		want   []string
	}{
		{"no field", nil, Tokens, nil},
		{"empty elements and OWS dropped", []string{" a ,, \tb\t, "}, Tokens, []string{"a", "b"}},
		{"field lines joined in order", []string{"a, b", "c"}, Tokens, []string{"a", "b", "c"}},
		{"an empty line between lines", []string{"a", "", "b"}, Tokens, []string{"a", "b"}},
		{"a token list splits at a comma inside quotes", []string{`"a,b", c`}, Tokens, []string{`"a`, `b"`, "c"}},
		{"a stray quote in a token list swallows nothing", []string{`"1.2.3.4`, "5.6.7.8"}, Tokens, []string{`"1.2.3.4`, "5.6.7.8"}},
		{"a quoted-string keeps its comma", []string{`for="a,b";by=c, for=d`}, QuotedStrings, []string{`for="a,b";by=c`, "for=d"}},
		{"a quoted-pair escapes the quote", []string{`x="a\",b", y`}, QuotedStrings, []string{`x="a\",b"`, "y"}},
		{"an escaped backslash ends with the quote", []string{`x="a\\", y`}, QuotedStrings, []string{`x="a\\"`, "y"}},
		{"an unclosed quoted-string runs to the end", []string{`x="a, b`, "c"}, QuotedStrings, []string{`x="a, b,c`}},
		{"an opaque-tag keeps its comma and star", []string{`"other,*,tag", W/"a,*"`}, entityTags, []string{`"other,*,tag"`, `W/"a,*"`}},
		{"a backslash in an opaque-tag escapes nothing", []string{`"a\", "b"`}, entityTags, []string{`"a\"`, `"b"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for _, line := range tc.lines {
				h.Add("X-List", line)
			}
			if got := slices.Collect(List(h, "X-List", tc.syntax)); !slices.Equal(got, tc.want) {
				t.Errorf("List(%q) = %q, want %q", tc.lines, got, tc.want)
			}
		})
	}

	t.Run("a caller stops the scan", func(t *testing.T) {
		h := http.Header{"X-List": {"a, b, c"}}
		var got []string
		for elem := range List(h, "X-List", Tokens) {
			got = append(got, elem)
			if elem == "b" {
				break
			}
		}
		if !slices.Equal(got, []string{"a", "b"}) {
			t.Errorf("elements read before the break = %q, want [a b]", got)
		}
	})
}

func TestIfNoneMatch(t *testing.T) {
	const etag = `"abc-gz"`
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"no header", nil, false},
		{"empty header", []string{""}, false},
		{"exact match", []string{`"abc-gz"`}, true},
		{"wildcard", []string{"*"}, true},
		{"present in comma list", []string{`"x", "abc-gz", "y"`}, true},
		{"absent from list", []string{`"x", "y"`}, false},
		{"whitespace trimmed around match", []string{`  "abc-gz"  `}, true},
		{"identity etag does not match gz etag", []string{`"abc"`}, false},
		{"weak tag matches by weak comparison", []string{`W/"abc-gz"`}, true},
		{"match on a later field line", []string{`"x"`, `"abc-gz"`}, true},
		{"unquoted tag does not match", []string{"abc-gz"}, false},
		{"a star inside an opaque tag is no wildcard", []string{`"other,*,tag"`}, false},
		{"a weak opaque tag holding a star is no wildcard", []string{`W/"a,*"`}, false},
		{"a quoted star is an opaque tag", []string{`"*"`}, false},
		{"a star beside tags is malformed", []string{`*, "x"`}, false},
		{"a star beside the matching tag still matches", []string{`*, "abc-gz"`}, true},
		{"a star on each of two lines is malformed", []string{"*", "*"}, false},
		{"a backslash in an opaque tag escapes nothing", []string{`"x\", "abc-gz"`}, true},
		{"a lower-case weak prefix is malformed", []string{`w/"abc-gz"`}, false},
		{"an unclosed tag matches nothing", []string{`"abc-gz`}, false},
		{"an unclosed tag swallows the rest of the list", []string{`"x, "abc-gz"`}, false},
		{"an unclosed tag on an earlier line swallows the next", []string{`"x`, `"abc-gz"`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for _, line := range tc.lines {
				h.Add("If-None-Match", line)
			}
			if got := IfNoneMatch(h, etag); got != tc.want {
				t.Errorf("IfNoneMatch(%q, %q) = %v, want %v", tc.lines, etag, got, tc.want)
			}
		})
	}
}

func TestIfMatchFails(t *testing.T) {
	const etag = `"abc-gz"`
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"no header", nil, false},
		{"empty header", []string{""}, false},
		{"only separators", []string{" , ,"}, false},
		{"exact match", []string{`"abc-gz"`}, false},
		{"wildcard", []string{"*"}, false},
		{"present in comma list", []string{`"x", "abc-gz", "y"`}, false},
		{"match on a later field line", []string{`"x"`, `"abc-gz"`}, false},
		{"whitespace trimmed around match", []string{`  "abc-gz"  `}, false},
		{"absent from list", []string{`"x", "y"`}, true},
		{"identity etag does not match gz etag", []string{`"abc"`}, true},
		{"a weak tag never matches by strong comparison", []string{`W/"abc-gz"`}, true},
		{"unquoted tag does not match", []string{"abc-gz"}, true},
		{"a quoted star is an opaque tag", []string{`"*"`}, true},
		{"a star beside tags is malformed", []string{`*, "x"`}, true},
		{"a star beside the matching tag still matches", []string{`*, "abc-gz"`}, false},
		{"a star on each of two lines is malformed", []string{"*", "*"}, true},
		{"a star inside an opaque tag is no wildcard", []string{`"other,*,tag"`}, true},
		{"an unclosed tag swallows the rest of the list", []string{`"x, "abc-gz"`}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for _, line := range tc.lines {
				h.Add("If-Match", line)
			}
			if got := IfMatchFails(h, etag); got != tc.want {
				t.Errorf("IfMatchFails(%q, %q) = %v, want %v", tc.lines, etag, got, tc.want)
			}
		})
	}
}
