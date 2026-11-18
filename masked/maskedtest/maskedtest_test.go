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
}
