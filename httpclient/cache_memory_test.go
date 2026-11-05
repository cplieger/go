package httpclient_test

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/cplieger/go/httpclient"
)

// These tests measure the heap, so they run serially.

func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// A field the cache does not keep must not stay reachable through a kept one:
// Header.Clone backs every field with one shared array, so a kept Link would hold
// a dropped cookie, or a value a 304 replaced, that the bound never charged.
func TestADroppedHeaderIsNotHeldPastTheBound(t *testing.T) {
	const bound, entries = 32 << 20, 48
	big := strings.Repeat("c", 1<<20)
	for _, test := range []struct {
		name     string
		first    http.Header
		replaced http.Header
	}{
		{name: "a_cookie_on_the_answer", first: http.Header{"Set-Cookie": {"session=" + big}}},
		{name: "a_value_a_304_replaced", first: http.Header{"X-Big": {big}}, replaced: http.Header{"X-Big": {"small"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{}`, `"v1"`)
			url := serveOrigin(t, o)
			tr := httpclient.NewCacheRoundTripper(nil, httpclient.CacheConfig{MaxBytes: bound, Held: []httpclient.HeldField{{Name: "Link"}}})
			header := func(h http.Header) {
				o.set(func(o *origin) {
					o.header = h.Clone()
					o.header.Set("Link", `<https://server.example/user?page=2>; rel="next"`)
				})
			}
			before := heapInUse()
			for i := range entries {
				path := fmt.Sprintf("%s/user/%d", url, i)
				header(test.first)
				cacheGet(t, tr, http.MethodGet, path, nil)
				if test.replaced != nil {
					header(test.replaced)
					cacheGet(t, tr, http.MethodGet, path, nil)
				}
			}
			held := int64(heapInUse()) - int64(before)
			charged := tr.State().Bytes
			if charged <= 0 {
				t.Fatalf("nothing was kept, want %d entries held", entries)
			}
			if limit := charged + 4<<20; held > limit {
				t.Errorf("%d entries charged %d bytes hold %d on the heap, want at most %d", entries, charged, held, limit)
			}
			runtime.KeepAlive(tr)
		})
	}
}

// A wire may answer header names and values that are substrings of a larger
// string, so whatever an entry keeps must own its bytes, or the bound charges a
// few bytes of a string that holds a megabyte.
func TestAKeptEntryOwnsItsBytes(t *testing.T) {
	const bound, entries = 32 << 20, 48
	for _, test := range []struct {
		answer func(h http.Header, big string)
		name   string
	}{
		{name: "a_header_value", answer: func(h http.Header, big string) { h.Set("X-Small", big[:8]) }},
		{name: "a_header_name", answer: func(h http.Header, big string) { h[("X-Small" + big)[:7]] = []string{"small"} }},
		{name: "a_held_field", answer: func(h http.Header, big string) { h.Set("Link", big[:8]) }},
		{name: "the_etag", answer: func(h http.Header, big string) { h.Set("ETag", (`"v1"` + big)[:4]) }},
		{name: "the_last_modified", answer: func(h http.Header, big string) {
			const date = "Mon, 05 Oct 2026 10:00:00 GMT"
			h.Del("ETag")
			h.Set("Last-Modified", (date + big)[:len(date)])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
				h := http.Header{"Etag": {`"v1"`}}
				test.answer(h, strings.Repeat("c", 1<<20))
				return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
			})
			tr := httpclient.NewCacheRoundTripper(wire, httpclient.CacheConfig{MaxBytes: bound, Held: []httpclient.HeldField{{Name: "Link"}}})
			before := heapInUse()
			for i := range entries {
				cacheGet(t, tr, http.MethodGet, fmt.Sprintf("http://server.example/user/%d", i), nil)
			}
			held := int64(heapInUse()) - int64(before)
			charged := tr.State().Bytes
			if charged <= 0 {
				t.Fatalf("nothing was kept, want %d entries held", entries)
			}
			if limit := charged + 4<<20; held > limit {
				t.Errorf("%d entries charged %d bytes hold %d on the heap, want at most %d", entries, charged, held, limit)
			}
			runtime.KeepAlive(tr)
		})
	}
}
