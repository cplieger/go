package imagecache_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
	"github.com/cplieger/go/httpserver/v2/imagecache"
)

var keyA = imagecache.Key{Space: "radarr-1", ID: "42", File: "poster-250.jpg"}

// An image is kept only when its declared type is one of the four and equals
// what http.DetectContentType sniffs; never SVG.
func TestSniffedTypeMustMatchDeclared(t *testing.T) {
	dropped := map[string]imagecache.Image{
		"png bytes declared jpeg":        {Type: "image/jpeg", Data: imageOf("image/png", 1024, 1)},
		"svg declared svg":               {Type: "image/svg+xml", Data: []byte(svgDoc)},
		"svg bytes declared png":         {Type: "image/png", Data: []byte(svgDoc)},
		"jpeg bytes declared IMAGE/JPEG": {Type: "IMAGE/JPEG", Data: imageOf("image/jpeg", 1024, 1)},
		"jpeg with a parameter":          {Type: "image/jpeg; q=1", Data: imageOf("image/jpeg", 1024, 1)},
	}
	for name, img := range dropped {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.set(keyA, reply{img: img, found: true})
			c, dir := newCache(t, imagecache.Options{}, f)
			rec := serve(c, get(t.Context()), keyA)
			if rec.Code != http.StatusServiceUnavailable || envelopeCode(rec) != "image_unavailable" {
				t.Errorf("answer = %d %q, want 503 image_unavailable", rec.Code, envelopeCode(rec))
			}
			if got := files(t, dir); len(got) != 0 {
				t.Errorf("root holds %q, want nothing kept", got)
			}
		})
	}
	for typ := range signatures {
		t.Run("kept "+strings.TrimPrefix(typ, "image/"), func(t *testing.T) {
			data := imageOf(typ, 1024, 7)
			f := newFake()
			f.set(keyA, reply{img: imagecache.Image{Type: typ, Data: data}, found: true})
			c, dir := newCache(t, imagecache.Options{}, f)
			rec := serve(c, get(t.Context()), keyA)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != typ {
				t.Errorf("Content-Type = %q, want %q", ct, typ)
			}
			if !bytes.Equal(body(rec), data) {
				t.Error("body differs from the fetched bytes")
			}
			if got := files(t, dir); len(got) != 1 {
				t.Errorf("root holds %q, want one kept file", got)
			}
		})
	}
}

// A failed fetch with nothing kept answers 503 image_unavailable with
// Retry-After: 60 through Options.Errors.
func TestUnavailableAnswersRetryAfter60(t *testing.T) {
	const upstreamText = "dial tcp 192.0.2.9:7878: refused for test-key"
	failing := func() *fakeFetch {
		f := newFake()
		f.set(keyA, reply{err: errors.New(upstreamText)})
		return f
	}
	check := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if ra := rec.Header().Get("Retry-After"); ra != "60" {
			t.Errorf("Retry-After = %q, want 60", ra)
		}
		if raw := rec.Body.String(); strings.Contains(raw, "192.0.2.9") || strings.Contains(raw, "test-key") {
			t.Errorf("body %q carries the fetch error's text", raw)
		}
	}

	t.Run("through Errors", func(t *testing.T) {
		var seen []httpserver.ErrorCode
		var statuses []int
		errs := func(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
			seen = append(seen, code)
			statuses = append(statuses, status)
			httpserver.WriteError(w, r, status, code, msg)
		}
		c, _ := newCache(t, imagecache.Options{Errors: errs}, failing())
		rec := serve(c, get(t.Context()), keyA)
		check(t, rec)
		if len(seen) != 1 || seen[0] != "image_unavailable" || statuses[0] != http.StatusServiceUnavailable {
			t.Errorf("Errors saw %v %v, want one image_unavailable 503", seen, statuses)
		}
	})

	t.Run("nil Errors writes the envelope", func(t *testing.T) {
		c, dir := newCache(t, imagecache.Options{}, failing())
		rec := serve(c, get(t.Context()), keyA)
		check(t, rec)
		if envelopeCode(rec) != "image_unavailable" {
			t.Errorf("code = %q, want image_unavailable", envelopeCode(rec))
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if got := files(t, dir); len(got) != 0 {
			t.Errorf("root holds %q, want nothing kept", got)
		}
	})
}

func TestNewRefusesOutOfRangeOptions(t *testing.T) {
	const kib, mib = 1 << 10, 1 << 20
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fetch := newFake().fetch
	refused := map[string]struct {
		o    imagecache.Options
		want string
	}{
		"MaxImage below 64 KiB": {imagecache.Options{MaxImage: 64*kib - 1}, "MaxImage"},
		"MaxImage over 16 MiB":  {imagecache.Options{MaxImage: 16*mib + 1}, "MaxImage"},
		"MaxBytes below 1 MiB":  {imagecache.Options{MaxBytes: mib - 1}, "MaxBytes"},
		"MaxBytes over 4 GiB":   {imagecache.Options{MaxBytes: 4<<30 + 1}, "MaxBytes"},
		"Refresh below 1 hour":  {imagecache.Options{Refresh: time.Hour - 1}, "Refresh"},
		"Refresh over 30 days":  {imagecache.Options{Refresh: 30*24*time.Hour + 1}, "Refresh"},
		"Fetches below 1":       {imagecache.Options{Fetches: -1}, "Fetches"},
		"Fetches over 16":       {imagecache.Options{Fetches: 17}, "Fetches"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			o := tc.o
			o.Root, o.Fetch = root, fetch
			c, err := imagecache.New(o)
			if err == nil || c != nil {
				t.Fatalf("New = %v, %v, want an error", c, err)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), " to ") {
				t.Errorf("error %q does not name %s and its range", err, tc.want)
			}
		})
	}
	edges := map[string]imagecache.Options{
		"lowest":  {MaxImage: 64 * kib, MaxBytes: mib, Refresh: time.Hour, Fetches: 1},
		"highest": {MaxImage: 16 * mib, MaxBytes: 4 << 30, Refresh: 30 * 24 * time.Hour, Fetches: 16},
	}
	for name, o := range edges {
		t.Run(name+" accepted", func(t *testing.T) {
			o.Root, o.Fetch = root, fetch
			c, err := imagecache.New(o)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_ = c.Shutdown(t.Context())
		})
	}
	t.Run("nil Root", func(t *testing.T) {
		if _, err := imagecache.New(imagecache.Options{Fetch: fetch}); err == nil || !strings.Contains(err.Error(), "Root") {
			t.Errorf("err = %v, want one naming Root", err)
		}
	})
	t.Run("nil Fetch", func(t *testing.T) {
		if _, err := imagecache.New(imagecache.Options{Root: root}); err == nil || !strings.Contains(err.Error(), "Fetch") {
			t.Errorf("err = %v, want one naming Fetch", err)
		}
	})
}

// subflux's maxPosterBytes, 2 << 20, is the default MaxImage.
func TestMaxImageDefault2MiB(t *testing.T) {
	rows := map[string]struct {
		size   int
		status int
		files  int
	}{
		"2 MiB kept":        {2 << 20, http.StatusOK, 1},
		"2 MiB + 1 dropped": {2<<20 + 1, http.StatusServiceUnavailable, 0},
	}
	for name, tc := range rows {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.set(keyA, reply{img: pngOf(tc.size, 5), found: true})
			c, dir := newCache(t, imagecache.Options{}, f)
			if rec := serve(c, get(t.Context()), keyA); rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := len(files(t, dir)); got != tc.files {
				t.Errorf("files = %d, want %d", got, tc.files)
			}
		})
	}
}

func TestOversizeAndEmptyDropped(t *testing.T) {
	const maxImage = 64 << 10
	rows := map[string]imagecache.Image{
		"over MaxImage": pngOf(maxImage+1, 1),
		"empty body":    {Type: "image/png"},
	}
	for name, img := range rows {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.set(keyA, reply{img: img, found: true})
			c, dir := newCache(t, imagecache.Options{MaxImage: maxImage}, f)
			rec := serve(c, get(t.Context()), keyA)
			if rec.Code != http.StatusServiceUnavailable || envelopeCode(rec) != "image_unavailable" {
				t.Errorf("answer = %d %q, want 503 image_unavailable", rec.Code, envelopeCode(rec))
			}
			if got := files(t, dir); len(got) != 0 {
				t.Errorf("root holds %q, want nothing kept", got)
			}
		})
	}
	t.Run("exactly MaxImage kept", func(t *testing.T) {
		f := newFake()
		f.set(keyA, reply{img: pngOf(maxImage, 1), found: true})
		c, dir := newCache(t, imagecache.Options{MaxImage: maxImage}, f)
		if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		if got := len(files(t, dir)); got != 1 {
			t.Errorf("files = %d, want 1", got)
		}
	})
}

func TestNotFoundAnswers404(t *testing.T) {
	rows := map[string]reply{
		"found false":           {},
		"found false with data": {img: pngOf(512, 1)},
	}
	for name, r := range rows {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.set(keyA, r)
			c, dir := newCache(t, imagecache.Options{}, f)
			rec := serve(c, get(t.Context()), keyA)
			if rec.Code != http.StatusNotFound || envelopeCode(rec) != "image_not_found" {
				t.Errorf("answer = %d %q, want 404 image_not_found", rec.Code, envelopeCode(rec))
			}
			if rec.Header().Get("Retry-After") != "" {
				t.Error("a 404 carries Retry-After")
			}
			if got := files(t, dir); len(got) != 0 {
				t.Errorf("root holds %q, want nothing kept", got)
			}
		})
	}
	t.Run("an error wins over found", func(t *testing.T) {
		f := newFake()
		f.set(keyA, reply{img: pngOf(512, 1), found: true, err: errors.New("upstream down")})
		c, _ := newCache(t, imagecache.Options{}, f)
		if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
	})
	t.Run("key part over 128 bytes", func(t *testing.T) {
		f := newFake()
		long := imagecache.Key{Space: "radarr-1", ID: strings.Repeat("9", 129), File: "poster.jpg"}
		f.set(long, reply{img: pngOf(512, 1), found: true})
		c, _ := newCache(t, imagecache.Options{}, f)
		rec := serve(c, get(t.Context()), long)
		if rec.Code != http.StatusNotFound || envelopeCode(rec) != "image_not_found" {
			t.Errorf("answer = %d %q, want 404 image_not_found", rec.Code, envelopeCode(rec))
		}
		if n := f.calls.Load(); n != 0 {
			t.Errorf("fetches = %d, want none", n)
		}
	})
}

// RFC 9110 section 13.1.2 (If-None-Match, weak comparison) and section
// 15.4.5 (a 304 carries ETag and Cache-Control); the ETag is the SHA-256 of
// the bytes.
func TestETagAnd304(t *testing.T) {
	data := imageOf("image/jpeg", 4096, 9)
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	f := newFake()
	f.set(keyA, reply{img: imagecache.Image{Type: "image/jpeg", Data: data}, found: true})
	c, _ := newCache(t, imagecache.Options{}, f)

	t.Run("fresh fetch answers 304", func(t *testing.T) {
		rec := serve(c, get(t.Context(), "If-None-Match", etag), keyA)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("answer = %d with %d body bytes, want an empty 304", rec.Code, rec.Body.Len())
		}
	})
	t.Run("200", func(t *testing.T) {
		rec := serve(c, get(t.Context()), keyA)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("ETag"); got != etag {
			t.Errorf("ETag = %q, want %q", got, etag)
		}
		if got := rec.Header().Get("Cache-Control"); got != "private, max-age=86400" {
			t.Errorf("Cache-Control = %q", got)
		}
		if got := rec.Header().Get("Content-Length"); got != "4096" {
			t.Errorf("Content-Length = %q, want 4096", got)
		}
		if !bytes.Equal(body(rec), data) {
			t.Error("body differs from the fetched bytes")
		}
	})
	matching := map[string]string{
		"exact":         etag,
		"weak":          "W/" + etag,
		"a list member": `"other", ` + etag,
		"star":          "*",
	}
	for name, inm := range matching {
		t.Run(name+" answers 304", func(t *testing.T) {
			rec := serve(c, get(t.Context(), "If-None-Match", inm), keyA)
			if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
				t.Errorf("answer = %d with %d body bytes, want an empty 304", rec.Code, rec.Body.Len())
			}
			if rec.Header().Get("ETag") != etag || rec.Header().Get("Cache-Control") != "private, max-age=86400" {
				t.Errorf("304 headers = %v, want ETag and Cache-Control", rec.Header())
			}
		})
	}
	t.Run("mismatch answers 200", func(t *testing.T) {
		if rec := serve(c, get(t.Context(), "If-None-Match", `"other"`), keyA); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
	t.Run("HEAD", func(t *testing.T) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodHead, "/img", http.NoBody)
		rec := serve(c, r, keyA)
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
			t.Errorf("HEAD = %d, %d body bytes, ETag %q; want 200, none, %q", rec.Code, rec.Body.Len(), rec.Header().Get("ETag"), etag)
		}
	})
	if n := f.calls.Load(); n != 1 {
		t.Errorf("fetches = %d, want 1", n)
	}
}

func TestCodeSpecsMatchSource(t *testing.T) {
	httpservertest.CheckCodeSpecs(t, ".", imagecache.CodeSpecs())
}

func TestCodeSpecsDeclaredValues(t *testing.T) {
	want := map[httpserver.ErrorCode]int{"image_not_found": http.StatusNotFound, "image_unavailable": http.StatusServiceUnavailable}
	specs := imagecache.CodeSpecs()
	if len(specs) != len(want) {
		t.Errorf("CodeSpecs holds %d codes, want %d", len(specs), len(want))
	}
	for code, status := range want {
		s := specs[code]
		if s.Status != status || !s.NoEffect || s.Remediation == "" {
			t.Errorf("%s = %+v, want status %d, NoEffect, a remediation", code, s, status)
		}
	}
	delete(specs, "image_not_found")
	if _, ok := imagecache.CodeSpecs()["image_not_found"]; !ok {
		t.Error("CodeSpecs returned the package's own map")
	}
}

func TestKeysThatConcatenateAlikeStayApart(t *testing.T) {
	k1 := imagecache.Key{Space: "a", ID: "bc"}
	k2 := imagecache.Key{Space: "ab", ID: "c"}
	f := newFake()
	f.set(k1, reply{img: pngOf(512, 1), found: true})
	f.set(k2, reply{img: pngOf(512, 2), found: true})
	c, dir := newCache(t, imagecache.Options{}, f)
	for _, k := range []imagecache.Key{k1, k2, k1, k2} {
		serve(c, get(t.Context()), k)
	}
	if got := len(files(t, dir)); got != 2 {
		t.Errorf("files = %d, want 2", got)
	}
	if !bytes.Equal(body(serve(c, get(t.Context()), k1)), pngOf(512, 1).Data) ||
		!bytes.Equal(body(serve(c, get(t.Context()), k2)), pngOf(512, 2).Data) {
		t.Error("a key was served the other key's bytes")
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("fetches = %d, want 2", n)
	}
}

func TestFetchPanicAnswersUnavailable(t *testing.T) {
	logger, logs := captureLogger()
	f := newFake()
	f.set(keyA, reply{img: pngOf(512, 1), found: true})
	calls := 0
	fetch := func(ctx context.Context, k imagecache.Key) (imagecache.Image, bool, error) {
		calls++
		if calls == 1 {
			panic("bad callback")
		}
		return f.fetch(ctx, k)
	}
	c, _ := newCache(t, imagecache.Options{Fetch: fetch, Logger: logger}, f)
	if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusServiceUnavailable || envelopeCode(rec) != "image_unavailable" {
		t.Errorf("answer = %d %q, want 503 image_unavailable", rec.Code, envelopeCode(rec))
	}
	if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "bad callback") {
		t.Errorf("log = %q, want an Error line naming the panic", out)
	}
	if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusOK {
		t.Errorf("after the panic: status = %d, want 200", rec.Code)
	}
}
