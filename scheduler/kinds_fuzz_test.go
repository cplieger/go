package scheduler

import (
	"regexp"
	"strings"
	"testing"
)

// scopeSpec is the scope grammar as written: lowercase letters, digits, '_',
// ':' and '/', at most 128 bytes.
var scopeSpec = regexp.MustCompile(`^[a-z0-9_:/]{0,128}$`)

func FuzzScopeGrammar(f *testing.F) {
	for _, s := range []string{
		"", "sweep", "connection:3", "repo:cplieger/forgeapi", "host:3/posture",
		strings.Repeat("a", 128), strings.Repeat("a", 129), "Repo", "a b", "a-b", "é", "a\x00b", "\xff",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := validScope(s), scopeSpec.MatchString(s); got != want {
			t.Errorf("validScope(%q) = %v, want %v", s, got, want)
		}
		if validCode(s) && !validScope(s) && len(s) <= maxScope {
			t.Errorf("code %q is not a scope", s)
		}
		if d := DigestScope("propose", []byte(s)); !validScope(d) {
			t.Errorf("DigestScope(propose, %q) = %q, outside scope grammar", s, d)
		}
	})
}
