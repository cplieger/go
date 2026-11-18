package imagecache_test

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpserver/v2/imagecache"
)

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func fills(m map[byte]bool) []byte { return slices.Sorted(maps.Keys(m)) }

// MaxBytes evicts the oldest fetched first. A read does not move an image; a
// refetch does.
func TestMaxBytesEvictsOldestFetched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const size = 300 << 10
		f := newFake()
		for i := range 6 {
			f.set(keyN(i), reply{img: pngOf(size, byte('A'+i)), found: true})
		}
		c, dir := newCache(t, imagecache.Options{MaxBytes: 1 << 20, MaxImage: 512 << 10, Refresh: time.Hour}, f)
		a, b, cc, d, e, g := keyN(0), keyN(1), keyN(2), keyN(3), keyN(4), keyN(5)
		for _, k := range []imagecache.Key{a, b, cc} {
			serve(c, get(t.Context()), k)
		}
		if got := fills(keptFills(t, dir)); string(got) != "ABC" {
			t.Fatalf("kept %q, want ABC", got)
		}

		serve(c, get(t.Context()), d)
		if got := fills(keptFills(t, dir)); string(got) != "BCD" {
			t.Fatalf("after D: kept %q, want BCD", got)
		}
		serve(c, get(t.Context()), b)
		serve(c, get(t.Context()), e)
		if got := fills(keptFills(t, dir)); string(got) != "CDE" {
			t.Fatalf("after reading B then fetching E: kept %q, want CDE", got)
		}
		if n := f.calls.Load(); n != 5 {
			t.Errorf("fetches = %d, want 5 (reading B fetched nothing)", n)
		}

		time.Sleep(time.Hour + time.Second)
		serve(c, get(t.Context()), cc)
		synctest.Wait()
		serve(c, get(t.Context()), g)
		if got := fills(keptFills(t, dir)); string(got) != "CEF" {
			t.Fatalf("after refetching C then fetching F: kept %q, want CEF", got)
		}

		before := f.calls.Load()
		if rec := serve(c, get(t.Context()), a); rec.Code != http.StatusOK || f.calls.Load() != before+1 {
			t.Errorf("evicted A: %d after %d fetches, want 200 from a new fetch", rec.Code, f.calls.Load()-before)
		}
	})
}

func TestImageOverMaxBytesIsServedNotKept(t *testing.T) {
	img := pngOf(3<<19, 1)
	f := newFake()
	f.set(keyA, reply{img: img, found: true})
	c, dir := newCache(t, imagecache.Options{MaxBytes: 1 << 20}, f)
	if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusOK || !bytes.Equal(body(rec), img.Data) {
		t.Errorf("status = %d, want 200 with the fetched bytes", rec.Code)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("root holds %q, want nothing kept", got)
	}
}

func TestMissingFileIsFetchedAgain(t *testing.T) {
	f := newFake()
	f.set(keyA, reply{img: pngOf(512, 1), found: true})
	c, dir := newCache(t, imagecache.Options{}, f)
	serve(c, get(t.Context()), keyA)
	names := files(t, dir)
	if len(names) != 1 {
		t.Fatalf("root holds %q, want one file", names)
	}
	if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
		t.Fatal(err)
	}
	if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusOK || f.calls.Load() != 2 {
		t.Errorf("answer = %d after %d fetches, want 200 from a second fetch", rec.Code, f.calls.Load())
	}
	if got := len(files(t, dir)); got != 1 {
		t.Errorf("files = %d, want 1", got)
	}
}

// A restart keeps every file and the fetch time recorded in it.
func TestRestartKeepsFilesAndFetchTimes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := openRoot(t, t.TempDir())
		keyB := keyN(1)
		f := newFake()
		f.set(keyA, reply{img: pngOf(512, 1), found: true})
		f.set(keyB, reply{img: pngOf(512, 2), found: true})
		o := imagecache.Options{Root: root, Fetch: f.fetch, Logger: discardLogger(), Refresh: 6 * 24 * time.Hour}
		first, err := imagecache.New(o)
		if err != nil {
			t.Fatal(err)
		}
		serve(first, get(t.Context()), keyA)
		time.Sleep(24 * time.Hour)
		serve(first, get(t.Context()), keyB)
		if err := first.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}

		f2 := newFake()
		f2.set(keyA, reply{img: pngOf(512, 3), found: true})
		f2.set(keyB, reply{img: pngOf(512, 4), found: true})
		o.Fetch = f2.fetch
		second, err := imagecache.New(o)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = second.Shutdown(t.Context()) }()
		if !bytes.Equal(body(serve(second, get(t.Context()), keyA)), pngOf(512, 1).Data) ||
			!bytes.Equal(body(serve(second, get(t.Context()), keyB)), pngOf(512, 2).Data) {
			t.Error("after the restart: not the kept bytes")
		}
		if n := f2.calls.Load(); n != 0 {
			t.Errorf("after the restart: fetches = %d, want 0", n)
		}

		time.Sleep(5*24*time.Hour + time.Second)
		if !bytes.Equal(body(serve(second, get(t.Context()), keyA)), pngOf(512, 1).Data) {
			t.Error("stale A: not the kept bytes")
		}
		serve(second, get(t.Context()), keyB)
		synctest.Wait()
		if n := f2.calls.Load(); n != 1 {
			t.Errorf("fetches = %d, want 1 (A past Refresh by its recorded time, B not)", n)
		}
	})
}

func TestRestartDropsWhatItCannotKeep(t *testing.T) {
	// prime keeps imgs under keyN(i) in a fresh directory and answers it.
	prime := func(t *testing.T, imgs ...imagecache.Image) string {
		t.Helper()
		f := newFake()
		for i, img := range imgs {
			f.set(keyN(i), reply{img: img, found: true})
		}
		c, dir := newCache(t, imagecache.Options{}, f)
		for i := range imgs {
			serve(c, get(t.Context()), keyN(i))
		}
		if err := c.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	restart := func(t *testing.T, dir string, o imagecache.Options) (*imagecache.Cache, *fakeFetch) {
		t.Helper()
		f := newFake()
		o.Root, o.Fetch, o.Logger = openRoot(t, dir), f.fetch, discardLogger()
		c, err := imagecache.New(o)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = c.Shutdown(context.WithoutCancel(t.Context())) })
		return c, f
	}
	write := func(t *testing.T, path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a hex-named file that is not a cache file", func(t *testing.T) {
		dir := prime(t, pngOf(512, 'A'))
		write(t, filepath.Join(dir, strings.Repeat("a", 64)), pngOf(512, 'Z').Data)
		restart(t, dir, imagecache.Options{})
		if got := fills(keptFills(t, dir)); string(got) != "A" {
			t.Errorf("kept %q, want A", got)
		}
	})
	t.Run("a file whose name is not its key's", func(t *testing.T) {
		dir := prime(t, pngOf(512, 'A'))
		names := files(t, dir)
		b, err := os.ReadFile(filepath.Join(dir, names[0]))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, strings.Repeat("b", 64)), b)
		c, f := restart(t, dir, imagecache.Options{})
		if got := files(t, dir); !slices.Equal(got, names) {
			t.Errorf("root holds %q, want %q", got, names)
		}
		serve(c, get(t.Context()), keyN(0))
		if n := f.calls.Load(); n != 0 {
			t.Errorf("fetches = %d, want the kept file served", n)
		}
	})
	t.Run("a file over a lowered MaxImage", func(t *testing.T) {
		dir := prime(t, pngOf(64<<10, 'A'), pngOf(64<<10+1, 'B'))
		restart(t, dir, imagecache.Options{MaxImage: 64 << 10})
		if got := fills(keptFills(t, dir)); string(got) != "A" {
			t.Errorf("kept %q, want A", got)
		}
	})
	t.Run("a lowered MaxBytes evicts the oldest", func(t *testing.T) {
		dir := prime(t, pngOf(300<<10, 'A'), pngOf(300<<10, 'B'), pngOf(300<<10, 'C'), pngOf(300<<10, 'D'))
		restart(t, dir, imagecache.Options{MaxBytes: 1 << 20})
		if got := fills(keptFills(t, dir)); string(got) != "BCD" {
			t.Errorf("kept %q, want BCD", got)
		}
	})
	t.Run("the app's own files and an orphaned temp", func(t *testing.T) {
		dir := prime(t, pngOf(512, 'A'))
		write(t, filepath.Join(dir, "notes.txt"), []byte("mine"))
		if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "sub", strings.Repeat("c", 64)), []byte("mine too"))
		temp := filepath.Join(dir, ".atomicfile-123.tmp")
		write(t, temp, []byte("half"))
		old := time.Now().Add(-2 * time.Minute)
		if err := os.Chtimes(temp, old, old); err != nil {
			t.Fatal(err)
		}
		restart(t, dir, imagecache.Options{})
		for _, kept := range []string{"notes.txt", filepath.Join("sub", strings.Repeat("c", 64))} {
			if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
				t.Errorf("%s: %v, want it left alone", kept, err)
			}
		}
		if _, err := os.Stat(temp); !os.IsNotExist(err) {
			t.Errorf("orphaned temp: %v, want it removed", err)
		}
		if got := fills(keptFills(t, dir)); string(got) != "A" {
			t.Errorf("kept %q, want A", got)
		}
	})
}

func TestForgetAndForgetSpace(t *testing.T) {
	a := imagecache.Key{Space: "radarr-1", ID: "1", File: "poster.jpg"}
	b := imagecache.Key{Space: "radarr-1", ID: "2", File: "poster.jpg"}
	other := imagecache.Key{Space: "sonarr-1", ID: "1", File: "poster.jpg"}
	f := newFake()
	f.set(a, reply{img: pngOf(512, 'A'), found: true})
	f.set(b, reply{img: pngOf(512, 'B'), found: true})
	f.set(other, reply{img: pngOf(512, 'O'), found: true})
	c, dir := newCache(t, imagecache.Options{}, f)
	for _, k := range []imagecache.Key{a, b, other} {
		serve(c, get(t.Context()), k)
	}

	t.Run("Forget", func(t *testing.T) {
		if err := c.Forget(a); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if got := fills(keptFills(t, dir)); string(got) != "BO" {
			t.Errorf("kept %q, want BO", got)
		}
		before := f.calls.Load()
		serve(c, get(t.Context()), a)
		if n := f.calls.Load() - before; n != 1 {
			t.Errorf("Serve after Forget fetched %d times, want 1", n)
		}
	})
	t.Run("ForgetSpace", func(t *testing.T) {
		if err := c.ForgetSpace("radarr-1"); err != nil {
			t.Fatalf("ForgetSpace: %v", err)
		}
		if got := fills(keptFills(t, dir)); string(got) != "O" {
			t.Errorf("kept %q, want only the other space's O", got)
		}
	})
	t.Run("a key never kept", func(t *testing.T) {
		if err := c.Forget(imagecache.Key{Space: "radarr-1", ID: "404"}); err != nil {
			t.Errorf("Forget = %v, want nil", err)
		}
		if err := c.Forget(imagecache.Key{ID: strings.Repeat("x", 129)}); err != nil {
			t.Errorf("Forget of an over-long key = %v, want nil", err)
		}
	})
	t.Run("an over-long space", func(t *testing.T) {
		if err := c.ForgetSpace(strings.Repeat("x", 129)); err == nil {
			t.Error("ForgetSpace = nil, want the length error")
		}
	})
}
