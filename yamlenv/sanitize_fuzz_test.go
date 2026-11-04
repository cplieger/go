package yamlenv_test

import (
	"strings"
	"testing"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v4"
)

// FuzzSanitizeDecodeError generalizes the value-independence contract of the
// wrong-type rebuild: a sentinel planted in the scalar-excerpt position of a
// TypeError entry must never survive sanitization — whatever the fuzzer packs
// around it (backticks, marker fragments, bare line prefixes) — and the
// rebuilt entry must be byte-for-byte the value-free template, with or
// without the unknown-key echo option. This is the generative form of the
// TestSanitizeDecodeErrorWrongType table.
func FuzzSanitizeDecodeError(f *testing.F) {
	f.Add("", "")
	f.Add("x", "y")
	f.Add(": mapping key x", " already defined at line 9")
	f.Add(": field oops", " not found in type main.fileConfig")
	f.Add("cannot unmarshal !!str `nested`", " into bool")
	f.Add("line 3", "")
	f.Add("`", "`")
	f.Add("first\nsecond", " into ")
	f.Fuzz(func(t *testing.T, pre, post string) {
		const sentinel = "EXCERPT-SENTINEL-9c2f"
		const want = "unmarshal errors: line 4: cannot unmarshal !!str <redacted> into bool"
		typeErr := &yaml.TypeError{Errors: []string{
			"line 4: cannot unmarshal !!str `" + pre + sentinel + post + "` into bool",
		}}
		for name, got := range map[string]string{
			"default": yamlenv.SanitizeDecodeError(typeErr).Error(),
			"echo":    yamlenv.SanitizeDecodeError(typeErr, yamlenv.WithUnknownKeyEcho(true)).Error(),
		} {
			if strings.Contains(got, sentinel) {
				t.Errorf("SanitizeDecodeError(%s, pre=%q post=%q) leaks the excerpt sentinel: %q", name, pre, post, got)
			}
			if got != want {
				t.Errorf("SanitizeDecodeError(%s, pre=%q post=%q) = %q, want %q", name, pre, post, got, want)
			}
		}
	})
}
