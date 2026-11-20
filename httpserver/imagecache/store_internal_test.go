package imagecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fetch := func(context.Context, Key) (Image, bool, error) { return Image{}, false, nil }
	c, err := New(Options{Root: root, Fetch: fetch})
	if err != nil {
		t.Fatal(err)
	}
	if c.maxImage != 2<<20 || c.maxBytes != 256<<20 || c.refresh != 7*24*time.Hour {
		t.Errorf("defaults = %d, %d, %v; want 2 MiB, 256 MiB, 7 days", c.maxImage, c.maxBytes, c.refresh)
	}
}

// unreadable answers a listing holding name, a 64-hex name the cache root
// does not contain, so the root's Open fails: it stands in for a file the
// start cannot read.
func unreadable(t *testing.T, name string) []os.DirEntry {
	t.Helper()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, name), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(other)
	if err != nil {
		t.Fatal(err)
	}
	return ents
}

func TestScanSkipsUnreadableFile(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var logs bytes.Buffer
	fetch := func(context.Context, Key) (Image, bool, error) { return Image{}, false, nil }
	c, err := New(Options{Root: root, Fetch: fetch, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("b", 64)

	if found := c.scan(unreadable(t, name)); len(found) != 0 {
		t.Errorf("scan found %d entries, want 0", len(found))
	}
	if got := logs.String(); !strings.Contains(got, "cache file not read") || !strings.Contains(got, name) {
		t.Errorf("log = %q, want a Warn naming %s", got, name)
	}
	if got := logs.String(); strings.Contains(got, "cannot keep") {
		t.Errorf("log = %q, want the unreadable file not counted as one it cannot keep", got)
	}
	if _, ok := c.orphans[name]; !ok {
		t.Error("the unreadable file is not an orphan")
	}
}

var orphanKey = Key{Space: "radarr-1", ID: "1", File: "poster.jpg"}

func pngSized(n int, fill byte) Image {
	data := bytes.Repeat([]byte{fill}, n)
	copy(data, "\x89PNG\r\n\x1a\n")
	return Image{Type: "image/png", Data: data}
}

func pngImage(fill byte) Image { return pngSized(512, fill) }

// writeCacheFile writes the file a Cache keeps for k holding img.
func writeCacheFile(t *testing.T, dir string, k Key, img Image) {
	t.Helper()
	h := header{key: k, typ: img.Type, fetched: time.Now(), n: int64(len(img.Data)), sum: sha256.Sum256(img.Data)}
	b := append(appendHeader(nil, &h), img.Data...)
	if err := os.WriteFile(filepath.Join(dir, fileName(k)), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// blockRemoval puts a non-empty directory in place of name's file, so its
// removal fails with ENOTEMPTY whatever the test's privileges.
func blockRemoval(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// unblockRemoval puts k's cache file holding img back in place of the
// directory blockRemoval left.
func unblockRemoval(t *testing.T, dir string, k Key, img Image) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(dir, fileName(k))); err != nil {
		t.Fatal(err)
	}
	writeCacheFile(t, dir, k, img)
}

// orphanCache answers a Cache over a fresh directory whose start could not
// read orphanKey's file, which now holds the image filled with 'A', and the
// count of its fetches. Its Fetch answers the image filled with 'N'.
func orphanCache(t *testing.T) (c *Cache, dir string, calls *atomic.Int32) {
	t.Helper()
	dir = t.TempDir()
	c, calls = openCache(t, dir)
	c.scan(unreadable(t, fileName(orphanKey)))
	writeCacheFile(t, dir, orphanKey, pngImage('A'))
	return c, dir, calls
}

func openCache(t *testing.T, dir string) (*Cache, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	fetch := func(context.Context, Key) (Image, bool, error) {
		calls.Add(1)
		return pngImage('N'), true, nil
	}
	return newCacheAt(t, dir, Options{Fetch: fetch}), calls
}

// newCacheAt answers a Cache over dir with o, its Root and Logger filled in.
func newCacheAt(t *testing.T, dir string, o Options) *Cache {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	o.Root, o.Logger = root, slog.New(slog.DiscardHandler)
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.WithoutCancel(t.Context())) })
	return c
}

func serveBody(t *testing.T, c *Cache, k Key) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	c.Serve(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/img", http.NoBody), k)
	b, _ := io.ReadAll(rec.Result().Body)
	return b
}

func assertGone(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("file %s: %v, want it removed", name, err)
	}
}

// An orphan is removed by a Forget of its key and by any ForgetSpace, an
// over-long space included, since its space is unknown, so a restart never
// serves it.
func TestForgetRemovesOrphan(t *testing.T) {
	for _, tc := range []struct {
		forget  func(*Cache) error
		name    string
		wantErr bool
	}{
		{name: "Forget", forget: func(c *Cache) error { return c.Forget(orphanKey) }},
		{name: "ForgetSpace", forget: func(c *Cache) error { return c.ForgetSpace("sonarr-1") }},
		{
			name:    "ForgetSpace of an over-long space",
			forget:  func(c *Cache) error { return c.ForgetSpace(strings.Repeat("s", 129)) },
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, dir, _ := orphanCache(t)
			if err := tc.forget(c); (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want an error: %v", err, tc.wantErr)
			}
			assertGone(t, dir, fileName(orphanKey))
			if n := len(c.orphans); n != 0 {
				t.Errorf("orphans = %d after the removal, want 0", n)
			}
			restarted, calls := openCache(t, dir)
			if got := serveBody(t, restarted, orphanKey); !bytes.Equal(got, pngImage('N').Data) {
				t.Error("after the restart: not the refetched bytes")
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("after the restart: fetches = %d, want 1", n)
			}
		})
	}
}

// Serve answers an orphan's key with a fetch, never the orphan's bytes, and
// the fetch's write indexes the name, so a ForgetSpace of another space
// keeps it.
func TestWriteTakesNameOutOfOrphans(t *testing.T) {
	c, dir, calls := orphanCache(t)
	if got := serveBody(t, c, orphanKey); !bytes.Equal(got, pngImage('N').Data) {
		t.Fatal("Serve: not the fetched bytes")
	}
	if n := len(c.orphans); n != 0 {
		t.Errorf("orphans = %d after the write, want 0", n)
	}
	if err := c.ForgetSpace("sonarr-1"); err != nil {
		t.Fatalf("ForgetSpace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, fileName(orphanKey))); err != nil {
		t.Errorf("rewritten file: %v, want it kept", err)
	}
	serveBody(t, c, orphanKey)
	if n := calls.Load(); n != 1 {
		t.Errorf("fetches = %d, want 1 (the rewritten file served)", n)
	}
}

// A removal that fails leaves an orphan: every later ForgetSpace retries it
// and answers the error until a removal succeeds.
func TestFailedRemoveIsRetried(t *testing.T) {
	name := fileName(orphanKey)
	for _, tc := range []struct {
		first func(*Cache) error
		name  string
	}{
		{name: "Forget", first: func(c *Cache) error { return c.Forget(orphanKey) }},
		{name: "ForgetSpace", first: func(c *Cache) error { return c.ForgetSpace(orphanKey.Space) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			c, calls := openCache(t, dir)
			serveBody(t, c, orphanKey)
			blockRemoval(t, dir, name)

			if err := tc.first(c); err == nil {
				t.Fatal("first call = nil, want the removal error")
			}
			if err := c.ForgetSpace("sonarr-1"); err == nil {
				t.Error("retry = nil, want the removal error while the file stays")
			}
			if _, ok := c.orphans[name]; !ok {
				t.Error("the file whose removal failed is not an orphan")
			}
			unblockRemoval(t, dir, orphanKey, pngImage('A'))
			if err := c.ForgetSpace("sonarr-1"); err != nil {
				t.Fatalf("retry once removable: %v", err)
			}
			assertGone(t, dir, name)
			if n := len(c.orphans); n != 0 {
				t.Errorf("orphans = %d after the removal, want 0", n)
			}
			serveBody(t, c, orphanKey)
			if n := calls.Load(); n != 2 {
				t.Errorf("fetches = %d, want 2 (the forgotten image fetched anew)", n)
			}
		})
	}
}

func TestEvictionFailureMakesOrphan(t *testing.T) {
	dir := t.TempDir()
	fetch := func(_ context.Context, k Key) (Image, bool, error) {
		return pngSized(600<<10, k.ID[0]), true, nil
	}
	c := newCacheAt(t, dir, Options{Fetch: fetch, MaxBytes: 1 << 20, MaxImage: 1 << 20})
	older := Key{Space: "radarr-1", ID: "a", File: "poster.jpg"}
	newer := Key{Space: "radarr-1", ID: "b", File: "poster.jpg"}
	serveBody(t, c, older)
	blockRemoval(t, dir, fileName(older))
	serveBody(t, c, newer)

	if _, ok := c.orphans[fileName(older)]; !ok {
		t.Fatal("the evicted file whose removal failed is not an orphan")
	}
	if c.total > c.maxBytes {
		t.Errorf("total = %d, want the orphan outside MaxBytes %d", c.total, c.maxBytes)
	}
	if err := c.ForgetSpace("sonarr-1"); err == nil {
		t.Error("ForgetSpace = nil, want the orphan's removal error")
	}
	unblockRemoval(t, dir, older, pngSized(600<<10, 'a'))
	if err := c.ForgetSpace("sonarr-1"); err != nil {
		t.Fatalf("ForgetSpace once removable: %v", err)
	}
	assertGone(t, dir, fileName(older))
	if _, err := os.Stat(filepath.Join(dir, fileName(newer))); err != nil {
		t.Errorf("newer file: %v, want it kept", err)
	}
}

// A kept file Serve cannot read leaves the index as an orphan, so a
// ForgetSpace removes it even when the refetch failed.
func TestUnreadableKeptFileBecomesOrphan(t *testing.T) {
	dir := t.TempDir()
	var down atomic.Bool
	fetch := func(context.Context, Key) (Image, bool, error) {
		if down.Load() {
			return Image{}, false, errors.New("upstream down")
		}
		return pngImage('N'), true, nil
	}
	c := newCacheAt(t, dir, Options{Fetch: fetch})
	serveBody(t, c, orphanKey)
	down.Store(true)
	if err := os.WriteFile(filepath.Join(dir, fileName(orphanKey)), []byte("not a cache file"), 0o600); err != nil {
		t.Fatal(err)
	}
	serveBody(t, c, orphanKey)

	if err := c.ForgetSpace("sonarr-1"); err != nil {
		t.Fatalf("ForgetSpace: %v", err)
	}
	assertGone(t, dir, fileName(orphanKey))
}
