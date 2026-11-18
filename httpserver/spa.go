package httpserver

import (
	"bytes"
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

// SPA serves a single-page app's bundle (BundlePath) for GET and HEAD. A
// listed asset answers through StaticHandler (immutable; a BundleClassic entry
// no-cache), a notice text/plain, /favicon.ico 204, and any other path under
// assets/ or licenses/ a refusal, whatever the pages match; then a page path
// answers its shell, any other the default shell with 404 (no-cache). A path
// at or under APIPrefix, a dot segment no page matches, another method and,
// unbuilt, a page (ui_unbuilt) answer a refusal through Errors, no-store.
// Every answer carries SPAPolicy's policy and every no-cache ETag binds it.
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
		if err := CheckShell(sh.body); err != nil {
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

// loadFiles keeps the notices in memory and the listed assets' handler, every
// other listed root file being refused as a path.
func (s *spaServer) loadFiles(fsys fs.FS, b Bundle) error {
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
		}
	}
	var err error
	s.static, s.served, err = bundleAssets(fsys, b, s.policy)
	return err
}

// bundleAssets is the StaticHandler for the files b lists under assets/ with
// a content-type table extension, and those files' tree paths: the manifest
// hash and immutable for each, a BundleClassic entry no-cache under a tag
// bound to policy. SPA and OutagePage both serve assets through it.
func bundleAssets(fsys fs.FS, b Bundle, policy string) (http.Handler, map[string]bool, error) {
	classic := make(map[string]bool)
	for _, e := range b.Entries {
		if e.Kind == BundleClassic {
			classic[e.Path] = true
		}
	}
	tags := make(map[string]string)
	served := make(map[string]bool)
	for name, hash := range b.Files {
		switch {
		case !strings.HasPrefix(name, "assets/"), contentTypes[path.Ext(name)] == "":
		case classic[name]:
			served[name], tags[name] = true, policyETag(hash, policy)
		default:
			served[name], tags[name] = true, `"`+hash+`"`
		}
	}
	h, err := StaticHandler(fsys, WithStaticETags(tags), WithStaticCacheControl(func(name string) string {
		if classic[name] {
			return "no-cache"
		}
		return immutableCache
	}))
	return h, served, err
}

func (s *spaServer) loadNotice(fsys fs.FS, name, hash string) error {
	f, err := readSPAFile(fsys, name, contentTypes[".txt"], policyETag(hash, s.policy))
	s.notices[name] = f
	return err
}

// CheckShell refuses a shell SPA's policy would block: one holding an inline
// script or style element, a style attribute or an on* event-handler
// attribute, reading attribute names as an HTML parser does. An attribute
// error names the element and the attribute. SPA calls it on every shell at
// start; a bundler calls it before writing one.
func CheckShell(body []byte) error {
	switch {
	case len(InlineScriptHashes(body)) > 0:
		return errors.New("holds an inline script element")
	case len(InlineStyleHashes(body)) > 0:
		return errors.New("holds an inline style element")
	}
	return checkShellAttrs(body)
}

func checkShellAttrs(html []byte) error {
	for i := 0; i+1 < len(html); i++ {
		if html[i] != '<' || !isASCIILetter(html[i+1]) {
			continue
		}
		tag, attrs, end := scanStartTag(html, i+1)
		for _, a := range attrs {
			switch {
			case a == "style":
				return fmt.Errorf("holds a style attribute on <%s>", tag)
			case len(a) > len("on") && strings.HasPrefix(a, "on"):
				return fmt.Errorf("holds the event-handler attribute %q on <%s>", a, tag)
			}
		}
		i = end - 1
	}
	return nil
}

func (s *spaServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", s.policy)
	p := r.URL.Path
	if underPrefix(p, s.apiPrefix) || underPrefix(path.Clean(p), s.apiPrefix) {
		refuse(s.errs, w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		refuse(s.errs, w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.built && s.serveTree(w, r) {
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
			refuse(s.errs, w, r, http.StatusServiceUnavailable, "ui_unbuilt", "the web UI is not built")
		default:
			s.shells[m.Name].serve(w, r, http.StatusOK)
		}
		return
	}
	if !s.built || hasDotSegment(strings.TrimPrefix(p, "/")) {
		refuse(s.errs, w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	s.notFound.serve(w, r, http.StatusNotFound)
}

// serveTree answers r when its path is the bundle's own: a listed file, a
// notice, /favicon.ico, or a refused name or path under assets/ or
// licenses/. It runs before the page match, so no page pattern shadows the
// tree; a page capture holding a dot segment is still a page. The prefixes
// are tested on the cleaned path, so /assets/../x is PageMatcher's to answer.
func (s *spaServer) serveTree(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	name := strings.TrimPrefix(p, "/")
	switch {
	case s.served[name]:
		s.static.ServeHTTP(w, r)
	case s.notices[name] != nil:
		s.notices[name].serve(w, r, http.StatusOK)
	case p == "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
	case s.refused[name], underPrefix(path.Clean(p), "/assets"), underPrefix(path.Clean(p), "/licenses"):
		refuse(s.errs, w, r, http.StatusNotFound, "not_found", "not found")
	default:
		return false
	}
	return true
}

// refuse answers a refusal through errs with Cache-Control: no-store, set
// before errs runs, so no cache keeps a refusal past the build or the outage
// that caused it.
func refuse(errs ErrorResponder, w http.ResponseWriter, r *http.Request, status int, code ErrorCode, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	errs(w, r, status, code, msg)
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
	writeBody(w, r, sh.body)
}

// writeBody writes body, bytes held since construction, unless r is a HEAD
// request. It writes through a bytes.Reader because gosec's G705 taint check
// flags a direct Write of such bytes on some runs only, so a suppression
// directive fails nolintlint on the others.
func writeBody(w http.ResponseWriter, r *http.Request, body []byte) {
	if r.Method != http.MethodHead {
		_, _ = bytes.NewReader(body).WriteTo(w)
	}
}

// scanStartTag reads the start tag whose name begins at html[from] through
// the tokenizer states of https://html.spec.whatwg.org/multipage/parsing.html#tag-name-state,
// returning the lowercase tag name, each lowercase attribute name, and the
// index after the closing '>' (len(html) when input ends first).
func scanStartTag(html []byte, from int) (tag string, attrs []string, end int) {
	i := from
	for i < len(html) && !isTagNameBoundary(html[i]) {
		i++
	}
	tag = lowerASCIIString(string(html[from:i]))
	for i < len(html) {
		switch c := html[i]; {
		case c == '>':
			return tag, attrs, i + 1
		case isASCIISpace(c) || c == '/':
			i++
		default:
			var name string
			name, i = scanAttr(html, i)
			attrs = append(attrs, name)
		}
	}
	return tag, attrs, i
}

// scanAttr reads the attribute whose name begins at html[from] and returns
// its lowercase name and the index after its value. A leading '=' belongs to
// the name, and a quote ends nothing but a quoted value, as the tokenizer has it.
func scanAttr(html []byte, from int) (name string, end int) {
	i := from + 1
	for i < len(html) && !isTagNameBoundary(html[i]) && html[i] != '=' {
		i++
	}
	name = lowerASCIIString(string(html[from:i]))
	i = skipASCIISpace(html, i)
	if i == len(html) || html[i] != '=' {
		return name, i
	}
	i = skipASCIISpace(html, i+1)
	if i == len(html) {
		return name, i
	}
	switch q := html[i]; q {
	case '"', '\'':
		if n := bytes.IndexByte(html[i+1:], q); n >= 0 {
			return name, i + 1 + n + 1
		}
		return name, len(html)
	case '>':
		return name, i
	}
	for i < len(html) && !isASCIISpace(html[i]) && html[i] != '>' {
		i++
	}
	return name, i
}

func skipASCIISpace(b []byte, i int) int {
	for i < len(b) && isASCIISpace(b[i]) {
		i++
	}
	return i
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
