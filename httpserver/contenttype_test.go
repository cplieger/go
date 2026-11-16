package httpserver_test

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/go/httpserver/v2"
)

// wantContentTypes is the table docs/static-assets.md publishes; .js is RFC 9239's
// text/javascript, never mime.TypeByExtension's application/javascript.
var wantContentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/vnd.microsoft.icon",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".wasm":        "application/wasm",
}

func TestContentTypeTable(t *testing.T) {
	t.Run("ContentTypes is the fixed table", func(t *testing.T) {
		if got := httpserver.ContentTypes(); !maps.Equal(got, wantContentTypes) {
			t.Errorf("ContentTypes() = %v, want %v", got, wantContentTypes)
		}
	})

	t.Run("the returned map is a copy", func(t *testing.T) {
		got := httpserver.ContentTypes()
		got[".js"] = "application/javascript"
		got[".ttf"] = "font/ttf"
		if again := httpserver.ContentTypes(); !maps.Equal(again, wantContentTypes) {
			t.Errorf("after editing a returned map, ContentTypes() = %v, want %v", again, wantContentTypes)
		}
	})

	// Each file compresses, so the gzip request exercises the gzip path and
	// the plain request the identity path.
	body := &fstest.MapFile{Data: []byte(strings.Repeat("compressible body line\n", 200))}
	fsys := fstest.MapFS{"blob.bin": body, "font.ttf": body}
	want := map[string]string{
		"blob.bin": "application/octet-stream",
		"font.ttf": "application/octet-stream",
	}
	for ext, ct := range wantContentTypes {
		fsys["assets/file"+ext] = body
		want["assets/file"+ext] = ct
	}
	h, err := httpserver.StaticHandler(fsys)
	if err != nil {
		t.Fatalf("StaticHandler: %v", err)
	}
	for name, ct := range want {
		t.Run("StaticHandler answers "+path.Base(name), func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+name, nil))
			if got := rec.Header().Get("Content-Type"); rec.Code != http.StatusOK || got != ct {
				t.Errorf("identity GET /%s = %d %q, want 200 %q", name, rec.Code, got, ct)
			}

			gz := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/"+name, nil)
			req.Header.Set("Accept-Encoding", "gzip")
			h.ServeHTTP(gz, req)
			if enc := gz.Header().Get("Content-Encoding"); enc != "gzip" {
				t.Fatalf("gzip GET /%s Content-Encoding = %q, want gzip", name, enc)
			}
			if got := gz.Header().Get("Content-Type"); got != ct {
				t.Errorf("gzip GET /%s Content-Type = %q, want %q", name, got, ct)
			}
		})
	}
}
