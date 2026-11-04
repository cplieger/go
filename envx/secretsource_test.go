package envx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecretWithSource_reports_the_channel pins the whole point of this function: a
// caller must be able to tell WHICH channel supplied the value, because Secret's
// KEY_FILE-wins precedence means an environment variable the operator also set is
// silently ignored.
func TestSecretWithSource_reports_the_channel(t *testing.T) {
	t.Run("file wins over env and is reported as such", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(p, []byte("from-file\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ENVX_SWS", "from-env")
		t.Setenv("ENVX_SWS_FILE", p)

		v, src, err := SecretWithSource("ENVX_SWS")
		if err != nil {
			t.Fatalf("SecretWithSource = %v, want nil", err)
		}
		if v != "from-file" {
			t.Errorf("value = %q, want %q (file content minus its line ending, file wins)", v, "from-file")
		}
		if src != SourceFile {
			t.Errorf("source = %q, want %q", src, SourceFile)
		}
	})

	t.Run("env is reported when no file is configured", func(t *testing.T) {
		t.Setenv("ENVX_SWS", "from-env")
		t.Setenv("ENVX_SWS_FILE", "")

		v, src, err := SecretWithSource("ENVX_SWS")
		if err != nil {
			t.Fatalf("SecretWithSource = %v, want nil", err)
		}
		if v != "from-env" || src != SourceEnv {
			t.Errorf("SecretWithSource = (%q, %q), want (%q, %q)", v, src, "from-env", SourceEnv)
		}
	})

	t.Run("neither channel set reports SourceNone with MissingError", func(t *testing.T) {
		t.Setenv("ENVX_SWS", "")
		t.Setenv("ENVX_SWS_FILE", "")

		_, src, err := SecretWithSource("ENVX_SWS")
		if _, ok := errors.AsType[*MissingError](err); !ok {
			t.Errorf("SecretWithSource = %v, want *MissingError", err)
		}
		if src != SourceNone {
			t.Errorf("source = %q, want %q", src, SourceNone)
		}
	})
}

// TestSecretWithSource_file_channel_returns_the_value_as_written pins the asymmetry this
// package used to carry: the env channel returns os.Getenv verbatim while the file
// channel ran strings.TrimSpace, so one credential resolved to two different secrets
// depending on how it was delivered. A consumer that validates a credential verbatim was
// handed a silently rewritten value. Only a single trailing line ending — the artifact of
// storing a value in a file at all — is removed now.
func TestSecretWithSource_file_channel_returns_the_value_as_written(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		want    string
	}{
		"one trailing newline is removed":         {content: "s3cret\n", want: "s3cret"},
		"a CRLF line ending is removed":           {content: "s3cret\r\n", want: "s3cret"},
		"no line ending is fine":                  {content: "s3cret", want: "s3cret"},
		"only ONE line ending is removed":         {content: "s3cret\n\n", want: "s3cret\n"},
		"a bare CR is not a line ending here":     {content: "s3cret\r", want: "s3cret\r"},
		"trailing spaces are content":             {content: "s3cret  ", want: "s3cret  "},
		"leading spaces are content":              {content: "  s3cret", want: "  s3cret"},
		"edge spaces survive the line-ending cut": {content: " s3cret \n", want: " s3cret "},
		"a trailing tab is content":               {content: "s3cret\t", want: "s3cret\t"},
		"a leading newline is content":            {content: "\ns3cret\n", want: "\ns3cret"},
		"a non-breaking space is content":         {content: "s3cret\u00a0\n", want: "s3cret\u00a0"},
		"interior whitespace is untouched":        {content: "two words\n", want: "two words"},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "secret")
			if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENVX_SWS", "")
			t.Setenv("ENVX_SWS_FILE", p)

			v, src, err := SecretWithSource("ENVX_SWS")
			if err != nil {
				t.Fatalf("SecretWithSource(%q) = %v, want nil", tc.content, err)
			}
			if v != tc.want {
				t.Errorf("SecretWithSource(%q) = %q, want %q", tc.content, v, tc.want)
			}
			if src != SourceFile {
				t.Errorf("source = %q, want %q", src, SourceFile)
			}
		})
	}
}

// TestSecretWithSource_channels_agree_on_the_value is the property the trim rule exists
// to protect: for a secret an operator could write into either channel, both channels
// must resolve to the SAME string. A caller that validates the credential (rejecting edge
// whitespace, say) must not get a different verdict per delivery mechanism.
func TestSecretWithSource_channels_agree_on_the_value(t *testing.T) {
	for _, secret := range []string{
		"s3cret",
		" leading",
		"trailing ",
		"\ttab-edged\t",
		"two words",
		"with\u00a0nbsp",
		"https://discord.com/api/webhooks/1/tok en",
	} {
		t.Run(secret, func(t *testing.T) {
			// The file channel: the secret plus the line ending a file inevitably gains.
			p := filepath.Join(t.TempDir(), "secret")
			if err := os.WriteFile(p, []byte(secret+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENVX_SWS", secret)
			t.Setenv("ENVX_SWS_FILE", p)
			fromFile, src, err := SecretWithSource("ENVX_SWS")
			if err != nil || src != SourceFile {
				t.Fatalf("file channel = (%q, %q, %v), want (_, %q, nil)", fromFile, src, err, SourceFile)
			}

			t.Setenv("ENVX_SWS_FILE", "")
			fromEnv, src, err := SecretWithSource("ENVX_SWS")
			if err != nil || src != SourceEnv {
				t.Fatalf("env channel = (%q, %q, %v), want (_, %q, nil)", fromEnv, src, err, SourceEnv)
			}

			if fromFile != fromEnv {
				t.Errorf("file channel = %q, env channel = %q; the same secret must resolve identically", fromFile, fromEnv)
			}
		})
	}
}

// ErrBlankSecretFile exists: a caller with an explicit opt-in for running without a
// secret has to apply that policy identically to both channels, and it can only do so if
// "the file you mounted is blank" is distinguishable from "the file is unusable" WITHOUT
// matching this package's error text.
func TestSecretWithSource_blank_file_is_a_distinct_sentinel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blank")
	if err := os.WriteFile(p, []byte("  \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENVX_SWS", "")
	t.Setenv("ENVX_SWS_FILE", p)

	_, src, err := SecretWithSource("ENVX_SWS")
	if !errors.Is(err, ErrBlankSecretFile) {
		t.Fatalf("SecretWithSource(blank file) = %v, want ErrBlankSecretFile", err)
	}
	// Not a MissingError: the operator DID configure a file, and collapsing the two
	// would hide a broken secret mount behind a caller's default for absence.
	if _, ok := errors.AsType[*MissingError](err); ok {
		t.Error("blank file reported as *MissingError; a configured-but-blank file is not an absent one")
	}
	if src != SourceFile {
		t.Errorf("source = %q, want %q even on the error path", src, SourceFile)
	}
}

// TestSecretWithSource_unreadable_file_keeps_the_channel pins that the source is
// meaningful on every error path. This is when it matters most: the caller needs to tell
// the operator that the environment variable they also set is NOT a fallback.
func TestSecretWithSource_unreadable_file_keeps_the_channel(t *testing.T) {
	t.Setenv("ENVX_SWS", "from-env")
	t.Setenv("ENVX_SWS_FILE", filepath.Join(t.TempDir(), "does-not-exist"))

	v, src, err := SecretWithSource("ENVX_SWS")
	if err == nil {
		t.Fatalf("SecretWithSource(missing file) = %q, nil; want an error rather than the env fallback", v)
	}
	if v != "" {
		t.Errorf("value = %q, want empty: an unusable file must never fall back to the env value", v)
	}
	if src != SourceFile {
		t.Errorf("source = %q, want %q", src, SourceFile)
	}
}

// TestSecretWithSource_never_leaks_the_value pins this package's standing rule against
// the new error path: errors carry the key and the path, never the secret.
func TestSecretWithSource_never_leaks_the_value(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blank")
	if err := os.WriteFile(p, []byte("   "), 0o600); err != nil {
		t.Fatal(err)
	}
	const sentinel = "sentinel-secret-value"
	t.Setenv("ENVX_SWS", sentinel)
	t.Setenv("ENVX_SWS_FILE", p)

	_, _, err := SecretWithSource("ENVX_SWS")
	if err == nil {
		t.Fatal("want an error for a blank file")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Errorf("error %q leaked the secret value", err)
	}
	if !strings.Contains(err.Error(), "ENVX_SWS") || !strings.Contains(err.Error(), p) {
		t.Errorf("error %q must name the key and the path so the mount is diagnosable", err)
	}
}

// TestSecret_delegates_to_SecretWithSource pins that the two cannot drift: Secret is a
// thin wrapper, so every resolution rule and every error is shared by construction.
func TestSecret_delegates_to_SecretWithSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, env, file string }{
		{"file channel", "ignored", p},
		{"env channel", "plain", ""},
		{"neither", "", ""},
		{"unreadable file", "ignored", filepath.Join(t.TempDir(), "nope")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENVX_SWS", tc.env)
			t.Setenv("ENVX_SWS_FILE", tc.file)

			wantV, _, wantErr := SecretWithSource("ENVX_SWS")
			gotV, gotErr := Secret("ENVX_SWS")

			if gotV != wantV {
				t.Errorf("Secret = %q, SecretWithSource = %q; they must agree", gotV, wantV)
			}
			if (gotErr == nil) != (wantErr == nil) {
				t.Errorf("Secret err = %v, SecretWithSource err = %v; they must agree", gotErr, wantErr)
			}
		})
	}
}

// TestIsBlankSecretFilePath pins the state the resolver cannot express: a KEY_FILE the
// operator DID write, holding no path. Present-but-empty is the shape compose
// interpolation of an undefined variable produces, and it is the one the resolver reads
// as absence.
func TestIsBlankSecretFilePath(t *testing.T) {
	for name, tc := range map[string]struct {
		set   bool // whether KEY_FILE is present at all
		value string
		want  bool
	}{
		"unset is not blank":                       {set: false, want: false},
		"present and empty is blank":               {set: true, value: "", want: true},
		"single space is blank":                    {set: true, value: " ", want: true},
		"spaces are blank":                         {set: true, value: "   ", want: true},
		"tab and newline are blank":                {set: true, value: "\t\n", want: true},
		"non-breaking space is blank":              {set: true, value: "\u00a0", want: true},
		"absolute path is not blank":               {set: true, value: "/run/secrets/token", want: false},
		"relative path is not blank":               {set: true, value: "secrets/token", want: false},
		"padded path is not blank":                 {set: true, value: " /run/secrets/token ", want: false},
		"a path this package rejects is not blank": {set: true, value: "../etc/passwd", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.set {
				t.Setenv("ENVX_BLANK_FILE", tc.value)
			} else {
				// t.Setenv restores the previous state, including absence.
				t.Setenv("ENVX_BLANK_FILE", "placeholder")
				if err := os.Unsetenv("ENVX_BLANK_FILE"); err != nil {
					t.Fatal(err)
				}
			}
			if got := IsBlankSecretFilePath("ENVX_BLANK"); got != tc.want {
				t.Errorf("IsBlankSecretFilePath(%q set=%v) = %v, want %v", tc.value, tc.set, got, tc.want)
			}
		})
	}
}

// TestIsBlankSecretFilePath_reports_without_changing_resolution is the compatibility
// contract, and the reason this is a predicate rather than a new source or a new
// sentinel: every consumer that already resolves a secret with a blank KEY_FILE in its
// environment must see exactly what it saw before. A deployment where `KEY_FILE=` falls
// through to KEY keeps starting; only a caller that ASKS learns the pointer was blank.
func TestIsBlankSecretFilePath_reports_without_changing_resolution(t *testing.T) {
	t.Run("empty file path still resolves through the env channel", func(t *testing.T) {
		t.Setenv("ENVX_SWS", "from-env")
		t.Setenv("ENVX_SWS_FILE", "")

		v, src, err := SecretWithSource("ENVX_SWS")
		if v != "from-env" || src != SourceEnv || err != nil {
			t.Errorf("SecretWithSource = (%q, %q, %v), want (%q, %q, nil): precedence must be unchanged",
				v, src, err, "from-env", SourceEnv)
		}
		if !IsBlankSecretFilePath("ENVX_SWS") {
			t.Error("IsBlankSecretFilePath = false, want true: the operator did set an empty _FILE")
		}
	})

	t.Run("empty file path with no env value is still MissingError", func(t *testing.T) {
		t.Setenv("ENVX_SWS", "")
		t.Setenv("ENVX_SWS_FILE", "")

		_, src, err := SecretWithSource("ENVX_SWS")
		if _, ok := errors.AsType[*MissingError](err); !ok || src != SourceNone {
			t.Errorf("SecretWithSource = (%q, %v), want (%q, *MissingError)", src, err, SourceNone)
		}
		if !IsBlankSecretFilePath("ENVX_SWS") {
			t.Error("IsBlankSecretFilePath = false, want true")
		}
	})

	t.Run("whitespace-only file path is still opened and still fails", func(t *testing.T) {
		t.Setenv("ENVX_SWS", "from-env")
		t.Setenv("ENVX_SWS_FILE", "   ")

		v, src, err := SecretWithSource("ENVX_SWS")
		if err == nil {
			t.Fatalf("SecretWithSource = %q, nil; want the unchanged failure for an unopenable path", v)
		}
		if v != "" || src != SourceFile {
			t.Errorf("SecretWithSource = (%q, %q), want (%q, %q): a set _FILE is never a fallback to the env value",
				v, src, "", SourceFile)
		}
		if !IsBlankSecretFilePath("ENVX_SWS") {
			t.Error("IsBlankSecretFilePath = false, want true: a whitespace-only path names no file either")
		}
	})

	t.Run("valid path is not blank and still wins", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(p, []byte("from-file\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ENVX_SWS", "from-env")
		t.Setenv("ENVX_SWS_FILE", p)

		v, src, err := SecretWithSource("ENVX_SWS")
		if v != "from-file" || src != SourceFile || err != nil {
			t.Errorf("SecretWithSource = (%q, %q, %v), want (%q, %q, nil)", v, src, err, "from-file", SourceFile)
		}
		if IsBlankSecretFilePath("ENVX_SWS") {
			t.Error("IsBlankSecretFilePath = true, want false for a real path")
		}
	})

	t.Run("blank file CONTENT keeps its own sentinel and is not a blank path", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "blank")
		if err := os.WriteFile(p, []byte("  \n\t"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ENVX_SWS", "")
		t.Setenv("ENVX_SWS_FILE", p)

		_, src, err := SecretWithSource("ENVX_SWS")
		if !errors.Is(err, ErrBlankSecretFile) || src != SourceFile {
			t.Errorf("SecretWithSource = (%q, %v), want (%q, ErrBlankSecretFile)", src, err, SourceFile)
		}
		// The two blank conditions are distinct: this pointer names a real file, and a
		// caller routing blank CONTENT through an allow-empty opt-out must not have that
		// opt-out silently widened to cover a broken mount.
		if IsBlankSecretFilePath("ENVX_SWS") {
			t.Error("IsBlankSecretFilePath = true for a path naming a blank file; only the PATH's blankness is its subject")
		}
	})
}

// TestIsBlankSecretFilePath_builds_the_companion_key pins that the predicate and the
// resolver agree on WHICH variable they read: the key itself being blank must not be
// mistaken for the pointer being blank.
func TestIsBlankSecretFilePath_builds_the_companion_key(t *testing.T) {
	t.Setenv("ENVX_SWS", "   ")
	t.Setenv("ENVX_SWS_FILE", "/run/secrets/token")

	if IsBlankSecretFilePath("ENVX_SWS") {
		t.Error("IsBlankSecretFilePath reads KEY, not KEY_FILE")
	}
	if got := secretFileKey("ENVX_SWS"); got != "ENVX_SWS_FILE" {
		t.Errorf("secretFileKey = %q, want %q", got, "ENVX_SWS_FILE")
	}
}
