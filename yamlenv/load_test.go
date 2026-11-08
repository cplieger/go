package yamlenv_test

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v4"
)

// loadConfig is the config shape most Load tests decode into: a plain string,
// a typed int, and a nested level, so unknown-key detection below the top
// level and wrong-type artifacts are both reachable.
type loadConfig struct {
	APIKey string `yaml:"api_key"`
	Port   int    `yaml:"port"`
	Nested struct {
		Level int `yaml:"level"`
	} `yaml:"nested"`
}

// strictDuration mimics a consumer config type whose UnmarshalYAML rejects
// anything time.ParseDuration cannot read, with an app-owned, value-withheld
// error vocabulary (the subflux Duration contract). Against raw pre-expansion
// bytes it fails on a literal ${VAR}, which is exactly the probe artifact
// Load's unknown-key filtering must ignore.
type strictDuration struct {
	d time.Duration
}

// UnmarshalYAML parses a Go duration string and withholds the offending value
// from its error (app-owned vocabulary, value-safe by construction).
func (s *strictDuration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration (value withheld)", value.Line)
	}
	s.d = parsed
	return nil
}

// durConfig is the custom-unmarshaler config shape (the probe-immunity cases).
type durConfig struct {
	Timeout strictDuration `yaml:"timeout"`
	Name    string         `yaml:"name"`
}

// claimUnmarshaler is the recommended WithErrorPassthrough predicate: it
// claims exactly the errors Load classified as the config type's own.
func claimUnmarshaler(err error) bool {
	_, ok := errors.AsType[*yamlenv.UnmarshalerError](err)
	return ok
}

func TestLoad(t *testing.T) {
	// Not parallel: subtests mutate the process environment via t.Setenv.
	t.Run("expands references and keeps defaults for absent keys", func(t *testing.T) {
		t.Setenv("APP_KEY", "s3cret-value")
		cfg := loadConfig{Port: 9090}
		unresolved, err := yamlenv.Load([]byte("api_key: ${APP_KEY}\n"), &cfg, allowAppPrefix)
		if err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		if len(unresolved) != 0 {
			t.Fatalf("unresolved = %v, want none", unresolved)
		}
		if cfg.APIKey != "s3cret-value" {
			t.Errorf("APIKey = %q, want the expanded value", cfg.APIKey)
		}
		if cfg.Port != 9090 {
			t.Errorf("Port = %d, want the pre-set default 9090", cfg.Port)
		}
	})

	t.Run("misspelled key fails loudly, redacted by default", func(t *testing.T) {
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("api_key: a\nprot_typo: x\n"), &cfg, allowAppPrefix)
		if err == nil {
			t.Fatal("Load = nil, want unknown-key error")
		}
		if !strings.Contains(err.Error(), "unknown configuration key") {
			t.Errorf("err = %q, want the unknown-key rewrite", err)
		}
		if strings.Contains(err.Error(), "prot_typo") {
			t.Errorf("err = %q, want the key name redacted by default", err)
		}
	})

	t.Run("unknown-key echo opts in through WithSanitizeOptions", func(t *testing.T) {
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("prot_typo: x\n"), &cfg, allowAppPrefix,
			yamlenv.WithSanitizeOptions(yamlenv.WithUnknownKeyEcho(true)))
		if err == nil || !strings.Contains(err.Error(), `unknown configuration key "prot_typo"`) {
			t.Fatalf("err = %v, want the echoed key name", err)
		}
	})

	t.Run("second document fails with the static sentinel", func(t *testing.T) {
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("api_key: a\n---\nport: 1\n"), &cfg, allowAppPrefix)
		if !errors.Is(err, yamlenv.ErrMultipleDocuments) {
			t.Fatalf("Load = %v, want ErrMultipleDocuments", err)
		}
	})

	t.Run("multiple documents win over unknown keys", func(t *testing.T) {
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("prot_typo: x\n---\nport: 1\n"), &cfg, allowAppPrefix)
		if !errors.Is(err, yamlenv.ErrMultipleDocuments) {
			t.Fatalf("Load = %v, want ErrMultipleDocuments before key strictness", err)
		}
	})

	t.Run("unresolved names identical to ExpandWith", func(t *testing.T) {
		src := []byte("api_key: ${APP_UNSET_ONE}\nnested:\n  level: 1\nother: ${APP_UNSET_TWO}\n")
		var direct yaml.Node
		if err := yaml.Unmarshal(src, &direct); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		want := yamlenv.ExpandWith(&direct, allowAppPrefix, os.LookupEnv)

		var cfg struct {
			APIKey string `yaml:"api_key"`
			Nested struct {
				Level int `yaml:"level"`
			} `yaml:"nested"`
			Other string `yaml:"other"`
		}
		got, err := yamlenv.Load(src, &cfg, allowAppPrefix)
		if err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("Load unresolved = %v, want ExpandWith's %v", got, want)
		}
	})

	t.Run("empty document keeps the caller's defaults", func(t *testing.T) {
		cfg := loadConfig{APIKey: "default-key", Port: 8080}
		for _, src := range []string{"", "# comment only\n"} {
			unresolved, err := yamlenv.Load([]byte(src), &cfg, allowAppPrefix)
			if err != nil || unresolved != nil {
				t.Fatalf("Load(%q) = (%v, %v), want (nil, nil)", src, unresolved, err)
			}
			if cfg.APIKey != "default-key" || cfg.Port != 8080 {
				t.Errorf("Load(%q) mutated out: %+v", src, cfg)
			}
		}
	})

	t.Run("literal ${VAR} in a custom-unmarshaler field cannot false-fail the probe", func(t *testing.T) {
		t.Setenv("APP_TIMEOUT", "5s")
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: ${APP_TIMEOUT}\nname: n\n"), &cfg, allowAppPrefix)
		if err != nil {
			t.Fatalf("Load = %v, want nil (probe must ignore the pre-expansion literal)", err)
		}
		if cfg.Timeout.d != 5*time.Second {
			t.Errorf("Timeout = %v, want the expanded 5s", cfg.Timeout.d)
		}
	})

	t.Run("unknown key still detected beside a wrong-type probe artifact", func(t *testing.T) {
		t.Setenv("APP_PORT", "8080")
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("port: ${APP_PORT}\ntypo_key: x\n"), &cfg, allowAppPrefix,
			yamlenv.WithSanitizeOptions(yamlenv.WithUnknownKeyEcho(true)))
		if err == nil || !strings.Contains(err.Error(), `unknown configuration key "typo_key"`) {
			t.Fatalf("err = %v, want the unknown-key finding to survive the artifact filter", err)
		}
		if strings.Contains(err.Error(), "cannot unmarshal") {
			t.Errorf("err = %q, want the pre-expansion wrong-type artifact dropped", err)
		}
	})

	t.Run("app-owned decode error passes through under the caller's predicate", func(t *testing.T) {
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: notaduration\n"), &cfg, allowAppPrefix,
			yamlenv.WithErrorPassthrough(claimUnmarshaler))
		if err == nil || !strings.Contains(err.Error(), "invalid duration (value withheld)") {
			t.Fatalf("err = %v, want the app-owned vocabulary unchanged", err)
		}
	})

	t.Run("app-owned decode error is sanitized without the predicate", func(t *testing.T) {
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: notaduration\n"), &cfg, allowAppPrefix)
		if err == nil {
			t.Fatal("Load = nil, want decode error")
		}
		if strings.Contains(err.Error(), "invalid duration") {
			t.Errorf("err = %q, want the default fail-closed rewrite, not the app vocabulary", err)
		}
	})

	t.Run("nil allow expands nothing and reports nothing", func(t *testing.T) {
		t.Setenv("APP_KEY", "set-but-not-allowed")
		var cfg loadConfig
		unresolved, err := yamlenv.Load([]byte("api_key: ${APP_KEY}\n"), &cfg, nil)
		if err != nil || unresolved != nil {
			t.Fatalf("Load = (%v, %v), want (nil, nil)", unresolved, err)
		}
		if cfg.APIKey != "${APP_KEY}" {
			t.Errorf("APIKey = %q, want the literal reference kept", cfg.APIKey)
		}
	})

	t.Run("WithLookupEnv replaces the environment for expansion and unresolved names", func(t *testing.T) {
		t.Setenv("APP_KEY", "from-env")
		t.Setenv("APP_ENV_ONLY", "from-env")
		var cfg struct {
			APIKey  string `yaml:"api_key"`
			EnvOnly string `yaml:"env_only"`
			Inner   string `yaml:"inner"`
		}
		lookup := mapLookup(map[string]string{"APP_KEY": "from-lookup", "APP_PASS": "a${APP_UNSET}"})
		src := "api_key: ${APP_KEY}\nenv_only: ${APP_ENV_ONLY}\ninner: ${APP_PASS}\n"
		unresolved, err := yamlenv.Load([]byte(src), &cfg, allowAppPrefix, yamlenv.WithLookupEnv(lookup))
		if err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		if cfg.APIKey != "from-lookup" {
			t.Errorf("APIKey = %q, want the lookup's value", cfg.APIKey)
		}
		if cfg.EnvOnly != "${APP_ENV_ONLY}" {
			t.Errorf("EnvOnly = %q, want the literal reference (set only in the environment)", cfg.EnvOnly)
		}
		if cfg.Inner != "a${APP_UNSET}" {
			t.Errorf("Inner = %q, want the resolved value unchanged", cfg.Inner)
		}
		if !slices.Equal(unresolved, []string{"APP_ENV_ONLY"}) {
			t.Errorf("unresolved = %v, want [APP_ENV_ONLY] (APP_UNSET is inside a resolved value)", unresolved)
		}
	})

	t.Run("the last WithLookupEnv wins", func(t *testing.T) {
		var cfg loadConfig
		first := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_KEY": "first"}))
		second := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_KEY": "second"}))
		unresolved, err := yamlenv.Load([]byte("api_key: ${APP_KEY}\n"), &cfg, allowAppPrefix, first, second)
		if err != nil || unresolved != nil {
			t.Fatalf("Load = (%v, %v), want (nil, nil)", unresolved, err)
		}
		if cfg.APIKey != "second" {
			t.Errorf("APIKey = %q, want the second lookup's value", cfg.APIKey)
		}
	})

	t.Run("WithLookupEnv(nil) keeps the process environment", func(t *testing.T) {
		t.Setenv("APP_KEY", "from-env")
		var cfg loadConfig
		unresolved, err := yamlenv.Load([]byte("api_key: ${APP_KEY}\n"), &cfg, allowAppPrefix, yamlenv.WithLookupEnv(nil))
		if err != nil || unresolved != nil {
			t.Fatalf("Load = (%v, %v), want (nil, nil)", unresolved, err)
		}
		if cfg.APIKey != "from-env" {
			t.Errorf("APIKey = %q, want the environment's value", cfg.APIKey)
		}
	})

	t.Run("a last WithLookupEnv(nil) restores the process environment", func(t *testing.T) {
		t.Setenv("APP_KEY", "from-env")
		var cfg loadConfig
		custom := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_KEY": "from-lookup"}))
		unresolved, err := yamlenv.Load([]byte("api_key: ${APP_KEY}\n"), &cfg, allowAppPrefix, custom, yamlenv.WithLookupEnv(nil))
		if err != nil || unresolved != nil {
			t.Fatalf("Load = (%v, %v), want (nil, nil)", unresolved, err)
		}
		if cfg.APIKey != "from-env" {
			t.Errorf("APIKey = %q, want the environment's value (the nil option is last)", cfg.APIKey)
		}
	})

	t.Run("out must be a non-nil pointer", func(t *testing.T) {
		cases := []struct {
			name string
			out  any
		}{
			{name: "nil interface", out: nil},
			{name: "nil typed pointer", out: (*loadConfig)(nil)},
			{name: "non-pointer", out: loadConfig{}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := yamlenv.Load([]byte("api_key: a\n"), tc.out, allowAppPrefix)
				if err == nil || !strings.Contains(err.Error(), "non-nil pointer") {
					t.Errorf("Load(out=%v) = %v, want the misuse error", tc.out, err)
				}
			})
		}
	})
}

// relayDecode is a config type whose UnmarshalYAML wraps go-yaml's own decode
// error with %w, so the cause chain reaches a *yaml.LoadErrors.
type relayDecode struct{ n int }

// UnmarshalYAML decodes an int and wraps go-yaml's error on failure.
func (r *relayDecode) UnmarshalYAML(value *yaml.Node) error {
	if err := value.Decode(&r.n); err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	return nil
}

// relayConfig holds a relayDecode.
type relayConfig struct {
	N relayDecode `yaml:"n"`
}

// scriptedHook is a config type whose UnmarshalYAML fails with the message
// the caller's defaults preset, so a test can spell it like go-yaml's own.
type scriptedHook struct{ msg string }

// UnmarshalYAML fails with the preset message.
func (h *scriptedHook) UnmarshalYAML(*yaml.Node) error { return errors.New(h.msg) }

// scriptedConfig holds a scriptedHook.
type scriptedConfig struct {
	H scriptedHook `yaml:"h"`
}

// copyingHook is a config type whose UnmarshalYAML runs a go-yaml call and
// returns copyErr's copy of its error. Its zero value, the one the unknown-key
// probe decodes into, accepts anything.
type copyingHook struct {
	call    func(*yaml.Node) error
	copyErr func(error) error
}

// UnmarshalYAML runs the preset call and returns its error's copy.
func (h *copyingHook) UnmarshalYAML(value *yaml.Node) error {
	if h.call == nil {
		return nil
	}
	if err := h.call(value); err != nil {
		return h.copyErr(err)
	}
	return nil
}

// copyingConfig holds a copyingHook.
type copyingConfig struct {
	H copyingHook `yaml:"h"`
}

// echoingHook is a config type whose UnmarshalYAML fails with its node's
// value between a preset prefix and suffix, so its error can put an expanded
// secret where go-yaml's wording keeps a value-free part.
type echoingHook struct{ prefix, suffix string }

// UnmarshalYAML fails with the value inside the preset wording.
func (h *echoingHook) UnmarshalYAML(value *yaml.Node) error {
	return errors.New(h.prefix + value.Value + h.suffix)
}

// echoingConfig holds an echoingHook.
type echoingConfig struct {
	H echoingHook `yaml:"h"`
}

// strictHook is a config type whose UnmarshalYAML strict-loads its mapping
// once no literal ${ remains, the way go-yaml v4's Node.Load docs suggest for
// hooks. The unknown-key probe sees the literal and passes.
type strictHook struct {
	S struct {
		A string `yaml:"a"`
	}
}

// UnmarshalYAML skips a pre-expansion node and strict-loads an expanded one.
func (h *strictHook) UnmarshalYAML(value *yaml.Node) error {
	if hasLiteralRef(value) {
		return nil
	}
	return value.Load(&h.S, yaml.WithKnownFields())
}

// hasLiteralRef reports whether any scalar under n, an alias's target
// included, still holds a literal ${.
func hasLiteralRef(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return hasLiteralRef(n.Alias)
	}
	if strings.Contains(n.Value, "${") {
		return true
	}
	return slices.ContainsFunc(n.Content, hasLiteralRef)
}

// strictHookConfig holds a strictHook under sub, beside the anchor it aliases.
type strictHookConfig struct {
	Anchor string     `yaml:"anchor"`
	Sub    strictHook `yaml:"sub"`
}

// forgedUnknownKey is a config type whose UnmarshalYAML always fails with its
// node's value in go-yaml's unknown-field wording, so the pre-expansion probe
// sees a hook-authored message in the shape the unknown-key echo keeps.
type forgedUnknownKey struct{}

// UnmarshalYAML fails with the value worded as an unknown field.
func (forgedUnknownKey) UnmarshalYAML(value *yaml.Node) error {
	return fmt.Errorf("field %s not found in type main.cfg", value.Value)
}

// forgedConfig holds a forgedUnknownKey beside a real key.
type forgedConfig struct {
	Name string           `yaml:"name"`
	H    forgedUnknownKey `yaml:"h"`
}

// treeNode is a recursive config type, so the walk over the types a config
// reaches must stop on a revisit.
type treeNode struct {
	Children []treeNode `yaml:"children"`
}

// TestLoadClassifiesUnmarshalerErrors pins the typed classification of the
// decode step: a failure produced only by the config type's own UnmarshalYAML
// is claimable as *yamlenv.UnmarshalerError with the app's message verbatim,
// while go-yaml's own errors, alone or mixed with a hook's, never are.
func TestLoadClassifiesUnmarshalerErrors(t *testing.T) {
	t.Parallel()

	t.Run("app unmarshaler failure is claimable by type", func(t *testing.T) {
		t.Parallel()
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: notaduration\n"), &cfg, allowAppPrefix,
			yamlenv.WithErrorPassthrough(claimUnmarshaler))
		ue, ok := errors.AsType[*yamlenv.UnmarshalerError](err)
		if !ok {
			t.Fatalf("Load = %v (%T), want *UnmarshalerError reachable with errors.As", err, err)
		}
		if want := "line 1: invalid duration (value withheld)"; err.Error() != want {
			t.Errorf("Error() = %q, want the app vocabulary verbatim: %q", err.Error(), want)
		}
		if ue.Err == nil || ue.Err.Error() != err.Error() {
			t.Errorf("Err = %v, want the unmarshaler's error unchanged", ue.Err)
		}
		if _, ok := errors.AsType[*yaml.LoadError](err); ok {
			t.Errorf("errors.As reaches go-yaml's *LoadError through the wrap: %v", err)
		}
	})

	t.Run("a hook decoding through go-yaml succeeds", func(t *testing.T) {
		t.Parallel()
		var cfg relayConfig
		if _, err := yamlenv.Load([]byte("n: 5\n"), &cfg, allowAppPrefix); err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		if cfg.N.n != 5 {
			t.Errorf("N = %d, want 5", cfg.N.n)
		}
	})

	t.Run("several app failures are all reachable", func(t *testing.T) {
		t.Parallel()
		var cfg struct {
			A strictDuration `yaml:"a"`
			B strictDuration `yaml:"b"`
		}
		_, err := yamlenv.Load([]byte("a: x\nb: y\n"), &cfg, allowAppPrefix,
			yamlenv.WithErrorPassthrough(claimUnmarshaler))
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); !ok {
			t.Fatalf("Load = %v (%T), want *UnmarshalerError", err, err)
		}
		want := "line 1: invalid duration (value withheld)\nline 2: invalid duration (value withheld)"
		if err.Error() != want {
			t.Errorf("Error() = %q, want both app messages in document order %q", err.Error(), want)
		}
	})

	t.Run("reachable through a consumer's own wrap", func(t *testing.T) {
		t.Parallel()
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: notaduration\n"), &cfg, allowAppPrefix,
			yamlenv.WithErrorPassthrough(claimUnmarshaler))
		wrapped := fmt.Errorf("parse YAML: %w", err)
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](wrapped); !ok {
			t.Errorf("errors.As through a consumer wrap = false, want the type reachable: %v", wrapped)
		}
	})

	// Every case here is a decode the caller claims through its passthrough,
	// so a misclassification would return go-yaml's message unsanitized.
	for name, tc := range map[string]struct {
		doc    string
		out    any
		secret string
	}{
		"wrong type": {
			doc: "port: notanint\n", out: &loadConfig{}, secret: "notanint",
		},
		"invalid base64": {
			doc: "api_key: !!binary |\n  @@@not-base64@@@\n", out: &struct {
				APIKey []byte `yaml:"api_key"`
			}{}, secret: "not-base64",
		},
		"app failure beside a wrong type": {
			doc: "timeout: x\nport: hunter2-mixed\n", out: &struct {
				Timeout strictDuration `yaml:"timeout"`
				Port    int            `yaml:"port"`
			}{}, secret: "hunter2",
		},
		"hook relaying go-yaml's error": {
			doc: "timeout: [hunter2-relayed]\n", out: &durConfig{}, secret: "hunter2",
		},
		"hook wrapping go-yaml's error": {
			doc: "n: hunter2-wrapped\n", out: &relayConfig{}, secret: "hunter2",
		},
		"resolver error": {
			doc: "port: !!int hunter2-resolver\n", out: &loadConfig{}, secret: "hunter2",
		},
	} {
		t.Run("go-yaml's own error is never claimable: "+name, func(t *testing.T) {
			t.Parallel()
			_, err := yamlenv.Load([]byte(tc.doc), tc.out, allowAppPrefix,
				yamlenv.WithErrorPassthrough(claimUnmarshaler))
			if err == nil {
				t.Fatal("Load = nil, want a decode error")
			}
			if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
				t.Errorf("go-yaml's own error was classified app-owned: %v", err)
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Errorf("err = %q, leaked the document fragment %q", err, tc.secret)
			}
		})
	}

	t.Run("a hook error spelled like go-yaml's is never claimable", func(t *testing.T) {
		t.Parallel()
		cfg := scriptedConfig{H: scriptedHook{msg: "cannot construct !!str `hunter2` into hunter2-SECRET-into-LEAKVALUE"}}
		_, err := yamlenv.Load([]byte("h: x\n"), &cfg, allowAppPrefix,
			yamlenv.WithErrorPassthrough(claimUnmarshaler))
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
			t.Errorf("a go-yaml-shaped hook error was classified app-owned: %v", err)
		}
		if want := "decode errors: line 1, column 4: cannot construct !!str <redacted> into <redacted>"; err == nil || err.Error() != want {
			t.Errorf("err = %v, want %q", err, want)
		}
	})

	// A config type's error copying go-yaml's text has lost the *LoadError
	// chain, so only the text says it is go-yaml's; claiming it would return
	// go-yaml's excerpt of the value.
	shortSecret := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_SECRET": "shortsec"}))
	decodeInt := func(n *yaml.Node) error { var i int; return n.Decode(&i) }
	decodeAny := func(n *yaml.Node) error { var v any; return n.Decode(&v) }
	loadAnchorRef := func(n *yaml.Node) error { var v any; return yaml.Load([]byte("x: *"+n.Value), &v) }
	loadValue := func(n *yaml.Node) error { var v any; return yaml.Load([]byte(n.Value), &v) }
	loadCycle := func(n *yaml.Node) error {
		var v any
		return yaml.Load([]byte("&"+n.Value+" [*"+n.Value+"]"), &v)
	}
	withContext := func(err error) error { return fmt.Errorf("ctx: %v", err) }
	byText := func(err error) error { return errors.New(err.Error()) }
	byMessage := func(err error) error {
		if le, ok := errors.AsType[*yaml.LoadError](err); ok {
			return errors.New(le.Message)
		}
		return err
	}
	verbatim := func(err error) error { return err }
	for name, tc := range map[string]struct {
		doc     string
		call    func(*yaml.Node) error
		copyErr func(error) error
	}{
		"%v of a wrong-type decode":             {doc: "h: ${APP_SECRET}\n", call: decodeInt, copyErr: withContext},
		"Error() text of a wrong-type decode":   {doc: "h: ${APP_SECRET}\n", call: decodeInt, copyErr: byText},
		"Message of a wrong-type decode":        {doc: "h: ${APP_SECRET}\n", call: decodeInt, copyErr: byMessage},
		"Message of a resolver error":           {doc: "h: !!int shortsec\n", call: decodeAny, copyErr: byMessage},
		"Message of an unhashable key":          {doc: "h: {[shortsec]: 1}\n", call: decodeAny, copyErr: byMessage},
		"Message of an unknown anchor":          {doc: "h: ${APP_SECRET}\n", call: loadAnchorRef, copyErr: byMessage},
		"%v of a nested load error":             {doc: "h: ${APP_SECRET}\n", call: loadAnchorRef, copyErr: withContext},
		"go-yaml's no-documents error relayed":  {doc: "h: '# shortsec'\n", call: loadValue, copyErr: verbatim},
		"%v of go-yaml's no-documents error":    {doc: "h: '# shortsec'\n", call: loadValue, copyErr: withContext},
		"Error() text of a nested load failure": {doc: "h: ${APP_SECRET}\n", call: loadAnchorRef, copyErr: byText},
		"Message of an alias cycle":             {doc: "h: ${APP_SECRET}\n", call: loadCycle, copyErr: byMessage},
	} {
		t.Run("a copy of go-yaml's error is never claimable: "+name, func(t *testing.T) {
			t.Parallel()
			cfg := copyingConfig{H: copyingHook{call: tc.call, copyErr: tc.copyErr}}
			_, err := yamlenv.Load([]byte(tc.doc), &cfg, allowAppPrefix, shortSecret,
				yamlenv.WithErrorPassthrough(claimUnmarshaler))
			if err == nil {
				t.Fatal("Load = nil, want a decode error")
			}
			if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
				t.Errorf("a copy of go-yaml's error was classified app-owned: %v", err)
			}
			if strings.Contains(err.Error(), "shortsec") {
				t.Errorf("err = %q, leaked the value", err)
			}
		})
	}

	// A hook that loads its expanded value as YAML gets go-yaml messages that
	// quote a character, a byte, a count or a key of that value.
	mergeConflict, optErr := yaml.OptsYAML("plugin: {merge-key: {conflicts: error}}")
	if optErr != nil {
		t.Fatalf("OptsYAML: %v", optErr)
	}
	loadArray := func(n *yaml.Node) error { var a [2]int; return yaml.Load([]byte(n.Value), &a) }
	loadMerge := func(n *yaml.Node) error {
		var v any
		doc := "a: &a {" + n.Value + ": 1}\nb:\n  <<: [*a, {" + n.Value + ": 2}]\n"
		return yaml.Load([]byte(doc), &v, mergeConflict)
	}
	for name, tc := range map[string]struct {
		secret string
		call   func(*yaml.Node) error
		leak   string
	}{
		"tag character":      {secret: "!aZ{q", call: loadValue, leak: "'{'"},
		"control character":  {secret: "a\x01b", call: loadValue, leak: "(value: 1)"},
		"invalid UTF-8":      {secret: "\xffzz", call: loadValue, leak: "(value: 255)"},
		"array length":       {secret: "[1, 2, 3]", call: loadArray, leak: "got 3"},
		"merge-key conflict": {secret: "mergesec", call: loadMerge, leak: "mergesec"},
	} {
		t.Run("a copy of go-yaml's value-quoting message is never claimable: "+name, func(t *testing.T) {
			t.Parallel()
			if err := tc.call(&yaml.Node{Kind: yaml.ScalarNode, Value: tc.secret}); err == nil ||
				!strings.Contains(err.Error(), tc.leak) {
				t.Fatalf("precondition: go-yaml's message %v does not quote %q", err, tc.leak)
			}
			cfg := copyingConfig{H: copyingHook{call: tc.call, copyErr: byMessage}}
			_, err := yamlenv.Load([]byte("h: ${APP_SECRET}\n"), &cfg, allowAppPrefix,
				yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_SECRET": tc.secret})),
				yamlenv.WithErrorPassthrough(claimUnmarshaler))
			if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
				t.Errorf("a copy of go-yaml's message was classified app-owned: %v", err)
			}
			const want = "decode errors: line 1, column 4: value rejected (details withheld: they may embed an expanded secret)"
			if err == nil || err.Error() != want {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}

	t.Run("the app's own error type claims nothing else", func(t *testing.T) {
		t.Parallel()
		claimPort := func(err error) bool { _, ok := errors.AsType[*portError](err); return ok }
		secret := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_SECRET": "hunter2-addr"}))
		load := func(doc string, pred func(error) bool) error {
			var cfg struct {
				Port port       `yaml:"port"`
				Addr netip.Addr `yaml:"addr"`
			}
			_, err := yamlenv.Load([]byte(doc), &cfg, allowAppPrefix, secret, yamlenv.WithErrorPassthrough(pred))
			if err == nil {
				t.Fatalf("Load(%q) = nil (port %d, addr %v), want a decode error", doc, cfg.Port.n, cfg.Addr)
			}
			return err
		}
		if err := load("port: ${APP_SECRET}\n", claimPort); err.Error() != "port: must be a port number from 1 to 65535" {
			t.Errorf("app error = %q, want the app's text", err)
		}
		if err := load("addr: ${APP_SECRET}\n", claimUnmarshaler); !strings.Contains(err.Error(), "hunter2-addr") {
			t.Errorf("err = %q: netip's echo no longer reaches an *UnmarshalerError predicate; update the WithErrorPassthrough godoc", err)
		}
		if err := load("addr: ${APP_SECRET}\n", claimPort); strings.Contains(err.Error(), "hunter2") {
			t.Errorf("err = %q, the app-type predicate passed netip's echo of the value", err)
		}
	})

	// errors.Join exposes every cause to errors.As, so a predicate judging
	// the joined error would claim a TextUnmarshaler field's echo beside one
	// of the app's own errors.
	t.Run("the app's own error type claims a join only when it owns every problem", func(t *testing.T) {
		t.Parallel()
		claimPort := func(err error) bool { _, ok := errors.AsType[*portError](err); return ok }
		secret := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_SECRET": "hunter2-mixed"})) //gitleaks:allow (planted fixture; the test asserts it never survives)
		var addrCfg struct {
			Port port       `yaml:"port"`
			Addr netip.Addr `yaml:"addr"`
		}
		var timeCfg struct {
			Port port      `yaml:"port"`
			When time.Time `yaml:"when"`
		}
		for name, tc := range map[string]struct {
			doc string
			out any
		}{
			"port and netip.Addr": {doc: "port: x\naddr: ${APP_SECRET}\n", out: &addrCfg},
			"port and time.Time":  {doc: "port: x\nwhen: ${APP_SECRET}\n", out: &timeCfg},
		} {
			_, err := yamlenv.Load([]byte(tc.doc), tc.out, allowAppPrefix, secret, yamlenv.WithErrorPassthrough(claimPort))
			if err == nil {
				t.Fatalf("%s: Load = nil (port %d, addr %v, when %v), want a decode error",
					name, addrCfg.Port.n, addrCfg.Addr, timeCfg.When)
			}
			if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
				t.Errorf("%s: err = %q passed through, want it sanitized", name, err)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("%s: err = %q, the app-type predicate passed the field's echo of the value", name, err)
			}
		}

		var twoPorts struct {
			Port  port `yaml:"port"`
			Admin port `yaml:"admin"`
		}
		_, err := yamlenv.Load([]byte("port: x\nadmin: y\n"), &twoPorts, allowAppPrefix, yamlenv.WithErrorPassthrough(claimPort))
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); !ok {
			t.Fatalf("Load = %v (%T), want the app's two errors passed through (ports %d, %d)",
				err, err, twoPorts.Port.n, twoPorts.Admin.n)
		}
		if want := "port: must be a port number from 1 to 65535\nport: must be a port number from 1 to 65535"; err.Error() != want {
			t.Errorf("err = %q, want the joined app text %q", err, want)
		}
	})

	t.Run("the predicate sees one UnmarshalerError per problem", func(t *testing.T) {
		t.Parallel()
		var calls []error
		record := func(err error) bool { calls = append(calls, err); return true }
		var cfg struct {
			Port port       `yaml:"port"`
			Addr netip.Addr `yaml:"addr"`
		}
		_, err := yamlenv.Load([]byte("port: x\naddr: y\n"), &cfg, allowAppPrefix, yamlenv.WithErrorPassthrough(record))
		if err == nil {
			t.Fatalf("Load = nil (port %d, addr %v), want a decode error", cfg.Port.n, cfg.Addr)
		}
		if len(calls) != 2 {
			t.Fatalf("predicate called %d times with %v, want once per problem", len(calls), calls)
		}
		for i, call := range calls {
			ue, ok := errors.AsType[*yamlenv.UnmarshalerError](call)
			if !ok || error(ue) != call {
				t.Fatalf("call %d: %T, want *yamlenv.UnmarshalerError", i, call)
			}
			if strings.Contains(ue.Err.Error(), "\n") {
				t.Errorf("call %d: Err = %q joins several problems, want one", i, ue.Err)
			}
		}
		if _, ok := errors.AsType[*portError](calls[0]); !ok {
			t.Errorf("first call = %v, want the port field's *portError", calls[0])
		}
		if _, ok := errors.AsType[*portError](calls[1]); ok {
			t.Errorf("second call = %v, want the addr field's error alone", calls[1])
		}
	})

	t.Run("the predicate is not asked again after it refuses a problem", func(t *testing.T) {
		t.Parallel()
		var calls []error
		record := func(err error) bool {
			calls = append(calls, err)
			_, ok := errors.AsType[*portError](err)
			return ok
		}
		var cfg struct {
			Addr  netip.Addr `yaml:"addr"`
			Port  port       `yaml:"port"`
			Admin port       `yaml:"admin"`
		}
		_, err := yamlenv.Load([]byte("addr: y\nport: x\nadmin: z\n"), &cfg, allowAppPrefix, yamlenv.WithErrorPassthrough(record))
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok || err == nil {
			t.Fatalf("Load = %v (addr %v, ports %d, %d), want it sanitized", err, cfg.Addr, cfg.Port.n, cfg.Admin.n)
		}
		if len(calls) != 1 {
			t.Fatalf("predicate called %d times with %v, want once, for the refused addr problem", len(calls), calls)
		}
	})

	// go-yaml found a problem beside the hook's, so the error is not the
	// config type's own and no predicate may claim it, whatever go-yaml's
	// error types let errors.As reach.
	t.Run("a mixed hook and go-yaml error never reaches the predicate", func(t *testing.T) {
		t.Parallel()
		var calls []error
		recordCalls := func(err error) bool { calls = append(calls, err); return true }
		for name, pred := range map[string]func(error) bool{
			"app type":    func(err error) bool { _, ok := errors.AsType[*portError](err); return ok },
			"always true": func(error) bool { return true },
			"recorded":    recordCalls,
		} {
			var cfg struct {
				Port    port `yaml:"port"`
				Workers int  `yaml:"workers"`
			}
			_, err := yamlenv.Load([]byte("port: x\nworkers: ${APP_SECRET}\n"), &cfg, allowAppPrefix,
				yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_SECRET": "hunter2"})),
				yamlenv.WithErrorPassthrough(pred))
			const want = "decode errors: line 1, column 7: value rejected (details withheld: they may embed an expanded secret); " +
				"line 2, column 10: cannot construct !!str <redacted> into int"
			if err == nil || err.Error() != want {
				t.Errorf("%s: err = %v, want %q", name, err, want)
			}
		}
		if len(calls) != 0 {
			t.Errorf("predicate called with %v, want no call for a mixed error", calls)
		}
	})

	t.Run("without a passthrough the classification stays fail-closed", func(t *testing.T) {
		t.Parallel()
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: notaduration\n"), &cfg, allowAppPrefix)
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
			t.Errorf("sanitized error still carries the typed wrap: %v", err)
		}
		const want = "decode errors: line 1, column 10: value rejected (details withheld: they may embed an expanded secret)"
		if err == nil || err.Error() != want {
			t.Errorf("err = %v, want %q", err, want)
		}
	})
}

// TestLoadErrorShapes pins the exact text Load returns for each failure shape.
func TestLoadErrorShapes(t *testing.T) {
	t.Parallel()
	const withheld = "configuration could not be decoded (details withheld: they may embed an expanded secret)"
	tests := map[string]struct {
		doc  string
		opts []yamlenv.LoadOption
		want string
	}{
		"parse error keeps only its position": {
			doc:  "api_key: a\n\thunter2: b\n",
			want: "line 2, column 1: " + withheld,
		},
		"unterminated quote keeps where the scanner stopped": {
			doc:  "api_key: a\nport: \"hunter2\n",
			want: "line 3, column 1: " + withheld,
		},
		"unknown anchor keeps only its position": {
			doc:  "api_key: *hunter2\n",
			want: "line 1, column 10: " + withheld,
		},
		"wrong type": {
			doc:  "port: hunter2\n",
			want: "decode errors: line 1, column 7: cannot construct !!str <redacted> into int",
		},
		"duplicate key": {
			doc:  "api_key: a\napi_key: hunter2\n",
			want: "decode errors: line 2, column 1: mapping key <redacted> already defined at line 1",
		},
		"unknown key redacted": {
			doc:  "api_key: a\nhunter2: b\n",
			want: "decode errors: line 2, column 1: unknown configuration key <redacted>",
		},
		"unknown key echoed": {
			doc:  "nested:\n  levl: 1\n",
			opts: []yamlenv.LoadOption{yamlenv.WithSanitizeOptions(yamlenv.WithUnknownKeyEcho(true))},
			want: `decode errors: line 2, column 3: unknown configuration key "levl"`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var cfg loadConfig
			_, err := yamlenv.Load([]byte(tt.doc), &cfg, allowAppPrefix, tt.opts...)
			if err == nil || err.Error() != tt.want {
				t.Errorf("Load = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestLoadKeepsOnlyVouchedParts pins that Load keeps a rebuilt entry's
// value-free part only when the config type or the document vouches for it:
// a config type's own error worded like go-yaml's cannot smuggle a value
// through the part go-yaml fills with a type name, a key or a line number.
func TestLoadKeepsOnlyVouchedParts(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-SECRET-into-LEAKVALUE" //gitleaks:allow (planted fixture; the test asserts it never survives)
	lookup := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_SECRET": secret}))
	echo := yamlenv.WithSanitizeOptions(yamlenv.WithUnknownKeyEcho(true))
	claim := yamlenv.WithErrorPassthrough(claimUnmarshaler)

	tests := map[string]struct {
		doc  string
		out  any
		opts []yamlenv.LoadOption
		want string
	}{
		"hook wrong-type wording with the value as the type": {
			doc:  "h: ${APP_SECRET}\n",
			out:  &echoingConfig{H: echoingHook{prefix: "cannot construct !!str into "}},
			want: "decode errors: line 1, column 4: cannot construct !!str into <redacted>",
		},
		"the same under the recommended passthrough": {
			doc:  "h: ${APP_SECRET}\n",
			out:  &echoingConfig{H: echoingHook{prefix: "cannot construct !!str into "}},
			opts: []yamlenv.LoadOption{claim},
			want: "decode errors: line 1, column 4: cannot construct !!str into <redacted>",
		},
		"hook TextUnmarshaler wording with the value as the type": {
			doc:  "h: ${APP_SECRET}\n",
			out:  &echoingConfig{H: echoingHook{prefix: "cannot construct !!map into ", suffix: " (TextUnmarshaler)"}},
			want: "decode errors: line 1, column 4: cannot construct !!map into <redacted>",
		},
		"hook duplicate-key wording at a value's position": {
			doc:  "h: ${APP_SECRET}\n",
			out:  &echoingConfig{H: echoingHook{prefix: `mapping key "`, suffix: `" already defined at line 1`}},
			want: "decode errors: line 1, column 4: " + entryWithheld,
		},
		"hook strict-loading expanded keys under the echo": {
			doc:  "anchor: &k ${APP_SECRET}\nsub:\n  a: ${APP_SECRET}\n  *k : 1\n",
			out:  &strictHookConfig{},
			opts: []yamlenv.LoadOption{echo},
			want: "decode errors: line 4, column 3: unknown configuration key <redacted>",
		},
		"go-yaml's wrong type into the config's own int": {
			doc: "h: ok\nport: ${APP_SECRET}\n",
			out: &struct {
				H    echoingHook `yaml:"h"`
				Port int         `yaml:"port"`
			}{},
			want: "decode errors: line 1, column 4: " + entryWithheld + "; line 2, column 7: cannot construct !!str <redacted> into int",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := yamlenv.Load([]byte(tt.doc), tt.out, allowAppPrefix, append([]yamlenv.LoadOption{lookup}, tt.opts...)...)
			if err == nil || err.Error() != tt.want {
				t.Errorf("Load = %v, want %q", err, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), "hunter2") {
				t.Errorf("err = %q, leaked the value", err)
			}
		})
	}
}

// TestLoadProbeRedactsHookWordedKeys pins that a config type's error worded
// as go-yaml's unknown field, raised in the raw-bytes probe, is redacted under
// the echo whatever it names, and that the failed load leaves out unchanged.
func TestLoadProbeRedactsHookWordedKeys(t *testing.T) {
	t.Parallel()
	echo := yamlenv.WithSanitizeOptions(yamlenv.WithUnknownKeyEcho(true))
	for name, doc := range map[string]string{
		"a pasted value":  "name: n\nh: hunter2-pasted\n",
		"a real key name": "name: n\nh: name\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := forgedConfig{Name: "default", H: forgedUnknownKey{}}
			_, err := yamlenv.Load([]byte(doc), &cfg, allowAppPrefix, echo)
			if want := "decode errors: line 2, column 4: unknown configuration key <redacted>"; err == nil || err.Error() != want {
				t.Errorf("Load = %v, want %q", err, want)
			}
			if cfg.Name != "default" {
				t.Errorf("Name = %q, want the default kept after a probe failure", cfg.Name)
			}
		})
	}
}

// TestLoadStrictHookSeesExpandedValues pins that strictHook strict-loads the
// expanded node, so the echo case above exercises a real post-expansion load.
func TestLoadStrictHookSeesExpandedValues(t *testing.T) {
	t.Parallel()
	var cfg strictHookConfig
	lookup := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_SECRET": "expanded"}))
	_, err := yamlenv.Load([]byte("anchor: ${APP_SECRET}\nsub:\n  a: ${APP_SECRET}\n"), &cfg, allowAppPrefix, lookup)
	if err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
	if cfg.Anchor != "expanded" || cfg.Sub.S.A != "expanded" {
		t.Errorf("cfg = %+v, want both values expanded", cfg)
	}
}

// TestLoadKeepsReachableTypes pins the other side of the vouching: go-yaml's
// own wrong-type entry keeps every Go type the config type reaches, through
// pointers, slices, arrays, map keys and values, nested and recursive structs,
// and a TextUnmarshaler destination.
func TestLoadKeepsReachableTypes(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		doc  string
		out  any
		want string
	}{
		"map value": {
			doc: "m: {a: hunter2}\n", out: &struct {
				M map[string]int `yaml:"m"`
			}{},
			want: "cannot construct !!str <redacted> into int",
		},
		"map key": {
			doc: "m: {hunter2: a}\n", out: &struct {
				M map[int]string `yaml:"m"`
			}{},
			want: "cannot construct !!str <redacted> into int",
		},
		"slice element": {
			doc: "s: [hunter2]\n", out: &struct {
				S []bool `yaml:"s"`
			}{},
			want: "cannot construct !!str <redacted> into bool",
		},
		"array element": {
			doc: "a: [hunter2]\n", out: &struct {
				A [1]float64 `yaml:"a"`
			}{},
			want: "cannot construct !!str <redacted> into float64",
		},
		"pointer": {
			doc: "p: hunter2\n", out: &struct {
				P *uint8 `yaml:"p"`
			}{},
			want: "cannot construct !!str <redacted> into uint8",
		},
		"TextUnmarshaler": {
			doc: "t: {a: hunter2}\n", out: &struct {
				T time.Time `yaml:"t"`
			}{},
			want: "cannot construct !!map into time.Time (TextUnmarshaler)",
		},
		"recursive type": {
			doc:  "children: [{children: hunter2}]\n",
			out:  &treeNode{},
			want: "cannot construct !!str <redacted> into []yamlenv_test.treeNode",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := yamlenv.Load([]byte(tt.doc), tt.out, allowAppPrefix)
			if err == nil || !strings.HasSuffix(err.Error(), ": "+tt.want) {
				t.Errorf("Load = %v, want it to end in %q", err, tt.want)
			}
		})
	}
}

// TestLoadUnknownKeysBesideAppFailure pins that a custom UnmarshalYAML
// rejecting a still-literal ${VAR} in the pre-expansion probe does not hide
// an unknown key elsewhere in the document.
func TestLoadUnknownKeysBesideAppFailure(t *testing.T) {
	var cfg durConfig
	_, err := yamlenv.Load([]byte("timeout: ${APP_TIMEOUT}\ntypo_key: x\n"), &cfg, allowAppPrefix,
		yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_TIMEOUT": "5s"})),
		yamlenv.WithSanitizeOptions(yamlenv.WithUnknownKeyEcho(true)))
	if want := `decode errors: line 2, column 1: unknown configuration key "typo_key"`; err == nil || err.Error() != want {
		t.Fatalf("Load = %v, want %q", err, want)
	}
}

// TestLoadNeverLeaksSecrets pins Load's whole-pipeline confidentiality
// contract: a secret, pasted literally into the document or expanded into it
// from the environment, never survives into any returned error text.
func TestLoadNeverLeaksSecrets(t *testing.T) {
	const secret = "hunter2-load-secret" //gitleaks:allow (planted fixture; the test asserts it never survives)
	lookup := yamlenv.WithLookupEnv(mapLookup(map[string]string{"APP_SECRET": secret}))
	echo := yamlenv.WithSanitizeOptions(yamlenv.WithUnknownKeyEcho(true))

	t.Run("pre-expansion parse error with a pasted literal secret", func(t *testing.T) {
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("api_key: *"+secret+"\n"), &cfg, allowAppPrefix)
		if err == nil {
			t.Fatal("Load = nil, want parse error")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("parse error leaks the pasted secret: %q", err)
		}
	})

	t.Run("post-expansion decode error with an expanded secret", func(t *testing.T) {
		var cfg struct {
			Flag bool `yaml:"flag"`
		}
		_, err := yamlenv.Load([]byte("flag: ${APP_SECRET}\n"), &cfg, allowAppPrefix, lookup)
		if err == nil {
			t.Fatal("Load = nil, want decode error")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("decode error leaks the expanded secret: %q", err)
		}
		if !strings.Contains(err.Error(), "cannot construct !!str <redacted> into bool") {
			t.Errorf("err = %q, want the redacted wrong-type shape kept actionable", err)
		}
	})

	t.Run("alias key naming an expanded value is echoed pre-expansion", func(t *testing.T) {
		var cfg struct {
			Anchor string `yaml:"anchor"`
		}
		_, err := yamlenv.Load([]byte("anchor: &a ${APP_SECRET}\n*a : 1\n"), &cfg, allowAppPrefix, lookup, echo)
		if err == nil {
			t.Fatal("Load = nil, want unknown-key error")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("unknown-key echo leaks the expanded secret: %q", err)
		}
		if !strings.Contains(err.Error(), `unknown configuration key "${APP_SECRET}"`) {
			t.Errorf("err = %q, want the literal pre-expansion key echoed", err)
		}
	})

	t.Run("expanded secret into an explicitly !!str-tagged node", func(t *testing.T) {
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("port: !!str ${APP_SECRET}\n"), &cfg, allowAppPrefix, lookup)
		if err == nil {
			t.Fatal("Load = nil, want decode error")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("decode error leaks the expanded secret: %q", err)
		}
	})
}

// TestLoadResourceLimits pins go-yaml's default refusals through Load: an
// alias bomb and nesting past 10,000 levels both fail instead of exhausting
// memory or stack. yamlenv sets no limit of its own, so this is the gate
// against a go-yaml upgrade relaxing DefaultAliasCheck or DefaultDepthCheck.
func TestLoadResourceLimits(t *testing.T) {
	t.Parallel()
	var bomb strings.Builder
	bomb.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&bomb, "a%d: &a%d [", i, i)
		for j := range 10 {
			if j > 0 {
				bomb.WriteString(", ")
			}
			fmt.Fprintf(&bomb, "*a%d", i-1)
		}
		bomb.WriteString("]\n")
	}
	tests := map[string]string{
		"alias expansion": bomb.String(),
		"flow depth":      "a: " + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + "\n",
		"block depth": func() string {
			var b strings.Builder
			for i := range 10002 {
				b.WriteString(strings.Repeat(" ", i) + "k:\n")
			}
			return b.String()
		}(),
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var cfg map[string]any
			_, err := yamlenv.Load([]byte(doc), &cfg, allowAppPrefix)
			if err == nil {
				t.Fatal("Load = nil, want the limit refusal")
			}
			if !strings.Contains(err.Error(), "details withheld") {
				t.Errorf("err = %q, want the fixed withheld message", err)
			}
		})
	}
}

// TestUnmarshalerError_nil_Err_does_not_panic pins that a hand-built
// UnmarshalerError without a cause renders a fixed message.
func TestUnmarshalerError_nil_Err_does_not_panic(t *testing.T) {
	t.Parallel()
	if got, want := (&yamlenv.UnmarshalerError{}).Error(), "yamlenv: unmarshaler error"; got != want {
		t.Errorf("UnmarshalerError{}.Error() = %q, want %q", got, want)
	}
}
