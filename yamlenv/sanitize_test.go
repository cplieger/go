package yamlenv_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v4"
)

func TestSanitizeDecodeErrorNil(t *testing.T) {
	t.Parallel()
	if got := yamlenv.SanitizeDecodeError(nil); got != nil {
		t.Errorf("SanitizeDecodeError(nil) = %v, want nil", got)
	}
	if got := yamlenv.SanitizeDecodeError(nil, yamlenv.WithUnknownKeyEcho(true)); got != nil {
		t.Errorf("SanitizeDecodeError(nil, WithUnknownKeyEcho(true)) = %v, want nil", got)
	}
}

// TestSanitizeDecodeErrorFallbacks pins the value-independent fallback
// branches: an error that is not a *yaml.TypeError, and TypeError entries that
// do not match a known "line N: ..." structure. Every fallback is a fixed
// message that cannot embed any fragment of the (potentially secret-bearing)
// original error text.
func TestSanitizeDecodeErrorFallbacks(t *testing.T) {
	t.Parallel()
	const secret = "leaked-secret-sentinel"

	t.Run("non-TypeError falls back to generic message", func(t *testing.T) {
		t.Parallel()
		got := yamlenv.SanitizeDecodeError(errors.New("decode blew up near " + secret))
		want := "configuration could not be decoded (details withheld: they may embed an expanded secret)"
		if got.Error() != want {
			t.Errorf("SanitizeDecodeError(non-TypeError) = %q, want %q", got, want)
		}
	})

	t.Run("syntax error keeps the line locator and withholds the message", func(t *testing.T) {
		t.Parallel()
		got := yamlenv.SanitizeDecodeError(errors.New("yaml: line 7: found character that cannot start any token near " + secret))
		want := "line 7: configuration could not be decoded (details withheld: they may embed an expanded secret)"
		if got.Error() != want {
			t.Errorf("SanitizeDecodeError(syntax error) = %q, want %q", got, want)
		}
	})

	t.Run("wrapped TypeError is still recognized and sanitized", func(t *testing.T) {
		t.Parallel()
		typeErr := &yaml.TypeError{Errors: []string{
			"line 3: cannot unmarshal !!str `" + secret + "` into bool",
		}}
		got := yamlenv.SanitizeDecodeError(errors.Join(typeErr)).Error()
		if strings.Contains(got, secret) {
			t.Errorf("SanitizeDecodeError(wrapped TypeError) leaks the scalar excerpt: %q", got)
		}
		if !strings.Contains(got, "line 3: cannot unmarshal !!str <redacted> into bool") {
			t.Errorf("SanitizeDecodeError(wrapped TypeError) = %q, want redacted line/type info kept", got)
		}
	})

	t.Run("marker-less TypeError entry falls back per entry", func(t *testing.T) {
		t.Parallel()
		typeErr := &yaml.TypeError{Errors: []string{
			"line 9: some future entry shape mentioning " + secret,
		}}
		got := yamlenv.SanitizeDecodeError(typeErr).Error()
		want := "unmarshal errors: configuration contains a value of the wrong type"
		if got != want {
			t.Errorf("SanitizeDecodeError(marker-less entry) = %q, want %q", got, want)
		}
	})

	t.Run("into-marker before unmarshal-marker falls back", func(t *testing.T) {
		t.Parallel()
		typeErr := &yaml.TypeError{Errors: []string{
			" into bool then cannot unmarshal !!str " + secret,
		}}
		got := yamlenv.SanitizeDecodeError(typeErr).Error()
		want := "unmarshal errors: configuration contains a value of the wrong type"
		if got != want {
			t.Errorf("SanitizeDecodeError(reordered markers) = %q, want %q", got, want)
		}
	})
}

// TestSanitizeDecodeErrorWrongType pins the exact rebuild of the wrong-type
// entry shape for adversarial excerpt contents. The table doubles as the
// deterministic form of the generative property in FuzzSanitizeDecodeError:
// whatever the excerpt holds — backticks, marker fragments, newlines — the
// rebuilt entry is byte-for-byte the value-free template.
func TestSanitizeDecodeErrorWrongType(t *testing.T) {
	t.Parallel()
	const want = "unmarshal errors: line 4: cannot unmarshal !!str <redacted> into bool"
	excerpts := map[string]string{
		"plain secret":                     "hunter2-super-secret",
		"empty":                            "",
		"embedded backtick":                "zq9`vw7-secret",
		"embedded into marker":             "secret into bool trap",
		"embedded unmarshal marker":        "cannot unmarshal !!str `nested`",
		"embedded dup-key marker pair":     "x: mapping key y already defined at line 9",
		"embedded unknown-key marker pair": "x: field oops not found in type main.fileConfig",
		"embedded newline":                 "first\nsecond",
		"embedded bare line prefix":        "line 3",
	}
	for name, excerpt := range excerpts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			typeErr := &yaml.TypeError{Errors: []string{
				"line 4: cannot unmarshal !!str `" + excerpt + "` into bool",
			}}
			if got := yamlenv.SanitizeDecodeError(typeErr).Error(); got != want {
				t.Errorf("SanitizeDecodeError(excerpt %q) = %q, want %q", excerpt, got, want)
			}
		})
	}
}

// TestSanitizeDecodeErrorDuplicateKey pins the duplicate-mapping-key rebuild:
// both line numbers are kept (value-independent), the key excerpt — which a
// misindented paste can fill with a secret — is redacted, and a key whose text
// itself embeds the trailing marker cannot extend what survives (LastIndex
// anchors on the entry's real tail).
func TestSanitizeDecodeErrorDuplicateKey(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		entry string
		want  string
	}{
		"plain duplicate key": {
			entry: `line 3: mapping key "sonarr" already defined at line 1`,
			want:  "unmarshal errors: line 3: mapping key <redacted> already defined at line 1",
		},
		"secret-bearing key": {
			entry: `line 12: mapping key "hunter2-secret" already defined at line 4`,
			want:  "unmarshal errors: line 12: mapping key <redacted> already defined at line 4",
		},
		"key embedding the trailing marker": {
			entry: `line 3: mapping key "a already defined at line 5 b" already defined at line 1`,
			want:  "unmarshal errors: line 3: mapping key <redacted> already defined at line 1",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			typeErr := &yaml.TypeError{Errors: []string{tt.entry}}
			if got := yamlenv.SanitizeDecodeError(typeErr).Error(); got != tt.want {
				t.Errorf("SanitizeDecodeError(%q) = %q, want %q", tt.entry, got, tt.want)
			}
		})
	}
}

// TestSanitizeDecodeErrorUnknownKey pins the policy seam: the strict-decode
// unknown-key entry redacts the key name by default and echoes it only under
// WithUnknownKeyEcho; the Go type name is dropped either way.
func TestSanitizeDecodeErrorUnknownKey(t *testing.T) {
	t.Parallel()
	typeErr := &yaml.TypeError{Errors: []string{
		"line 5: field anime_bytes not found in type main.fileConfig",
	}}

	t.Run("default redacts the key name", func(t *testing.T) {
		t.Parallel()
		got := yamlenv.SanitizeDecodeError(typeErr).Error()
		want := "unmarshal errors: line 5: unknown configuration key <redacted>"
		if got != want {
			t.Errorf("SanitizeDecodeError(unknown key) = %q, want %q", got, want)
		}
	})

	t.Run("WithUnknownKeyEcho keeps the key name and drops the type", func(t *testing.T) {
		t.Parallel()
		got := yamlenv.SanitizeDecodeError(typeErr, yamlenv.WithUnknownKeyEcho(true)).Error()
		want := `unmarshal errors: line 5: unknown configuration key "anime_bytes"`
		if got != want {
			t.Errorf("SanitizeDecodeError(unknown key, echo) = %q, want %q", got, want)
		}
		if strings.Contains(got, "main.fileConfig") {
			t.Errorf("SanitizeDecodeError(unknown key, echo) = %q, leaks the Go type name", got)
		}
	})

	t.Run("WithUnknownKeyEcho(false) spells the default", func(t *testing.T) {
		t.Parallel()
		got := yamlenv.SanitizeDecodeError(typeErr, yamlenv.WithUnknownKeyEcho(false)).Error()
		want := "unmarshal errors: line 5: unknown configuration key <redacted>"
		if got != want {
			t.Errorf("SanitizeDecodeError(unknown key, echo=false) = %q, want the no-option default %q", got, want)
		}
	})

	t.Run("the last repeated option wins", func(t *testing.T) {
		t.Parallel()
		redacted := yamlenv.SanitizeDecodeError(typeErr,
			yamlenv.WithUnknownKeyEcho(true), yamlenv.WithUnknownKeyEcho(false)).Error()
		if strings.Contains(redacted, "anime_bytes") {
			t.Errorf("SanitizeDecodeError(echo=true, echo=false) = %q, want the later false to win", redacted)
		}
		echoed := yamlenv.SanitizeDecodeError(typeErr,
			yamlenv.WithUnknownKeyEcho(false), yamlenv.WithUnknownKeyEcho(true)).Error()
		if !strings.Contains(echoed, `unknown configuration key "anime_bytes"`) {
			t.Errorf("SanitizeDecodeError(echo=false, echo=true) = %q, want the later opt-in to win", echoed)
		}
	})

	t.Run("multiple entries join with semicolons", func(t *testing.T) {
		t.Parallel()
		multi := &yaml.TypeError{Errors: []string{
			"line 5: field anime_bytes not found in type main.fileConfig",
			"line 6: field animebytes not found in type main.filtersConfig",
		}}
		got := yamlenv.SanitizeDecodeError(multi, yamlenv.WithUnknownKeyEcho(true)).Error()
		want := `unmarshal errors: line 5: unknown configuration key "anime_bytes"; ` +
			`line 6: unknown configuration key "animebytes"`
		if got != want {
			t.Errorf("SanitizeDecodeError(multi unknown keys) = %q, want %q", got, want)
		}
	})
}

// TestSanitizeDecodeErrorCraftedPrefix pins the wrong-type branch's own
// isLinePrefix guard: a hand-built *yaml.TypeError entry whose text before
// "cannot unmarshal !!" is not exactly the genuine "line N: " prefix must not
// have that text survive into the sanitized output — it falls back to the
// fixed wrong-type message (genuine yaml.v3 entries always carry the prefix,
// so they are unaffected).
func TestSanitizeDecodeErrorCraftedPrefix(t *testing.T) {
	t.Parallel()
	const want = "unmarshal errors: " + "configuration contains a value of the wrong type"
	entries := map[string]string{
		"crafted text instead of line prefix": "leaked-prefix-sentinel cannot unmarshal !!str `x` into bool",
		"crafted text after line prefix":      "line 4: leaked-prefix-sentinel cannot unmarshal !!str `x` into bool",
		"missing separator":                   "line 4 cannot unmarshal !!str `x` into bool",
		"non-digit line number":               "line 4x: cannot unmarshal !!str `x` into bool",
		"empty prefix":                        "cannot unmarshal !!str `x` into bool",
	}
	for name, entry := range entries {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			typeErr := &yaml.TypeError{Errors: []string{entry}}
			got := yamlenv.SanitizeDecodeError(typeErr).Error()
			if got != want {
				t.Errorf("SanitizeDecodeError(%q) = %q, want %q", entry, got, want)
			}
			if strings.Contains(got, "leaked-prefix-sentinel") {
				t.Errorf("SanitizeDecodeError(%q) = %q, crafted prefix text survived", entry, got)
			}
		})
	}
}

// TestSanitizeDecodeErrorExcerptCollisions pins the isLinePrefix guard through
// the public API: a wrong-type scalar excerpt that embeds the duplicate-key or
// unknown-key marker pair must never be mistaken for those shapes (whose
// rebuilds keep text from around their markers) — even with the echo option
// on, nothing from the excerpt survives.
func TestSanitizeDecodeErrorExcerptCollisions(t *testing.T) {
	t.Parallel()
	const secret = "leaked-secret-sentinel"
	entries := map[string]string{
		"unknown-key markers in excerpt": "line 4: cannot unmarshal !!str `" + secret + ": field oops not found in type x` into bool",
		"dup-key markers in excerpt":     "line 4: cannot unmarshal !!str `" + secret + ": mapping key x already defined at line 9-" + secret + "` into bool",
	}
	for name, entry := range entries {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			typeErr := &yaml.TypeError{Errors: []string{entry}}
			got := yamlenv.SanitizeDecodeError(typeErr, yamlenv.WithUnknownKeyEcho(true)).Error()
			if strings.Contains(got, secret) || strings.Contains(got, "oops") {
				t.Errorf("SanitizeDecodeError(colliding excerpt) leaks excerpt content: %q", got)
			}
			if !strings.Contains(got, "line 4: cannot unmarshal !!str <redacted> into bool") {
				t.Errorf("SanitizeDecodeError(colliding excerpt) = %q, want the wrong-type redaction", got)
			}
		})
	}
}

// TestSanitizeDecodeErrorRealYAMLErrors is the module's vocabulary-drift
// gate: it sanitizes errors produced by the ACTUAL yaml.v3 in go.mod — a
// wrong-type decode of an expanded secret, a duplicate mapping key, a
// strict-decode unknown key, and a syntax error — and asserts the planted
// secret never survives while the value-independent structure does. If a
// yaml.v3 upgrade reworded its errors, this test fails the dependency-bump PR
// before any consumer leaks or degrades to the withheld fallback. Keep it
// even where it looks redundant beside the synthetic-entry tests above: they
// pin the sanitizer against crafted entries, this one pins the marker
// vocabulary against the real dependency.
func TestSanitizeDecodeErrorRealYAMLErrors(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-expanded-secret" //gitleaks:allow (planted fixture; the test asserts it never survives sanitization)

	type shape struct {
		Flag bool   `yaml:"flag"`
		Name string `yaml:"name"`
	}

	t.Run("wrong-type entry from a real decode", func(t *testing.T) {
		t.Parallel()
		var s shape
		err := yaml.Unmarshal([]byte("flag: "+secret+"\n"), &s)
		if err == nil {
			t.Fatal("Unmarshal = nil error, want type error")
		}
		got := yamlenv.SanitizeDecodeError(err).Error()
		if strings.Contains(got, secret) || strings.Contains(got, "hunter2") {
			t.Errorf("sanitized real wrong-type error leaks the value: %q", got)
		}
		if !strings.Contains(got, "cannot unmarshal !!str <redacted> into bool") {
			t.Errorf("sanitized real wrong-type error = %q, want the redacted entry shape", got)
		}
	})

	t.Run("duplicate-key entry from a real decode", func(t *testing.T) {
		t.Parallel()
		var s shape
		err := yaml.Unmarshal([]byte("name: a\nname: b\n"), &s)
		if err == nil {
			t.Fatal("Unmarshal = nil error, want duplicate-key error")
		}
		got := yamlenv.SanitizeDecodeError(err).Error()
		if strings.Contains(got, "name") {
			t.Errorf("sanitized real duplicate-key error leaks the key: %q", got)
		}
		if !strings.Contains(got, "line 2: mapping key <redacted> already defined at line 1") {
			t.Errorf("sanitized real duplicate-key error = %q, want both line numbers kept", got)
		}
	})

	t.Run("unknown-key entry from a real strict decode", func(t *testing.T) {
		t.Parallel()
		dec := yaml.NewDecoder(bytes.NewReader([]byte("nam: x\n")))
		dec.KnownFields(true)
		var s shape
		err := dec.Decode(&s)
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("Decode = %v, want unknown-field error", err)
		}
		redacted := yamlenv.SanitizeDecodeError(err).Error()
		if strings.Contains(redacted, "nam") {
			t.Errorf("default sanitization leaks the unknown key: %q", redacted)
		}
		echoed := yamlenv.SanitizeDecodeError(err, yamlenv.WithUnknownKeyEcho(true)).Error()
		if !strings.Contains(echoed, `unknown configuration key "nam"`) {
			t.Errorf("echoed sanitization = %q, want the key name kept", echoed)
		}
	})

	t.Run("syntax error from a real parse", func(t *testing.T) {
		t.Parallel()
		var n yaml.Node
		err := yaml.Unmarshal([]byte("a: b\n\t"+secret+": c\n"), &n)
		if err == nil {
			t.Fatal("Unmarshal = nil error, want syntax error")
		}
		got := yamlenv.SanitizeDecodeError(err).Error()
		if strings.Contains(got, secret) {
			t.Errorf("sanitized real syntax error leaks document text: %q", got)
		}
		if !strings.HasPrefix(got, "line 2: ") {
			t.Errorf("sanitized real syntax error = %q, want the line locator kept", got)
		}
	})
}

// TestSanitizeDecodeErrorDoesNotWrap pins the no-Unwrap contract: the
// sanitized error must give no path back to the original (secret-bearing)
// error through errors.Is or errors.As.
func TestSanitizeDecodeErrorDoesNotWrap(t *testing.T) {
	t.Parallel()
	typeErr := &yaml.TypeError{Errors: []string{"line 1: cannot unmarshal !!str `s3cret` into int"}}
	got := yamlenv.SanitizeDecodeError(typeErr)
	if errors.Is(got, typeErr) {
		t.Error("sanitized error wraps the original TypeError (errors.Is reaches it)")
	}
	if _, ok := errors.AsType[*yaml.TypeError](got); ok {
		t.Error("sanitized error wraps the original TypeError (errors.As reaches it)")
	}
}
