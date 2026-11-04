package yamlenv_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v3"
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

// appOwned is the passthrough predicate the subflux consumer shape uses: an
// error is app-owned when it is neither a *yaml.TypeError nor "yaml:"-prefixed.
func appOwned(err error) bool {
	if _, ok := errors.AsType[*yaml.TypeError](err); ok {
		return false
	}
	return !strings.HasPrefix(err.Error(), "yaml:")
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
			yamlenv.WithErrorPassthrough(appOwned))
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

// TestLoadClassifiesUnmarshalerErrors pins the typed classification of the
// decode step: a failure produced by the config type's OWN UnmarshalYAML is
// detectable — and claimable through WithErrorPassthrough — as
// *yamlenv.UnmarshalerError with errors.As, carrying the app's message
// verbatim and the original error underneath, while yaml.v3's own decode
// errors never receive the type. The old string-prefix predicate (the
// appOwned helper above) keeps working through the wrap; this is the typed
// replacement consumers migrate to.
func TestLoadClassifiesUnmarshalerErrors(t *testing.T) {
	t.Parallel()
	claimUnmarshaler := func(err error) bool {
		_, ok := errors.AsType[*yamlenv.UnmarshalerError](err)
		return ok
	}

	t.Run("yaml's own non-TypeError decode errors never carry the type", func(t *testing.T) {
		t.Parallel()
		// These two are the whole reason classifyDecodeError keeps a textual
		// leg. yaml.v3 raises every non-TypeError decode failure through
		// failf, which panics with fmt.Errorf("yaml: "+format, …) — a bare
		// *errors.errorString carrying NO exported type and, for most of the
		// ~19 failf sites, NO "line N:" locator either. So the prefix is the
		// only structural signal that separates them from an error the config
		// type's own UnmarshalYAML returned verbatim.
		//
		// This is the gate on that assumption: if yaml.v3 ever rewords or
		// drops the prefix, these fail loudly here instead of silently
		// reclassifying every alias and binary fault as app-owned, which
		// would withhold the diagnostic on the fail-closed path.
		//
		// Deliberately NOT matched with sanitize.go's parseLineRe: neither
		// message carries a line locator, so the narrower pattern would
		// misclassify both.
		for _, tc := range []struct {
			name   string
			doc    string
			secret string
		}{
			{"unknown anchor", "api_key: *nope\n", "nope"},
			{"invalid base64", "api_key: !!binary |\n  @@@not-base64@@@\n", "not-base64"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				var cfg loadConfig
				_, err := yamlenv.Load([]byte(tc.doc), &cfg, allowAppPrefix,
					yamlenv.WithErrorPassthrough(claimUnmarshaler))
				if err == nil {
					t.Fatal("Load = nil, want yaml.v3's own decode error")
				}
				if _, ok := errors.AsType[*yaml.TypeError](err); ok {
					t.Fatalf("err = %v, want a non-TypeError failure so the prefix arm is what classifies it", err)
				}
				if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
					t.Errorf("yaml.v3's own %s error was classified app-owned: %v", tc.name, err)
				}
				// The consequence that makes the classification load-bearing:
				// this caller claims UnmarshalerErrors through its
				// passthrough, so a misclassification here would return
				// yaml.v3's message UNSANITIZED and disclose the document
				// fragment it names.
				if strings.Contains(err.Error(), tc.secret) {
					t.Errorf("err = %q, leaked the document fragment %q", err, tc.secret)
				}
			})
		}
	})

	t.Run("app unmarshaler failure is claimable by type", func(t *testing.T) {
		t.Parallel()
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: notaduration\n"), &cfg, allowAppPrefix,
			yamlenv.WithErrorPassthrough(claimUnmarshaler))
		if err == nil {
			t.Fatal("Load = nil, want the app-owned decode error")
		}
		ue, ok := errors.AsType[*yamlenv.UnmarshalerError](err)
		if !ok {
			t.Fatalf("Load = %v (%T), want *UnmarshalerError reachable with errors.As", err, err)
		}
		if want := "line 1: invalid duration (value withheld)"; err.Error() != want {
			t.Errorf("Error() = %q, want the app vocabulary verbatim: %q", err.Error(), want)
		}
		if ue.Err == nil || ue.Err.Error() != err.Error() {
			t.Errorf("Err = %v, want the unmarshaler's error unchanged under Unwrap", ue.Err)
		}
	})

	t.Run("reachable through a consumer's own wrap", func(t *testing.T) {
		t.Parallel()
		// The consumer shape: a config loader returning
		// fmt.Errorf("parse YAML: %w", err) around Load's error.
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: notaduration\n"), &cfg, allowAppPrefix,
			yamlenv.WithErrorPassthrough(claimUnmarshaler))
		if err == nil {
			t.Fatal("Load = nil, want the app-owned decode error")
		}
		wrapped := fmt.Errorf("parse YAML: %w", err)
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](wrapped); !ok {
			t.Errorf("errors.As through a consumer wrap = false, want the type reachable: %v", wrapped)
		}
	})

	t.Run("yaml's own wrong-type error never carries the type", func(t *testing.T) {
		t.Parallel()
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("port: notanint\n"), &cfg, allowAppPrefix,
			yamlenv.WithErrorPassthrough(claimUnmarshaler))
		if err == nil {
			t.Fatal("Load = nil, want a decode error")
		}
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
			t.Errorf("yaml.v3's own decode error was classified app-owned: %v", err)
		}
		// The typed predicate could not claim it, so it was sanitized: the
		// structurally-safe predicate can never re-open the leak.
		if strings.Contains(err.Error(), "notanint") {
			t.Errorf("err = %q, leaked the scalar excerpt", err)
		}
		if !strings.Contains(err.Error(), "cannot unmarshal") {
			t.Errorf("err = %q, want the sanitized wrong-type shape", err)
		}
	})

	t.Run("without a passthrough the classification stays fail-closed", func(t *testing.T) {
		t.Parallel()
		var cfg durConfig
		_, err := yamlenv.Load([]byte("timeout: notaduration\n"), &cfg, allowAppPrefix)
		if err == nil {
			t.Fatal("Load = nil, want a decode error")
		}
		if _, ok := errors.AsType[*yamlenv.UnmarshalerError](err); ok {
			t.Errorf("sanitized error still carries the typed wrap: %v", err)
		}
		if strings.Contains(err.Error(), "invalid duration") {
			t.Errorf("err = %q, want the app vocabulary withheld without an explicit opt-in", err)
		}
	})
}

// TestLoadNeverLeaksSecrets pins Load's whole-pipeline confidentiality
// contract: a secret — pasted literally into the document, or expanded into
// it from the environment — never survives into any returned error text, on
// both the pre-expansion parse path and the post-expansion decode path.
func TestLoadNeverLeaksSecrets(t *testing.T) {
	const secret = "hunter2-load-secret" //gitleaks:allow (planted fixture; the test asserts it never survives)

	t.Run("pre-expansion parse error with a pasted literal secret", func(t *testing.T) {
		// An unquoted literal secret read as an alias: yaml.v3's raw error is
		// "unknown anchor '<secret>' referenced".
		var cfg loadConfig
		_, err := yamlenv.Load([]byte("api_key: *"+secret+"\n"), &cfg, allowAppPrefix)
		if err == nil {
			t.Fatal("Load = nil, want parse error")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("parse error leaks the pasted secret: %q", err)
		}
	})

	t.Run("post-expansion decode error with an expanded secret", func(t *testing.T) {
		t.Setenv("APP_SECRET", secret)
		var cfg struct {
			Flag bool `yaml:"flag"`
		}
		_, err := yamlenv.Load([]byte("flag: ${APP_SECRET}\n"), &cfg, allowAppPrefix)
		if err == nil {
			t.Fatal("Load = nil, want decode error")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("decode error leaks the expanded secret: %q", err)
		}
		if !strings.Contains(err.Error(), "cannot unmarshal !!str <redacted> into bool") {
			t.Errorf("err = %q, want the redacted wrong-type shape kept actionable", err)
		}
	})
}
