//go:build !windows

package filesafe

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAbsClean(t *testing.T) {
	t.Parallel()

	t.Run("rejects_relative_path", func(t *testing.T) {
		t.Parallel()
		if _, err := validateAbsClean("relative/path"); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("validateAbsClean(relative) = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("collapses_traversal_to_clean_absolute_path", func(t *testing.T) {
		t.Parallel()
		got, err := validateAbsClean("/foo/../etc/passwd")
		if err != nil {
			t.Fatalf("validateAbsClean(%q) = error %v, want nil (Clean removes \"..\")",
				"/foo/../etc/passwd", err)
		}
		if got != "/etc/passwd" {
			t.Errorf("validateAbsClean(%q) = %q, want %q", "/foo/../etc/passwd", got, "/etc/passwd")
		}
	})

	t.Run("accepts_literal_dots_in_path_segment", func(t *testing.T) {
		t.Parallel()
		for _, in := range []string{"/tmp/foo../bar", "/tmp/.../file"} {
			got, err := validateAbsClean(in)
			if err != nil {
				t.Fatalf("validateAbsClean(%q) = %v, want nil (literal dots are not traversal)", in, err)
			}
			if got != filepath.Clean(in) {
				t.Fatalf("validateAbsClean(%q) = %q, want %q", in, got, filepath.Clean(in))
			}
		}
	})

	t.Run("accepts_absolute_clean_path", func(t *testing.T) {
		t.Parallel()
		got, err := validateAbsClean("/tmp/test.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/tmp/test.txt" {
			t.Errorf("got %q, want %q", got, "/tmp/test.txt")
		}
	})

	t.Run("rejects_empty", func(t *testing.T) {
		t.Parallel()
		if _, err := validateAbsClean(""); !errors.Is(err, ErrEmptyPath) {
			t.Fatalf("validateAbsClean(empty) = %v, want ErrEmptyPath", err)
		}
	})

	t.Run("rejects_null_byte", func(t *testing.T) {
		t.Parallel()
		_, err := validateAbsClean("/tmp/foo\x00bar")
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("validateAbsClean(null) = %v, want ErrUnsafePath", err)
		}
		if !strings.Contains(err.Error(), "null byte") {
			t.Errorf("error = %q, want mention of null byte", err.Error())
		}
	})

	t.Run("rejects_null_byte_suffix", func(t *testing.T) {
		t.Parallel()
		if _, err := validateAbsClean("/tmp/test.txt\x00ignored"); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("validateAbsClean(null suffix) = %v, want ErrUnsafePath", err)
		}
	})
}

// TestValidatePath covers the exported gate: the same verdicts as the private
// validator behind it (absolute accepted, relative / empty / NUL rejected with
// the sentinel the write path returns), plus the delegation itself — every case
// asserts that ValidatePath and validateAbsClean agree, so the two can never
// become two rules.
func TestValidatePath(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		wantErr error
		name    string
		path    string
	}{
		{name: "accepts_absolute_path", path: "/tmp/test.txt"},
		{name: "accepts_absolute_path_needing_clean", path: "/tmp//sub/./test.txt"},
		// Clean normalizes ".." in an absolute path: accepted, and documented
		// as not being a containment boundary.
		{name: "accepts_absolute_path_with_traversal", path: "/foo/../etc/passwd"},
		{name: "rejects_relative_path", path: "relative/path", wantErr: ErrUnsafePath},
		{name: "rejects_bare_relative_name", path: "file.txt", wantErr: ErrUnsafePath},
		{name: "rejects_empty_path", path: "", wantErr: ErrEmptyPath},
		{name: "rejects_null_byte", path: "/tmp/foo\x00bar", wantErr: ErrUnsafePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ValidatePath(tc.path)

			// One rule, one implementation: the exported gate must return
			// exactly what the write path's validator returns.
			_, want := validateAbsClean(tc.path)
			switch {
			case (got == nil) != (want == nil):
				t.Fatalf("ValidatePath(%q) = %v but validateAbsClean(%q) = %v; the exported gate must be the same rule the write path applies",
					tc.path, got, tc.path, want)
			case got != nil && got.Error() != want.Error():
				t.Errorf("ValidatePath(%q) = %q, want the validator's own %q", tc.path, got, want)
			}

			if tc.wantErr == nil {
				if got != nil {
					t.Errorf("ValidatePath(%q) = %v, want nil", tc.path, got)
				}
				return
			}
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("ValidatePath(%q) = %v, want %v", tc.path, got, tc.wantErr)
			}
		})
	}
}
