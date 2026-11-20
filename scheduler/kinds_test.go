package scheduler

import (
	"strings"
	"testing"
)

func TestScopeGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		scope string
		want  bool
	}{
		{"empty", "", true},
		{"code", "sweep", true},
		{"colon", "connection:3", true},
		{"slash", "repo:cplieger/forgeapi", true},
		{"host_path", "host:3/posture", true},
		{"at_128_bytes", strings.Repeat("a", 128), true},
		{"at_129_bytes", strings.Repeat("a", 129), false},
		{"uppercase", "repo:Owner/name", false},
		{"space", "a b", false},
		{"dot", "a.b", false},
		{"hyphen", "a-b", false},
		{"non_ascii", "é", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := validScope(c.scope); got != c.want {
				t.Errorf("validScope(%q) = %v, want %v", c.scope, got, c.want)
			}
		})
	}
}

func TestCodeGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code string
		want bool
	}{
		{"word", "full_check", true},
		{"digits", "pass2", true},
		{"empty", "", false},
		{"colon", "a:b", false},
		{"slash", "a/b", false},
		{"uppercase", "Scan", false},
		{"hyphen", "full-check", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := validCode(c.code); got != c.want {
				t.Errorf("validCode(%q) = %v, want %v", c.code, got, c.want)
			}
		})
	}
}
