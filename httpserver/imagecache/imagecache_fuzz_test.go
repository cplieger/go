package imagecache_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2/imagecache"
)

// FuzzKeyStaysInsideRoot holds that no key part, however hostile, names a
// path: a key within the 128-byte part bound is kept as one hex-named file
// inside the root, and one past it is answered 404 without a fetch.
func FuzzKeyStaysInsideRoot(f *testing.F) {
	long := strings.Repeat("a", 128)
	for _, part := range []string{"/", "..", "../../x", "a/b", "\x00", "a\x00b", "", long, long + "b", "ポスター/é"} {
		f.Add(part, "42", "poster.jpg")
		f.Add("radarr", part, "poster.jpg")
		f.Add("radarr", "42", part)
	}
	f.Fuzz(func(t *testing.T, space, id, file string) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "cache")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		fake := newFake()
		k := imagecache.Key{Space: space, ID: id, File: file}
		fake.set(k, reply{img: pngOf(256, 3), found: true})
		c, err := imagecache.New(imagecache.Options{Root: root, Fetch: fake.fetch, Logger: discardLogger()})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Shutdown(t.Context()) }()
		first := serve(c, get(t.Context()), k)
		second := serve(c, get(t.Context()), k)

		top, err := os.ReadDir(parent)
		if err != nil {
			t.Fatal(err)
		}
		if len(top) != 1 || top[0].Name() != "cache" {
			t.Fatalf("the root's parent holds %v, want only cache/", top)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !hexName.MatchString(e.Name()) {
				t.Fatalf("root holds %q (%v), want only 64-hex regular files", e.Name(), e.Type())
			}
		}
		inBound := len(space) <= 128 && len(id) <= 128 && len(file) <= 128
		if inBound {
			if first.Code != http.StatusOK || second.Code != http.StatusOK {
				t.Fatalf("answers = %d, %d, want 200 twice", first.Code, second.Code)
			}
			if len(entries) != 1 || fake.calls.Load() != 1 {
				t.Fatalf("files = %d, fetches = %d, want one kept file from one fetch", len(entries), fake.calls.Load())
			}
			return
		}
		if first.Code != http.StatusNotFound || envelopeCode(first) != "image_not_found" {
			t.Fatalf("answer = %d %q, want 404 image_not_found", first.Code, envelopeCode(first))
		}
		if len(entries) != 0 || fake.calls.Load() != 0 {
			t.Fatalf("files = %d, fetches = %d, want none for a part over 128 bytes", len(entries), fake.calls.Load())
		}
	})
}
