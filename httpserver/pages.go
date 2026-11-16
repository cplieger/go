package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Page is one page of a single-page app: the path pattern it answers and the
// shell it answers with.
type Page struct {
	Name    string // the route's name in the TS router, an ASCII identifier
	Pattern string // a ServeMux path pattern with no method and no host: "/library/{id}"
	Shell   string // the shell file at the bundle's root; "" is "index.html"
}

// DefaultShell is the shell a Page with no Shell answers, and the shell SPA
// answers with 404 for a path that is no page.
const DefaultShell = "index.html"

const (
	maxPages  = 256
	maxShells = 8
)

func (p Page) shell() string {
	if p.Shell == "" {
		return DefaultShell
	}
	return p.Shell
}

// CheckPages refuses a page table SPA cannot serve: more than 256 pages or 8
// shells; a name that is empty, repeated or not an ASCII identifier; a pattern
// that is not a valid ServeMux pattern, names a method or a host, or conflicts
// with another as ServeMux judges conflicts (two patterns that each match a
// path the other does, neither more specific, a repeated pattern included);
// and a shell that is not an .html file name at the root.
func CheckPages(pages []Page) error {
	if len(pages) > maxPages {
		return fmt.Errorf("httpserver: %d pages, more than %d", len(pages), maxPages)
	}
	names := make(map[string]bool, len(pages))
	shells := make(map[string]bool)
	for _, p := range pages {
		if !asciiIdentifier(p.Name) {
			return fmt.Errorf("httpserver: page name %q is not an ASCII identifier", p.Name)
		}
		if names[p.Name] {
			return fmt.Errorf("httpserver: page name %q is used twice", p.Name)
		}
		names[p.Name] = true
		if err := checkShellName(p.shell()); err != nil {
			return fmt.Errorf("httpserver: page %q: %w", p.Name, err)
		}
		shells[p.shell()] = true
		if err := checkPattern(p.Pattern); err != nil {
			return fmt.Errorf("httpserver: page %q: %w", p.Name, err)
		}
	}
	if len(shells) > maxShells {
		return fmt.Errorf("httpserver: %d shells, more than %d", len(shells), maxShells)
	}
	mux := http.NewServeMux()
	for i, p := range pages {
		if register(mux, p.Pattern) != nil {
			return conflictError(pages[:i], p)
		}
	}
	return nil
}

func checkShellName(s string) error {
	if strings.Contains(s, "/") || strings.HasPrefix(s, ".") || path.Ext(s) != htmlExt || len(s) == len(htmlExt) {
		return fmt.Errorf("shell %q is not an .html file name at the bundle's root", s)
	}
	return nil
}

func checkPattern(pattern string) error {
	if !strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("pattern %q is a path with no method and no host", pattern)
	}
	if err := register(http.NewServeMux(), pattern); err != nil {
		return fmt.Errorf("pattern %q: %w", pattern, err)
	}
	return nil
}

// register adds pattern to mux, turning ServeMux's registration panic into an
// error.
func register(mux *http.ServeMux, pattern string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	mux.Handle(pattern, http.NotFoundHandler())
	return nil
}

// conflictError names the earlier page p conflicts with, found by pairing p
// with each in turn on a fresh mux.
func conflictError(earlier []Page, p Page) error {
	for _, e := range earlier {
		mux := http.NewServeMux()
		_ = register(mux, e.Pattern)
		if register(mux, p.Pattern) != nil {
			return fmt.Errorf("httpserver: page %q pattern %q conflicts with page %q pattern %q: both match some path and neither is more specific", p.Name, p.Pattern, e.Name, e.Pattern)
		}
	}
	return fmt.Errorf("httpserver: page %q pattern %q conflicts with an earlier page", p.Name, p.Pattern)
}

func asciiIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// PageMatcher is SPA's page match. It registers each pattern on an
// http.ServeMux and serves the request path through it, so the match, the
// path cleaning and the trailing-slash redirect are ServeMux's own.
type PageMatcher struct {
	mux       *http.ServeMux
	apiPrefix string
}

// PageMatch is one Match answer: a page, or a redirect.
type PageMatch struct {
	Params   map[string]string // each wildcard's value as Request.PathValue gives it
	Name     string            // the page's Name; "" with a Redirect
	Redirect string            // the path and query ServeMux redirects to; "" on a match
}

// NewPageMatcher builds the matcher for pages, refusing what CheckPages
// refuses, an apiPrefix that is not a clean path of literal segments with no
// trailing slash ("/api"), and a page whose pattern's literal part is at or
// under apiPrefix.
func NewPageMatcher(pages []Page, apiPrefix string) (*PageMatcher, error) {
	if err := checkAPIPrefix(apiPrefix); err != nil {
		return nil, err
	}
	if err := CheckPages(pages); err != nil {
		return nil, err
	}
	m := &PageMatcher{mux: http.NewServeMux(), apiPrefix: apiPrefix}
	for _, p := range pages {
		if underPrefix(patternLiteral(p.Pattern), apiPrefix) {
			return nil, fmt.Errorf("httpserver: page %q pattern %q is at or under the API prefix %q", p.Name, p.Pattern, apiPrefix)
		}
		m.mux.Handle(p.Pattern, pageHandler(p))
	}
	return m, nil
}

func checkAPIPrefix(p string) error {
	if p == "" || p == "/" || !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") ||
		path.Clean(p) != p || strings.ContainsAny(p, "{}?#%") {
		return fmt.Errorf("httpserver: API prefix %q is not a clean path such as \"/api\"", p)
	}
	return nil
}

// patternLiteral is a pattern's path up to its first wildcard, unescaped as
// ServeMux reads a pattern's literal segments.
func patternLiteral(pattern string) string {
	lit, _, _ := strings.Cut(pattern, "{")
	if u, err := url.PathUnescape(lit); err == nil {
		return u
	}
	return lit
}

func underPrefix(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// matchWriter carries a Match through ServeMux: the page handler fills match,
// and a redirect writes its status and Location.
type matchWriter struct {
	header http.Header
	match  *PageMatch
	status int
}

func (w *matchWriter) Header() http.Header         { return w.header }
func (w *matchWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *matchWriter) WriteHeader(status int)      { w.status = status }

func pageHandler(p Page) http.Handler {
	var wildcards []string
	for seg := range strings.SplitSeq(p.Pattern, "/") {
		if name, ok := strings.CutPrefix(seg, "{"); ok && seg != "{$}" {
			name = strings.TrimSuffix(strings.TrimSuffix(name, "}"), "...")
			wildcards = append(wildcards, name)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw, ok := w.(*matchWriter)
		if !ok {
			return
		}
		params := make(map[string]string, len(wildcards))
		for _, name := range wildcards {
			params[name] = r.PathValue(name)
		}
		mw.match = &PageMatch{Name: p.Name, Params: params}
	})
}

// Match answers the page target names, or the redirect ServeMux answers it
// with. target is the escaped path and query as the client sent them. ok is
// false for a target that does not parse, names no page, redirects to no
// page, or whose path, decoded or cleaned, is at or under the API prefix.
func (m *PageMatcher) Match(target string) (PageMatch, bool) {
	u, err := url.ParseRequestURI(target)
	if err != nil || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return PageMatch{}, false
	}
	if underPrefix(u.Path, m.apiPrefix) || underPrefix(path.Clean(u.Path), m.apiPrefix) {
		return PageMatch{}, false
	}
	r := &http.Request{Method: http.MethodGet, URL: u, Header: http.Header{}, RequestURI: target}
	if _, pattern := m.mux.Handler(r); pattern == "" {
		return PageMatch{}, false
	}
	w := &matchWriter{header: http.Header{}}
	m.mux.ServeHTTP(w, r)
	switch {
	case w.match != nil:
		return *w.match, true
	case w.status >= 300 && w.status < 400 && w.header.Get("Location") != "":
		return PageMatch{Redirect: w.header.Get("Location")}, true
	}
	return PageMatch{}, false
}

// NotFound answers every request with not_found (404) through errs, nil being
// WriteError: the root of a build with no web UI.
func NotFound(errs ErrorResponder) http.Handler {
	if errs == nil {
		errs = WriteError
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errs(w, r, http.StatusNotFound, "not_found", "not found")
	})
}
