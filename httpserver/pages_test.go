package httpserver_test

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

var testPages = []httpserver.Page{
	{Name: "library", Pattern: "/library"},
	{Name: "entry", Pattern: "/library/{id}"},
	{Name: "settings", Pattern: "/settings/{part...}"},
	{Name: "docs", Pattern: "/docs/"},
	{Name: "files", Pattern: "/file/{part...}"},
	{Name: "home", Pattern: "/{$}"},
	{Name: "login", Pattern: "/login", Shell: "login.html"},
}

func TestCheckPagesRefusesConflict(t *testing.T) {
	err := httpserver.CheckPages([]httpserver.Page{
		{Name: "a", Pattern: "/a/{x}"},
		{Name: "b", Pattern: "/{y}/b"},
	})
	if err == nil {
		t.Fatal("CheckPages accepted /a/{x} beside /{y}/b")
	}
	for _, want := range []string{"/a/{x}", "/{y}/b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if err := httpserver.CheckPages(testPages); err != nil {
		t.Errorf("CheckPages refused the test table: %v", err)
	}
}

func TestCheckPagesRefusesBadTable(t *testing.T) {
	many := func(n, shells int) []httpserver.Page {
		var out []httpserver.Page
		for i := range n {
			out = append(out, httpserver.Page{Name: fmt.Sprintf("p%d", i), Pattern: fmt.Sprintf("/p%d", i), Shell: fmt.Sprintf("s%d.html", i%shells)})
		}
		return out
	}
	cases := map[string][]httpserver.Page{
		"duplicate pattern":   {{Name: "a", Pattern: "/a"}, {Name: "b", Pattern: "/a"}},
		"invalid pattern":     {{Name: "a", Pattern: "/a/{"}},
		"empty pattern":       {{Name: "a", Pattern: ""}},
		"method in pattern":   {{Name: "a", Pattern: "GET /a"}},
		"host in pattern":     {{Name: "a", Pattern: "example.com/a"}},
		"empty name":          {{Name: "", Pattern: "/a"}},
		"name not identifier": {{Name: "a-b", Pattern: "/a"}},
		"name with digit":     {{Name: "1a", Pattern: "/a"}},
		"duplicate name":      {{Name: "a", Pattern: "/a"}, {Name: "a", Pattern: "/b"}},
		"shell in a folder":   {{Name: "a", Pattern: "/a", Shell: "sub/a.html"}},
		"shell a dot-file":    {{Name: "a", Pattern: "/a", Shell: ".a.html"}},
		"shell not html":      {{Name: "a", Pattern: "/a", Shell: "a.htm"}},
		"257 pages":           many(257, 1),
		"9 shells":            many(9, 9),
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckPages(pages); err == nil {
				t.Error("CheckPages accepted it")
			}
		})
	}
	t.Run("256 pages and 8 shells pass", func(t *testing.T) {
		if err := httpserver.CheckPages(many(256, 8)); err != nil {
			t.Error(err)
		}
	})
}

func TestSPARefusesPageUnderAPIPrefix(t *testing.T) {
	for _, pattern := range []string{"/api", "/api/", "/api/x", "/api/{x}", "/api/{rest...}"} {
		t.Run(pattern, func(t *testing.T) {
			pages := []httpserver.Page{{Name: "a", Pattern: pattern}}
			if _, err := httpserver.NewPageMatcher(pages, "/api"); err == nil {
				t.Error("NewPageMatcher accepted a page at or under /api")
			}
			c := spaConfig(t, nil)
			c.Pages = pages
			if _, err := httpserver.SPA(c); err == nil {
				t.Error("SPA accepted a page at or under /api")
			}
		})
	}
	for _, prefix := range []string{"", "/", "api", "/api/", "/a/../api", "/api/{x}"} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			if _, err := httpserver.NewPageMatcher(testPages, prefix); err == nil {
				t.Errorf("NewPageMatcher accepted APIPrefix %q", prefix)
			}
			c := spaConfig(t, nil)
			c.APIPrefix = prefix
			if _, err := httpserver.SPA(c); err == nil {
				t.Errorf("SPA accepted APIPrefix %q", prefix)
			}
		})
	}
	t.Run("a sibling of the prefix passes", func(t *testing.T) {
		if _, err := httpserver.NewPageMatcher([]httpserver.Page{{Name: "a", Pattern: "/apis"}}, "/api"); err != nil {
			t.Error(err)
		}
	})
}

func TestPageMatcherMatchesServeMux(t *testing.T) {
	m, err := httpserver.NewPageMatcher(testPages, "/api")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		target string
		name   string
		params map[string]string
	}{
		{"/library", "library", map[string]string{}},
		{"/library?q=1", "library", map[string]string{}},
		{"/library/a%2Fb", "entry", map[string]string{"id": "a/b"}},
		{"/library/%C3%A9", "entry", map[string]string{"id": "é"}},
		{"/library/a%20b", "entry", map[string]string{"id": "a b"}},
		{"/library/%2e%2e", "entry", map[string]string{"id": ".."}},
		{"/library/%2E%2e", "entry", map[string]string{"id": ".."}},
		{"/library/.%2e", "entry", map[string]string{"id": ".."}},
		{"/settings/a%2Fb/c", "settings", map[string]string{"part": "a/b/c"}},
		{"/settings/", "settings", map[string]string{"part": ""}},
		{"/docs/", "docs", map[string]string{}},
		{"/docs/deep/page", "docs", map[string]string{}},
		{"/", "home", map[string]string{}},
		{"/login", "login", map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.target, "/", "|"), func(t *testing.T) {
			got, ok := m.Match(tc.target)
			if !ok || got.Name != tc.name || got.Redirect != "" || !maps.Equal(got.Params, tc.params) {
				t.Errorf("Match = %+v, %v; want page %q with %v", got, ok, tc.name, tc.params)
			}
		})
	}
	for _, target := range []string{"/library/", "/nope", "/login/x", "/api", "/api/x", "/api/library"} {
		t.Run("no page "+strings.ReplaceAll(target, "/", "|"), func(t *testing.T) {
			if got, ok := m.Match(target); ok {
				t.Errorf("Match = %+v, want no page", got)
			}
		})
	}
	t.Run("a wildcard page never matches under the prefix", func(t *testing.T) {
		m, err := httpserver.NewPageMatcher([]httpserver.Page{{Name: "slug", Pattern: "/{slug}"}}, "/api")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := m.Match("/x"); !ok {
			t.Error("/x is no page")
		}
		if got, ok := m.Match("/api"); ok {
			t.Errorf("Match(/api) = %+v, want no page", got)
		}
	})
}

func TestPageMatcherRedirects(t *testing.T) {
	m, err := httpserver.NewPageMatcher(testPages, "/api")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/docs":                "/docs/",
		"/docs?q=1":            "/docs/?q=1",
		"/settings":            "/settings/",
		"/library/x/../a?q=1":  "/library/a?q=1",
		"//library?q=1":        "/library?q=1",
		"/library/./a":         "/library/a",
		"/library//a":          "/library/a",
		"/nope/../library?q=1": "/library?q=1",
	}
	for target, want := range cases {
		t.Run(strings.ReplaceAll(target, "/", "|"), func(t *testing.T) {
			got, ok := m.Match(target)
			if !ok || got.Redirect != want || got.Name != "" {
				t.Errorf("Match = %+v, %v; want a redirect to %q", got, ok, want)
			}
		})
	}
	for _, target := range []string{"/nope/../x", "//nope", "/a/./b", "/x/../api/y"} {
		t.Run("to no page "+strings.ReplaceAll(target, "/", "|"), func(t *testing.T) {
			if got, ok := m.Match(target); ok {
				t.Errorf("Match = %+v, want no page", got)
			}
		})
	}
}
