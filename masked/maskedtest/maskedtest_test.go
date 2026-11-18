package maskedtest_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/masked"
	"github.com/cplieger/go/masked/maskedtest"
	one "github.com/cplieger/go/masked/maskedtest/internal/fixture/one/config"
	two "github.com/cplieger/go/masked/maskedtest/internal/fixture/two/config"
)

// pkg is this package's import path, which names its types in an allow entry
// and a reported line.
const pkg = "github.com/cplieger/go/masked/maskedtest_test."

type upstream struct {
	Name   string
	Token  string
	Key    masked.Secret
	Backup *masked.Secret
	Keys   []masked.Secret
	Since  time.Time
}

func TestUnmaskedFindsPlainField(t *testing.T) {
	t.Parallel()
	// A field this literal does not write is dead to deadset, which keeps a
	// field reflection reads only where the program writes it
	// (github.com/cplieger/deadset-go docs/encoding-reflection.md).
	k := masked.NewSecret("k")
	fixture := upstream{Name: "n", Token: "t", Key: k, Backup: &k, Keys: []masked.Secret{k}, Since: time.Unix(1, 0)}
	allow := maskedtest.Allow{
		Masked: []reflect.Type{reflect.TypeFor[masked.Secret]()},
		Fields: []string{pkg + "upstream.Name"},
		Types:  []reflect.Type{reflect.TypeFor[time.Time]()},
	}
	got := maskedtest.Unmasked(allow, reflect.TypeOf(fixture))
	want := []string{pkg + "upstream.Token: holds a string (string)"}
	if !slices.Equal(got, want) {
		t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
	}
}

// vault stands in for a caller's own masked type.
type vault struct{}

func TestUnmaskedFailsStaleAllow(t *testing.T) {
	t.Parallel()
	allow := maskedtest.Allow{
		Masked: []reflect.Type{reflect.TypeFor[vault](), reflect.TypeFor[masked.Secret]()},
		Fields: []string{pkg + "upstream.Gone"},
		Types:  []reflect.Type{reflect.TypeFor[time.Time]()},
	}
	got := maskedtest.Unmasked(allow, reflect.TypeFor[struct{ n int }]())
	want := []string{
		"allow entry github.com/cplieger/go/masked.Secret: reached by no root",
		"allow entry " + pkg + "upstream.Gone: reached by no root",
		"allow entry " + pkg + "vault: reached by no root",
		"allow entry time.Time: reached by no root",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
	}
}

// TestChecksKeyByImportPath pins that two types sharing a package name, a
// type name and a field name are told apart, so allowing one field leaves
// the other reported.
func TestChecksKeyByImportPath(t *testing.T) {
	t.Parallel()
	// The literals write every field: deadset keeps a field reflection reads
	// only where the program writes it
	// (github.com/cplieger/deadset-go docs/encoding-reflection.md).
	roots := []reflect.Type{reflect.TypeOf(one.Box{Token: "a"}), reflect.TypeOf(two.Box{Token: "b"})}
	const fixture = "github.com/cplieger/go/masked/maskedtest/internal/fixture/"
	t.Run("unmasked", func(t *testing.T) {
		t.Parallel()
		allow := maskedtest.Allow{Fields: []string{fixture + "one/config.Box.Token"}}
		got := maskedtest.Unmasked(allow, roots...)
		want := []string{fixture + "two/config.Box.Token: holds a string (string)"}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("secret_named", func(t *testing.T) {
		t.Parallel()
		allow := maskedtest.NameAllow{Fields: map[string]string{fixture + "one/config.Box.Token": "a reviewed field"}}
		got := maskedtest.SecretNamed(allow, roots...)
		want := []string{fixture + `two/config.Box.Token: json name "token" holds text (string)`}
		if !slices.Equal(got, want) {
			t.Errorf("SecretNamed() =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("unnamed_struct", func(t *testing.T) {
		t.Parallel()
		// Both types are struct { token string } to reflect.Type.String.
		roots := []reflect.Type{reflect.TypeOf(one.Settings), reflect.TypeOf(two.Settings)}
		oneLine := "struct { " + fixture + "one/config.token string }.token"
		twoLine := "struct { " + fixture + "two/config.token string }.token: holds a string (string)"
		got := maskedtest.Unmasked(maskedtest.Allow{}, roots...)
		want := []string{oneLine + ": holds a string (string)", twoLine}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
		}
		got = maskedtest.Unmasked(maskedtest.Allow{Fields: []string{oneLine}}, roots...)
		if want := []string{twoLine}; !slices.Equal(got, want) {
			t.Errorf("Unmasked(allowing one) =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("unnamed_struct_of_named_types", func(t *testing.T) {
		t.Parallel()
		// Both types are struct { T config.Tok } to reflect.Type.String.
		roots := []reflect.Type{reflect.TypeFor[struct{ T one.Tok }](), reflect.TypeFor[struct{ T two.Tok }]()}
		oneLine := "struct { T " + fixture + "one/config.Tok }.T"
		twoLine := "struct { T " + fixture + "two/config.Tok }.T: holds a string (config.Tok)"
		got := maskedtest.Unmasked(maskedtest.Allow{}, roots...)
		want := []string{oneLine + ": holds a string (config.Tok)", twoLine}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
		}
		got = maskedtest.Unmasked(maskedtest.Allow{Fields: []string{oneLine}}, roots...)
		if want := []string{twoLine}; !slices.Equal(got, want) {
			t.Errorf("Unmasked(allowing one) =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("func_chan_interface", func(t *testing.T) {
		t.Parallel()
		// reflect.Type.String writes each pair alike: func(config.Tok) and so on.
		const oneTok, twoTok = fixture + "one/config.Tok", fixture + "two/config.Tok"
		for _, tc := range []struct {
			name, oneLine, twoLine string
			roots                  []reflect.Type
		}{
			{
				name: "func",
				roots: []reflect.Type{reflect.TypeFor[struct {
					F func(one.Tok, ...one.Tok) one.Tok
					S string
				}](), reflect.TypeFor[struct {
					F func(two.Tok, ...two.Tok) two.Tok
					S string
				}]()},
				oneLine: "struct { F func(" + oneTok + ", ..." + oneTok + ") " + oneTok + "; S string }.S: holds a string (string)",
				twoLine: "struct { F func(" + twoTok + ", ..." + twoTok + ") " + twoTok + "; S string }.S: holds a string (string)",
			},
			{
				name: "chan",
				roots: []reflect.Type{reflect.TypeFor[struct {
					C <-chan one.Tok
					S string
				}](), reflect.TypeFor[struct {
					C <-chan two.Tok
					S string
				}]()},
				oneLine: "struct { C <-chan " + oneTok + "; S string }.S: holds a string (string)",
				twoLine: "struct { C <-chan " + twoTok + "; S string }.S: holds a string (string)",
			},
			{
				name:    "interface",
				roots:   []reflect.Type{reflect.TypeFor[struct{ I interface{ M(one.Tok) } }](), reflect.TypeFor[struct{ I interface{ M(two.Tok) } }]()},
				oneLine: "struct { I interface { M(" + oneTok + ") } }.I: holds an interface (interface { M(config.Tok) })",
				twoLine: "struct { I interface { M(" + twoTok + ") } }.I: holds an interface (interface { M(config.Tok) })",
			},
		} {
			got := maskedtest.Unmasked(maskedtest.Allow{}, tc.roots...)
			if want := []string{tc.oneLine, tc.twoLine}; !slices.Equal(got, want) {
				t.Errorf("%s: Unmasked() =\n%q\nwant\n%q", tc.name, got, want)
			}
			allowed, _, _ := strings.Cut(tc.oneLine, ": ")
			got = maskedtest.Unmasked(maskedtest.Allow{Fields: []string{allowed}}, tc.roots...)
			if want := []string{tc.twoLine}; !slices.Equal(got, want) {
				t.Errorf("%s: Unmasked(allowing one) =\n%q\nwant\n%q", tc.name, got, want)
			}
		}
	})
	t.Run("unnamed_root", func(t *testing.T) {
		t.Parallel()
		// Both types are []config.Tok to reflect.Type.String.
		got := maskedtest.Unmasked(maskedtest.Allow{}, reflect.TypeFor[[]one.Tok](), reflect.TypeFor[[]two.Tok]())
		want := []string{
			"[]" + fixture + "one/config.Tok: holds a string (config.Tok)",
			"[]" + fixture + "two/config.Tok: holds a string (config.Tok)",
		}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
		}
	})
}
