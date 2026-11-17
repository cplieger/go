package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/go/httpserver/v2"
)

const immutable = "public, max-age=31536000, immutable"

func etagTestFS() fstest.MapFS {
	body := func(s string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(strings.Repeat(s, 300))}
	}
	return fstest.MapFS{
		"index.html":       body("<p>shell</p>\n"),
		"assets/app-1.js":  body("console.log(1);\n"),
		"licenses/app.txt": body("MIT licence text\n"),
	}
}

func etagHandler(t *testing.T, tags map[string]string) http.Handler {
	t.Helper()
	h, err := httpserver.StaticHandler(etagTestFS(),
		httpserver.WithStaticETags(tags),
		httpserver.WithStaticCacheControl(func(p string) string {
			if strings.HasPrefix(p, "assets/") {
				return immutable
			}
			return "no-cache"
		}))
	if err != nil {
		t.Fatalf("StaticHandler: %v", err)
	}
	return h
}

// get serves one GET; gzip offers the gzip body, inm sets If-None-Match.
func get(h http.Handler, target string, gzip bool, inm string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if gzip {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStaticHandlerWithStaticETags(t *testing.T) {
	tags := map[string]string{"assets/app-1.js": `"aaaa"`, "index.html": `"bbbb"`}
	h := etagHandler(t, tags)

	t.Run("a tagged file answers the caller's tag", func(t *testing.T) {
		rec := get(h, "/assets/app-1.js", false, "")
		if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"aaaa"` {
			t.Errorf("GET = %d ETag %q, want 200 %q", rec.Code, rec.Header().Get("ETag"), `"aaaa"`)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != immutable {
			t.Errorf("Cache-Control = %q, want %q", cc, immutable)
		}
		if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
			t.Errorf("Vary = %q, want Accept-Encoding", v)
		}
	})

	t.Run("the root answers index.html's tag", func(t *testing.T) {
		if got := get(h, "/", false, "").Header().Get("ETag"); got != `"bbbb"` {
			t.Errorf("GET / ETag = %q, want %q", got, `"bbbb"`)
		}
	})

	t.Run("the gzip body answers the tag plus -gz", func(t *testing.T) {
		rec := get(h, "/assets/app-1.js", true, "")
		if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") != `"aaaa-gz"` {
			t.Errorf("gzip GET = %q ETag %q, want gzip %q",
				rec.Header().Get("Content-Encoding"), rec.Header().Get("ETag"), `"aaaa-gz"`)
		}
	})

	t.Run("each path revalidates against its own tag", func(t *testing.T) {
		if rec := get(h, "/assets/app-1.js", false, `"aaaa"`); rec.Code != http.StatusNotModified {
			t.Errorf("identity If-None-Match of the tag = %d, want 304", rec.Code)
		}
		if rec := get(h, "/assets/app-1.js", true, `"aaaa-gz"`); rec.Code != http.StatusNotModified {
			t.Errorf("gzip If-None-Match of the -gz tag = %d, want 304", rec.Code)
		}
	})

	t.Run("a moved tag answers 200 to the old one on both paths", func(t *testing.T) {
		moved := etagHandler(t, map[string]string{"assets/app-1.js": `"cccc"`})
		rec := get(moved, "/assets/app-1.js", false, `"aaaa"`)
		if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"cccc"` || rec.Body.Len() == 0 {
			t.Errorf("identity with the old tag = %d ETag %q (%d bytes), want 200 %q with the body",
				rec.Code, rec.Header().Get("ETag"), rec.Body.Len(), `"cccc"`)
		}
		rec = get(moved, "/assets/app-1.js", true, `"aaaa-gz"`)
		if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"cccc-gz"` || rec.Body.Len() == 0 {
			t.Errorf("gzip with the old -gz tag = %d ETag %q (%d bytes), want 200 %q with the body",
				rec.Code, rec.Header().Get("ETag"), rec.Body.Len(), `"cccc-gz"`)
		}
	})

	t.Run("a file the map does not name is served unknown", func(t *testing.T) {
		rec := get(h, "/licenses/app.txt", true, "")
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("GET = %d with %d bytes, want 200 with the body", rec.Code, rec.Body.Len())
		}
		for _, name := range []string{"ETag", "Cache-Control", "Content-Encoding"} {
			if got, ok := rec.Header()[name]; ok {
				t.Errorf("%s = %q, want no such header on an untagged file", name, got)
			}
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q, want the table's text/plain", ct)
		}
	})

	t.Run("the map is copied at construction", func(t *testing.T) {
		own := map[string]string{"assets/app-1.js": `"aaaa"`}
		copied := etagHandler(t, own)
		own["assets/app-1.js"] = `"zzzz"`
		own["licenses/app.txt"] = `"yyyy"`
		if got := get(copied, "/assets/app-1.js", false, "").Header().Get("ETag"); got != `"aaaa"` {
			t.Errorf("ETag after editing the caller's map = %q, want %q", got, `"aaaa"`)
		}
		if got := get(copied, "/licenses/app.txt", false, "").Header().Get("ETag"); got != "" {
			t.Errorf("an entry added after construction tagged the file %q, want untagged", got)
		}
	})

	t.Run("a nil map tags no file", func(t *testing.T) {
		if got := get(etagHandler(t, nil), "/", false, "").Header().Get("ETag"); got != "" {
			t.Errorf("ETag = %q, want none: with the option no file is hashed", got)
		}
	})
}

func TestStaticHandlerWithStaticETagsRefusesBadTags(t *testing.T) {
	cases := []struct {
		name string
		key  string
		tag  string
	}{
		{"a path naming no file", "assets/missing.js", `"aaaa"`},
		{"a directory", "assets", `"aaaa"`},
		{"a weak tag", "index.html", `W/"aaaa"`},
		{"an unquoted tag", "index.html", `aaaa`},
		{"a quote inside the tag", "index.html", `"aa"aa"`},
		{"a control byte", "index.html", "\"aa\x01aa\""},
		{"a space", "index.html", `"aa aa"`},
		{"an unclosed tag", "index.html", `"aaaa`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := httpserver.StaticHandler(etagTestFS(), httpserver.WithStaticETags(map[string]string{tc.key: tc.tag}))
			if err == nil {
				t.Fatalf("StaticHandler with %q: %q = nil error, want a refusal", tc.key, tc.tag)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name the path %q", err, tc.key)
			}
		})
	}

	t.Run("an empty opaque tag is a valid entity-tag", func(t *testing.T) {
		if _, err := httpserver.StaticHandler(etagTestFS(), httpserver.WithStaticETags(map[string]string{"index.html": `""`})); err != nil {
			t.Errorf(`StaticHandler with "" = %v, want nil (RFC 9110 section 8.8.3 allows an empty opaque-tag)`, err)
		}
	})
}

func TestStaticHandlerDirectoryIndexCarriesValidators(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":      {Data: []byte("root\n")},
		"docs/index.html": {Data: []byte("docs index\n")},
	}
	h, err := httpserver.StaticHandler(fsys)
	if err != nil {
		t.Fatalf("StaticHandler: %v", err)
	}
	root := get(h, "/", false, "").Header().Get("ETag")
	rec := get(h, "/docs/", false, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "docs index\n" {
		t.Fatalf("GET /docs/ = %d %q, want 200 with the directory's index", rec.Code, rec.Body.String())
	}
	if etag := rec.Header().Get("ETag"); etag == "" || etag == root {
		t.Errorf("GET /docs/ ETag = %q, want the directory index's own tag (root's is %q)", etag, root)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("GET /docs/ Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if rec := get(h, "/docs/", false, rec.Header().Get("ETag")); rec.Code != http.StatusNotModified {
		t.Errorf("GET /docs/ with its tag = %d, want 304", rec.Code)
	}
}
