package httpwire_test

import (
	"testing"

	"github.com/cplieger/go/httpwire"
)

// FuzzTag_oneTextPerTag: a text UnmarshalText accepts is the String of the
// tag it gives, and every tag's String parses back to it.
func FuzzTag_oneTextPerTag(f *testing.F) {
	for _, s := range []string{"0123456789abcdef.7", "0123456789abcdef.07", "0123456789ABCDEF.7", "7", "ffffffffffffffff.18446744073709551616", ""} {
		f.Add(s, []byte{1, 2, 3, 4, 5, 6, 7, 8}, uint64(7))
	}
	f.Fuzz(func(t *testing.T, text string, open []byte, rev uint64) {
		var tag httpwire.Tag
		if tag.UnmarshalText([]byte(text)) == nil && tag.String() != text {
			t.Fatalf("UnmarshalText(%q) accepted a text whose tag reads %q", text, tag.String())
		}
		var want httpwire.Tag
		copy(want.Open[:], open)
		want.Rev = rev
		var got httpwire.Tag
		if err := got.UnmarshalText([]byte(want.String())); err != nil || got != want {
			t.Fatalf("UnmarshalText(%q) = %+v, %v; want %+v", want.String(), got, err, want)
		}
	})
}
