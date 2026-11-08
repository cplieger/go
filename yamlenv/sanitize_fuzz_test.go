package yamlenv_test

import (
	"strings"
	"testing"

	"github.com/cplieger/go/yamlenv"
)

// FuzzSanitizeDecodeError generalizes the value-independence contract of the
// wrong-type and duplicate-key rebuilds: a sentinel planted where go-yaml puts
// the value-derived excerpt or key must never survive sanitization, whatever
// the fuzzer packs around it (backticks, marker fragments, line prefixes), and
// each rebuilt entry must be byte-for-byte the value-free template, with or
// without the unknown-key echo option.
func FuzzSanitizeDecodeError(f *testing.F) {
	f.Add("", "")
	f.Add("x", "y")
	f.Add("mapping key x", " already defined at line 9")
	f.Add("field oops", " not found in type main.fileConfig")
	f.Add("cannot construct !!str `nested`", " into bool")
	f.Add("line 3: ", "")
	f.Add("`", "`")
	f.Add("first\nsecond", " into ")
	f.Fuzz(func(t *testing.T, pre, post string) {
		const sentinel = "EXCERPT-SENTINEL-9c2f"
		value := pre + sentinel + post
		cases := map[string]struct {
			msg  string
			want string
		}{
			"wrong type": {
				msg:  "cannot construct !!str `" + value + "` into bool",
				want: "decode errors: line 4, column 7: cannot construct !!str <redacted> into bool",
			},
			"duplicate key": {
				msg:  "mapping key \"" + value + "\" already defined at line 2",
				want: "decode errors: line 4, column 7: mapping key <redacted> already defined at line 2",
			},
		}
		for shape, tc := range cases {
			err := loadErrors(constructEntry(4, 7, tc.msg))
			for name, got := range map[string]string{
				"default": yamlenv.SanitizeDecodeError(err).Error(),
				"echo":    yamlenv.SanitizeDecodeError(err, yamlenv.WithUnknownKeyEcho(true)).Error(),
			} {
				if strings.Contains(got, sentinel) {
					t.Errorf("%s/%s (pre=%q post=%q) leaks the sentinel: %q", shape, name, pre, post, got)
				}
				if got != tc.want {
					t.Errorf("%s/%s (pre=%q post=%q) = %q, want %q", shape, name, pre, post, got, tc.want)
				}
			}
		}
	})
}
