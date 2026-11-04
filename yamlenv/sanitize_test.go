package yamlenv_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cplieger/go/yamlenv"
	"go.yaml.in/yaml/v4"
)

const (
	withheld      = "configuration could not be decoded (details withheld: they may embed an expanded secret)"
	entryWithheld = "value rejected (details withheld: they may embed an expanded secret)"
)

// constructEntry builds a constructor-stage entry the way go-yaml's
// constructor does: Message is the cause's text and Unwrap returns the cause.
func constructEntry(line, column int, msg string) *yaml.LoadError {
	return yaml.NewLoadError(yaml.ConstructorStage, msg, yaml.Mark{Line: line, Column: column}, errors.New(msg))
}

func loadErrors(entries ...*yaml.LoadError) *yaml.LoadErrors {
	return &yaml.LoadErrors{Errors: entries}
}

func TestSanitizeDecodeErrorNil(t *testing.T) {
	t.Parallel()
	if got := yamlenv.SanitizeDecodeError(nil); got != nil {
		t.Errorf("SanitizeDecodeError(nil) = %v, want nil", got)
	}
	if got := yamlenv.SanitizeDecodeError(nil, yamlenv.WithUnknownKeyEcho(true)); got != nil {
		t.Errorf("SanitizeDecodeError(nil, WithUnknownKeyEcho(true)) = %v, want nil", got)
	}
}

// TestSanitizeDecodeErrorFallbacks pins the branches that keep at most a
// position: every one is a fixed message that cannot embed any fragment of
// the original error text.
func TestSanitizeDecodeErrorFallbacks(t *testing.T) {
	t.Parallel()
	const secret = "leaked-secret-sentinel"
	tests := map[string]struct {
		err  error
		want string
	}{
		"plain error": {
			err:  errors.New("decode blew up near " + secret),
			want: withheld,
		},
		"text that looks like a v3 syntax error": {
			err:  errors.New("yaml: line 7: found character that cannot start any token near " + secret),
			want: withheld,
		},
		"scanner error keeps its position": {
			err:  yaml.NewLoadError(yaml.ScannerStage, "found character near "+secret, yaml.Mark{Line: 7, Column: 3}, nil),
			want: "line 7, column 3: " + withheld,
		},
		"parser error without a column": {
			err:  yaml.NewLoadError(yaml.ParserStage, "did not find expected key "+secret, yaml.Mark{Line: 2}, nil),
			want: "line 2: " + withheld,
		},
		"resolver error with no position": {
			err:  yaml.NewLoadError(yaml.ResolverStage, "cannot construct !!str `"+secret+"` as a !!float", yaml.Mark{}, nil),
			want: withheld,
		},
		"aborting constructor error shaped like a wrong-type entry": {
			err:  constructEntry(4, 7, "cannot construct !!str `"+secret+"` into bool"),
			want: "line 4, column 7: " + withheld,
		},
		"empty LoadErrors": {
			err:  loadErrors(),
			want: withheld,
		},
		"unrecognized constructor entry": {
			err:  loadErrors(constructEntry(9, 2, "some future entry shape mentioning "+secret)),
			want: "decode errors: line 9, column 2: " + entryWithheld,
		},
		"wrong-type text from a non-constructor stage": {
			err: loadErrors(yaml.NewLoadError(yaml.ComposerStage, "cannot construct !!str `x` into bool "+secret,
				yaml.Mark{Line: 1, Column: 1}, nil)),
			want: "decode errors: line 1, column 1: " + entryWithheld,
		},
		"nil entry": {
			err:  loadErrors(nil),
			want: "decode errors: " + entryWithheld,
		},
		"entry with no position": {
			err:  loadErrors(constructEntry(0, 0, "mapping key \""+secret+"\" already defined at line 1")),
			want: "decode errors: mapping key <redacted> already defined at line 1",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := yamlenv.SanitizeDecodeError(tt.err).Error()
			if got != tt.want {
				t.Errorf("SanitizeDecodeError = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, secret) {
				t.Errorf("SanitizeDecodeError = %q, leaks the original text", got)
			}
		})
	}
}

// TestSanitizeDecodeErrorWrapped pins that a LoadErrors or LoadError wrapped
// by a caller is still found and rebuilt.
func TestSanitizeDecodeErrorWrapped(t *testing.T) {
	t.Parallel()
	const secret = "leaked-secret-sentinel"
	entries := loadErrors(constructEntry(3, 7, "cannot construct !!str `"+secret+"` into bool"))
	for name, err := range map[string]error{
		"errors.Join": errors.Join(entries),
		"fmt %w":      fmt.Errorf("parse config: %w", entries),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := yamlenv.SanitizeDecodeError(err).Error()
			if want := "decode errors: line 3, column 7: cannot construct !!str <redacted> into bool"; got != want {
				t.Errorf("SanitizeDecodeError = %q, want %q", got, want)
			}
		})
	}
	t.Run("wrapped single LoadError", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("parse: %w", yaml.NewLoadError(yaml.ParserStage, secret, yaml.Mark{Line: 5, Column: 1}, nil))
		if got, want := yamlenv.SanitizeDecodeError(err).Error(), "line 5, column 1: "+withheld; got != want {
			t.Errorf("SanitizeDecodeError = %q, want %q", got, want)
		}
	})
}

// TestSanitizeDecodeErrorWrongType pins the exact rebuild of the wrong-type
// entry for adversarial excerpt contents. It is the deterministic form of the
// property FuzzSanitizeDecodeError generates.
func TestSanitizeDecodeErrorWrongType(t *testing.T) {
	t.Parallel()
	const want = "decode errors: line 4, column 7: cannot construct !!str <redacted> into bool"
	excerpts := map[string]string{
		"plain secret":                     "hunter2-super-secret",
		"empty":                            "",
		"embedded backtick":                "zq9`vw7-secret",
		"embedded into marker":             "secret into bool trap",
		"embedded construct marker":        "cannot construct !!str `nested`",
		"embedded dup-key marker pair":     "mapping key y already defined at line 9",
		"embedded unknown-key marker pair": "field oops not found in type main.fileConfig",
		"embedded newline":                 "first\nsecond",
		"embedded line prefix":             "line 3: ",
	}
	for name, excerpt := range excerpts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := loadErrors(constructEntry(4, 7, "cannot construct !!str `"+excerpt+"` into bool"))
			for _, opts := range [][]yamlenv.SanitizeOption{nil, {yamlenv.WithUnknownKeyEcho(true)}} {
				if got := yamlenv.SanitizeDecodeError(err, opts...).Error(); got != want {
					t.Errorf("SanitizeDecodeError(excerpt %q) = %q, want %q", excerpt, got, want)
				}
			}
		})
	}

	tests := map[string]struct {
		msg  string
		want string
	}{
		"collection tag has no excerpt": {
			msg:  "cannot construct !!seq into int",
			want: "cannot construct !!seq into int",
		},
		"TextUnmarshaler destination": {
			msg:  "cannot construct !!map into time.Time (TextUnmarshaler)",
			want: "cannot construct !!map into time.Time (TextUnmarshaler)",
		},
		"custom tag is redacted with the value": {
			msg:  "cannot construct !s3cret-tag `val` into int",
			want: "cannot construct <redacted> into int",
		},
		"custom secondary-handle tag is redacted": {
			msg:  "cannot construct !!s3cret `val` into int",
			want: "cannot construct <redacted> into int",
		},
		"no destination marker": {
			msg:  "cannot construct node with unknown kind: '9'",
			want: entryWithheld,
		},
		"text before the shape": {
			msg:  "leaked-prefix cannot construct !!str `x` into bool",
			want: entryWithheld,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := yamlenv.SanitizeDecodeError(loadErrors(constructEntry(2, 5, tt.msg))).Error()
			if want := "decode errors: line 2, column 5: " + tt.want; got != want {
				t.Errorf("SanitizeDecodeError(%q) = %q, want %q", tt.msg, got, want)
			}
		})
	}
}

// TestSanitizeDecodeErrorDuplicateKey pins the duplicate-key rebuild: the
// position and the first definition's line are kept, the key is redacted, and
// a key embedding the trailing marker cannot extend what survives.
func TestSanitizeDecodeErrorDuplicateKey(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		msg  string
		want string
	}{
		"plain duplicate key": {
			msg:  `mapping key "sonarr" already defined at line 1`,
			want: "decode errors: line 3, column 1: mapping key <redacted> already defined at line 1",
		},
		"key embedding the trailing marker": {
			msg:  `mapping key "a already defined at line 5 b" already defined at line 1`,
			want: "decode errors: line 3, column 1: mapping key <redacted> already defined at line 1",
		},
		"tail that is not a line number": {
			msg:  `mapping key "x" already defined at line 1 leaked-tail`,
			want: "decode errors: line 3, column 1: " + entryWithheld,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := yamlenv.SanitizeDecodeError(loadErrors(constructEntry(3, 1, tt.msg))).Error()
			if got != tt.want {
				t.Errorf("SanitizeDecodeError(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}

// TestSanitizeDecodeErrorUnknownKey pins the policy seam: the unknown-key
// entry redacts the key by default and echoes it only under
// WithUnknownKeyEcho; the Go type name is dropped either way.
func TestSanitizeDecodeErrorUnknownKey(t *testing.T) {
	t.Parallel()
	err := loadErrors(constructEntry(5, 1, "field anime_bytes not found in type main.fileConfig"))
	const redacted = "decode errors: line 5, column 1: unknown configuration key <redacted>"
	const echoed = `decode errors: line 5, column 1: unknown configuration key "anime_bytes"`

	tests := map[string]struct {
		opts []yamlenv.SanitizeOption
		want string
	}{
		"default redacts":      {want: redacted},
		"echo keeps the key":   {opts: []yamlenv.SanitizeOption{yamlenv.WithUnknownKeyEcho(true)}, want: echoed},
		"false spells default": {opts: []yamlenv.SanitizeOption{yamlenv.WithUnknownKeyEcho(false)}, want: redacted},
		"later false wins":     {opts: []yamlenv.SanitizeOption{yamlenv.WithUnknownKeyEcho(true), yamlenv.WithUnknownKeyEcho(false)}, want: redacted},
		"later opt-in wins":    {opts: []yamlenv.SanitizeOption{yamlenv.WithUnknownKeyEcho(false), yamlenv.WithUnknownKeyEcho(true)}, want: echoed},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := yamlenv.SanitizeDecodeError(err, tt.opts...).Error()
			if got != tt.want {
				t.Errorf("SanitizeDecodeError = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "main.fileConfig") {
				t.Errorf("SanitizeDecodeError = %q, leaks the Go type name", got)
			}
		})
	}

	t.Run("key embedding the trailing marker is cut at the last one", func(t *testing.T) {
		t.Parallel()
		e := loadErrors(constructEntry(2, 3, "field a not found in type b not found in type main.cfg"))
		got := yamlenv.SanitizeDecodeError(e, yamlenv.WithUnknownKeyEcho(true)).Error()
		if want := `decode errors: line 2, column 3: unknown configuration key "a not found in type b"`; got != want {
			t.Errorf("SanitizeDecodeError = %q, want %q", got, want)
		}
	})

	t.Run("already-set message with a key embedding the marker is withheld", func(t *testing.T) {
		t.Parallel()
		e := loadErrors(constructEntry(2, 3, "field a not found in type b already set in type main.cfg"))
		got := yamlenv.SanitizeDecodeError(e, yamlenv.WithUnknownKeyEcho(true)).Error()
		if want := "decode errors: line 2, column 3: " + entryWithheld; got != want {
			t.Errorf("SanitizeDecodeError = %q, want %q", got, want)
		}
	})

	t.Run("multiple entries join with semicolons", func(t *testing.T) {
		t.Parallel()
		multi := loadErrors(
			constructEntry(5, 1, "field anime_bytes not found in type main.fileConfig"),
			constructEntry(6, 3, "field animebytes not found in type main.filtersConfig"),
		)
		got := yamlenv.SanitizeDecodeError(multi, yamlenv.WithUnknownKeyEcho(true)).Error()
		want := `decode errors: line 5, column 1: unknown configuration key "anime_bytes"; ` +
			`line 6, column 3: unknown configuration key "animebytes"`
		if got != want {
			t.Errorf("SanitizeDecodeError(multi) = %q, want %q", got, want)
		}
	})
}

// TestSanitizeDecodeErrorRealYAMLErrors is the vocabulary-drift gate: it
// sanitizes errors the go-yaml in go.mod actually produces and asserts the
// planted secret never survives while the value-independent structure does.
// If a go-yaml upgrade rewords its errors, this fails the dependency-bump PR
// before any consumer degrades to the withheld fallback.
func TestSanitizeDecodeErrorRealYAMLErrors(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-expanded-secret" //gitleaks:allow (planted fixture; the test asserts it never survives sanitization)

	type shape struct {
		Flag  bool    `yaml:"flag"`
		Name  string  `yaml:"name"`
		Port  int     `yaml:"port"`
		Ratio float64 `yaml:"ratio"`
	}
	tests := map[string]struct {
		doc   string
		known bool
		want  string
	}{
		"wrong type": {
			doc:  "flag: " + secret + "\n",
			want: "decode errors: line 1, column 7: cannot construct !!str <redacted> into bool",
		},
		"wrong type with a custom tag": {
			doc:  "port: !" + secret + " x\n",
			want: "decode errors: line 1, column 7: cannot construct <redacted> into int",
		},
		"duplicate key": {
			doc:  "name: a\nname: b\n",
			want: "decode errors: line 2, column 1: mapping key <redacted> already defined at line 1",
		},
		"unknown key": {
			doc:   "nam: x\n",
			known: true,
			want:  "decode errors: line 1, column 1: unknown configuration key <redacted>",
		},
		"resolver error embeds the whole value": {
			doc:  "ratio: !!float " + secret + "\n",
			want: withheld,
		},
		"syntax error": {
			doc:  "a: b\n\t" + secret + ": c\n",
			want: "line 2, column 1: " + withheld,
		},
		"unknown anchor": {
			doc:  "name: *" + secret + "\n",
			want: "line 1, column 7: " + withheld,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var opts []yaml.Option
			if tt.known {
				opts = append(opts, yaml.WithKnownFields())
			}
			var s shape
			err := yaml.Load([]byte(tt.doc), &s, opts...)
			if err == nil {
				t.Fatal("Load = nil, want an error")
			}
			got := yamlenv.SanitizeDecodeError(err).Error()
			if got != tt.want {
				t.Errorf("SanitizeDecodeError(%v) = %q, want %q", err, got, tt.want)
			}
			if strings.Contains(got, secret) || strings.Contains(got, "hunter2") {
				t.Errorf("SanitizeDecodeError = %q, leaks the value", got)
			}
		})
	}

	t.Run("unknown key echo from a real strict load", func(t *testing.T) {
		t.Parallel()
		l, err := yaml.NewLoader(bytes.NewReader([]byte("nam: x\n")), yaml.WithKnownFields())
		if err != nil {
			t.Fatal(err)
		}
		var s shape
		err = l.Load(&s)
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("Load = %v, want an unknown-field error", err)
		}
		got := yamlenv.SanitizeDecodeError(err, yamlenv.WithUnknownKeyEcho(true)).Error()
		if want := `decode errors: line 1, column 1: unknown configuration key "nam"`; got != want {
			t.Errorf("SanitizeDecodeError = %q, want %q", got, want)
		}
	})
}

// TestSanitizeDecodeErrorDoesNotWrap pins the no-Unwrap contract: the
// sanitized error gives no path back to the original error.
func TestSanitizeDecodeErrorDoesNotWrap(t *testing.T) {
	t.Parallel()
	entry := constructEntry(1, 7, "cannot construct !!str `s3cret` into int")
	for name, err := range map[string]error{
		"LoadErrors": loadErrors(entry),
		"LoadError":  entry,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := yamlenv.SanitizeDecodeError(err)
			if errors.Is(got, err) || errors.Is(got, entry) {
				t.Error("sanitized error wraps the original (errors.Is reaches it)")
			}
			if _, ok := errors.AsType[*yaml.LoadErrors](got); ok {
				t.Error("sanitized error wraps the original (errors.As reaches *LoadErrors)")
			}
			if _, ok := errors.AsType[*yaml.LoadError](got); ok {
				t.Error("sanitized error wraps the original (errors.As reaches *LoadError)")
			}
		})
	}
}
