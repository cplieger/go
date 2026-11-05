package yamlenv_test

import (
	"strings"
	"testing"

	"github.com/cplieger/go/yamlenv"
)

// FuzzLoad pins Load's pipeline invariants for arbitrary config-file bytes:
// it never panics, every returned error's text is value-independent (a fixed
// environment secret never survives into it, whichever pipeline stage
// failed — single-document check, parse, unknown-key probe, or decode, and
// whether or not the recommended passthrough is set, with config types whose
// own errors are worded like go-yaml's), and every reported unresolved name
// obeys the ${VAR} grammar.
func FuzzLoad(f *testing.F) {
	for _, seed := range []string{
		"",
		"api_key: ${APP_FUZZ_SECRET}\n",
		"api_key: a\nport: 1\n",
		"prot_typo: x\n",
		"a: b\n---\nc: d\n",
		"api_key: [\n",
		"port: ${APP_FUZZ_SECRET}\n",
		"api_key: *anchor\n",
		"port: notanint\n",
		"\x00",
		"h: ${APP_FUZZ_SECRET}\n",
		"anchor: &k ${APP_FUZZ_SECRET}\nsub:\n  a: x\n  *k : 1\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		const secret = "hunter2-fuzz-load-secret" //gitleaks:allow (planted fixture; the invariant asserts it never survives)
		t.Setenv("APP_FUZZ_SECRET", secret)
		for name, opts := range map[string][]yamlenv.LoadOption{
			"default":     nil,
			"passthrough": {yamlenv.WithErrorPassthrough(claimUnmarshaler)},
		} {
			cfg := fuzzLoadConfig{H: echoingHook{prefix: "cannot construct !!str into "}}
			unresolved, err := yamlenv.Load([]byte(doc), &cfg, allowAppPrefix, opts...)
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("Load(%q) %s error leaks the environment secret: %q", doc, name, err)
			}
			for _, ref := range unresolved {
				if !validName.MatchString(ref) {
					t.Fatalf("unresolved name %q does not match the ${VAR} grammar", ref)
				}
			}
		}
	})
}

// fuzzLoadConfig is FuzzLoad's config type: plain fields beside hooks whose
// errors are worded like go-yaml's or strict-load expanded content.
type fuzzLoadConfig struct {
	APIKey string      `yaml:"api_key"`
	Port   int         `yaml:"port"`
	H      echoingHook `yaml:"h"`
	Sub    strictHook  `yaml:"sub"`
}
