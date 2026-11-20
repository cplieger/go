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
	"time"

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
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q on a refusal, want no-store", cc)
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

func TestSPATailPatternAnswersEveryDepth(t *testing.T) {
	c := spaConfig(t, nil)
	c.Pages = []httpserver.Page{{Name: "file", Pattern: "/file/{path...}"}}
	h := newSPA(t, c)
	for _, target := range []string{"/file/a/b/c.ts", "/file/.github/x.yaml"} {
		t.Run(pathName(target), func(t *testing.T) {
			wantShell(t, spaServe(h, http.MethodGet, target), http.StatusOK, indexHTML)
		})
	}
	t.Run("the bare prefix redirects to its slash", func(t *testing.T) {
		rec := spaServe(h, http.MethodGet, "/file")
		if rec.Code != http.StatusTemporaryRedirect {
			t.Errorf("status = %d, want 307", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/file/" {
			t.Errorf("Location = %q, want /file/", loc)
		}
	})
}

func TestSPARedirectIs307(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	cases := map[string]string{
		"/docs?q=1":           "/docs/?q=1",
		"//library?q=1":       "/library?q=1",
		"/library/x/../a?q=1": "/library/a?q=1",
		"/settings":           "/settings/",
		// A dot segment leaving the tree is a page path once cleaned.
		"/assets/../library":       "/library",
		"/licenses/../library?q=1": "/library?q=1",
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

// noCacheETag is the tag docs/spa.md gives a no-cache answer: the first 32
// hex digits of SHA-256 over the manifest hash, a zero byte and the policy.
func noCacheETag(fileHash, policy string) string {
	return `"` + sha256Hex(fileHash + "\x00" + policy)[:32] + `"`
}

func TestSPAServesStableEntry(t *testing.T) {
	const blobPolicy = defaultPolicy + "; media-src 'self' blob:"
	sw := strings.Repeat("self.addEventListener('fetch', () => {});\n", 40)
	c := spaConfig(t, nil)
	c.Assets = stableTree(t, sw)
	h := newSPA(t, c)
	for _, policy := range []string{defaultPolicy, blobPolicy} {
		t.Run("under "+policy, func(t *testing.T) {
			pc := spaConfig(t, nil)
			pc.Assets = stableTree(t, sw)
			if policy == blobPolicy {
				pc.CSPAdd = map[string][]string{"media-src": {"blob:"}}
			}
			ph := newSPA(t, pc)
			rec := spaServe(ph, http.MethodGet, "/"+stableAsset)
			if rec.Code != http.StatusOK || rec.Body.String() != sw {
				t.Errorf("GET /%s = %d %q, want 200 and the file", stableAsset, rec.Code, rec.Body.String())
			}
			hdr := rec.Header()
			if got := hdr.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/javascript; charset=utf-8", got)
			}
			if got := hdr.Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", got)
			}
			if got, want := hdr.Get("ETag"), noCacheETag(sha256Hex(sw), policy); got != want {
				t.Errorf("ETag = %q, want the policy-bound tag %q", got, want)
			}
			wantPolicy(t, rec, policy)
			gz := spaServe(ph, http.MethodGet, "/"+stableAsset, "Accept-Encoding", "gzip")
			if gz.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzip(t, gz.Body.Bytes()), []byte(sw)) {
				t.Errorf("gzip answer = %q encoded, want the file gzipped", gz.Header().Get("Content-Encoding"))
			}
			if got, want := gz.Header().Get("ETag"), strings.TrimSuffix(noCacheETag(sha256Hex(sw), policy), `"`)+`-gz"`; got != want {
				t.Errorf("gzip ETag = %q, want %q", got, want)
			}
			wantPolicy(t, gz, policy)
		})
	}
	t.Run("a stable module entry is no-cache too", func(t *testing.T) {
		files := spaFiles()
		files[stableAsset] = sw
		mc := spaConfig(t, nil)
		mc.Assets = bundleTree(t, files, append(slices.Clone(spaListed), stableAsset),
			append(slices.Clone(spaEntries), httpserver.BundleEntry{Name: "sw", Kind: httpserver.BundleModule, Path: stableAsset, Stable: true}))
		rec := spaServe(newSPA(t, mc), http.MethodGet, "/"+stableAsset)
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("Cache-Control = %q, want no-cache", got)
		}
		if got, want := rec.Header().Get("ETag"), noCacheETag(sha256Hex(sw), defaultPolicy); got != want {
			t.Errorf("ETag = %q, want the policy-bound tag %q", got, want)
		}
	})
	t.Run("the ETag changes when its bytes change", func(t *testing.T) {
		old := spaServe(h, http.MethodGet, "/"+stableAsset).Header().Get("ETag")
		next := sw + "self.skipWaiting();\n"
		nc := spaConfig(t, nil)
		nc.Assets = stableTree(t, next)
		rec := spaServe(newSPA(t, nc), http.MethodGet, "/"+stableAsset, "If-None-Match", old)
		if rec.Code != http.StatusOK || rec.Body.String() != next {
			t.Errorf("If-None-Match of the old tag = %d %q, want 200 and the new file", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("ETag"); got == old || got != noCacheETag(sha256Hex(next), defaultPolicy) {
			t.Errorf("ETag = %q after the bytes changed from the tag %q, want %q", got, old, noCacheETag(sha256Hex(next), defaultPolicy))
		}
	})
	t.Run("an unchanged file revalidates", func(t *testing.T) {
		etag := spaServe(h, http.MethodGet, "/"+stableAsset).Header().Get("ETag")
		if rec := spaServe(h, http.MethodGet, "/"+stableAsset, "If-None-Match", etag); rec.Code != http.StatusNotModified {
			t.Errorf("If-None-Match of the current tag: status %d, want 304", rec.Code)
		}
	})
	t.Run("with no stable entry the path answers the 404 shell", func(t *testing.T) {
		wantShell(t, spaServe(newSPA(t, spaConfig(t, nil)), http.MethodGet, "/"+stableAsset), http.StatusNotFound, indexHTML)
	})
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
			for _, target := range []string{"/assets/missing-1.js", "/assets//missing.js", "/assets/./x.js", "/licenses/nope.txt", "/bundle.json", "/index.html", "/login.html"} {
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
		"script after a quote in an unquoted value":   `<script data=a"b>alert(1)</script>"`,
		"script after a script tag in a quoted value": `<div title="<script src=x>"></div><script>alert(1)</script>`,
		"style open at end of input":                  `<style>a{}`,
		"svg script":                                  `<svg><script>s=1</script></svg>`,
		"svg style":                                   `<svg><style>a{}</style></svg>`,
	}
	for name, html := range refused {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckShell([]byte(html)); err == nil {
				t.Errorf("CheckShell accepted %q", html)
			}
		})
	}
	passed := map[string]string{
		"external script":               `<script type="module" src="/assets/app.js"></script>`,
		"data-style":                    `<body data-style="x">style=plain text</body>`,
		"style in a value":              `<p title="style=x">y</p>`,
		"script tags in a quoted value": `<div title="</script><script>alert(1)</script>">x</div>`,
		"json data block":               `<script type="application/json">{"a":1}</script>`,
		"template data block":           `<script type="text/template"><p onclick=go()>x</p></script>`,
		"svg script with href":          `<svg><script href="/assets/a.js"></script></svg>`,
	}
	for name, html := range passed {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckShell([]byte(html)); err != nil {
				t.Errorf("CheckShell refused %q: %v", html, err)
			}
		})
	}
}

func TestCheckShellRefusesEventHandlers(t *testing.T) {
	refused := []struct {
		name, html, element, attr string
	}{
		{"onload on body", `<body onload="init()">x</body>`, "<body>", `"onload"`},
		{"mixed-case OnClick on a button", `<button type=button OnClick='go()'>x</button>`, "<button>", `"onclick"`},
		{"after a quoted value with no space", `<a title="x"onclick=go()>x</a>`, "<a>", `"onclick"`},
		{"after a slash", `<img/onerror=go() src=x>`, "<img>", `"onerror"`},
		{"after an unquoted value holding a quote", `<p title=a"b onclick=go()>x</p>`, "<p>", `"onclick"`},
		{"upper-case element", `<DIV ONMOUSEOVER=go()>x</DIV>`, "<div>", `"onmouseover"`},
		{"style after an unquoted value holding a quote", `<p title=a"b style=x>y</p>`, "<p>", "style"},
		{"after a comment holding an open quote", `<!-- <a title=' --><body onload="init()">x</body><!-- ' -->`, "<body>", `"onload"`},
		{"after a title holding an open quote", `<title><a title='</title><body onload=go()>'</title>`, "<body>", `"onload"`},
		{"after a title holding a fake tag", `<title><x onclick=go()></title><body onload=init()>x</body>`, "<body>", `"onload"`},
		{"after a textarea holding an open quote", `<textarea><a title='</textarea><button onclick=go()>x</button>'</textarea>`, "<button>", `"onclick"`},
		{"after a script holding an open quote", `<script src=/a.js>var s = "<a title='";</script><body onload=go()><script src=/b.js>'</script>`, "<body>", `"onload"`},
		{"after a noscript holding an open quote", `<noscript><a title='</noscript><button onclick=go()>'</noscript>`, "<button>", `"onclick"`},
		// HTML "in foreign content": an <img> start tag breaks out of svg, so a
		// svg title is no raw text and the img is an element.
		{"inside an svg title", `<svg><title><img src=x onerror=alert(1)></title></svg>`, "<img>", `"onerror"`},
		{"on an svg element", `<svg onload=go()></svg>`, "<svg>", `"onload"`},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			err := httpserver.CheckShell([]byte(tc.html))
			if err == nil {
				t.Fatalf("CheckShell accepted %q", tc.html)
			}
			if !strings.Contains(err.Error(), tc.element) || !strings.Contains(err.Error(), tc.attr) {
				t.Errorf("CheckShell(%q) = %q, want it to name %s and %s", tc.html, err, tc.element, tc.attr)
			}
		})
	}
	passed := map[string]string{
		"on in a quoted title":    `<button title="onclick=go()">x</button>`,
		"on in a single-quoted":   `<p title='a" onclick=go() "b'>x</p>`,
		"on in an unquoted value": `<p title=onclick>x</p>`,
		"data-on":                 `<div data-on="click">x</div>`,
		"a bare on attribute":     `<p on="x">y</p>`,
		"on in text":              `<p>onclick=go()</p>`,
		"in a comment":            `<!-- <button onclick="old()">x</button> -->`,
		"in a title":              `<title><button onclick="x">t</title>`,
		"in a textarea":           `<textarea><img onerror=alert(1)></textarea>`,
		"in a noscript":           `<noscript><button onclick="x">n</button></noscript>`,
	}
	for name, html := range passed {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckShell([]byte(html)); err != nil {
				t.Errorf("CheckShell refused %q: %v", html, err)
			}
		})
	}
}

// HTML "text" insertion mode: a script closed by end of input never runs, so
// a shell ending inside one is broken whatever its source.
func TestCheckShellRefusesAnOpenScript(t *testing.T) {
	refused := map[string]string{
		"no end tag":                     `<script src=/assets/a.js>`,
		"an end tag missing its >":       `<script src=/assets/a.js></script`,
		"an end tag ending in a space":   `<script src=/assets/a.js></script `,
		"an end tag ending in attribute": `<script src=/assets/a.js></SCRIPT  a=`,
		"an inline script":               `<script>x=1</script `,
		"an svg script":                  `<svg><script href=/assets/a.js>`,
	}
	for name, html := range refused {
		t.Run(name, func(t *testing.T) {
			err := httpserver.CheckShell([]byte(html))
			if err == nil {
				t.Fatalf("CheckShell accepted %q", html)
			}
			if !strings.Contains(err.Error(), "<script>") || !strings.Contains(err.Error(), "open at end of input") {
				t.Errorf("CheckShell(%q) = %q, want it to name <script> left open at end of input", html, err)
			}
		})
	}
	passed := map[string]string{
		"a closed external script":                    `<script src=/assets/a.js></script>`,
		"an unfinished end tag after a closed script": `<script src=/assets/a.js></script><p></script `,
	}
	for name, html := range passed {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckShell([]byte(html)); err != nil {
				t.Errorf("CheckShell refused %q: %v", html, err)
			}
		})
	}
}

// html.Parse refuses a document nested deeper than 512 open elements (its
// doc comment), so no tree is checked and no hash is computed.
func TestShellPastParserDepth(t *testing.T) {
	deep := []byte("<html><body>" + strings.Repeat("<div>", 512) + "<script>x()</script><style>a{}</style>")
	err := httpserver.CheckShell(deep)
	if err == nil || !strings.Contains(err.Error(), "cannot be read as HTML") {
		t.Errorf("CheckShell(512 nested <div>) = %v, want a refusal saying it cannot be read as HTML", err)
	}
	if got := httpserver.InlineScriptHashes(deep); got != nil {
		t.Errorf("InlineScriptHashes(512 nested <div>) = %v, want nil", got)
	}
	if got := httpserver.InlineStyleHashes(deep); got != nil {
		t.Errorf("InlineStyleHashes(512 nested <div>) = %v, want nil", got)
	}
	shallow := "<html><body>" + strings.Repeat("<div>", 500) + "x"
	if err := httpserver.CheckShell([]byte(shallow)); err != nil {
		t.Errorf("CheckShell(500 nested <div>) refused: %v", err)
	}
}

// x/net/html v0.63.0 drops every HTML token after a <template> start tag that
// arrives with an <svg> or <math> element open (parse.go inHeadIM), keeping
// only foreign-content ones, where a browser reads on and builds the rest.
func TestCheckShellRefusesTemplateInForeignContent(t *testing.T) {
	refused := map[string]string{
		"an img after a template in a foreignObject":    `<svg><foreignObject><template></template><img src=x onerror=alert(1)></foreignObject></svg>`,
		"a script after it":                             `<svg><foreignObject><template></template><script>alert(1)</script>`,
		"a body onload after it":                        `<svg><foreignObject><template></template><body onload=alert(1)>`,
		"an annotation-xml template":                    `<math><annotation-xml encoding="text/html"><template></template><img src=x onerror=alert(1)>`,
		"an svg title template":                         `<svg><title><template></template><img src=x onerror=alert(1)>`,
		"a kept div before the template":                `<svg><foreignObject><div id=keep></div><template></template><img src=x onerror=alert(1)>`,
		"svg content read on after foreignObject":       `<svg><foreignObject><template></template><img onerror=x></foreignObject><g></g></svg>`,
		"an upper-case template with an attribute":      `<svg><desc><TEMPLATE id=t></TEMPLATE><img onerror=x>`,
		"a mathml text integration point":               `<math><mi><template></template><img onerror=x>`,
		"a template behind a cloned formatting element": `<svg></svg><p><b title="<template>">a<p>b<svg><foreignObject><template></template><img src=x onerror=alert(1)>`,
		"an svg-markup template after the stop":         `<svg><foreignObject><template></template></foreignObject><template></template><img onerror=x>`,
		"two templates behind two clones":               `<svg></svg><p><b title="<template>">a<p>b<p>c<svg><foreignObject><template></template><template></template><img src=x onerror=alert(1)>`,
	}
	for name, html := range refused {
		t.Run(name, func(t *testing.T) {
			err := httpserver.CheckShell([]byte(html))
			if err == nil {
				t.Fatalf("CheckShell accepted %q", html)
			}
			if !strings.Contains(err.Error(), "<template> inside <svg> or <math>") {
				t.Errorf("CheckShell(%q) = %q, want it to name a <template> inside <svg> or <math>", html, err)
			}
		})
	}
	t.Run("a template after an svg is read", func(t *testing.T) {
		html := `<svg></svg><template></template><img src=x onerror=alert(1)>`
		err := httpserver.CheckShell([]byte(html))
		if err == nil || !strings.Contains(err.Error(), `"onerror" on <img>`) {
			t.Errorf("CheckShell(%q) = %v, want the onerror on <img> refused", html, err)
		}
	})
	t.Run("at most 16 templates are read past an svg", func(t *testing.T) {
		pair := "<template></template>"
		for _, html := range []string{
			"<svg></svg>" + strings.Repeat(pair, 16),
			strings.Repeat(pair, 17) + "<svg></svg>",
		} {
			if err := httpserver.CheckShell([]byte(html)); err != nil {
				t.Errorf("CheckShell refused %q: %v", html, err)
			}
		}
		over := map[string]string{
			"17 templates after an svg":                 "<svg></svg>" + strings.Repeat(pair, 17),
			"17 templates after a commented-out svg":    "<!-- <svg> -->" + strings.Repeat(pair, 17),
			"17 template spellings in attribute values": "<svg></svg>" + strings.Repeat(`<p title="<template>"></p>`, 17),
		}
		for name, html := range over {
			err := httpserver.CheckShell([]byte(html))
			if err == nil || !strings.Contains(err.Error(), "spells <template more than 16 times after its first <svg or <math") {
				t.Errorf("CheckShell(%s) = %v, want the limit of 16 spellings named", name, err)
			}
		}
	})
	passed := map[string]string{
		"a template beside an svg icon":                    `<svg><path d="M0 0"/></svg><template id=row><p>x</p></template>`,
		"a template in svg markup":                         `<svg><template></template></svg>`,
		"a template tag in a comment":                      `<svg><foreignObject><!-- <template> --></foreignObject></svg>`,
		"a template tag in a dropped duplicate attribute":  `<svg></svg><p title=x title="<template>">y</p>`,
		"a template tag in an attribute value":             `<svg><foreignObject><div title="<template>"></div></foreignObject></svg>`,
		"a longer tag name":                                `<svg><foreignObject><templates></templates></foreignObject></svg>`,
		"a longer tag name beside a dropped attribute":     `<svg><foreignObject><templates></templates><p title=x title="<template>">y</p></foreignObject></svg>`,
		"a template tag whose quote runs past a later one": `<svg></svg><p title="<template a='"><template></template>'>x</p>`,
		"a template in a frameset document":                `<frameset><svg><desc><template></template></desc></svg></frameset>`,
	}
	for name, html := range passed {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckShell([]byte(html)); err != nil {
				t.Errorf("CheckShell refused %q: %v", html, err)
			}
		})
	}
}

func TestSPARefusalNamesShellElementAndAttribute(t *testing.T) {
	files := spaFiles()
	files["login.html"] = `<html><body><button OnClick="go()">x</button></body></html>`
	c := spaConfig(t, nil)
	c.Assets = bundleTree(t, files, spaListed, spaEntries)
	_, err := httpserver.SPA(c)
	if err == nil {
		t.Fatal("SPA accepted a shell with an event handler")
	}
	for _, want := range []string{`"login.html"`, "<button>", `"onclick"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("SPA error %q does not name %s", err, want)
		}
	}
}

func TestSPAErrorsResponderAnswersRefusals(t *testing.T) {
	var codes []httpserver.ErrorCode
	errs := func(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: the policy is not set before the responder runs", code)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control = %q before the responder runs, want no-store", code, cc)
		}
		codes = append(codes, code)
		w.WriteHeader(status)
	}
	c := spaConfig(t, nil)
	c.Errors = errs
	h := newSPA(t, c)
	spaServe(h, http.MethodGet, "/assets/missing.js")
	spaServe(h, http.MethodPost, "/library")
	spaServe(h, http.MethodGet, "/"+moduleAsset, "If-Match", `"other"`)
	c.Assets = nil
	spaServe(newSPA(t, c), http.MethodGet, "/library")
	if want := []httpserver.ErrorCode{"not_found", "method_not_allowed", "precondition_failed", "ui_unbuilt"}; !slices.Equal(codes, want) {
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

// spaServedFiles is one target of each kind SPA writes with a 200: an
// immutable asset, a classic entry, a notice and a page's shell.
func spaServedFiles() map[string]string {
	files := spaFiles()
	return map[string]string{"/" + moduleAsset: files[moduleAsset], "/" + classicAsset: files[classicAsset], "/" + noticeFile: files[noticeFile], "/library": indexHTML}
}

func TestSPAIgnoresRange(t *testing.T) {
	c := spaConfig(t, nil)
	h := newSPA(t, c)
	for target, body := range spaServedFiles() {
		etag := spaServe(h, http.MethodGet, target).Header().Get("ETag")
		for _, header := range [][]string{
			{"Range", "bytes=0-3"},
			{"Range", "bytes=999999-"},
			{"Range", "bytes=0-3", "If-Range", etag},
			{"Range", "nonsense"},
		} {
			t.Run(pathName(target)+" "+strings.Join(header, " "), func(t *testing.T) {
				rec := spaServe(h, http.MethodGet, target, header...)
				if rec.Code != http.StatusOK || rec.Body.String() != body {
					t.Errorf("status %d body %d bytes, want 200 and the whole file (%d bytes)", rec.Code, rec.Body.Len(), len(body))
				}
				for _, k := range []string{"Accept-Ranges", "Content-Range"} {
					if v := rec.Header().Values(k); v != nil {
						t.Errorf("%s = %q, want none", k, v)
					}
				}
				wantPolicy(t, rec, defaultPolicy)
			})
		}
		t.Run(pathName(target)+" gzip", func(t *testing.T) {
			rec := spaServe(h, http.MethodGet, target, "Range", "bytes=0-3", "Accept-Encoding", "gzip")
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzip(t, rec.Body.Bytes()), []byte(body)) {
				t.Errorf("status %d Content-Encoding %q, want 200 and the whole gzip body", rec.Code, rec.Header().Get("Content-Encoding"))
			}
		})
	}
}

func TestSPAEvaluatesIfMatch(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	for target, body := range spaServedFiles() {
		etag := spaServe(h, http.MethodGet, target).Header().Get("ETag")
		gzTag := spaServe(h, http.MethodGet, target, "Accept-Encoding", "gzip").Header().Get("ETag")
		if etag == "" || gzTag == etag {
			t.Fatalf("%s: ETag %q, gzip ETag %q, want two tags", target, etag, gzTag)
		}
		holds := map[string][]string{
			"its tag":              {"If-Match", etag},
			"a list holding it":    {"If-Match", `"x", ` + etag},
			"a star":               {"If-Match", "*"},
			"its gzip tag gzipped": {"If-Match", gzTag, "Accept-Encoding", "gzip"},
			"the matching tag beside a failing If-None-Match": {"If-Match", etag, "If-None-Match", `"x"`},
		}
		for name, header := range holds {
			t.Run(pathName(target)+" holds "+name, func(t *testing.T) {
				rec := spaServe(h, http.MethodGet, target, header...)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200", rec.Code)
				}
				got := rec.Body.Bytes()
				if rec.Header().Get("Content-Encoding") == "gzip" {
					got = gunzip(t, got)
				}
				if string(got) != body {
					t.Error("body is not the file")
				}
			})
		}
		fails := map[string][]string{
			"another tag":                     {"If-Match", `"other"`},
			"its weak tag":                    {"If-Match", "W/" + etag},
			"its identity tag gzipped":        {"If-Match", etag, "Accept-Encoding", "gzip"},
			"its gzip tag for the identity":   {"If-Match", gzTag},
			"a star beside a tag":             {"If-Match", `*, "other"`},
			"before a matching If-None-Match": {"If-Match", `"other"`, "If-None-Match", etag},
		}
		for name, header := range fails {
			t.Run(pathName(target)+" fails "+name, func(t *testing.T) {
				rec := spaServe(h, http.MethodGet, target, header...)
				wantRefusal(t, rec, http.StatusPreconditionFailed, "precondition_failed")
				if v := rec.Header().Get("ETag"); v != "" {
					t.Errorf("ETag = %q on the refusal, want none", v)
				}
			})
		}
		t.Run(pathName(target)+" HEAD fails too", func(t *testing.T) {
			rec := spaServe(h, http.MethodHead, target, "If-Match", `"other"`)
			if rec.Code != http.StatusPreconditionFailed {
				t.Errorf("status = %d, want 412", rec.Code)
			}
		})
	}
}

func TestSPAPreconditionsSkipTheNotFoundShell(t *testing.T) {
	h := newSPA(t, spaConfig(t, nil))
	rec := spaServe(h, http.MethodGet, "/no/such/page", "If-Match", `"other"`)
	wantShell(t, rec, http.StatusNotFound, indexHTML)
}

func TestSPAIgnoresDatePreconditions(t *testing.T) {
	c := spaConfig(t, nil)
	tree := c.Assets.(fstest.MapFS)
	modTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, f := range tree {
		f.ModTime = modTime
	}
	h := newSPA(t, c)
	for target, body := range spaServedFiles() {
		for _, header := range [][]string{
			{"If-Unmodified-Since", modTime.Add(-time.Hour).Format(http.TimeFormat)},
			{"If-Modified-Since", modTime.Add(time.Hour).Format(http.TimeFormat)},
		} {
			t.Run(pathName(target)+" "+header[0], func(t *testing.T) {
				rec := spaServe(h, http.MethodGet, target, header...)
				if rec.Code != http.StatusOK || rec.Body.String() != body {
					t.Errorf("status = %d, want 200 and the file", rec.Code)
				}
				if v := rec.Header().Get("Last-Modified"); v != "" {
					t.Errorf("Last-Modified = %q, want none", v)
				}
			})
		}
	}
}

func TestSPAServesListedIndexUnderAssets(t *testing.T) {
	const name, body = "assets/sub/index.html", "<p>nested</p>"
	files := spaFiles()
	files[name] = body
	c := spaConfig(t, nil)
	c.Assets = bundleTree(t, files, append(slices.Clone(spaListed), name), spaEntries)
	rec := spaServe(newSPA(t, c), http.MethodGet, "/"+name)
	if rec.Code != http.StatusOK || rec.Body.String() != body {
		t.Errorf("status %d body %q, want 200 and the file", rec.Code, rec.Body.String())
	}
}
