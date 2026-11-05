package httpclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The key folds the credential as a digest, so nothing the cache holds carries
// the Authorization value a request sent.
func TestTheCacheHoldsNoCredentialInClear(t *testing.T) {
	const secret = "test-key-credential"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Link", "<next>")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	tr := NewCacheRoundTripper(nil, CacheConfig{MaxBytes: 1 << 20, Held: []HeldField{{Name: "Link"}}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip = %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	tr.cache.mu.Lock()
	defer tr.cache.mu.Unlock()
	if len(tr.cache.index) != 1 {
		t.Fatalf("the cache holds %d entries, want the one read", len(tr.cache.index))
	}
	for key, el := range tr.cache.index {
		e, _ := el.Value.(*cacheEntry)
		held := key + e.key + e.page.ETag + e.page.LastModified + string(e.page.Body) + fmt.Sprint(e.page.Fields)
		if strings.Contains(held, secret) {
			t.Errorf("the cache holds %q, which carries the credential in clear", held)
		}
	}
}

// An entry is immutable once kept, because a reader holds it past the lock: the
// entry a 304 leaves is a copy.
func TestRefreshingAnEntryLeavesTheKeptOneUnchanged(t *testing.T) {
	held := heldTable{{name: "Link"}}
	kept := &pageEntry{ETag: `"v1"`, Fields: [][]string{{"<old>"}}}
	fresh := kept.refreshed(held, http.Header{"Link": {"<new>"}})
	if !slices.Equal(kept.Fields[0], []string{"<old>"}) || !slices.Equal(fresh.Fields[0], []string{"<new>"}) {
		t.Errorf("after a refresh the kept entry holds %q and the fresh one %q, want <old> and <new>", kept.Fields[0], fresh.Fields[0])
	}
}

// An entry is charged every byte the cache holds for it, spare capacity included,
// so the bound holds the memory the entries own, not only their bodies' lengths.
func TestAnEntryIsChargedEverythingItHolds(t *testing.T) {
	const key = "page-key"
	body := make(json.RawMessage, 2, 4096)
	copy(body, "{}")
	link := newHeldTable([]HeldField{{Name: "Link"}})
	p := &pageEntry{ETag: `"v1"`, Fields: link.hold(http.Header{"Link": {"next"}}), Body: body}
	if got, model := pageCost(key, p), int64(len(key)+cap(body)+len(`"v1"`)+len("next")); got < model {
		t.Errorf("a page whose body holds %d bytes is charged %d, want at least %d", cap(body), got, model)
	}
	if got := pageCost(key, &pageEntry{Fields: [][]string{{"next"}}}) - pageCost(key, &pageEntry{Fields: [][]string{{""}}}); got != int64(len("next")) {
		t.Errorf("a held line of %d bytes adds %d to the charge of an empty one, want exactly its length", len("next"), got)
	}
	slots := &pageEntry{ETag: p.ETag, Fields: heldTable(make([]heldField, 4096)).hold(http.Header{})}
	want := int64(len(slots.Fields)) * int64(reflect.TypeFor[[]string]().Size())
	if got := pageCost(key, slots) - pageCost(key, &pageEntry{ETag: p.ETag}); got < want {
		t.Errorf("%d empty held field slots add %d to the charge, want at least the %d bytes their storage takes", len(slots.Fields), got, want)
	}
	lines := &pageEntry{ETag: p.ETag, Fields: [][]string{make([]string, 4096)}}
	want = int64(len(lines.Fields[0])) * int64(reflect.TypeFor[string]().Size())
	if got := pageCost(key, lines) - pageCost(key, &pageEntry{ETag: p.ETag, Fields: [][]string{nil}}); got < want {
		t.Errorf("a held field of %d empty lines adds %d to the charge, want at least the %d bytes their storage takes", len(lines.Fields[0]), got, want)
	}
}

func TestAZeroBoundHoldsTheDefault(t *testing.T) {
	if got := NewCache(CacheConfig{}).max; got != 32<<20 {
		t.Errorf("a zero MaxBytes bounds the cache at %d bytes, want the documented 32 MiB", got)
	}
}
