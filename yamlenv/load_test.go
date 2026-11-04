package yamlenv_test

import (
	"errors"
	"fmt"
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

	t.Run("unresolved names identical to Expand", func(t *testing.T) {
		src := []byte("api_key: ${APP_UNSET_ONE}\nnested:\n  level: 1\nother: ${APP_UNSET_TWO}\n")
		var direct yaml.Node
		if err := yaml.Unmarshal(src, &direct); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		want := yamlenv.Expand(&direct, allowAppPrefix)

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
			t.Errorf("Load unresolved = %v, want Expand's %v", got, want)
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

// mimicYAML is a config type whose UnmarshalYAML error is spelled like one of
// go-yaml's own constructor messages.
type mimicYAML struct{}

// UnmarshalYAML always fails with go-yaml's wrong-type wording.
func (*mimicYAML) UnmarshalYAML(*yaml.Node) error {
	return errors.New("cannot construct !!str `x` into int")
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
			doc: "n: hunter2-wrapped\n", out: &struct {
				N relayDecode `yaml:"n"`
			}{}, secret: "hunter2",
		},
		"hook error spelled like go-yaml's": {
			doc: "m: x\n", out: &struct {
				M mimicYAML `yaml:"m"`
			}{}, secret: "`x`",
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

	t.Run("expanded secret into a custom-tagged node", func(t *testing.T) {
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
