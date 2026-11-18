package envx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/go/masked"
	"github.com/cplieger/go/masked/maskedtest"
)

func TestSecretAnswersMasked(t *testing.T) {
	const text = "envx-masked-s3cret"
	getters := map[string]func(Key) (masked.Secret, error){
		"Secret": Secret,
		"SecretWithSource": func(k Key) (masked.Secret, error) {
			v, _, err := SecretWithSource(k)
			return v, err
		},
	}
	for name, get := range getters {
		t.Run(name, func(t *testing.T) {
			t.Run("env channel", func(t *testing.T) {
				t.Setenv("ENVX_MASKED", text)
				t.Setenv("ENVX_MASKED_FILE", "")
				checkMasked(t, get, text)
			})
			t.Run("file channel", func(t *testing.T) {
				p := filepath.Join(t.TempDir(), "secret")
				if err := os.WriteFile(p, []byte(text+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ENVX_MASKED", "from-env")
				t.Setenv("ENVX_MASKED_FILE", p)
				checkMasked(t, get, text)
			})
			t.Run("failure answers the zero Secret", func(t *testing.T) {
				t.Setenv("ENVX_MASKED", text)
				t.Setenv("ENVX_MASKED_FILE", filepath.Join(t.TempDir(), "absent"))
				v, err := get("ENVX_MASKED")
				if err == nil {
					t.Fatal("err = nil for an absent secret file, want an error")
				}
				if !v.IsZero() {
					t.Error("value is not the zero Secret on an error")
				}
			})
		})
	}
}

func checkMasked(t *testing.T, get func(Key) (masked.Secret, error), text string) {
	t.Helper()
	v, err := get("ENVX_MASKED")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := v.Reveal(); got != text {
		t.Fatalf("Reveal() = %q, want %q", got, text)
	}
	if !v.Equal(masked.NewSecret(text)) {
		t.Error("Equal(NewSecret(text)) = false, want true")
	}
	holder := struct{ Key masked.Secret }{v}
	var outputs []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
		outputs = append(outputs, fmt.Sprintf(verb, v), fmt.Sprintf(verb, holder))
	}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("loaded", "key", v)
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("loaded", "key", v)
	encoded, err := json.Marshal(holder)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, logged.String(), string(encoded))
	for _, out := range outputs {
		if maskedtest.Leak(out, text) {
			t.Errorf("printed form %q shows the secret", out)
		}
	}
}
