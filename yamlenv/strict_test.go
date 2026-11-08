package yamlenv_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v4"
)

// probeConfig is the throwaway config shape the strict unknown-key tests
// decode into, with one nested level to prove the check reaches below the
// top level.
type probeConfig struct {
	Name   string `yaml:"name"`
	Nested struct {
		Level int `yaml:"level"`
	} `yaml:"nested"`
}

func TestCheckUnknownKeys(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string // substring; empty means nil error
	}{
		{name: "known keys only", input: "name: a\nnested:\n  level: 3\n"},
		{name: "empty document", input: ""},
		{name: "comment-only document", input: "# just a comment\n"},
		{name: "top-level unknown key", input: "name: a\nnome_typo: b\n", wantErr: "field nome_typo not found"},
		{name: "nested unknown key", input: "nested:\n  depth_typo: 3\n", wantErr: "field depth_typo not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := yamlenv.CheckUnknownKeys([]byte(tc.input), &probeConfig{})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckUnknownKeys = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CheckUnknownKeys = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestCheckUnknownKeysErrorSanitizes pins the documented pairing: the raw
// unknown-key error may embed document content, and SanitizeDecodeError
// recognizes its entry shape and rewrites it value-independently.
func TestCheckUnknownKeysErrorSanitizes(t *testing.T) {
	err := yamlenv.CheckUnknownKeys([]byte("secret_key_name: x\n"), &probeConfig{})
	if err == nil {
		t.Fatal("CheckUnknownKeys = nil, want unknown-key error")
	}
	if _, ok := errors.AsType[*yaml.LoadErrors](err); !ok {
		t.Errorf("CheckUnknownKeys = %v (%T), want go-yaml's structured *yaml.LoadErrors", err, err)
	}
	sanitized := yamlenv.SanitizeDecodeError(err)
	if !strings.Contains(sanitized.Error(), "unknown configuration key") {
		t.Errorf("sanitized = %q, want the value-independent unknown-key rewrite", sanitized.Error())
	}
	if strings.Contains(sanitized.Error(), "secret_key_name") {
		t.Errorf("sanitized = %q, want the key name redacted by default", sanitized.Error())
	}
}

func TestCheckSingleDocument(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantMult bool
	}{
		{name: "single document", input: "a: b\n"},
		{name: "single document no trailing newline", input: "a: b"},
		{name: "empty input", input: ""},
		{name: "leading separator only", input: "---\na: b\n"},
		{name: "comment only", input: "# nothing\n"},
		{name: "document end marker", input: "a: b\n...\n"},
		{name: "unparseable first document", input: "a: [\n"}, // parse steps own this
		{name: "two documents", input: "a: b\n---\nc: d\n", wantMult: true},
		{name: "trailing separator empty second doc", input: "a: b\n---\n", wantMult: true},
		{name: "syntax error in second document", input: "a: b\n---\nc: [\n", wantMult: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := yamlenv.CheckSingleDocument([]byte(tc.input))
			if tc.wantMult {
				if !errors.Is(err, yamlenv.ErrMultipleDocuments) {
					t.Fatalf("CheckSingleDocument = %v, want ErrMultipleDocuments", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckSingleDocument = %v, want nil", err)
			}
		})
	}
}
