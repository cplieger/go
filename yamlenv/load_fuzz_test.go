package yamlenv_test

import (
	"strings"
	"testing"

	"github.com/cplieger/go/yamlenv"
)

// FuzzLoad pins Load's pipeline invariants for arbitrary config-file bytes:
// it never panics, every returned error's text is value-independent (a fixed
// environment secret never survives into it, whichever pipeline stage
// failed — single-document check, parse, unknown-key probe, or decode), and
// every reported unresolved name obeys the ${VAR} grammar.
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
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		const secret = "hunter2-fuzz-load-secret" //gitleaks:allow (planted fixture; the invariant asserts it never survives)
		t.Setenv("APP_FUZZ_SECRET", secret)
		var cfg struct {
			APIKey string `yaml:"api_key"`
			Port   int    `yaml:"port"`
		}
		unresolved, err := yamlenv.Load([]byte(doc), &cfg, allowAppPrefix)
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Fatalf("Load(%q) error leaks the environment secret: %q", doc, err)
		}
		for _, name := range unresolved {
			if !validName.MatchString(name) {
				t.Fatalf("unresolved name %q does not match the ${VAR} grammar", name)
			}
		}
	})
}
