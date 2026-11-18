package envx

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecret_trailing_dot_alias_of_a_directory_is_refused pins that a path the rule
// accepts delivers only a regular file's content even where the OS rewrites it:
// Windows strips the trailing periods of dir\... and opens dir itself
// (https://learn.microsoft.com/en-us/dotnet/standard/io/file-path-formats#trim-characters).
func TestSecret_trailing_dot_alias_of_a_directory_is_refused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "...")
	t.Setenv("ENVX_TEST_PATHRULE", "from-env")
	t.Setenv("ENVX_TEST_PATHRULE_FILE", p)

	v, err := Secret("ENVX_TEST_PATHRULE")
	if errors.Is(err, ErrSecretFilePathRejected) {
		t.Fatalf("Secret() = %v for %q; the rule judges the spelling, and \"...\" is a name, not a \"..\" component", err, p)
	}
	if !errors.Is(err, ErrSecretFileUnreadable) {
		t.Errorf("Secret() = %v for %q, want errors.Is ErrSecretFileUnreadable", err, p)
	}
	if err != nil && !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("Secret() = %v for %q, want the not-a-regular-file reason: the alias names a directory", err, p)
	}
	if !v.IsZero() {
		t.Errorf("value = %q for a refused path, want empty: a broken pointer never falls through to KEY", v)
	}
}
