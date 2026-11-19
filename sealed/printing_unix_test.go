//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package sealed

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/masked/maskedtest"
)

// TestPrinting checks that no printer shows a key (the Sealer godoc: "no
// printer shows its keys"; masked's docs): maskedtest.Unmasked finds no
// text field in a Sealer or a KeyFile beyond the key ids, the path and the
// lock file's, and maskedtest.Leak finds no form of a key, its HMAC pads
// included (RFC 2104 §2), in fmt's and encoding/json's renderings of a
// Sealer, a KeyFile holding an uninstalled write, and their KeyIDs.
func TestPrinting(t *testing.T) {
	t.Run("fields", func(t *testing.T) {
		allow := maskedtest.Allow{
			Types: []reflect.Type{reflect.TypeFor[filesafe.Lease](), reflect.TypeFor[KeyIDs]()},
			Fields: []maskedtest.Field{
				{In: reflect.TypeFor[ringKey](), Name: "id"},
				{In: reflect.TypeFor[KeyFile](), Name: "path"},
			},
		}
		for _, line := range maskedtest.Unmasked(allow, reflect.TypeFor[*Sealer](), reflect.TypeFor[*KeyFile]()) {
			t.Error(line)
		}
	})
	t.Run("renderings", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := createKeyFile(t, path)
		installed := readKeyLines(t, path)
		notDurable(t)
		if err := f.Rotate(t.Context()); !errors.Is(err, ErrNotDurable) {
			t.Fatalf("Rotate = %v, want ErrNotDurable", err)
		}
		if f.pending == (KeyIDs{}) {
			t.Fatal("the KeyFile holds no uninstalled write")
		}
		keys := append(installed, readKeyLines(t, path)...)
		s := f.Sealer()
		for name, v := range map[string]any{"KeyFile": f, "Sealer": s, "Sealer value": *s, "KeyIDs": s.KeyIDs(), "record": f.pending} {
			var out []string
			for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
				out = append(out, fmt.Sprintf(verb, v))
			}
			j, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("json.Marshal(%s): %v", name, err)
			}
			out = append(out, string(j))
			for _, o := range out {
				for _, k := range keys {
					if maskedtest.Leak(o, string(k.key[:])) {
						t.Errorf("a rendering of the %s shows the %s key: %s", name, k.word, o)
					}
				}
			}
		}
	})
}
