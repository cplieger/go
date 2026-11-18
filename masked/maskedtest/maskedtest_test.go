package maskedtest_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/go/masked"
	"github.com/cplieger/go/masked/maskedtest"
	one "github.com/cplieger/go/masked/maskedtest/internal/fixture/one/config"
	two "github.com/cplieger/go/masked/maskedtest/internal/fixture/two/config"
)

// pkg is this package's import path, which names its types in a reported
// line.
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
		Fields: []maskedtest.Field{{In: reflect.TypeFor[upstream](), Name: "Name"}},
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
		Fields: []maskedtest.Field{{In: reflect.TypeFor[upstream](), Name: "Gone"}},
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

// Str, Txt and low are aliases to string: reflect.Type.String writes a struct
// embedding any of them as it writes one embedding string.
type (
	Str = string
	Txt = string
	low = string
)

// identityCase is a set of distinct types whose report lines can read alike,
// and an allow entry for one field of the first.
type identityCase struct {
	allow maskedtest.Field
	name  string
	// want is every root's report, allowed is it with the entry applied, and
	// stale is the entry applied to every root but the first.
	want, allowed, stale []string
	roots                []reflect.Type
}

// TestAllowMatchesByIdentity pins that an allow entry silences one field of
// one type, however alike two types print: allowing a field of the first root
// leaves every other root's fields reported, and the entry is stale when only
// the other roots are walked.
func TestAllowMatchesByIdentity(t *testing.T) {
	t.Parallel()
	const fixture = "github.com/cplieger/go/masked/maskedtest/internal/fixture/"
	const oneTok, twoTok = fixture + "one/config.Tok", fixture + "two/config.Tok"
	const embedded = "struct { string; S string }"
	tokLine := func(owner string) string { return owner + ": holds a string (config.Tok)" }
	strLine := func(owner string) string { return owner + ": holds a string (string)" }
	stale := func(owner string) string { return "allow entry " + owner + ": reached by no root" }
	cases := []identityCase{
		{
			name:    "same_package_name",
			roots:   []reflect.Type{reflect.TypeOf(one.Box{Token: "a"}), reflect.TypeOf(two.Box{Token: "b"})},
			allow:   maskedtest.Field{In: reflect.TypeFor[one.Box](), Name: "Token"},
			want:    []string{strLine(fixture + "one/config.Box.Token"), strLine(fixture + "two/config.Box.Token")},
			allowed: []string{strLine(fixture + "two/config.Box.Token")},
			stale:   []string{stale(fixture + "one/config.Box.Token"), strLine(fixture + "two/config.Box.Token")},
		},
		{
			// Both types are struct { token string } to reflect.Type.String.
			name:  "unnamed_struct",
			roots: []reflect.Type{reflect.TypeOf(one.Settings), reflect.TypeOf(two.Settings)},
			allow: maskedtest.Field{In: reflect.TypeOf(one.Settings), Name: "token"},
			want: []string{
				strLine("struct { " + fixture + "one/config.token string }.token"),
				strLine("struct { " + fixture + "two/config.token string }.token"),
			},
			allowed: []string{strLine("struct { " + fixture + "two/config.token string }.token")},
			stale: []string{
				stale("struct { " + fixture + "one/config.token string }.token"),
				strLine("struct { " + fixture + "two/config.token string }.token"),
			},
		},
		{
			name:    "unnamed_struct_of_named_types",
			roots:   []reflect.Type{reflect.TypeFor[struct{ T one.Tok }](), reflect.TypeFor[struct{ T two.Tok }]()},
			allow:   maskedtest.Field{In: reflect.TypeFor[struct{ T one.Tok }](), Name: "T"},
			want:    []string{tokLine("struct { T " + oneTok + " }.T"), tokLine("struct { T " + twoTok + " }.T")},
			allowed: []string{tokLine("struct { T " + twoTok + " }.T")},
			stale:   []string{stale("struct { T " + oneTok + " }.T"), tokLine("struct { T " + twoTok + " }.T")},
		},
		{
			// The two types print alike, so their lines are alike too.
			name:  "embedded_predeclared",
			roots: []reflect.Type{reflect.TypeOf(one.Embedded), reflect.TypeOf(two.Embedded)},
			allow: maskedtest.Field{In: reflect.TypeOf(one.Embedded), Name: "S"},
			want: []string{
				strLine(embedded + ".S"), strLine(embedded + ".S"),
				strLine(embedded + ".string"), strLine(embedded + ".string"),
			},
			allowed: []string{strLine(embedded + ".S"), strLine(embedded + ".string"), strLine(embedded + ".string")},
			stale:   []string{stale(embedded + ".S"), strLine(embedded + ".S"), strLine(embedded + ".string")},
		},
		{
			name: "embedded_alias",
			roots: []reflect.Type{
				reflect.TypeFor[struct {
					Str
					S string
				}](),
				reflect.TypeFor[struct {
					Txt
					S string
				}](),
				reflect.TypeFor[struct {
					low
					S string
				}](),
			},
			allow: maskedtest.Field{In: reflect.TypeFor[struct {
				Str
				S string
			}](), Name: "S"},
			want: []string{
				strLine(embedded + ".S"), strLine(embedded + ".S"), strLine(embedded + ".S"),
				strLine(embedded + ".Str"), strLine(embedded + ".Txt"), strLine(embedded + ".low"),
			},
			allowed: []string{
				strLine(embedded + ".S"), strLine(embedded + ".S"),
				strLine(embedded + ".Str"), strLine(embedded + ".Txt"), strLine(embedded + ".low"),
			},
			stale: []string{
				stale(embedded + ".S"), strLine(embedded + ".S"), strLine(embedded + ".S"),
				strLine(embedded + ".Txt"), strLine(embedded + ".low"),
			},
		},
		{
			name: "embedded_unexported_alias",
			roots: []reflect.Type{
				reflect.TypeFor[struct {
					low
					S string
				}](),
				reflect.TypeFor[struct {
					Str
					S string
				}](),
			},
			allow: maskedtest.Field{In: reflect.TypeFor[struct {
				low
				S string
			}](), Name: "low"},
			want: []string{
				strLine(embedded + ".S"), strLine(embedded + ".S"),
				strLine(embedded + ".Str"), strLine(embedded + ".low"),
			},
			allowed: []string{strLine(embedded + ".S"), strLine(embedded + ".S"), strLine(embedded + ".Str")},
			stale:   []string{stale(embedded + ".low"), strLine(embedded + ".S"), strLine(embedded + ".Str")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkIdentity(t, &tc)
		})
	}
	t.Run("secret_named", func(t *testing.T) {
		t.Parallel()
		// Both types are struct { string; Token string } to the report.
		strTok := reflect.TypeFor[struct {
			Str
			Token string
		}]()
		txtTok := reflect.TypeFor[struct {
			Txt
			Token string
		}]()
		const line = `struct { string; Token string }.Token: json name "Token" holds text (string)`
		for _, sc := range []struct {
			name  string
			want  []string
			roots []reflect.Type
		}{
			{name: "both_roots", roots: []reflect.Type{reflect.TypeFor[one.Box](), strTok, reflect.TypeFor[two.Box](), txtTok}, want: []string{
				fixture + `two/config.Box.Token: json name "token" holds text (string)`, line,
			}},
			{name: "other_roots", roots: []reflect.Type{reflect.TypeFor[two.Box](), txtTok}, want: []string{
				"allow entry " + fixture + "one/config.Box.Token: names no credential-named text field a root reaches",
				"allow entry struct { string; Token string }.Token: names no credential-named text field a root reaches",
				fixture + `two/config.Box.Token: json name "token" holds text (string)`, line,
			}},
		} {
			allow := maskedtest.NameAllow{Fields: map[maskedtest.Field]string{
				{In: reflect.TypeFor[one.Box](), Name: "Token"}: "a reviewed field",
				{In: strTok, Name: "Token"}:                     "a reviewed field",
			}}
			got := maskedtest.SecretNamed(allow, sc.roots...)
			if !slices.Equal(got, sc.want) {
				t.Errorf("%s: SecretNamed() =\n%q\nwant\n%q", sc.name, got, sc.want)
			}
		}
		if got, want := maskedtest.SecretNamed(maskedtest.NameAllow{}, strTok, txtTok), []string{line, line}; !slices.Equal(got, want) {
			t.Errorf("no_allow: SecretNamed() =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("unnamed_root", func(t *testing.T) {
		t.Parallel()
		// Both types are []config.Tok to reflect.Type.String.
		got := maskedtest.Unmasked(maskedtest.Allow{}, reflect.TypeFor[[]one.Tok](), reflect.TypeFor[[]two.Tok]())
		want := []string{"[]" + tokLine(oneTok), "[]" + tokLine(twoTok)}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
		}
	})
}

// checkIdentity runs tc's roots with no allow entry, with tc.allow, and with
// tc.allow over every root but the first.
func checkIdentity(t *testing.T, tc *identityCase) {
	t.Helper()
	if got := maskedtest.Unmasked(maskedtest.Allow{}, tc.roots...); !slices.Equal(got, tc.want) {
		t.Errorf("Unmasked() =\n%q\nwant\n%q", got, tc.want)
	}
	allow := maskedtest.Allow{Fields: []maskedtest.Field{tc.allow}}
	if got := maskedtest.Unmasked(allow, tc.roots...); !slices.Equal(got, tc.allowed) {
		t.Errorf("Unmasked(allowing %s) =\n%q\nwant\n%q", tc.allow.Name, got, tc.allowed)
	}
	if got := maskedtest.Unmasked(allow, tc.roots[1:]...); !slices.Equal(got, tc.stale) {
		t.Errorf("Unmasked(allowing %s over the other roots) =\n%q\nwant\n%q", tc.allow.Name, got, tc.stale)
	}
}

// TestUnmaskedNamesTypes pins each report line's spelling of a type without
// a name: reflect.Type.String's form with each named type and unexported
// method qualified by its import path.
func TestUnmaskedNamesTypes(t *testing.T) {
	t.Parallel()
	const fixture = "github.com/cplieger/go/masked/maskedtest/internal/fixture/"
	const tok = fixture + "one/config.Tok"
	for _, tc := range []struct {
		name string
		root reflect.Type
		want []string
	}{
		{
			name: "func",
			root: reflect.TypeFor[struct {
				F func(one.Tok, ...one.Tok) one.Tok
				S string
			}](),
			want: []string{"struct { F func(" + tok + ", ..." + tok + ") " + tok + "; S string }.S: holds a string (string)"},
		},
		{
			name: "func_results",
			root: reflect.TypeFor[struct {
				F func() (one.Tok, error)
				S string
			}](),
			want: []string{"struct { F func() (" + tok + ", error); S string }.S: holds a string (string)"},
		},
		{
			name: "recv_chan",
			root: reflect.TypeFor[struct {
				C <-chan one.Tok
				S string
			}](),
			want: []string{"struct { C <-chan " + tok + "; S string }.S: holds a string (string)"},
		},
		{
			name: "send_chan",
			root: reflect.TypeFor[struct {
				C chan<- one.Tok
				S string
			}](),
			want: []string{"struct { C chan<- " + tok + "; S string }.S: holds a string (string)"},
		},
		{
			name: "chan_of_recv_chan",
			root: reflect.TypeFor[struct {
				C chan (<-chan one.Tok)
				S string
			}](),
			want: []string{"struct { C chan (<-chan " + tok + "); S string }.S: holds a string (string)"},
		},
		{
			name: "interface",
			root: reflect.TypeFor[struct{ I interface{ M(one.Tok) } }](),
			want: []string{"struct { I interface { M(" + tok + ") } }.I: holds an interface (interface { M(config.Tok) })"},
		},
		{
			name: "interface_unexported_method",
			root: reflect.TypeOf(one.Iface),
			want: []string{
				"struct { I interface { " + fixture + "one/config.m() }; S string }.I: holds an interface (interface { config.m() })",
				"struct { I interface { " + fixture + "one/config.m() }; S string }.S: holds a string (string)",
			},
		},
	} {
		if got := maskedtest.Unmasked(maskedtest.Allow{}, tc.root); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Unmasked() =\n%q\nwant\n%q", tc.name, got, tc.want)
		}
	}
}
