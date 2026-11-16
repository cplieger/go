package httpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// SPAConfig configures SPA.
type SPAConfig struct {
	Assets    fs.FS               // the bundle; nil, or no BundlePath in it, is unbuilt
	CSPAdd    map[string][]string // additions to the default policy; SPAPolicy says which are allowed
	Errors    ErrorResponder      // answers every refusal; nil is WriteError
	Logger    *slog.Logger        // nil is slog.Default()
	APIPrefix string              // the API's path prefix ("/api"); SPA answers nothing at or under it
	Pages     []Page              // the page patterns, each answering its shell
}

const immutableCache = "public, max-age=31536000, immutable"

// SPA serves a single-page app's bundle (BundlePath) for GET and HEAD. A page
// path answers its shell, a listed asset answers through StaticHandler
// (immutable; a BundleClassic entry no-cache), a notice answers text/plain,
// /favicon.ico 204, and any other path the default shell with 404; a path at
// or under assets/, licenses/ or APIPrefix, a dot segment and another method
// answer a refusal through Errors. Every answer carries SPAPolicy's policy and
// every no-cache ETag binds it; unbuilt, a page answers ui_unbuilt. The error
// names the bundle, shell, CSPAdd or page it refuses; docs/spa.md has each rule.
func SPA(c SPAConfig) (http.Handler, error) { //nolint:gocritic // hugeParam: read once at construction, and a value keeps the caller's config unshared
	policy, err := SPAPolicy(c.CSPAdd)
	if err != nil {
		return nil, err
	}
	pages, err := NewPageMatcher(c.Pages, c.APIPrefix)
	if err != nil {
		return nil, err
	}
	s := &spaServer{policy: policy, pages: pages, apiPrefix: c.APIPrefix, errs: c.Errors}
	if s.errs == nil {
		s.errs = WriteError
	}
	b, found, err := ReadBundle(c.Assets)
	if err != nil {
		return nil, err
	}
	if !found {
		logger := c.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("httpserver: the web UI is not built, so every page answers ui_unbuilt; build it with go generate ./...", "manifest", BundlePath)
		return s, nil
	}
	if err := s.load(c.Assets, b, c.Pages); err != nil {
		return nil, err
	}
	return s, nil
}

type spaServer struct {
	errs      ErrorResponder
	pages     *PageMatcher
	shells    map[string]*spaFile // by page name
	notices   map[string]*spaFile // by tree path
	notFound  *spaFile
	static    http.Handler
	served    map[string]bool // tree paths answered through static
	refused   map[string]bool // root paths that are files, never paths
	policy    string
	apiPrefix string
	built     bool
}

// spaFile is a file SPA writes itself from memory: a shell or a notice.
type spaFile struct {
	contentType string
	body        []byte
	etag        string
	gz          gzAsset
}

func readSPAFile(fsys fs.FS, name, contentType, etag string) (*spaFile, error) {
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	f := &spaFile{contentType: contentType, body: body, etag: etag}
	if gz, ok := gzipAsset(body, name); ok {
		gz.contentType = contentType
		f.gz = gz
	}
	return f, nil
}

func (s *spaServer) load(fsys fs.FS, b Bundle, pages []Page) error {
	byPath := make(map[string]*spaFile)
	shellFor := func(name string) (*spaFile, error) {
		if sh, ok := byPath[name]; ok {
			return sh, nil
		}
		hash, ok := b.Files[name]
		if !ok {
			return nil, fmt.Errorf("httpserver: %s does not list the shell %q", BundlePath, name)
		}
		sh, err := readSPAFile(fsys, name, contentTypes[htmlExt], policyETag(hash, s.policy))
		if err != nil {
			return nil, err
		}
		if err := checkShell(sh.body); err != nil {
			return nil, fmt.Errorf("httpserver: shell %q %w", name, err)
		}
		byPath[name] = sh
		return sh, nil
	}
	var err error
	if s.notFound, err = shellFor(DefaultShell); err != nil {
		return err
	}
	s.shells = make(map[string]*spaFile, len(pages))
	for _, p := range pages {
		if s.shells[p.Name], err = shellFor(p.shell()); err != nil {
			return err
		}
	}
	if err := s.loadFiles(fsys, b); err != nil {
		return err
	}
	s.built = true
	return nil
}

// loadFiles keeps the notices in memory and builds the StaticHandler for the
// listed assets, every other listed root file being refused as a path.
func (s *spaServer) loadFiles(fsys fs.FS, b Bundle) error {
	classic := make(map[string]bool)
	for _, e := range b.Entries {
		if e.Kind == BundleClassic {
			classic[e.Path] = true
		}
	}
	tags := make(map[string]string)
	s.served = make(map[string]bool)
	s.refused = map[string]bool{BundlePath: true}
	s.notices = make(map[string]*spaFile)
	for name, hash := range b.Files {
		switch {
		case !strings.Contains(name, "/"):
			s.refused[name] = true
		case strings.HasPrefix(name, "licenses/"):
			if err := s.loadNotice(fsys, name, hash); err != nil {
				return err
			}
		case contentTypes[path.Ext(name)] == "":
		case classic[name]:
			s.served[name], tags[name] = true, policyETag(hash, s.policy)
		default:
			s.served[name], tags[name] = true, `"`+hash+`"`
		}
	}
	var err error
	s.static, err = StaticHandler(fsys, WithStaticETags(tags), WithStaticCacheControl(func(name string) string {
		if classic[name] {
			return "no-cache"
		}
		return immutableCache
	}))
	return err
}

func (s *spaServer) loadNotice(fsys fs.FS, name, hash string) error {
	f, err := readSPAFile(fsys, name, contentTypes[".txt"], policyETag(hash, s.policy))
	s.notices[name] = f
	return err
}

// checkShell refuses what the policy would block or what needs a hash.
func checkShell(body []byte) error {
	switch {
	case len(InlineScriptHashes(body)) > 0:
		return errors.New("holds an inline script element")
	case len(InlineStyleHashes(body)) > 0:
		return errors.New("holds an inline style element")
	case hasStyleAttr(body):
		return errors.New("holds a style attribute")
	}
	return nil
}

func (s *spaServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", s.policy)
	p := r.URL.Path
	if underPrefix(p, s.apiPrefix) || underPrefix(path.Clean(p), s.apiPrefix) {
		s.refuse(w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.refuse(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	target := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	if m, ok := s.pages.Match(target); ok {
		switch {
		case m.Redirect != "":
			w.Header().Set("Location", m.Redirect)
			w.WriteHeader(http.StatusTemporaryRedirect)
		case !s.built:
			s.refuse(w, r, http.StatusServiceUnavailable, "ui_unbuilt", "the web UI is not built")
		default:
			s.shells[m.Name].serve(w, r, http.StatusOK)
		}
		return
	}
	name := strings.TrimPrefix(p, "/")
	switch {
	case !s.built:
		s.refuse(w, r, http.StatusNotFound, "not_found", "not found")
	case s.served[name]:
		s.static.ServeHTTP(w, r)
	case s.notices[name] != nil:
		s.notices[name].serve(w, r, http.StatusOK)
	case p == "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
	case hasDotSegment(name), s.refused[name], underPrefix(p, "/assets"), underPrefix(p, "/licenses"):
		s.refuse(w, r, http.StatusNotFound, "not_found", "not found")
	default:
		s.notFound.serve(w, r, http.StatusNotFound)
	}
}

func (s *spaServer) refuse(w http.ResponseWriter, r *http.Request, status int, code ErrorCode, msg string) {
	s.errs(w, r, status, code, msg)
}

// serve writes f with status, no-cache. A 200 carries f's ETag and its gzip
// body when offered; a 404 carries neither, so nothing revalidates it.
func (sh *spaFile) serve(w http.ResponseWriter, r *http.Request, status int) {
	h := w.Header()
	h.Set("Content-Type", sh.contentType)
	h.Set("Cache-Control", "no-cache")
	if status == http.StatusOK {
		h.Set("ETag", sh.etag)
		h.Add("Vary", "Accept-Encoding")
		if sh.gz.body != nil && serveGzip(w, r, sh.etag, sh.gz) {
			return
		}
		if ifNoneMatch(r.Header, sh.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(sh.body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(sh.body) //nolint:gosec // G705: the bundle's own file, read at construction, never request data
	}
}

// hasStyleAttr reports whether a start tag in html carries a style
// attribute, quoted values skipped.
func hasStyleAttr(html []byte) bool {
	for i := 0; i+1 < len(html); i++ {
		if html[i] != '<' || !isASCIILetter(html[i+1]) {
			continue
		}
		end := openTagEnd(html, i+1)
		if end < 0 {
			end = len(html)
		}
		if hasAttr(html[i+1:end], "style") {
			return true
		}
		i = end
	}
	return false
}

// hasAttr reports whether tag, a start tag's bytes after "<", names the
// lowercase attribute name, ASCII case-insensitively.
func hasAttr(tag []byte, name string) bool {
	var quote byte
	atName := false // the tag name comes first
	for i := range tag {
		switch c := tag[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote, atName = c, false
		case isASCIISpace(c) || c == '/':
			atName = true
		case atName:
			if hasFoldPrefix(tag[i:], name) && attrNameEnds(tag, i+len(name)) {
				return true
			}
			atName = false
		}
	}
	return false
}

func attrNameEnds(tag []byte, j int) bool {
	return j >= len(tag) || isASCIISpace(tag[j]) || tag[j] == '=' || tag[j] == '/' || tag[j] == '>'
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
