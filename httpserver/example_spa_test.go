package httpserver_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing/fstest"
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

// exampleBundle is a built tree as a bundler writes it: a shell, one module
// entry and its manifest.
func exampleBundle() fstest.MapFS {
	fsys := fstest.MapFS{
		"index.html":         {Data: []byte(`<!doctype html><script type="module" src="/assets/app-3f2a.js"></script>`)},
		"assets/app-3f2a.js": {Data: []byte(`console.log("app")`)},
	}
	b := httpserver.Bundle{
		Files:   map[string]string{},
		Entries: []httpserver.BundleEntry{{Name: "app", Kind: httpserver.BundleModule, Path: "assets/app-3f2a.js"}},
	}
	for name, f := range fsys {
		sum := sha256.Sum256(f.Data)
		b.Files[name] = hex.EncodeToString(sum[:])
	}
	manifest, _ := json.Marshal(b)
	fsys[httpserver.BundlePath] = &fstest.MapFile{Data: manifest}
	return fsys
}

func ExampleSPA() {
	ui, err := httpserver.SPA(httpserver.SPAConfig{
		Assets:    exampleBundle(),
		Pages:     []httpserver.Page{{Name: "home", Pattern: "/{$}"}, {Name: "item", Pattern: "/items/{id}"}},
		APIPrefix: "/api",
	})
	if err != nil {
		panic(err)
	}
	for _, path := range []string{"/items/7", "/assets/app-3f2a.js", "/assets/gone.js", "/nowhere"} {
		rec := httptest.NewRecorder()
		ui.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		fmt.Printf("%s %d %s %q\n", path, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	// Output:
	// /items/7 200 text/html; charset=utf-8 "no-cache"
	// /assets/app-3f2a.js 200 text/javascript; charset=utf-8 "public, max-age=31536000, immutable"
	// /assets/gone.js 404 application/json "no-store"
	// /nowhere 404 text/html; charset=utf-8 "no-cache"
}

func ExampleOutagePage() {
	page, err := httpserver.OutagePage(httpserver.OutageConfig{
		Title:     "myapp cannot start",
		App:       "myapp",
		APIPrefix: "/api",
		Files:     []httpserver.OutageFile{{Name: "ACCOUNTS_PATH, /config/accounts.db", Class: httpserver.OutageNewer}},
		Notice:    "Sign-in is off until the accounts file opens.",
		Commands: []httpserver.OutageCommand{{
			Run:  "docker exec myapp myapp accounts restore <archive>",
			Note: "Restores the accounts file from an export archive.",
		}},
	})
	if err != nil {
		panic(err)
	}
	rec := httptest.NewRecorder()
	page.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library", nil))
	fmt.Println(rec.Code, rec.Header().Get("Cache-Control"))

	_, err = httpserver.OutagePage(httpserver.OutageConfig{
		Title: "myapp cannot start", App: "myapp", APIPrefix: "/api",
		Files: []httpserver.OutageFile{{Name: "DATA_PATH", Class: "corrupt"}},
	})
	fmt.Println(err)
	// Output:
	// 503 no-store
	// httpserver: outage file "DATA_PATH" has class "corrupt"; want damaged, format, newer, unreadable, key or unwritable
}

func ExampleKeyedRateLimiter_Middleware() {
	limiter, err := httpserver.NewKeyedRateLimiter(httpserver.KeyedRate{Burst: 1, Interval: time.Minute})
	if err != nil {
		panic(err)
	}
	byKey := func(r *http.Request) (string, bool) {
		key := r.Header.Get("X-Api-Key")
		return key, key != ""
	}
	h := limiter.Middleware(byKey, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, key := range []string{"a", "a", "b"} {
		r := httptest.NewRequest(http.MethodGet, "/things", nil)
		r.Header.Set("X-Api-Key", key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		fmt.Printf("%s %d %q\n", key, rec.Code, rec.Header().Get("Retry-After"))
	}
	// Output:
	// a 200 ""
	// a 429 "60"
	// b 200 ""
}
