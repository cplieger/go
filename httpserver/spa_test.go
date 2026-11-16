package httpserver_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/go/httpserver/v2"
)

// defaultPolicy is the SPA policy docs/spa.md publishes, written out here as
// the oracle rather than read from the package.
const defaultPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

const (
	indexHTML = `<!doctype html><html><head><link rel="stylesheet" href="/assets/app-AAAA.css"><script type="module" src="/assets/app-BBBB.js"></script></head><body data-style="x">style=plain text</body></html>`
	loginHTML = `<!doctype html><html><head><script type="module" src="/assets/app-BBBB.js"></script></head><body>sign in</body></html>`

	moduleAsset  = "assets/app-BBBB.js"
	classicAsset = "assets/worker-CCCC.js"
	styleAsset   = "assets/app-AAAA.css"
	chunkAsset   = "assets/chunks/shared-DDDD.js"
	fontAsset    = "assets/font-EEEE.woff2"
	noticeIndex  = "licenses/index.txt"
	noticeFile   = "licenses/pkg/LICENSE"
)

func spaFiles() map[string]string {
	return map[string]string{
		"index.html":         indexHTML,
		"login.html":         loginHTML,
		moduleAsset:          strings.Repeat("console.log('app');\n", 40),
		classicAsset:         strings.Repeat("self.onconnect = () => {};\n", 40),
		styleAsset:           strings.Repeat("body { color: red; }\n", 40),
		chunkAsset:           strings.Repeat("export const x = 1;\n", 40),
		fontAsset:            "wOF2 font bytes",
		noticeIndex:          "pkg 1.0.0\n",
		noticeFile:           strings.Repeat("MIT License\n", 20),
		"stray.js":           "stray",
		moduleAsset + ".map": "{}",
		".gitignore":         "*\n",
		"assets/.keep":       "",
	}
}

var spaListed = []string{"index.html", "login.html", moduleAsset, classicAsset, styleAsset, chunkAsset, fontAsset, noticeIndex, noticeFile}

var spaEntries = []httpserver.BundleEntry{
	{Name: "app", Kind: httpserver.BundleModule, Path: moduleAsset},
	{Name: "worker", Kind: httpserver.BundleClassic, Path: classicAsset},
	{Name: "app", Kind: httpserver.BundleStyle, Path: styleAsset},
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// bundleTree builds a tree holding files and a bundle.json listing the named
// paths with their real SHA-256 and the given entries.
func bundleTree(t *testing.T, files map[string]string, listed []string, entries []httpserver.BundleEntry) fstest.MapFS {
	t.Helper()
	tree := fstest.MapFS{}
	for name, body := range files {
		tree[name] = &fstest.MapFile{Data: []byte(body)}
	}
	b := httpserver.Bundle{Files: map[string]string{}, Entries: entries}
	for _, name := range listed {
		b.Files[name] = sha256Hex(files[name])
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	tree[httpserver.BundlePath] = &fstest.MapFile{Data: raw}
	return tree
}

func spaConfig(t *testing.T, cspAdd map[string][]string) httpserver.SPAConfig {
	t.Helper()
	return httpserver.SPAConfig{
		Assets:    bundleTree(t, spaFiles(), spaListed, spaEntries),
		Pages:     testPages,
		CSPAdd:    cspAdd,
		APIPrefix: "/api",
		Logger:    discardLogger(),
	}
}

func newSPA(t *testing.T, c httpserver.SPAConfig) http.Handler {
	t.Helper()
	h, err := httpserver.SPA(c)
	if err != nil {
		t.Fatalf("SPA: %v", err)
	}
	return h
}

func spaServe(h http.Handler, method, target string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Add(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// policyOf parses a Content-Security-Policy value into its directives, each
// with its sorted sources.
func policyOf(v string) map[string][]string {
	out := map[string][]string{}
	for d := range strings.SplitSeq(v, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		src := slices.Clone(f[1:])
		slices.Sort(src)
		out[f[0]] = src
	}
	return out
}

func wantPolicy(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	got := rec.Header().Values("Content-Security-Policy")
	if len(got) != 1 {
		t.Fatalf("Content-Security-Policy = %q, want one header", got)
	}
	if !maps.EqualFunc(policyOf(got[0]), policyOf(want), slices.Equal) {
		t.Errorf("Content-Security-Policy = %q, want %q", got[0], want)
	}
}

func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, code httpserver.ErrorCode) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d (body %q)", rec.Code, status, rec.Body.String())
	}
	if env := strictEnvelope(t, rec); env.Code != code {
		t.Errorf("code = %q, want %q", env.Code, code)
	}
	wantPolicy(t, rec, defaultPolicy)
}

func wantShell(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d", rec.Code, status)
	}
	if rec.Body.String() != body {
		t.Errorf("body = %q, want the shell %q", rec.Body.String(), body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	wantPolicy(t, rec, defaultPolicy)
}

func pathName(target string) string { return strings.ReplaceAll(target, "/", "|") }

func TestSPAPageAnswersItsShell(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, target := range []string{"/library", "/library/a%2Fb", "/settings/x/y", "/docs/", "/", "/library?q=1"} {
		t.Run(pathName(target), func(t *testing.T) {
			rec := spaServe(h, http.MethodGet, target)
			wantShell(t, rec, http.StatusOK, indexHTML)
			if etag := rec.Header().Get("ETag"); len(etag) != 34 || etag[0] != '"' || etag[33] != '"' {
				t.Errorf("ETag = %q, want 32 hex characters, quoted", etag)
			}
		})
	}
}

func TestSPASecondShellServesItsPages(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	wantShell(t, spaServe(h, http.MethodGet, "/login"), http.StatusOK, loginHTML)
}

func TestSPAPathEndingIndexHTMLAnswersTheShell(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, target := range []string{"/docs/index.html", "/file/a%2Fb/index.html", "/settings/index.html"} {
		t.Run(pathName(target), func(t *testing.T) {
			wantShell(t, spaServe(h, http.MethodGet, target), http.StatusOK, indexHTML)
		})
	}
}

func TestSPAUnknownPathAnswersShell404(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, target := range []string{"/nope", "/library/", "/healthz", "/metrics", "/stray.js", "/app.js", "/x//y", "/nope/"} {
		t.Run(pathName(target), func(t *testing.T) {
			rec := spaServe(h, http.MethodGet, target)
			wantShell(t, rec, http.StatusNotFound, indexHTML)
			if etag := rec.Header().Get("ETag"); etag != "" {
				t.Errorf("ETag = %q on a 404, want none", etag)
			}
		})
	}
}

func TestSPARedirectIs307(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	cases := map[string]string{
		"/docs?q=1":           "/docs/?q=1",
		"//library?q=1":       "/library?q=1",
		"/library/x/../a?q=1": "/library/a?q=1",
		"/settings":           "/settings/",
	}
	for target, want := range cases {
		t.Run(pathName(target), func(t *testing.T) {
			rec := spaServe(h, http.MethodGet, target)
			if rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("status = %d, want 307", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != want {
				t.Errorf("Location = %q, want %q", loc, want)
			}
			wantPolicy(t, rec, defaultPolicy)
		})
	}
}

func TestSPAHeadAnswersLengthOnly(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	files := spaFiles()
	cases := map[string]string{"/library": indexHTML, "/" + moduleAsset: files[moduleAsset], "/" + noticeFile: files[noticeFile]}
	for target, body := range cases {
		t.Run(pathName(target), func(t *testing.T) {
			rec := spaServe(h, http.MethodHead, target)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("HEAD body = %q, want none", rec.Body.String())
			}
			if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(body)) {
				t.Errorf("Content-Length = %q, want %d", cl, len(body))
			}
		})
	}
}

func TestSPAAssetRevalidates(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	files := spaFiles()
	rec := spaServe(h, http.MethodGet, "/"+moduleAsset)
	if want := `"` + sha256Hex(files[moduleAsset]) + `"`; rec.Header().Get("ETag") != want {
		t.Errorf("ETag = %q, want the manifest hash %q", rec.Header().Get("ETag"), want)
	}
	if rec.Body.String() != files[moduleAsset] {
		t.Error("body is not the file")
	}
	again := spaServe(h, http.MethodGet, "/"+moduleAsset, "If-None-Match", rec.Header().Get("ETag"))
	if again.Code != http.StatusNotModified {
		t.Errorf("revalidation status = %d, want 304", again.Code)
	}
	wantPolicy(t, again, defaultPolicy)
}

func TestSPAGzipVariant(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	files := spaFiles()
	for _, name := range []string{moduleAsset, classicAsset, noticeFile} {
		t.Run(pathName(name), func(t *testing.T) {
			plain := spaServe(h, http.MethodGet, "/"+name)
			rec := spaServe(h, http.MethodGet, "/"+name, "Accept-Encoding", "gzip")
			if rec.Header().Get("Content-Encoding") != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
			}
			if !bytes.Equal(gunzip(t, rec.Body.Bytes()), []byte(files[name])) {
				t.Error("gzip body does not decode to the file")
			}
			if rec.Header().Get("Content-Type") != plain.Header().Get("Content-Type") {
				t.Errorf("gzip Content-Type = %q, identity %q", rec.Header().Get("Content-Type"), plain.Header().Get("Content-Type"))
			}
			if !varies(rec.Header()) || !varies(plain.Header()) {
				t.Error("Vary: Accept-Encoding missing")
			}
			if want := strings.TrimSuffix(plain.Header().Get("ETag"), `"`) + `-gz"`; rec.Header().Get("ETag") != want {
				t.Errorf("gzip ETag = %q, want %q", rec.Header().Get("ETag"), want)
			}
			wantPolicy(t, rec, defaultPolicy)
		})
	}
}

func TestClassicEntryETagMovesWithPolicy(t *testing.T) {
	const blobPolicy = defaultPolicy + "; media-src 'self' blob:"
	first := newSPA(t, spaConfig(t, nil))
	second := newSPA(t, spaConfig(t, map[string][]string{"media-src": {"blob:"}}))

	for _, enc := range []string{"", "gzip"} {
		t.Run("encoding "+enc, func(t *testing.T) {
			for _, target := range []string{"/" + classicAsset, "/library"} {
				t.Run(pathName(target), func(t *testing.T) {
					a := spaServe(first, http.MethodGet, target, "Accept-Encoding", enc)
					b := spaServe(second, http.MethodGet, target, "Accept-Encoding", enc)
					for _, rec := range []*httptest.ResponseRecorder{a, b} {
						if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
							t.Errorf("Cache-Control = %q, want no-cache", cc)
						}
					}
					if enc == "gzip" && (a.Header().Get("Content-Encoding") != "gzip" || b.Header().Get("Content-Encoding") != "gzip") {
						t.Fatal("no gzip body, so the -gz tag is untested")
					}
					ta, tb := a.Header().Get("ETag"), b.Header().Get("ETag")
					if ta == "" || ta == tb {
						t.Fatalf("ETags %q and %q, want two different tags", ta, tb)
					}
					rec := spaServe(second, http.MethodGet, target, "Accept-Encoding", enc, "If-None-Match", ta)
					if rec.Code != http.StatusOK {
						t.Errorf("If-None-Match of the old tag: status %d, want 200", rec.Code)
					}
					wantPolicy(t, rec, blobPolicy)
					if same := spaServe(second, http.MethodGet, target, "Accept-Encoding", enc, "If-None-Match", tb); same.Code != http.StatusNotModified {
						t.Errorf("If-None-Match of the current tag: status %d, want 304", same.Code)
					}
				})
			}
			t.Run("a module asset stays immutable", func(t *testing.T) {
				a := spaServe(first, http.MethodGet, "/"+moduleAsset, "Accept-Encoding", enc)
				b := spaServe(second, http.MethodGet, "/"+moduleAsset, "Accept-Encoding", enc)
				for _, rec := range []*httptest.ResponseRecorder{a, b} {
					if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
						t.Errorf("Cache-Control = %q, want immutable", cc)
					}
				}
				if a.Header().Get("ETag") == "" || a.Header().Get("ETag") != b.Header().Get("ETag") {
					t.Errorf("ETags %q and %q, want one unmoved tag", a.Header().Get("ETag"), b.Header().Get("ETag"))
				}
			})
		})
	}
}

func TestSPAMissingAssetIsNotFoundEnvelope(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, target := range []string{"/assets/missing-1.js", "/assets/", "/assets/sub/", "/assets", "/licenses/nope.txt", "/licenses/", "/" + moduleAsset + ".map", "/" + moduleAsset + "/x", "/assets/app.js"} {
		t.Run(pathName(target), func(t *testing.T) {
			wantRefusal(t, spaServe(h, http.MethodGet, target), http.StatusNotFound, "not_found")
		})
	}
}

func TestSPAServesOnlyListedFiles(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, target := range []string{"/index.html", "/login.html", "/bundle.json"} {
		t.Run(pathName(target), func(t *testing.T) {
			wantRefusal(t, spaServe(h, http.MethodGet, target), http.StatusNotFound, "not_found")
		})
	}
	t.Run("an unlisted root file answers the 404 shell", func(t *testing.T) {
		wantShell(t, spaServe(h, http.MethodGet, "/stray.js"), http.StatusNotFound, indexHTML)
	})
	t.Run("a listed file outside the content-type table", func(t *testing.T) {
		files := spaFiles()
		files["assets/data-1.bin"] = "binary"
		c := spaConfig(t, nil)
		c.Assets = bundleTree(t, files, append(slices.Clone(spaListed), "assets/data-1.bin"), spaEntries)
		wantRefusal(t, spaServe(newSPA(t, c), http.MethodGet, "/assets/data-1.bin"), http.StatusNotFound, "not_found")
	})
}

func TestSPADotSegmentIsNotFound(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, target := range []string{"/.gitignore", "/.env", "/assets/.keep", "/.well-known/x", "/a/.b/c", "/%2egit"} {
		t.Run(pathName(target), func(t *testing.T) {
			wantRefusal(t, spaServe(h, http.MethodGet, target), http.StatusNotFound, "not_found")
		})
	}
	t.Run("a dot path cleaning to a page redirects", func(t *testing.T) {
		if rec := spaServe(h, http.MethodGet, "/x/../library"); rec.Code != http.StatusTemporaryRedirect {
			t.Errorf("status = %d, want 307", rec.Code)
		}
	})
}

func TestSPAPageCaptureOfDotSegmentAnswersTheShell(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	wantShell(t, spaServe(h, http.MethodGet, "/library/.env"), http.StatusOK, indexHTML)
	for _, target := range []string{"/assets/.env", "/.env"} {
		t.Run(pathName(target), func(t *testing.T) {
			wantRefusal(t, spaServe(h, http.MethodGet, target), http.StatusNotFound, "not_found")
		})
	}
}

func TestSPATreeAnswersBeforeWildcardPages(t *testing.T) {
	files := spaFiles()
	tables := map[string][]httpserver.Page{
		"root subtree":  {{Name: "any", Pattern: "/"}},
		"two wildcards": {{Name: "any", Pattern: "/{a}/{b}"}},
	}
	for name, pages := range tables {
		t.Run(name, func(t *testing.T) {
			c := spaConfig(t, nil)
			c.Pages = pages
			h := newSPA(t, c)
			t.Run("listed asset", func(t *testing.T) {
				rec := spaServe(h, http.MethodGet, "/"+moduleAsset)
				if rec.Code != http.StatusOK || rec.Body.String() != files[moduleAsset] {
					t.Errorf("status %d body %q, want 200 and the asset", rec.Code, rec.Body.String())
				}
				if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
					t.Errorf("Cache-Control = %q, want immutable", cc)
				}
				wantPolicy(t, rec, defaultPolicy)
			})
			t.Run("notice", func(t *testing.T) {
				rec := spaServe(h, http.MethodGet, "/"+noticeIndex)
				if rec.Code != http.StatusOK || rec.Body.String() != files[noticeIndex] {
					t.Errorf("status %d body %q, want 200 and the notice", rec.Code, rec.Body.String())
				}
				if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
					t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
				}
			})
			t.Run("favicon", func(t *testing.T) {
				if rec := spaServe(h, http.MethodGet, "/favicon.ico"); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
					t.Errorf("status %d body %q, want 204 and none", rec.Code, rec.Body.String())
				}
			})
			for _, target := range []string{"/assets/missing-1.js", "/licenses/nope.txt", "/bundle.json", "/index.html", "/login.html"} {
				t.Run(pathName(target), func(t *testing.T) {
					wantRefusal(t, spaServe(h, http.MethodGet, target), http.StatusNotFound, "not_found")
				})
			}
		})
	}
}

func TestSPALicenceNoticeIsPlainTextNoCache(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	files := spaFiles()
	for _, name := range []string{noticeIndex, noticeFile} {
		t.Run(pathName(name), func(t *testing.T) {
			rec := spaServe(h, http.MethodGet, "/"+name)
			if rec.Code != http.StatusOK || rec.Body.String() != files[name] {
				t.Errorf("status %d body %q, want 200 and the notice", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", cc)
			}
			if rec.Header().Get("ETag") == "" {
				t.Error("no ETag")
			}
			wantPolicy(t, rec, defaultPolicy)
		})
	}
}

func TestSPAListedNestedFileServed(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	files := spaFiles()
	cases := map[string]string{chunkAsset: "text/javascript; charset=utf-8", fontAsset: "font/woff2", styleAsset: "text/css; charset=utf-8"}
	for name, ct := range cases {
		t.Run(pathName(name), func(t *testing.T) {
			rec := spaServe(h, http.MethodGet, "/"+name)
			if rec.Code != http.StatusOK || rec.Body.String() != files[name] {
				t.Errorf("status %d body %q, want 200 and the file", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != ct {
				t.Errorf("Content-Type = %q, want %q", got, ct)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
				t.Errorf("Cache-Control = %q, want immutable", cc)
			}
		})
	}
}

func TestSPAFaviconIs204(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			rec := spaServe(h, method, "/favicon.ico")
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Errorf("status %d body %q, want 204 and none", rec.Code, rec.Body.String())
			}
			wantPolicy(t, rec, defaultPolicy)
		})
	}
	t.Run("POST", func(t *testing.T) {
		rec := spaServe(h, http.MethodPost, "/favicon.ico")
		wantRefusal(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
	})
}

func TestSPAOtherMethodIs405(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		for _, target := range []string{"/library", "/" + moduleAsset, "/nope"} {
			t.Run(method+pathName(target), func(t *testing.T) {
				rec := spaServe(h, method, target, "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example")
				wantRefusal(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
				if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
					t.Errorf("Allow = %q, want GET, HEAD", allow)
				}
			})
		}
	}
}

func TestSPALeavesAPIPrefix(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, target := range []string{"/api", "/api/", "/api/x", "/api/library", "/ap%69/x", "/api%2Fx"} {
			t.Run(method+pathName(target), func(t *testing.T) {
				wantRefusal(t, spaServe(h, method, target), http.StatusNotFound, "not_found")
			})
		}
	}
}

func TestSPACSPOnEveryAnswer(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	etag := spaServe(h, http.MethodGet, "/"+classicAsset).Header().Get("ETag")
	cases := []struct{ method, target, inm string }{
		{http.MethodGet, "/library", ""},
		{http.MethodGet, "/nope", ""},
		{http.MethodGet, "/" + moduleAsset, ""},
		{http.MethodGet, "/" + classicAsset, ""},
		{http.MethodGet, "/" + classicAsset, etag},
		{http.MethodGet, "/" + styleAsset, ""},
		{http.MethodGet, "/" + fontAsset, ""},
		{http.MethodGet, "/" + noticeFile, ""},
		{http.MethodGet, "/assets/missing.js", ""},
		{http.MethodGet, "/bundle.json", ""},
		{http.MethodGet, "/favicon.ico", ""},
		{http.MethodGet, "/docs", ""},
		{http.MethodGet, "/api/x", ""},
		{http.MethodPost, "/library", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+pathName(tc.target)+tc.inm, func(t *testing.T) {
			wantPolicy(t, spaServe(h, tc.method, tc.target, "If-None-Match", tc.inm), defaultPolicy)
		})
	}
}

func TestSPACSPAddAdmitsBlobAndData(t *testing.T) {
	add := map[string][]string{"img-src": {"blob:"}, "media-src": {"blob:"}, "font-src": {"data:", "blob:"}}
	h := newSPA(t, spaConfig(t, add))
	want := strings.Replace(defaultPolicy, "img-src 'self' data:", "img-src 'self' data: blob:", 1)
	want = strings.Replace(want, "font-src 'self'", "font-src 'self' data: blob:", 1) + "; media-src 'self' blob:"
	for _, target := range []string{"/library", "/" + classicAsset, "/assets/missing.js"} {
		t.Run(pathName(target), func(t *testing.T) {
			rec := spaServe(h, http.MethodGet, target)
			got := rec.Header().Get("Content-Security-Policy")
			if !maps.EqualFunc(policyOf(got), policyOf(want), slices.Equal) {
				t.Errorf("Content-Security-Policy = %q, want %q", got, want)
			}
		})
	}
	refused := map[string]map[string][]string{
		"script-src":      {"script-src": {"blob:"}},
		"worker-src":      {"worker-src": {"blob:"}},
		"connect-src":     {"connect-src": {"data:"}},
		"a host":          {"img-src": {"https://cdn.example"}},
		"a scheme":        {"img-src": {"https:"}},
		"unsafe-inline":   {"img-src": {"'unsafe-inline'"}},
		"a nonce":         {"img-src": {"'nonce-abc'"}},
		"a hash":          {"img-src": {"'sha256-abc'"}},
		"self":            {"media-src": {"'self'"}},
		"default dup":     {"img-src": {"data:"}},
		"repeated":        {"media-src": {"blob:", "blob:"}},
		"empty list":      {"media-src": {}},
		"empty source":    {"media-src": {""}},
		"spaced source":   {"media-src": {"blob: data:"}},
		"unknown":         {"frame-src": {"blob:"}},
		"uppercase key":   {"IMG-SRC": {"blob:"}},
		"semicolon value": {"img-src": {"blob:;script-src"}},
	}
	for name, add := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			if _, err := httpserver.SPA(spaConfig(t, add)); err == nil {
				t.Errorf("SPA accepted CSPAdd %v", add)
			}
		})
	}
}

func TestSPAUnbuiltAnswersUIUnbuilt(t *testing.T) {
	trees := map[string]fs.FS{
		"nil assets":  nil,
		"no manifest": fstest.MapFS{"index.html": {Data: []byte(indexHTML)}, ".gitignore": {Data: []byte("*")}},
	}
	for name, tree := range trees {
		t.Run(name, func(t *testing.T) {
			c := spaConfig(t, nil)
			c.Assets = tree
			h := newSPA(t, c)
			for _, target := range []string{"/library", "/login", "/", "/library/x"} {
				wantRefusal(t, spaServe(h, http.MethodGet, target), http.StatusServiceUnavailable, "ui_unbuilt")
			}
			for _, target := range []string{"/nope", "/" + moduleAsset, "/index.html", "/favicon.ico", "/api/x"} {
				wantRefusal(t, spaServe(h, http.MethodGet, target), http.StatusNotFound, "not_found")
			}
			wantRefusal(t, spaServe(h, http.MethodPost, "/library"), http.StatusMethodNotAllowed, "method_not_allowed")
		})
	}
}

func TestSPAUnbuiltWarnsOnce(t *testing.T) {
	t.Run("unbuilt", func(t *testing.T) {
		capture := &captureHandler{}
		c := spaConfig(t, nil)
		c.Assets, c.Logger = nil, slog.New(capture)
		h := newSPA(t, c)
		spaServe(h, http.MethodGet, "/library")
		spaServe(h, http.MethodGet, "/nope")
		recs := capture.snapshot()
		if len(recs) != 1 || recs[0].Level != slog.LevelWarn {
			t.Fatalf("records = %v, want one WARN", recs)
		}
		text := recs[0].Message
		for _, v := range attrsOf(recs[0]) {
			if s, ok := v.(string); ok {
				text += " " + s
			}
		}
		if !strings.Contains(text, "go generate ./...") {
			t.Errorf("WARN %q does not name go generate ./...", text)
		}
	})
	t.Run("built", func(t *testing.T) {
		capture := &captureHandler{}
		c := spaConfig(t, nil)
		c.Logger = slog.New(capture)
		newSPA(t, c)
		if recs := capture.snapshot(); len(recs) != 0 {
			t.Errorf("records = %v, want none", recs)
		}
	})
}

func TestSPARefusesBrokenTree(t *testing.T) {
	withManifest := func(raw string) fs.FS {
		tree := bundleTree(t, spaFiles(), spaListed, spaEntries)
		tree[httpserver.BundlePath] = &fstest.MapFile{Data: []byte(raw)}
		return tree
	}
	without := func(name string) fs.FS {
		tree := bundleTree(t, spaFiles(), spaListed, spaEntries)
		delete(tree, name)
		return tree
	}
	listing := func(extra, body string) fs.FS {
		files := spaFiles()
		files[extra] = body
		return bundleTree(t, files, append(slices.Clone(spaListed), extra), spaEntries)
	}
	entries := func(e ...httpserver.BundleEntry) fs.FS {
		return bundleTree(t, spaFiles(), spaListed, e)
	}
	badHash := bundleTree(t, spaFiles(), spaListed, spaEntries)
	badHash[httpserver.BundlePath] = &fstest.MapFile{Data: bytes.Replace(badHash[httpserver.BundlePath].Data, []byte(sha256Hex(spaFiles()[moduleAsset])), []byte("abc"), 1)}
	cases := map[string]fs.FS{
		"manifest not JSON":        withManifest("{"),
		"manifest unknown field":   withManifest(`{"files":{},"entries":[],"extra":1}`),
		"manifest trailing data":   withManifest(`{"files":{},"entries":[]} {}`),
		"listed file missing":      without(moduleAsset),
		"login shell missing":      without("login.html"),
		"default shell missing":    without("index.html"),
		"hash not hex":             badHash,
		"listed dot-file":          listing("assets/.x.js", "x"),
		"listed nested index":      listing("assets/sub/index.html", "<p>"),
		"listed manifest":          listing(httpserver.BundlePath, "{}"),
		"entry path not listed":    entries(httpserver.BundleEntry{Name: "w", Kind: httpserver.BundleClassic, Path: "assets/none.js"}),
		"entry outside assets":     entries(httpserver.BundleEntry{Name: "w", Kind: httpserver.BundleClassic, Path: noticeIndex}),
		"entry kind unknown":       entries(httpserver.BundleEntry{Name: "w", Kind: "worker", Path: classicAsset}),
		"entry name empty":         entries(httpserver.BundleEntry{Kind: httpserver.BundleClassic, Path: classicAsset}),
		"entry name kind repeated": entries(spaEntries[1], httpserver.BundleEntry{Name: "worker", Kind: httpserver.BundleClassic, Path: moduleAsset}),
	}
	for name, tree := range cases {
		t.Run(name, func(t *testing.T) {
			c := spaConfig(t, nil)
			c.Assets = tree
			if _, err := httpserver.SPA(c); err == nil {
				t.Error("SPA accepted the tree")
			}
		})
	}
	t.Run("an invalid page", func(t *testing.T) {
		c := spaConfig(t, nil)
		c.Pages = []httpserver.Page{{Name: "a", Pattern: "/a/{"}}
		if _, err := httpserver.SPA(c); err == nil {
			t.Error("SPA accepted an invalid page")
		}
	})
	t.Run("conflicting pages", func(t *testing.T) {
		c := spaConfig(t, nil)
		c.Pages = []httpserver.Page{{Name: "a", Pattern: "/a/{x}"}, {Name: "b", Pattern: "/{y}/b"}}
		if _, err := httpserver.SPA(c); err == nil {
			t.Error("SPA accepted conflicting pages")
		}
	})
}

func TestSPARefusesInlineScriptOrStyleInShell(t *testing.T) {
	bad := map[string]string{
		"inline script":         `<html><head><script>alert(1)</script></head></html>`,
		"empty inline script":   `<html><head><script></script></head></html>`,
		"inline style":          `<html><head><style>a{}</style></head></html>`,
		"style attribute":       `<html><body><p style="color:red">x</p></body></html>`,
		"style attribute upper": `<html><body><P STYLE='x'>x</P></body></html>`,
		"style attribute bare":  `<html><body><p class=a style=color:red>x</p></body></html>`,
	}
	for name, html := range bad {
		for _, shell := range []string{"index.html", "login.html"} {
			t.Run(name+" in "+shell, func(t *testing.T) {
				files := spaFiles()
				files[shell] = html
				c := spaConfig(t, nil)
				c.Assets = bundleTree(t, files, spaListed, spaEntries)
				if _, err := httpserver.SPA(c); err == nil {
					t.Error("SPA accepted the shell")
				}
			})
		}
	}
	t.Run("external script, data-style and text pass", func(t *testing.T) {
		newSPA(t, spaConfig(t, nil))
	})
}

func TestCheckShell(t *testing.T) {
	refused := map[string]string{
		"script element":    `<script>alert(1)</script>`,
		"empty script":      `<script></script>`,
		"style element":     `<style>a{}</style>`,
		"style attribute":   `<p style="color:red">x</p>`,
		"upper case STYLE":  `<P STYLE='x'>x</P>`,
		"unquoted style":    `<p class=a style=color:red>x</p>`,
		"style after quote": `<p title="a>b" style=x>y</p>`,
	}
	for name, html := range refused {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckShell([]byte(html)); err == nil {
				t.Errorf("CheckShell accepted %q", html)
			}
		})
	}
	passed := map[string]string{
		"external script":  `<script type="module" src="/assets/app.js"></script>`,
		"data-style":       `<body data-style="x">style=plain text</body>`,
		"style in a value": `<p title="style=x">y</p>`,
	}
	for name, html := range passed {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckShell([]byte(html)); err != nil {
				t.Errorf("CheckShell refused %q: %v", html, err)
			}
		})
	}
}

func TestSPAErrorsResponderAnswersRefusals(t *testing.T) {
	var codes []httpserver.ErrorCode
	errs := func(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: the policy is not set before the responder runs", code)
		}
		codes = append(codes, code)
		w.WriteHeader(status)
	}
	c := spaConfig(t, nil)
	c.Errors = errs
	h := newSPA(t, c)
	spaServe(h, http.MethodGet, "/assets/missing.js")
	spaServe(h, http.MethodPost, "/library")
	c.Assets = nil
	spaServe(newSPA(t, c), http.MethodGet, "/library")
	if want := []httpserver.ErrorCode{"not_found", "method_not_allowed", "ui_unbuilt"}; !slices.Equal(codes, want) {
		t.Errorf("responder codes = %v, want %v", codes, want)
	}
}

func TestNotFoundAnswersEnvelope(t *testing.T) {
	h := httpserver.NotFound(nil)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		for _, target := range []string{"/", "/x", "/assets/app.js"} {
			t.Run(method+pathName(target), func(t *testing.T) {
				rec := spaServe(h, method, target)
				if rec.Code != http.StatusNotFound {
					t.Errorf("status = %d, want 404", rec.Code)
				}
				if method != http.MethodHead {
					if env := strictEnvelope(t, rec); env.Code != "not_found" {
						t.Errorf("code = %q, want not_found", env.Code)
					}
				}
			})
		}
	}
	t.Run("through the app's responder", func(t *testing.T) {
		var got httpserver.ErrorCode
		h := httpserver.NotFound(func(w http.ResponseWriter, _ *http.Request, status int, code httpserver.ErrorCode, _ string) {
			got = code
			w.WriteHeader(status)
		})
		if rec := spaServe(h, http.MethodGet, "/x"); rec.Code != http.StatusNotFound || got != "not_found" {
			t.Errorf("status %d code %q, want 404 not_found", rec.Code, got)
		}
	})
}

func TestReadBundle(t *testing.T) {
	t.Run("no manifest", func(t *testing.T) {
		_, found, err := httpserver.ReadBundle(fstest.MapFS{"index.html": {}})
		if found || err != nil {
			t.Errorf("found %v err %v, want not found and no error", found, err)
		}
	})
	t.Run("nil tree", func(t *testing.T) {
		if _, found, err := httpserver.ReadBundle(nil); found || err != nil {
			t.Errorf("found %v err %v, want not found and no error", found, err)
		}
	})
	t.Run("a manifest", func(t *testing.T) {
		b, found, err := httpserver.ReadBundle(bundleTree(t, spaFiles(), spaListed, spaEntries))
		if !found || err != nil {
			t.Fatalf("found %v err %v", found, err)
		}
		if !slices.Equal(b.Entries, spaEntries) {
			t.Errorf("entries = %v, want %v", b.Entries, spaEntries)
		}
		if b.Files[moduleAsset] != sha256Hex(spaFiles()[moduleAsset]) || len(b.Files) != len(spaListed) {
			t.Errorf("files = %v", b.Files)
		}
	})
}
