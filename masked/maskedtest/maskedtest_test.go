package maskedtest_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/go/masked"
	"github.com/cplieger/go/masked/maskedtest"
)

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
		Fields: []string{"maskedtest_test.upstream.Name"},
		Types:  []reflect.Type{reflect.TypeFor[time.Time]()},
	}
	got := maskedtest.Unmasked(allow, reflect.TypeOf(fixture))
	want := []string{"maskedtest_test.upstream.Token: holds a string (string)"}
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
		Fields: []string{"maskedtest_test.upstream.Gone"},
		Types:  []reflect.Type{reflect.TypeFor[time.Time]()},
	}
	got := maskedtest.Unmasked(allow, reflect.TypeFor[struct{ n int }]())
	want := []string{
		"allow entry masked.Secret: reached by no root",
		"allow entry maskedtest_test.upstream.Gone: reached by no root",
		"allow entry maskedtest_test.vault: reached by no root",
		"allow entry time.Time: reached by no root",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
	}
}
