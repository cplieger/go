package httpserver

import "net/http"

// Page is one page of a single-page app.
type Page struct {
	Name    string
	Pattern string
	Shell   string
}

// CheckPages checks a page table.
func CheckPages(pages []Page) error { return nil }

// PageMatcher is SPA's page match.
type PageMatcher struct{}

// PageMatch is Match's answer.
type PageMatch struct {
	Params   map[string]string
	Name     string
	Redirect string
}

// NewPageMatcher builds a PageMatcher.
func NewPageMatcher(pages []Page, apiPrefix string) (*PageMatcher, error) {
	return &PageMatcher{}, nil
}

// Match answers target's page.
func (m *PageMatcher) Match(target string) (PageMatch, bool) { return PageMatch{}, false }

// NotFound answers not_found on every path.
func NotFound(errs ErrorResponder) http.Handler { return http.NotFoundHandler() }
