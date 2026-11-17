package httpserver_test

import (
	"bytes"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

// The class sentences and the closing lines docs/spa.md publishes for the
// page, with "seadex-scout" as the app.
var outageSentences = map[httpserver.OutageClass]string{
	httpserver.OutageDamaged:    "The file is damaged.",
	httpserver.OutageFormat:     "The file was written by another version of seadex-scout, or is not seadex-scout's file.",
	httpserver.OutageNewer:      "The file was written by a newer version of seadex-scout.",
	httpserver.OutageUnreadable: "The file cannot be read.",
	httpserver.OutageKey:        "The key file is missing or does not match this accounts file.",
}

const (
	reloadLine  = "Reload this page once the command finishes."
	restartLine = "Run the command above or restart the container, then reload this page."
	outageTitle = "seadex-scout cannot start"
	accountsRow = "ACCOUNTS_PATH, /config/accounts.db"
	restoreCmd  = "docker exec seadex-scout seadex-scout accounts restore <archive>"
	resetCmd    = "docker exec seadex-scout seadex-scout accounts reset"
)

func outageConfig(t *testing.T) httpserver.OutageConfig {
	t.Helper()
	return httpserver.OutageConfig{
		Assets:    bundleTree(t, spaFiles(), spaListed, spaEntries),
		Style:     "app.css",
		Title:     outageTitle,
		App:       "seadex-scout",
		Files:     []httpserver.OutageFile{{Name: accountsRow, Class: httpserver.OutageKey}},
		Commands:  []string{restoreCmd, resetCmd},
		APIPrefix: "/api",
	}
}

func newOutage(t *testing.T, c httpserver.OutageConfig) http.Handler {
	t.Helper()
	h, err := httpserver.OutagePage(c)
	if err != nil {
		t.Fatalf("OutagePage: %v", err)
	}
	if h == nil {
		t.Fatal("OutagePage returned a nil handler and no error")
	}
	return h
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

// pageText is every text run between tags in body, unescaped and trimmed:
// what a reader of the page sees, the <title> first.
func pageText(body string) []string {
	var out []string
	for _, run := range tagPattern.Split(body, -1) {
		if s := strings.TrimSpace(html.UnescapeString(run)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func wantOutagePage(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	res := rec.Result()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if et := res.Header.Get("ETag"); et != "" {
		t.Errorf("ETag = %q, want none on a no-store page", et)
	}
}

func TestOutagePageAnswers503NoStore(t *testing.T) {
	h := newOutage(t, outageConfig(t))
	page := spaServe(h, http.MethodGet, "/").Body.String()
	if !strings.Contains(page, "<h1>"+outageTitle+"</h1>") {
		t.Fatalf("GET / body %q is not the page", page)
	}
	for _, target := range []string{"/", "/library", "/library/12?tab=files", "/assets/missing.js", "/index.html", "/favicon.ico", "/apis"} {
		t.Run("GET"+pathName(target), func(t *testing.T) {
			rec := spaServe(h, http.MethodGet, target)
			wantOutagePage(t, rec)
			if rec.Body.String() != page {
				t.Errorf("body = %q, want the page", rec.Body.String())
			}
			wantPolicy(t, rec, defaultPolicy)
		})
	}
	t.Run("HEAD answers the headers and length only", func(t *testing.T) {
		rec := spaServe(h, http.MethodHead, "/")
		wantOutagePage(t, rec)
		wantPolicy(t, rec, defaultPolicy)
		if rec.Body.Len() != 0 {
			t.Errorf("HEAD body = %q, want empty", rec.Body.String())
		}
		if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(page)); got != want {
			t.Errorf("Content-Length = %q, want %q", got, want)
		}
	})
}

func TestOutagePageLeavesAPIPrefix(t *testing.T) {
	h := newOutage(t, outageConfig(t))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, target := range []string{"/api", "/api/", "/api/library", "/ap%69/x", "/api/../api/x"} {
			t.Run(method+pathName(target), func(t *testing.T) {
				wantRefusal(t, spaServe(h, method, target), http.StatusNotFound, "not_found")
			})
		}
	}
}

func TestOutagePageRefusesOtherMethods(t *testing.T) {
	h := newOutage(t, outageConfig(t))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			rec := spaServe(h, method, "/library")
			wantRefusal(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q, want GET, HEAD", got)
			}
		})
	}
}

func TestOutagePageErrorsResponderAnswersRefusals(t *testing.T) {
	var codes []httpserver.ErrorCode
	c := outageConfig(t)
	c.Errors = func(w http.ResponseWriter, _ *http.Request, status int, code httpserver.ErrorCode, _ string) {
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: the policy is not set before the responder runs", code)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control = %q before the responder runs, want no-store", code, cc)
		}
		codes = append(codes, code)
		w.WriteHeader(status)
	}
	h := newOutage(t, c)
	spaServe(h, http.MethodGet, "/api/x")
	spaServe(h, http.MethodPost, "/")
	if want := []httpserver.ErrorCode{"not_found", "method_not_allowed"}; !slices.Equal(codes, want) {
		t.Errorf("responder codes = %v, want %v", codes, want)
	}
}

func TestOutagePageWritesEachClassSentence(t *testing.T) {
	for class, sentence := range outageSentences {
		t.Run(string(class), func(t *testing.T) {
			c := outageConfig(t)
			c.Files = []httpserver.OutageFile{{Name: accountsRow, Class: class}}
			text := pageText(spaServe(newOutage(t, c), http.MethodGet, "/").Body.String())
			i := slices.Index(text, accountsRow)
			if i < 0 || i+1 >= len(text) || text[i+1] != sentence {
				t.Errorf("page text %q does not write %q then %q", text, accountsRow, sentence)
			}
		})
	}
	t.Run("every failed file in order", func(t *testing.T) {
		c := outageConfig(t)
		c.Files = []httpserver.OutageFile{
			{Name: accountsRow, Class: httpserver.OutageKey},
			{Name: "SETTINGS_PATH, /config/settings.db", Class: httpserver.OutageNewer},
		}
		text := strings.Join(pageText(spaServe(newOutage(t, c), http.MethodGet, "/").Body.String()), "\n")
		want := strings.Join([]string{
			accountsRow, outageSentences[httpserver.OutageKey],
			"SETTINGS_PATH, /config/settings.db", outageSentences[httpserver.OutageNewer],
		}, "\n")
		if !strings.Contains(text, want) {
			t.Errorf("page text %q does not hold %q", text, want)
		}
	})
}

// The page's text is exactly the configured strings and the fixed lines, so
// no other text, an error's included, can reach it.
func TestOutagePageWritesNoErrorText(t *testing.T) {
	t.Run("the text is the configured strings and fixed lines only", func(t *testing.T) {
		c := outageConfig(t)
		c.Files = nil
		var want []string
		want = append(want, outageTitle, outageTitle)
		for _, class := range []httpserver.OutageClass{httpserver.OutageDamaged, httpserver.OutageFormat, httpserver.OutageNewer, httpserver.OutageUnreadable, httpserver.OutageKey} {
			name := "FILE_" + strings.ToUpper(string(class)) + ", /config/" + string(class) + ".db"
			c.Files = append(c.Files, httpserver.OutageFile{Name: name, Class: class})
			want = append(want, name, outageSentences[class])
		}
		want = append(want, restoreCmd, resetCmd, reloadLine)
		got := pageText(spaServe(newOutage(t, c), http.MethodGet, "/").Body.String())
		if !slices.Equal(got, want) {
			t.Errorf("page text = %q, want %q", got, want)
		}
	})
	t.Run("an error string as a class is refused", func(t *testing.T) {
		c := outageConfig(t)
		c.Files = []httpserver.OutageFile{{Name: accountsRow, Class: "open /config/accounts.db: key id 3f2a does not match"}}
		if h, err := httpserver.OutagePage(c); err == nil || h != nil {
			t.Errorf("OutagePage = %v, %v; want a nil handler and an error", h, err)
		}
	})
}

func TestOutagePageRefusesUnknownClass(t *testing.T) {
	for _, class := range []httpserver.OutageClass{"", "Damaged", "corrupt", "locked", " key"} {
		t.Run(strconv.Quote(string(class)), func(t *testing.T) {
			c := outageConfig(t)
			c.Files = []httpserver.OutageFile{{Name: "DATA_PATH, /config/data.db", Class: httpserver.OutageDamaged}, {Name: accountsRow, Class: class}}
			h, err := httpserver.OutagePage(c)
			if err == nil || h != nil {
				t.Fatalf("OutagePage = %v, %v; want a nil handler and an error", h, err)
			}
			if !strings.Contains(err.Error(), accountsRow) {
				t.Errorf("error %q does not name the file %q", err, accountsRow)
			}
		})
	}
}

func TestOutagePageRefusesBadConfig(t *testing.T) {
	for name, edit := range map[string]func(*httpserver.OutageConfig){
		"no title":              func(c *httpserver.OutageConfig) { c.Title = "" },
		"no app":                func(c *httpserver.OutageConfig) { c.App = "" },
		"no API prefix":         func(c *httpserver.OutageConfig) { c.APIPrefix = "" },
		"an unclean API prefix": func(c *httpserver.OutageConfig) { c.APIPrefix = "/api/" },
		"a style the bundle does not build": func(c *httpserver.OutageConfig) {
			c.Style = "admin.css"
		},
		"a style that is a script entry": func(c *httpserver.OutageConfig) {
			c.Style = "worker.css"
		},
		"a broken bundle": func(c *httpserver.OutageConfig) {
			c.Assets = bundleTree(t, spaFiles(), append(slices.Clone(spaListed), "missing.html"), spaEntries)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := outageConfig(t)
			edit(&c)
			if h, err := httpserver.OutagePage(c); err == nil || h != nil {
				t.Errorf("OutagePage = %v, %v; want a nil handler and an error", h, err)
			}
		})
	}
}

func TestOutagePageWritesCommands(t *testing.T) {
	rec := spaServe(newOutage(t, outageConfig(t)), http.MethodGet, "/")
	text := pageText(rec.Body.String())
	for _, cmd := range []string{restoreCmd, resetCmd} {
		if !slices.Contains(text, cmd) {
			t.Errorf("page text %q does not hold the command %q", text, cmd)
		}
	}
	if !strings.Contains(rec.Body.String(), "&lt;archive&gt;") {
		t.Errorf("body %q does not escape the command's placeholder", rec.Body.String())
	}
}

func TestOutagePageClosingLine(t *testing.T) {
	t.Run("reload by default", func(t *testing.T) {
		text := pageText(spaServe(newOutage(t, outageConfig(t)), http.MethodGet, "/").Body.String())
		if text[len(text)-1] != reloadLine || slices.Contains(text, restartLine) {
			t.Errorf("page text %q, want it to end %q with no restart line", text, reloadLine)
		}
	})
	t.Run("restart when Restart is set", func(t *testing.T) {
		c := outageConfig(t)
		c.Restart = true
		c.Commands = []string{"docker exec seadex-scout seadex-scout import resolve"}
		text := pageText(spaServe(newOutage(t, c), http.MethodGet, "/").Body.String())
		if text[len(text)-1] != restartLine || slices.Contains(text, reloadLine) {
			t.Errorf("page text %q, want it to end %q with no reload line", text, restartLine)
		}
	})
}

func TestOutagePageCleansNameAndCommands(t *testing.T) {
	c := outageConfig(t)
	c.Files = []httpserver.OutageFile{{Name: "ACCOUNTS_PATH, /config/\u202eaccounts.db\nforged line", Class: httpserver.OutageKey}}
	c.Commands = []string{"docker exec seadex-scout\r\nseadex-scout accounts reset\x1b[2J"}
	body := spaServe(newOutage(t, c), http.MethodGet, "/").Body.String()
	for _, bad := range []string{"\u202e", "\nforged", "\r", "\x1b"} {
		if strings.Contains(body, bad) {
			t.Errorf("body %q holds %q", body, bad)
		}
	}
	text := pageText(body)
	if !slices.Contains(text, "ACCOUNTS_PATH, /config/ accounts.db forged line") {
		t.Errorf("page text %q does not hold the cleaned name", text)
	}
	if !slices.Contains(text, "docker exec seadex-scout  seadex-scout accounts reset [2J") {
		t.Errorf("page text %q does not hold the cleaned command", text)
	}
}

func TestOutagePageLinksStyleEntry(t *testing.T) {
	link := `<link rel="stylesheet" href="/` + styleAsset + `">`
	t.Run("the style entry's hashed path", func(t *testing.T) {
		if body := spaServe(newOutage(t, outageConfig(t)), http.MethodGet, "/").Body.String(); !strings.Contains(body, link) {
			t.Errorf("body %q does not link %s", body, link)
		}
	})
	for name, edit := range map[string]func(*httpserver.OutageConfig){
		"no style":       func(c *httpserver.OutageConfig) { c.Style = "" },
		"an unbuilt app": func(c *httpserver.OutageConfig) { c.Assets = nil },
	} {
		t.Run(name+" is unstyled", func(t *testing.T) {
			c := outageConfig(t)
			edit(&c)
			if body := spaServe(newOutage(t, c), http.MethodGet, "/").Body.String(); strings.Contains(body, "<link") {
				t.Errorf("body %q links a stylesheet", body)
			}
		})
	}
}

// The classes the page writes, each one @cplieger/ui's system.css ships.
var outageClasses = []string{"cp", "cp-gate", "cp-mono", "cp-panel", "cp-stack"}

var classAttr = regexp.MustCompile(`\sclass="([^"]*)"`)

func TestOutagePageHasNoInlineScriptOrStyle(t *testing.T) {
	c := outageConfig(t)
	c.Files = append(c.Files, httpserver.OutageFile{Name: "DATA_PATH, /config/data.db", Class: httpserver.OutageDamaged})
	for name, cfg := range map[string]httpserver.OutageConfig{"styled": c, "unstyled": func() httpserver.OutageConfig { u := c; u.Assets = nil; return u }()} {
		t.Run(name, func(t *testing.T) {
			body := spaServe(newOutage(t, cfg), http.MethodGet, "/").Body.String()
			if !strings.Contains(body, outageTitle) || !strings.Contains(body, "class=") {
				t.Fatalf("body %q is not the page", body)
			}
			if err := httpserver.CheckShell([]byte(body)); err != nil {
				t.Errorf("CheckShell: %v", err)
			}
			if strings.Contains(strings.ToLower(body), "<script") {
				t.Errorf("body %q holds a script element", body)
			}
			for _, m := range classAttr.FindAllStringSubmatch(body, -1) {
				for class := range strings.FieldsSeq(m[1]) {
					if !slices.Contains(outageClasses, class) {
						t.Errorf("class %q is not a system class the page may write", class)
					}
				}
			}
		})
	}
}

func TestOutagePageServesListedAssets(t *testing.T) {
	h := newOutage(t, outageConfig(t))
	files := spaFiles()
	t.Run("the stylesheet and a font answer their bytes", func(t *testing.T) {
		for name, ct := range map[string]string{styleAsset: "text/css; charset=utf-8", fontAsset: "font/woff2"} {
			rec := spaServe(h, http.MethodGet, "/"+name)
			if rec.Code != http.StatusOK || rec.Body.String() != files[name] {
				t.Errorf("GET /%s = %d %q, want 200 and the file", name, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != ct {
				t.Errorf("GET /%s Content-Type = %q, want %q", name, got, ct)
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Errorf("GET /%s Cache-Control = %q, want immutable", name, got)
			}
			if got, want := rec.Header().Get("ETag"), `"`+sha256Hex(files[name])+`"`; got != want {
				t.Errorf("GET /%s ETag = %q, want the manifest hash %q", name, got, want)
			}
			wantPolicy(t, rec, defaultPolicy)
		}
	})
	t.Run("every listed asset answers as SPA answers it", func(t *testing.T) {
		spa := newSPA(t, spaConfig(t, nil))
		for _, name := range []string{moduleAsset, classicAsset, styleAsset, chunkAsset, fontAsset} {
			for _, header := range [][]string{nil, {"Accept-Encoding", "gzip"}, {"If-None-Match", `"` + sha256Hex(files[name]) + `"`}} {
				want := spaServe(spa, http.MethodGet, "/"+name, header...)
				got := spaServe(h, http.MethodGet, "/"+name, header...)
				if got.Code != want.Code || !bytes.Equal(got.Body.Bytes(), want.Body.Bytes()) {
					t.Errorf("GET /%s %q = %d (%d bytes), SPA answers %d (%d bytes)", name, header, got.Code, got.Body.Len(), want.Code, want.Body.Len())
				}
				for _, k := range []string{"Content-Type", "Cache-Control", "ETag", "Vary", "Content-Encoding", "Content-Security-Policy"} {
					if g, w := got.Header().Values(k), want.Header().Values(k); !slices.Equal(g, w) {
						t.Errorf("GET /%s %q %s = %q, SPA answers %q", name, header, k, g, w)
					}
				}
			}
		}
	})
	t.Run("an unlisted path answers the page", func(t *testing.T) {
		page := spaServe(h, http.MethodGet, "/").Body.String()
		for _, target := range []string{"/assets/missing.css", "/" + moduleAsset + ".map", "/assets/.keep", "/stray.js", "/" + noticeIndex, "/" + httpserver.BundlePath, "/index.html"} {
			rec := spaServe(h, http.MethodGet, target)
			wantOutagePage(t, rec)
			if rec.Body.String() != page {
				t.Errorf("GET %s body = %q, want the page", target, rec.Body.String())
			}
		}
	})
}
