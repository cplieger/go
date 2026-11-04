package envx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequire(t *testing.T) {
	t.Run("set returns value", func(t *testing.T) {
		t.Setenv("ENVX_TEST_REQ", "v")
		got, err := Require("ENVX_TEST_REQ")
		if err != nil || got != "v" {
			t.Errorf("Require() = (%q, %v), want (v, nil)", got, err)
		}
	})
	t.Run("unset returns MissingError", func(t *testing.T) {
		_, err := Require("ENVX_TEST_REQ_UNSET")
		me, ok := errors.AsType[*MissingError](err)
		if !ok {
			t.Fatalf("Require() error = %v, want *MissingError", err)
		}
		if me.Key != "ENVX_TEST_REQ_UNSET" {
			t.Errorf("MissingError.Key = %q", me.Key)
		}
		if !strings.Contains(me.Error(), "ENVX_TEST_REQ_UNSET") {
			t.Errorf("Error() = %q, should name the key", me.Error())
		}
	})
	t.Run("empty returns MissingError", func(t *testing.T) {
		t.Setenv("ENVX_TEST_REQ", "")
		if _, err := Require("ENVX_TEST_REQ"); err == nil {
			t.Error("Require() on empty = nil error, want *MissingError")
		}
	})
}

func TestSecret(t *testing.T) {
	t.Run("plain env value", func(t *testing.T) {
		t.Setenv("ENVX_TEST_SEC", "s3cret")
		got, err := Secret("ENVX_TEST_SEC")
		if err != nil || got != "s3cret" {
			t.Errorf("Secret() = (%q, %v), want (s3cret, nil)", got, err)
		}
	})
	t.Run("unset returns MissingError", func(t *testing.T) {
		_, err := Secret("ENVX_TEST_SEC_UNSET")
		if _, ok := errors.AsType[*MissingError](err); !ok {
			t.Fatalf("Secret() error = %v, want *MissingError", err)
		}
	})
	t.Run("file variant wins over plain", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(p, []byte("from-file\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ENVX_TEST_SEC", "from-env")
		t.Setenv("ENVX_TEST_SEC_FILE", p)
		got, err := Secret("ENVX_TEST_SEC")
		if err != nil || got != "from-file" {
			t.Errorf("Secret() = (%q, %v), want the file content minus its line ending", got, err)
		}
	})
	t.Run("missing file is an error naming key and path, not the value", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "nope")
		t.Setenv("ENVX_TEST_SEC_FILE", p)
		_, err := Secret("ENVX_TEST_SEC")
		if err == nil {
			t.Fatal("Secret() = nil error for missing file")
		}
		if !strings.Contains(err.Error(), "ENVX_TEST_SEC") {
			t.Errorf("error should name the key: %v", err)
		}
	})
	t.Run("empty file is an error", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "empty")
		if err := os.WriteFile(p, []byte("  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ENVX_TEST_SEC_FILE", p)
		if _, err := Secret("ENVX_TEST_SEC"); err == nil {
			t.Error("Secret() = nil error for whitespace-only file")
		}
	})
	t.Run("traversal path rejected", func(t *testing.T) {
		t.Setenv("ENVX_TEST_SEC_FILE", "/run/secrets/../../etc/passwd")
		if _, err := Secret("ENVX_TEST_SEC"); err == nil {
			t.Error("Secret() = nil error for traversal path")
		}
	})
	t.Run("oversized file rejected", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "big")
		if err := os.WriteFile(p, make([]byte, maxSecretFileSize+1), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ENVX_TEST_SEC_FILE", p)
		_, err := Secret("ENVX_TEST_SEC")
		if err == nil {
			t.Fatal("Secret() = nil error for oversized file")
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error should mention the size bound: %v", err)
		}
	})
	t.Run("secret value never in error text", func(t *testing.T) {
		// The empty-file and missing-file errors carry key + path only. This
		// guards the redaction contract for the paths that do error.
		p := filepath.Join(t.TempDir(), "empty2")
		if err := os.WriteFile(p, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ENVX_TEST_SEC_FILE", p)
		_, err := Secret("ENVX_TEST_SEC")
		if err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("error should carry the file path for diagnosis: %v", err)
		}
	})
}

// TestSecret_file_exactly_at_the_size_limit_is_read_whole pins the size gate at
// its boundary. The ceiling exists to refuse a device file or a runaway log, so
// a real secret whose length lands exactly on it is content rather than an
// overrun, and a gate that refused it would reject the largest file the limit
// was written to allow. Both gates see that same length — the pre-read stat and
// the post-read re-check that catches a file growing underneath the read — so
// the value arriving whole is what says neither one is off by a byte.
func TestSecret_file_exactly_at_the_size_limit_is_read_whole(t *testing.T) {
	p := filepath.Join(t.TempDir(), "atlimit")
	secret := strings.Repeat("s", maxSecretFileSize)
	if err := os.WriteFile(p, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENVX_TEST_SEC", "from-env")
	t.Setenv("ENVX_TEST_SEC_FILE", p)

	got, err := Secret("ENVX_TEST_SEC")
	if err != nil {
		t.Fatalf("Secret() for a %d byte file = %v, want the content: the limit is a ceiling, not an exclusive bound", maxSecretFileSize, err)
	}
	if len(got) != maxSecretFileSize {
		t.Errorf("Secret() returned %d bytes, want %d", len(got), maxSecretFileSize)
	}
	if got != secret {
		t.Errorf("Secret() returned %d bytes differing from the file content, want it read whole and unaltered", len(got))
	}
}

// TestSecret_failure_classes_are_nameable pins the reason the class sentinels exist: a
// caller must be able to say WHY a secret file was unusable without matching this
// package's error text and without echoing the operator-supplied path, which is the one
// thing that must not reach a log (a KEY_FILE misconfigured to hold the secret ITSELF is
// the failure mode). Each case also pins the message the class rides alongside, and that
// the classes do not overlap.
func TestSecret_failure_classes_are_nameable(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, make([]byte, maxSecretFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	blank := filepath.Join(dir, "blank")
	if err := os.WriteFile(blank, []byte(" \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	unclean := "/run/secrets/../token"

	allClasses := []error{
		ErrSecretFilePathRejected,
		ErrSecretFileTooLarge,
		ErrSecretFileGrew,
		ErrSecretFileUnreadable,
		ErrBlankSecretFile,
	}

	for name, tc := range map[string]struct {
		path string
		want error
		text string // must survive verbatim inside the message
	}{
		"an unclean path is a policy rejection": {
			path: unclean,
			want: ErrSecretFilePathRejected,
			text: `secret file path rejected (must be clean and contain no ".." path component): ` + unclean,
		},
		"an oversized file is its own class": {
			path: big,
			want: ErrSecretFileTooLarge,
			text: fmt.Sprintf("secret file is %d bytes, exceeds %d byte limit", maxSecretFileSize+1, maxSecretFileSize),
		},
		"blank content keeps its own sentinel": {
			path: blank,
			want: ErrBlankSecretFile,
		},
		"an absent file is an OS failure": {
			path: filepath.Join(dir, "nope"),
			want: ErrSecretFileUnreadable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ENVX_TEST_SEC", "from-env")
			t.Setenv("ENVX_TEST_SEC_FILE", tc.path)

			v, err := Secret("ENVX_TEST_SEC")
			if err == nil {
				t.Fatalf("Secret() = %q, nil; want an error", v)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("Secret() = %v, want errors.Is %v", err, tc.want)
			}
			for _, other := range allClasses {
				if other != tc.want && errors.Is(err, other) {
					t.Errorf("Secret() = %v also matches %v; the classes must not overlap", err, other)
				}
			}
			if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
				t.Errorf("Secret() = %q, want it to still contain %q verbatim", err, tc.text)
			}
		})
	}
}

// TestSecret_secret_file_path_rule pins the KEY_FILE path rule at full precision, both
// halves of it.
//
// The rule is the OR of pathinside's two hygiene predicates — !IsCanonical(path) ||
// HasDotDot(path) — and it judges the path AS WRITTEN. It replaced a substring ".." test,
// which loosens exactly one class and no other: a path that is ALREADY in Clean form and
// whose ".." occurrences all sit inside a component NAME. Such a path traverses nowhere
// (every component is an ordinary name, and Clean form means the string opened is the
// string validated), so refusing it bought nothing. The accepted half is asserted by
// actually reading the secret rather than by the absence of a rejection: the point is
// that these operators get their credential, not merely a different error.
//
// The refused half is asserted exhaustively by shape, because that is the half a
// loosening could have damaged: any ".." component, and any unclean spelling whether or
// not it traverses.
func TestSecret_secret_file_path_rule(t *testing.T) {
	dir := t.TempDir()
	// The canonical spelling of a real, readable secret file whose NAME carries two
	// dots, plus a subdirectory to spell a traversal through. Both exist before any
	// case runs, so every refusal below is the rule's and never the filesystem's.
	realFile := filepath.Join(dir, "key..v2")
	if err := os.WriteFile(realFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("two dots inside a name is an ordinary file and is read", func(t *testing.T) {
		for _, name := range []string{"key..v2", "...", "....", "token..", "..v2"} {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte("s3cret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENVX_TEST_PATHRULE_FILE", p)
			got, err := Secret("ENVX_TEST_PATHRULE")
			if err != nil {
				t.Errorf("Secret() for %q = %v; %q is an ordinary filename, not a traversal", name, err, name)
				continue
			}
			if got != "s3cret" {
				t.Errorf("Secret() for %q = %q, want s3cret", name, got)
			}
		}
	})

	t.Run("a directory whose name begins with two dots is traversed into normally", func(t *testing.T) {
		sub := filepath.Join(dir, "..extras")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(sub, "token")
		if err := os.WriteFile(p, []byte("s3cret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ENVX_TEST_PATHRULE_FILE", p)
		got, err := Secret("ENVX_TEST_PATHRULE")
		if err != nil {
			t.Fatalf("Secret() = %v, want the file read: %q is a directory name, not a traversal", err, "..extras")
		}
		if got != "s3cret" {
			t.Errorf("Secret() = %q, want s3cret", got)
		}
	})

	// Two shapes are refused: a ".." COMPONENT, and an unclean spelling whether or not
	// it traverses. The last four resolve to realFile, so they pin that the rule judges
	// the spelling and not the destination — and that a refused pointer never falls
	// through to the plain KEY.
	for name, path := range map[string]string{
		"the traversal itself":                       "..",
		"a leading traversal":                        "../token",
		"an interior traversal":                      "/run/secrets/../token",
		"a traversal Clean would normalize away":     "/run/secrets/../../etc/shadow",
		"a relative traversal leaving its own tree":  "a/../../b",
		"a relative path spelled with a leading dot": "./token",
		"a traversal that resolves to the real file": filepath.Join(dir, "sub") + "/../key..v2",
		`a "." component before the real file`:       dir + "/./key..v2",
		"a doubled separator before the real file":   dir + "//key..v2",
		"a trailing separator on the real file":      realFile + "/",
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			t.Setenv("ENVX_TEST_PATHRULE", "from-env")
			t.Setenv("ENVX_TEST_PATHRULE_FILE", path)
			v, err := Secret("ENVX_TEST_PATHRULE")
			if err == nil {
				t.Fatalf("Secret() = %q, nil for %q; want the path rule to refuse it", v, path)
			}
			if !errors.Is(err, ErrSecretFilePathRejected) {
				t.Errorf("Secret() = %v for %q, want errors.Is ErrSecretFilePathRejected (refused by the rule, not by the filesystem)", err, path)
			}
			if v != "" {
				t.Errorf("value = %q for a refused path, want empty: a broken pointer never falls through to KEY", v)
			}
		})
	}
}

// TestSecret_OS_failure_keeps_the_PathError pins the half of the OS-failure class the
// sentinel cannot carry: consumers key on *os.PathError to report the syscall and its
// reason in their own words, so classifying the failure must not hide the cause. The
// message is asserted for exact equality because the class was added without rewriting
// any existing text.
func TestSecret_OS_failure_keeps_the_PathError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope")
	t.Setenv("ENVX_TEST_SEC", "from-env")
	t.Setenv("ENVX_TEST_SEC_FILE", p)

	_, err := Secret("ENVX_TEST_SEC")
	if err == nil {
		t.Fatal("Secret() = nil error for an absent file")
	}
	pathErr, ok := errors.AsType[*os.PathError](err)
	if !ok {
		t.Fatalf("Secret() = %v, want a reachable *os.PathError", err)
	}
	if !errors.Is(pathErr.Err, fs.ErrNotExist) {
		t.Errorf("PathError.Err = %v, want fs.ErrNotExist", pathErr.Err)
	}
	if pathErr.Path != p {
		t.Errorf("PathError.Path = %q, want %q", pathErr.Path, p)
	}
	if want := "read secret file for ENVX_TEST_SEC: " + pathErr.Error(); err.Error() != want {
		t.Errorf("Secret() = %q, want %q: classifying the failure must not alter the message", err, want)
	}
}

// TestSecret_stream_longer_than_its_stat_size is the grew-during-read class. A character
// device is the reachable shape of it: Stat reports 0 bytes so the size gate passes, and
// the bounded read then returns more than the limit — exactly the silent-truncation case
// the post-read length check exists to refuse, and the reason the size ceiling mentions
// device files at all.
func TestSecret_stream_longer_than_its_stat_size(t *testing.T) {
	const dev = "/dev/zero"
	if _, err := os.Stat(dev); err != nil {
		t.Skipf("no %s on this platform: %v", dev, err)
	}
	t.Setenv("ENVX_TEST_SEC", "from-env")
	t.Setenv("ENVX_TEST_SEC_FILE", dev)

	v, err := Secret("ENVX_TEST_SEC")
	if err == nil {
		t.Fatalf("Secret() = %q, nil; want the size-limit refusal rather than truncated content", v)
	}
	if !errors.Is(err, ErrSecretFileGrew) {
		t.Errorf("Secret() = %v, want errors.Is ErrSecretFileGrew", err)
	}
	if v != "" {
		t.Errorf("value = %q, want empty", v)
	}
	if want := fmt.Sprintf("secret file grew past the %d byte limit during read", maxSecretFileSize); !strings.Contains(err.Error(), want) {
		t.Errorf("Secret() = %q, want it to still contain %q verbatim", err, want)
	}
}
