package httpservertest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/go/httpserver/v2"
)

const (
	indexShell   = `<!doctype html><html><head><script type="module" src="/assets/app-BBBB.js"></script></head><body>app</body></html>`
	loginShell   = `<!doctype html><html><head><script type="module" src="/assets/app-BBBB.js"></script></head><body>sign in</body></html>`
	moduleAsset  = "assets/app-BBBB.js"
	classicAsset = "assets/worker-CCCC.js"
	styleAsset   = "assets/app-AAAA.css"
	fontAsset    = "assets/font-EEEE.woff2"
	noticeFile   = "licenses/index.txt"
	strayAsset   = "assets/stray-FFFF.js"
	stableAsset  = "sw.js"
)

var fixturePages = []httpserver.Page{
	{Name: "home", Pattern: "/{$}"},
	{Name: "library", Pattern: "/library"},
	{Name: "entry", Pattern: "/library/{id}"},
	{Name: "settings", Pattern: "/settings/{part...}"},
	{Name: "docs", Pattern: "/docs/"},
	{Name: "login", Pattern: "/login", Shell: "login.html"},
}

func fixtureFiles() map[string]string {
	return map[string]string{
		"index.html": indexShell,
		"login.html": loginShell,
		moduleAsset:  strings.Repeat("console.log('app');\n", 40),
		classicAsset: strings.Repeat("self.onconnect = () => {};\n", 40),
		stableAsset:  strings.Repeat("self.addEventListener('fetch', () => {});\n", 40),
		styleAsset:   strings.Repeat("body { color: red; }\n", 40),
		fontAsset:    "wOF2 font bytes",
		noticeFile:   strings.Repeat("pkg 1.0.0 MIT\n", 20),
		strayAsset:   "stray",
		".env":       "SECRET=1\n",
	}
}

var fixtureListed = []string{"index.html", "login.html", moduleAsset, classicAsset, stableAsset, styleAsset, fontAsset, noticeFile}

func fixtureTree(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fixtureFiles()
	tree := fstest.MapFS{}
	for name, body := range files {
		tree[name] = &fstest.MapFile{Data: []byte(body)}
	}
	b := httpserver.Bundle{
		Files: map[string]string{},
		Entries: []httpserver.BundleEntry{
			{Name: "app", Kind: httpserver.BundleModule, Path: moduleAsset},
			{Name: "worker", Kind: httpserver.BundleClassic, Path: classicAsset},
			{Name: "sw", Kind: httpserver.BundleClassic, Path: stableAsset, Stable: true},
			{Name: "app", Kind: httpserver.BundleStyle, Path: styleAsset},
		},
	}
	for _, name := range fixtureListed {
		sum := sha256.Sum256([]byte(files[name]))
		b.Files[name] = hex.EncodeToString(sum[:])
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	tree[httpserver.BundlePath] = &fstest.MapFile{Data: raw}
	return tree
}

func fixtureConfig(t *testing.T) httpserver.SPAConfig {
	t.Helper()
	return httpserver.SPAConfig{
		Assets:    fixtureTree(t),
		Pages:     fixturePages,
		CSPAdd:    map[string][]string{"img-src": {"blob:"}},
		APIPrefix: "/api",
	}
}

func fixtureSPA(t *testing.T, c httpserver.SPAConfig) http.Handler {
	t.Helper()
	h, err := httpserver.SPA(c)
	if err != nil {
		t.Fatalf("SPA: %v", err)
	}
	return h
}

// tamper answers through h, then lets edit rewrite the answer to a request
// match picks.
func tamper(h http.Handler, match func(*http.Request) bool, edit func(*httptest.ResponseRecorder)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !match(r) {
			h.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		edit(rec)
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

func atPath(p string) func(*http.Request) bool {
	return func(r *http.Request) bool { return r.URL.Path == p }
}

func runCheckSPA(t *testing.T, h http.Handler, c httpserver.SPAConfig) *recordingTB {
	t.Helper()
	return record(t, func(tb testing.TB) { CheckSPA(tb, h, c) })
}

// wantFailure fails t unless r reported a failure holding every part.
func wantFailure(t *testing.T, r *recordingTB, parts ...string) {
	t.Helper()
	for _, line := range append(r.errors, r.fatal) {
		hit := line != ""
		for _, p := range parts {
			hit = hit && strings.Contains(line, p)
		}
		if hit {
			return
		}
	}
	t.Errorf("no failure names %q; failures:\n%s", parts, r.failures())
}

func TestCheckSPAPassesOnItsBundle(t *testing.T) {
	c := fixtureConfig(t)
	spa := fixtureSPA(t, c)
	app := http.NewServeMux()
	app.Handle("/api/", httpserver.NotFound(nil))
	app.Handle("/", spa)
	for name, h := range map[string]http.Handler{"spa alone": spa, "spa beside an api": app} {
		t.Run(name, func(t *testing.T) {
			if r := runCheckSPA(t, h, c); len(r.errors) != 0 || r.fatal != "" {
				t.Errorf("CheckSPA failed a correct SPA:\n%s", r.failures())
			}
		})
	}
	for name, pages := range wildcardTables {
		t.Run(name, func(t *testing.T) {
			c := fixtureConfig(t)
			c.Pages = pages
			if r := runCheckSPA(t, fixtureSPA(t, c), c); len(r.errors) != 0 || r.fatal != "" {
				t.Errorf("CheckSPA failed a correct SPA:\n%s", r.failures())
			}
		})
	}
}

// wildcardTables are page tables whose patterns also match the bundle's
// own paths.
var wildcardTables = map[string][]httpserver.Page{
	"root subtree":  {{Name: "any", Pattern: "/"}, {Name: "login", Pattern: "/login", Shell: "login.html"}},
	"two wildcards": {{Name: "home", Pattern: "/{$}"}, {Name: "any", Pattern: "/{a}/{b}"}, {Name: "login", Pattern: "/login", Shell: "login.html"}},
}

func TestCheckSPAFailsAssetAnsweringAShell(t *testing.T) {
	for name, pages := range wildcardTables {
		t.Run(name, func(t *testing.T) {
			c := fixtureConfig(t)
			c.Pages = pages
			h := tamper(fixtureSPA(t, c), atPath("/"+moduleAsset), func(rec *httptest.ResponseRecorder) {
				rec.Header().Set("Content-Type", "text/html; charset=utf-8")
				rec.Header().Set("Cache-Control", "no-cache")
				rec.Header().Del("Content-Encoding")
				rec.Code = http.StatusOK
				rec.Body.Reset()
				rec.Body.WriteString(indexShell)
			})
			wantFailure(t, runCheckSPA(t, h, c), "/"+moduleAsset)
		})
	}
}

func TestCheckSPAFailsWorkerEntryWithoutCSP(t *testing.T) {
	c := fixtureConfig(t)
	h := tamper(fixtureSPA(t, c), atPath("/"+classicAsset), func(rec *httptest.ResponseRecorder) {
		rec.Header().Del("Content-Security-Policy")
	})
	wantFailure(t, runCheckSPA(t, h, c), "/"+classicAsset, "Content-Security-Policy")
}

func TestCheckSPAFailsImmutableClassicEntry(t *testing.T) {
	c := fixtureConfig(t)
	h := tamper(fixtureSPA(t, c), atPath("/"+classicAsset), func(rec *httptest.ResponseRecorder) {
		rec.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	})
	wantFailure(t, runCheckSPA(t, h, c), "/"+classicAsset, "Cache-Control")
}

func TestCheckSPAFailsClassicETagThatIgnoresPolicy(t *testing.T) {
	c := fixtureConfig(t)
	sum := sha256.Sum256([]byte(fixtureFiles()[classicAsset]))
	h := tamper(fixtureSPA(t, c), atPath("/"+classicAsset), func(rec *httptest.ResponseRecorder) {
		if rec.Header().Get("Content-Encoding") == "" {
			rec.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		}
	})
	wantFailure(t, runCheckSPA(t, h, c), "/"+classicAsset, "ETag")
}

func TestCheckSPAFailsStableEntryServedWrong(t *testing.T) {
	sum := sha256.Sum256([]byte(fixtureFiles()[stableAsset]))
	edits := map[string]struct {
		edit func(*httptest.ResponseRecorder)
		want string
	}{
		"immutable": {func(rec *httptest.ResponseRecorder) {
			rec.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}, "Cache-Control"},
		"an ETag that ignores the policy": {func(rec *httptest.ResponseRecorder) {
			if rec.Header().Get("Content-Encoding") == "" {
				rec.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
			}
		}, "ETag"},
		"no policy": {func(rec *httptest.ResponseRecorder) {
			rec.Header().Del("Content-Security-Policy")
		}, "Content-Security-Policy"},
		"refused": {func(rec *httptest.ResponseRecorder) {
			rec.Code = http.StatusNotFound
		}, "status"},
	}
	for name, tc := range edits {
		t.Run(name, func(t *testing.T) {
			c := fixtureConfig(t)
			h := tamper(fixtureSPA(t, c), atPath("/"+stableAsset), tc.edit)
			wantFailure(t, runCheckSPA(t, h, c), "/"+stableAsset, tc.want)
		})
	}
}

func TestCheckSPAFailsUndeclaredCSPAddition(t *testing.T) {
	built := fixtureConfig(t)
	built.CSPAdd = map[string][]string{"img-src": {"blob:"}, "media-src": {"blob:"}}
	wantFailure(t, runCheckSPA(t, fixtureSPA(t, built), fixtureConfig(t)), "/"+classicAsset, "Content-Security-Policy")
}

func TestCheckSPAFailsWeakerCSP(t *testing.T) {
	c := fixtureConfig(t)
	h := tamper(fixtureSPA(t, c), atPath("/library"), func(rec *httptest.ResponseRecorder) {
		p := rec.Header().Get("Content-Security-Policy")
		rec.Header().Set("Content-Security-Policy", strings.Replace(p, "object-src 'none'", "object-src *", 1))
	})
	wantFailure(t, runCheckSPA(t, h, c), "GET /library", "Content-Security-Policy")
}

func TestCheckSPAFailsPageAnsweringDefaultShell(t *testing.T) {
	built := fixtureConfig(t)
	built.Pages = []httpserver.Page{
		fixturePages[0], fixturePages[1], fixturePages[2], fixturePages[3], fixturePages[4],
		{Name: "login", Pattern: "/login"},
	}
	wantFailure(t, runCheckSPA(t, fixtureSPA(t, built), fixtureConfig(t)), "GET /login", "login.html")
}

func TestCheckSPAFailsServedUnlistedFile(t *testing.T) {
	c := fixtureConfig(t)
	h := tamper(fixtureSPA(t, c), atPath("/"+strayAsset), func(rec *httptest.ResponseRecorder) {
		rec.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		rec.Code = http.StatusOK
		rec.Body.Reset()
		rec.Body.WriteString(fixtureFiles()[strayAsset])
	})
	wantFailure(t, runCheckSPA(t, h, c), "/"+strayAsset)
}

func TestCheckSPAFailsContentTypeOutsideTable(t *testing.T) {
	c := fixtureConfig(t)
	h := tamper(fixtureSPA(t, c), atPath("/"+moduleAsset), func(rec *httptest.ResponseRecorder) {
		rec.Header().Set("Content-Type", "application/javascript")
	})
	wantFailure(t, runCheckSPA(t, h, c), "/"+moduleAsset, "Content-Type")
}

func TestCheckSPAFailsImmutableShell(t *testing.T) {
	c := fixtureConfig(t)
	h := tamper(fixtureSPA(t, c), atPath("/docs/"), func(rec *httptest.ResponseRecorder) {
		rec.Header().Set("Cache-Control", "public, max-age=3600")
	})
	wantFailure(t, runCheckSPA(t, h, c), "GET /docs/", "Cache-Control")
}

func TestCheckSPAFailsWrongStatus(t *testing.T) {
	c := fixtureConfig(t)
	h := tamper(fixtureSPA(t, c), func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/checkspa-") },
		func(rec *httptest.ResponseRecorder) { rec.Code = http.StatusOK })
	wantFailure(t, runCheckSPA(t, h, c), "/checkspa-", "status")
}

func TestCheckSPAFailsCacheableRefusal(t *testing.T) {
	c := fixtureConfig(t)
	for _, target := range []string{"/" + httpserver.BundlePath, "/assets/checkspa-unlisted.js"} {
		t.Run(strings.ReplaceAll(target, "/", "|"), func(t *testing.T) {
			h := tamper(fixtureSPA(t, c), atPath(target), func(rec *httptest.ResponseRecorder) {
				rec.Header().Set("Cache-Control", "no-cache")
			})
			wantFailure(t, runCheckSPA(t, h, c), target, "Cache-Control")
		})
	}
}

func TestCheckSPAFailsRefusalNotEnvelope(t *testing.T) {
	c := fixtureConfig(t)
	h := tamper(fixtureSPA(t, c), atPath("/"+httpserver.BundlePath), func(rec *httptest.ResponseRecorder) {
		rec.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rec.Body.Reset()
		rec.Body.WriteString("404 page not found\n")
	})
	wantFailure(t, runCheckSPA(t, h, c), "/"+httpserver.BundlePath, "envelope")
}

func TestCheckSPAFailsRefusedShell(t *testing.T) {
	c := fixtureConfig(t)
	for name, inline := range map[string]string{
		"script element":          `<script>alert(1)</script>`,
		"style element":           `<style>body{}</style>`,
		"style attribute":         `<p style="color:red">x</p>`,
		"event-handler attribute": `<button onclick="go()">x</button>`,
	} {
		t.Run(name, func(t *testing.T) {
			h := tamper(fixtureSPA(t, c), atPath("/library"), func(rec *httptest.ResponseRecorder) {
				body := strings.Replace(rec.Body.String(), "<body>", "<body>"+inline, 1)
				rec.Body.Reset()
				rec.Body.WriteString(body)
			})
			wantFailure(t, runCheckSPA(t, h, c), "GET /library", "the shell is refused")
		})
	}
}

func TestCheckSPAFailsAcceptedPOST(t *testing.T) {
	c := fixtureConfig(t)
	for name, crossSite := range map[string]bool{"same origin": false, "cross site": true} {
		t.Run(name, func(t *testing.T) {
			h := tamper(fixtureSPA(t, c), func(r *http.Request) bool {
				return r.Method == http.MethodPost && (r.Header.Get("Sec-Fetch-Site") == "cross-site") == crossSite
			}, func(rec *httptest.ResponseRecorder) { rec.Code = http.StatusOK })
			wantFailure(t, runCheckSPA(t, h, c), "POST", "status")
		})
	}
}

func TestCheckSPAFailsShellUnderAPIPrefix(t *testing.T) {
	c := fixtureConfig(t)
	spa := fixtureSPA(t, c)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/library"
			spa.ServeHTTP(w, r2)
			return
		}
		spa.ServeHTTP(w, r)
	})
	wantFailure(t, runCheckSPA(t, h, c), "/api/")
}

func TestCheckSPAFailsWithNoBundle(t *testing.T) {
	c := fixtureConfig(t)
	h := fixtureSPA(t, c)
	c.Assets = fstest.MapFS{}
	if r := runCheckSPA(t, h, c); r.fatal == "" {
		t.Errorf("CheckSPA ran with no bundle; failures:\n%s", r.failures())
	}
}

func runCheckNoUI(t *testing.T, h http.Handler) *recordingTB {
	t.Helper()
	return record(t, func(tb testing.TB) { CheckNoUI(tb, h) })
}

func TestCheckNoUIPassesOnNotFound(t *testing.T) {
	if r := runCheckNoUI(t, httpserver.NotFound(nil)); len(r.errors) != 0 || r.fatal != "" {
		t.Errorf("CheckNoUI failed NotFound:\n%s", r.failures())
	}
}

func TestCheckNoUIFailsOnAnotherAnswer(t *testing.T) {
	for name, h := range map[string]http.Handler{
		"plain text 404": http.NotFoundHandler(),
		"a web UI":       fixtureSPA(t, fixtureConfig(t)),
	} {
		t.Run(name, func(t *testing.T) {
			if r := runCheckNoUI(t, h); len(r.errors) == 0 {
				t.Error("CheckNoUI passed a handler that does not answer not_found everywhere")
			}
		})
	}
}
