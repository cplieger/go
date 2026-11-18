package webbundle_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
	"github.com/cplieger/go/webbundle"
)

// stableFixture is the fixture app registering a service worker built as a
// classic entry at url.
func stableFixture(t *testing.T, name, url string) webbundle.Config {
	t.Helper()
	c := newFixture(t, files{
		"src/app.ts": "declare const BUNDLE_URLS: { " + name + ": string };\nnavigator.serviceWorker.register(BUNDLE_URLS." + name + ", { scope: \"/\" });\n",
		"src/sw.js":  "self.addEventListener(\"fetch\", () => { console.log(\"worker v1\"); });\n",
	})
	c.Entries = append(c.Entries, webbundle.Entry{Name: name, Path: "src/sw.js", Kind: webbundle.Classic, URL: url})
	return c
}

func TestBuildWritesAnEntryAtItsURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ test, name, url, file string }{
		{test: "NameAsURL", name: "sw", url: "/sw.js", file: "sw.js"},
		{test: "OtherFileName", name: "worker", url: "/Service-Worker_v2.0~a.js", file: "Service-Worker_v2.0~a.js"},
	} {
		t.Run(tc.test, func(t *testing.T) {
			t.Parallel()
			c := stableFixture(t, tc.name, tc.url)
			r := mustBuild(t, c)
			b := readBundle(t, c.Out)
			for _, e := range b.Entries {
				if want := e.Name == tc.name; e.Stable != want {
					t.Errorf("%s entry %q has stable %v, want %v", httpserver.BundlePath, e.Name, e.Stable, want)
				}
			}
			if got := entryPath(t, b, tc.name, webbundle.Classic); got != tc.file {
				t.Fatalf("%s gives the entry the path %q, want %q", httpserver.BundlePath, got, tc.file)
			}
			if _, ok := b.Files[tc.file]; !ok {
				t.Errorf("%s does not list %s", httpserver.BundlePath, tc.file)
			}
			if got := listed(b, hashedName("sw.js")); len(got) != 0 {
				t.Errorf("the entry is also written hashed: %q", got)
			}
			if _, ok := r.Outputs[tc.file]; !ok {
				t.Errorf("Result.Outputs has no %s: %v", tc.file, r.Outputs)
			}
			app := loadedJS(t, c.Out, entryPath(t, b, "app", webbundle.Module))
			if !bytes.Contains(app, []byte(strconv.Quote(tc.url))) {
				t.Errorf("the module entry does not read BUNDLE_URLS.%s as %q:\n%s", tc.name, tc.url, app)
			}
		})
	}
}

func TestBuildKeepsAnEntrysURLAcrossBuilds(t *testing.T) {
	t.Parallel()
	c := stableFixture(t, "sw", "/sw.js")
	mustBuild(t, c)
	first := readFile(t, c.Out, "sw.js")
	writeFiles(t, c.Src, files{"src/sw.js": "self.addEventListener(\"fetch\", () => { console.log(\"worker v2\"); });\n"})
	mustBuild(t, c)
	b := readBundle(t, c.Out)
	if got := entryPath(t, b, "sw", webbundle.Classic); got != "sw.js" {
		t.Fatalf("a rebuild moved the entry to %q", got)
	}
	if again := readFile(t, c.Out, "sw.js"); bytes.Equal(first, again) || !bytes.Contains(again, []byte("worker v2")) {
		t.Errorf("a rebuild with a changed source did not rewrite sw.js:\n%s", again)
	}
}

// SPA serves an entry with a URL of its own as it serves a classic entry,
// but revalidated on every load: the browser's update check for a service
// worker reads that answer.
func TestSPAServesAnEntrysURLNoCache(t *testing.T) {
	t.Parallel()
	c := stableFixture(t, "sw", "/sw.js")
	mustBuild(t, c)
	cfg := httpserver.SPAConfig{Assets: os.DirFS(c.Out), Pages: c.Pages, APIPrefix: c.APIPrefix}
	h, err := httpserver.SPA(cfg)
	if err != nil {
		t.Fatalf("httpserver.SPA refuses the built tree: %v", err)
	}
	httpservertest.CheckSPA(t, h, cfg)
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody))
		return rec
	}
	sw := get("/sw.js")
	classic := get("/" + entryPath(t, readBundle(t, c.Out), "prepaint", webbundle.Classic))
	if sw.Code != http.StatusOK {
		t.Fatalf("GET /sw.js = %d", sw.Code)
	}
	if got := sw.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("GET /sw.js Cache-Control = %q, want no-cache", got)
	}
	for _, h := range []string{"Content-Type", "Content-Security-Policy"} {
		if got, want := sw.Header().Get(h), classic.Header().Get(h); got == "" || got != want {
			t.Errorf("GET /sw.js %s = %q, want the classic entry's %q", h, got, want)
		}
	}
	if !bytes.Equal(sw.Body.Bytes(), readFile(t, c.Out, "sw.js")) {
		t.Errorf("GET /sw.js does not answer the built file")
	}
}

func TestBuildRefusesEntryURL(t *testing.T) {
	t.Parallel()
	sw := func(url string) webbundle.Entry {
		return webbundle.Entry{Name: "sw", Path: "src/sw.js", Kind: webbundle.Classic, URL: url}
	}
	cases := []struct {
		name  string
		extra []webbundle.Entry
		want  []string
	}{
		{name: "OnAModule", extra: []webbundle.Entry{{Name: "sw", Path: "src/sw.js", Kind: webbundle.Module, URL: "/sw.js"}}, want: []string{`"sw"`, "/sw.js", "classic"}},
		{name: "OnAStyle", extra: []webbundle.Entry{{Name: "theme", Path: "src/app.css", Kind: webbundle.Style, URL: "/theme.js"}}, want: []string{`"theme"`, "/theme.js", "classic"}},
		{name: "NoLeadingSlash", extra: []webbundle.Entry{sw("sw.js")}, want: []string{`"sw"`, `"sw.js"`}},
		{name: "UnderAssets", extra: []webbundle.Entry{sw("/assets/sw.js")}, want: []string{"/assets/sw.js"}},
		{name: "Nested", extra: []webbundle.Entry{sw("/a/sw.js")}, want: []string{"/a/sw.js"}},
		{name: "NotJS", extra: []webbundle.Entry{sw("/sw.mjs")}, want: []string{"/sw.mjs"}},
		{name: "Stylesheet", extra: []webbundle.Entry{sw("/sw.css")}, want: []string{"/sw.css"}},
		{name: "Shell", extra: []webbundle.Entry{sw("/index.html")}, want: []string{"/index.html"}},
		{name: "DotFile", extra: []webbundle.Entry{sw("/.sw.js")}, want: []string{"/.sw.js"}},
		{name: "NoStem", extra: []webbundle.Entry{sw("/.js")}, want: []string{`"/.js"`}},
		{name: "Root", extra: []webbundle.Entry{sw("/")}, want: []string{`"/"`}},
		{name: "Query", extra: []webbundle.Entry{sw("/sw.js?v=1")}, want: []string{"/sw.js?v=1"}},
		{name: "Fragment", extra: []webbundle.Entry{sw("/sw.js#a")}, want: []string{"/sw.js#a"}},
		{name: "Space", extra: []webbundle.Entry{sw("/s w.js")}, want: []string{"/s w.js"}},
		{name: "PercentEncoded", extra: []webbundle.Entry{sw("/s%20w.js")}, want: []string{"/s%20w.js"}},
		{name: "NotASCII", extra: []webbundle.Entry{sw("/é.js")}, want: []string{"/é.js"}},
		{name: "Backslash", extra: []webbundle.Entry{sw(`/a\sw.js`)}, want: []string{`a\\sw.js`}},
		{name: "DoubleSlash", extra: []webbundle.Entry{sw("//sw.js")}, want: []string{"//sw.js"}},
		{
			name: "TwoURLsDifferingInCase",
			extra: []webbundle.Entry{
				{Name: "worker", Path: "src/sw.js", Kind: webbundle.Classic, URL: "/service.js"},
				{Name: "other", Path: "src/sw.js", Kind: webbundle.Classic, URL: "/Service.js"},
			},
			want: []string{`"other"`, "/Service.js", `the URL of classic entry "worker"`},
		},
		{name: "AnotherEntrysLogicalPath", extra: []webbundle.Entry{sw("/app.js")}, want: []string{`"sw"`, "/app.js", `module entry "app"`}},
		{name: "AnotherEntrysLogicalPathInOtherCase", extra: []webbundle.Entry{sw("/Prepaint.js")}, want: []string{`"sw"`, "/Prepaint.js", `classic entry "prepaint"`}},
		{
			name:  "AnotherEntrysName",
			extra: []webbundle.Entry{sw(""), {Name: "worker", Path: "src/sw.js", Kind: webbundle.Classic, URL: "/sw.js"}},
			want:  []string{`"worker"`, "/sw.js", `classic entry "sw"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{"src/sw.js": "self.addEventListener(\"fetch\", () => {});\n"})
			c.Entries = append(c.Entries, tc.extra...)
			buildRefused(t, c, tc.want...)
			outMissing(t, c.Out)
		})
	}
}

// An entry's URL keeps the rule every classic entry follows: a module
// reads it through BUNDLE_URLS or a shell names it.
func TestBuildRefusesAnEntryAtAURLNothingReads(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{"src/sw.js": "self.addEventListener(\"fetch\", () => {});\n"})
	c.Entries = append(c.Entries, webbundle.Entry{Name: "sw", Path: "src/sw.js", Kind: webbundle.Classic, URL: "/sw.js"})
	buildRefused(t, c, `"sw"`, "BUNDLE_URLS")
}
