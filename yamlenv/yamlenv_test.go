package yamlenv_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v4"
)

// allowAppPrefix is the allowlist most tests use: the app's own APP_* names.
func allowAppPrefix(name string) bool { return strings.HasPrefix(name, "APP_") }

// parse unmarshals src into a yaml.Node document tree, failing the test on error.
func parse(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		t.Fatalf("yaml.Unmarshal(%q) failed: %v", src, err)
	}
	return &root
}

// decodeMap decodes an expanded document into a generic map for assertions.
func decodeMap(t *testing.T, root *yaml.Node) map[string]any {
	t.Helper()
	var out map[string]any
	if err := root.Decode(&out); err != nil {
		t.Fatalf("Decode after ExpandWith failed: %v", err)
	}
	return out
}

func TestExpand(t *testing.T) {
	// Not parallel: subtests mutate the process environment via t.Setenv.
	cases := []struct {
		name           string
		src            string
		env            map[string]string
		wantValues     map[string]any
		wantUnresolved []string
	}{
		{
			name:       "allowlisted set reference expands",
			src:        "api_key: ${APP_KEY}\n",
			env:        map[string]string{"APP_KEY": "s3cret"},
			wantValues: map[string]any{"api_key": "s3cret"},
		},
		{
			name:       "non-allowlisted reference stays literal even when set",
			src:        "home: ${OTHER_HOME}\n",
			env:        map[string]string{"OTHER_HOME": "/root"},
			wantValues: map[string]any{"home": "${OTHER_HOME}"},
		},
		{
			name:           "allowlisted unset reference stays literal and is reported",
			src:            "api_key: ${APP_MISSING}\n",
			wantValues:     map[string]any{"api_key": "${APP_MISSING}"},
			wantUnresolved: []string{"APP_MISSING"},
		},
		{
			name:           "unresolved names dedupe in first-seen order",
			src:            "a: ${APP_ONE}\nb: ${APP_TWO}\nc: ${APP_ONE}\n",
			wantValues:     map[string]any{"a": "${APP_ONE}", "b": "${APP_TWO}", "c": "${APP_ONE}"},
			wantUnresolved: []string{"APP_ONE", "APP_TWO"},
		},
		{
			name:       "multiple references in one value",
			src:        "url: http://${APP_HOST}:${APP_PORT}/x\n",
			env:        map[string]string{"APP_HOST": "sonarr", "APP_PORT": "8989"},
			wantValues: map[string]any{"url": "http://sonarr:8989/x"},
		},
		{
			name:       "unbraced reference is never rewritten",
			src:        "v: $APP_KEY\n",
			env:        map[string]string{"APP_KEY": "s3cret"},
			wantValues: map[string]any{"v": "$APP_KEY"},
		},
		{
			name:       "bare dollar and empty braces are untouched",
			src:        "a: cost is 5$\nb: ${}\n",
			wantValues: map[string]any{"a": "cost is 5$", "b": "${}"},
		},
		{
			name:       "empty-but-set variable substitutes an empty string",
			src:        "v: ${APP_BLANK}\n",
			env:        map[string]string{"APP_BLANK": ""},
			wantValues: map[string]any{"v": ""},
		},
		{
			name:       "nested mappings and sequences expand",
			src:        "arr:\n  keys:\n    - ${APP_KEY}\n    - literal\n",
			env:        map[string]string{"APP_KEY": "s3cret"},
			wantValues: map[string]any{"arr": map[string]any{"keys": []any{"s3cret", "literal"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			root := parse(t, tc.src)
			unresolved := yamlenv.ExpandWith(root, allowAppPrefix, os.LookupEnv)
			if !slices.Equal(unresolved, tc.wantUnresolved) {
				t.Errorf("ExpandWith unresolved = %v, want %v", unresolved, tc.wantUnresolved)
			}
			got := decodeMap(t, root)
			for k, want := range tc.wantValues {
				if !equalValue(got[k], want) {
					t.Errorf("value %q = %#v, want %#v", k, got[k], want)
				}
			}
		})
	}
}

// equalValue compares decoded YAML values deeply enough for these tables
// (strings, nested maps, and slices of strings).
func equalValue(got, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			if !equalValue(g[k], wv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !equalValue(g[i], w[i]) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}

func TestExpandLeavesMappingKeysLiteral(t *testing.T) {
	t.Setenv("APP_KEY", "expanded")
	root := parse(t, "${APP_KEY}: value\n")
	yamlenv.ExpandWith(root, allowAppPrefix, os.LookupEnv)
	var out map[string]string
	if err := root.Decode(&out); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if _, ok := out["${APP_KEY}"]; !ok {
		t.Errorf("mapping key was rewritten: %v (keys must stay literal)", out)
	}
}

func TestExpandLeavesNonStringScalarsUntouched(t *testing.T) {
	t.Setenv("APP_KEY", "zzz")
	src := "count: 5\nflag: true\nratio: 1.5\nnothing: null\n"
	root := parse(t, src)
	yamlenv.ExpandWith(root, allowAppPrefix, os.LookupEnv)
	out := decodeMap(t, root)
	if out["count"] != 5 || out["flag"] != true || out["ratio"] != 1.5 || out["nothing"] != nil {
		t.Errorf("non-string scalars changed: %#v", out)
	}
}

// TestExpandCannotChangeDocumentStructure is the reason this package exists:
// an environment value full of YAML syntax lands as an inert string value and
// cannot inject keys, comments, or truncation the way pre-parse text expansion
// can.
func TestExpandCannotChangeDocumentStructure(t *testing.T) {
	evil := "x\"\ninjected_key: pwned\n# comment"
	t.Setenv("APP_EVIL", evil)
	root := parse(t, "api_key: ${APP_EVIL}\nother: keep\n")
	yamlenv.ExpandWith(root, allowAppPrefix, os.LookupEnv)
	out := decodeMap(t, root)
	if len(out) != 2 {
		t.Fatalf("document grew to %d keys: %#v (structure injection)", len(out), out)
	}
	if out["api_key"] != evil {
		t.Errorf("api_key = %q, want the raw environment value %q", out["api_key"], evil)
	}
	if out["other"] != "keep" {
		t.Errorf("sibling key disturbed: %#v", out)
	}
}

func TestExpandIsSinglePass(t *testing.T) {
	// A ${VAR} arriving FROM an expanded value is neither re-expanded nor
	// reported, whether or not its name is set: it is not a reference the
	// document holds.
	t.Run("introduced ref naming a set variable is kept literal, not reported", func(t *testing.T) {
		t.Setenv("APP_OUTER", "${APP_INNER}")
		t.Setenv("APP_INNER", "never-substituted")
		root := parse(t, "v: ${APP_OUTER}\n")
		unresolved := yamlenv.ExpandWith(root, allowAppPrefix, os.LookupEnv)
		out := decodeMap(t, root)
		if out["v"] != "${APP_INNER}" {
			t.Errorf("v = %q, want the literal ${APP_INNER} (single-pass, no recursion)", out["v"])
		}
		if len(unresolved) != 0 {
			t.Errorf("unresolved = %v, want none (APP_INNER is not in the document)", unresolved)
		}
	})
	t.Run("introduced ref naming an unset variable is kept literal, not reported", func(t *testing.T) {
		t.Setenv("APP_OUTER", "${APP_INNER}")
		root := parse(t, "v: ${APP_OUTER}\n")
		unresolved := yamlenv.ExpandWith(root, allowAppPrefix, os.LookupEnv)
		out := decodeMap(t, root)
		if out["v"] != "${APP_INNER}" {
			t.Errorf("v = %q, want the literal ${APP_INNER} (single-pass, no recursion)", out["v"])
		}
		if len(unresolved) != 0 {
			t.Errorf("unresolved = %v, want none (APP_INNER is not in the document)", unresolved)
		}
	})
}

func allowAll(string) bool { return true }

func mapLookup(m map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func TestExpandWithDoesNotRescanResolvedValue(t *testing.T) {
	t.Parallel()
	root := parse(t, "pass: ${SUBFLUX_PASS}\nboth: ${SUBFLUX_PASS} ${SUBFLUX_MISSING}\nmissing: ${SUBFLUX_MISSING}\n")
	unresolved := yamlenv.ExpandWith(root, allowAll, mapLookup(map[string]string{"SUBFLUX_PASS": "a${b}"}))
	if !slices.Equal(unresolved, []string{"SUBFLUX_MISSING"}) {
		t.Errorf("unresolved = %v, want [SUBFLUX_MISSING] (b is inside a resolved value)", unresolved)
	}
	out := decodeMap(t, root)
	if out["pass"] != "a${b}" {
		t.Errorf("pass = %q, want the resolved value a${b} unchanged", out["pass"])
	}
	if out["both"] != "a${b} ${SUBFLUX_MISSING}" {
		t.Errorf("both = %q, want a${b} beside the literal ${SUBFLUX_MISSING}", out["both"])
	}
	if out["missing"] != "${SUBFLUX_MISSING}" {
		t.Errorf("missing = %q, want the literal reference", out["missing"])
	}
}

func TestExpandWithNeverLooksUpRejectedName(t *testing.T) {
	t.Parallel()
	var asked []string
	lookup := func(name string) (string, bool) {
		asked = append(asked, name)
		return "found", true
	}
	root := parse(t, "allowed: ${APP_A}\nrejected: ${OTHER_SECRET}\n")
	if got := yamlenv.ExpandWith(root, allowAppPrefix, lookup); got != nil {
		t.Errorf("unresolved = %v, want nil", got)
	}
	if !slices.Equal(asked, []string{"APP_A"}) {
		t.Errorf("lookup asked for %v, want only [APP_A] (a rejected name must never reach lookup)", asked)
	}
	out := decodeMap(t, root)
	if out["allowed"] != "found" || out["rejected"] != "${OTHER_SECRET}" {
		t.Errorf("expanded = %#v, want allowed=found and rejected kept literal", out)
	}
}

func TestExpandWithReadsLookupOnly(t *testing.T) {
	// Not parallel: t.Setenv proves the process environment is never read.
	t.Setenv("SUBFLUX_X", "env")
	t.Setenv("SUBFLUX_ENV_ONLY", "env")
	root := parse(t, "x: ${SUBFLUX_X}\nenv_only: ${SUBFLUX_ENV_ONLY}\n")
	unresolved := yamlenv.ExpandWith(root, allowAll, mapLookup(map[string]string{"SUBFLUX_X": "map"}))
	out := decodeMap(t, root)
	if out["x"] != "map" {
		t.Errorf("x = %q, want the lookup's value map", out["x"])
	}
	if out["env_only"] != "${SUBFLUX_ENV_ONLY}" {
		t.Errorf("env_only = %q, want the literal reference (set only in the environment)", out["env_only"])
	}
	if !slices.Equal(unresolved, []string{"SUBFLUX_ENV_ONLY"}) {
		t.Errorf("unresolved = %v, want [SUBFLUX_ENV_ONLY]", unresolved)
	}
}

func TestExpandWithLookupAlone(t *testing.T) {
	t.Parallel()
	root := parse(t, "a: ${APP_A}\nblank: ${APP_BLANK}\nother: ${OTHER_A}\n")
	lookup := mapLookup(map[string]string{"APP_A": "one", "APP_BLANK": "", "OTHER_A": "refused"})
	if got := yamlenv.ExpandWith(root, allowAppPrefix, lookup); got != nil {
		t.Errorf("unresolved = %v, want nil", got)
	}
	out := decodeMap(t, root)
	if out["a"] != "one" || out["blank"] != "" || out["other"] != "${OTHER_A}" {
		t.Errorf("expanded = %#v, want a=one, blank empty, other literal (not allowed)", out)
	}
}

// TestExpandedNodeKeepsStringTag pins the package doc's limit: an expanded
// reference stays !!str, so it never decodes into an int field.
func TestExpandedNodeKeepsStringTag(t *testing.T) {
	t.Parallel()
	root := parse(t, "port: ${APP_PORT}\n")
	yamlenv.ExpandWith(root, allowAppPrefix, mapLookup(map[string]string{"APP_PORT": "8080"}))
	var asInt struct {
		Port int `yaml:"port"`
	}
	if err := root.Decode(&asInt); err == nil {
		t.Errorf("Decode into int = nil error (Port %d), want a !!str type error", asInt.Port)
	}
	var asString struct {
		Port string `yaml:"port"`
	}
	if err := root.Decode(&asString); err != nil || asString.Port != "8080" {
		t.Errorf("Decode into string = (%q, %v), want (8080, nil)", asString.Port, err)
	}
}

func TestExpandAnchorsExpandOnce(t *testing.T) {
	t.Setenv("APP_KEY", "shared")
	root := parse(t, "a: &k ${APP_KEY}\nb: *k\n")
	yamlenv.ExpandWith(root, allowAppPrefix, os.LookupEnv)
	out := decodeMap(t, root)
	if out["a"] != "shared" || out["b"] != "shared" {
		t.Errorf("anchor/alias expansion = %#v, want both fields shared", out)
	}
}

func TestExpandNilInputs(t *testing.T) {
	t.Parallel()
	if got := yamlenv.ExpandWith(nil, allowAppPrefix, os.LookupEnv); got != nil {
		t.Errorf("ExpandWith(nil root) = %v, want nil", got)
	}
	root := parse(t, "v: ${APP_X}\n")
	if got := yamlenv.ExpandWith(root, nil, os.LookupEnv); got != nil {
		t.Errorf("ExpandWith(nil allow) = %v, want nil", got)
	}
	out := decodeMap(t, root)
	if out["v"] != "${APP_X}" {
		t.Errorf("nil allow rewrote the document: %#v", out)
	}
	if got := yamlenv.ExpandWith(nil, allowAll, mapLookup(nil)); got != nil {
		t.Errorf("ExpandWith(nil root) = %v, want nil", got)
	}
	if got := yamlenv.ExpandWith(root, allowAll, nil); got != nil {
		t.Errorf("ExpandWith(nil lookup) = %v, want nil", got)
	}
	if out := decodeMap(t, root); out["v"] != "${APP_X}" {
		t.Errorf("nil lookup rewrote the document: %#v", out)
	}
}
