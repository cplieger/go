package httpserver

import (
	"bytes"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
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
// listed asset answers through StaticHandler (immutable; a BundleClassic or
// stable entry no-cache), a notice text/plain, /favicon.ico 204, any other
// path under assets/ or licenses/ a refusal, whatever the pages match; then a
// page path answers its shell, any other the default shell with 404, no-cache.
// A path at or under APIPrefix, a dot segment no page matches, another method
// and, unbuilt, a page (ui_unbuilt) answer a refusal through Errors, no-store.
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
// other listed root file, a shell, being refused as a path.
func (s *spaServer) loadFiles(fsys fs.FS, b Bundle) error {
	var err error
	if s.static, s.served, err = bundleAssets(fsys, b, s.policy); err != nil {
		return err
	}
	s.refused = map[string]bool{BundlePath: true}
	s.notices = make(map[string]*spaFile)
	for name, hash := range b.Files {
		switch {
		case s.served[name]:
		case !strings.Contains(name, "/"):
			s.refused[name] = true
		case strings.HasPrefix(name, "licenses/"):
			if err := s.loadNotice(fsys, name, hash); err != nil {
				return err
			}
		}
	}
	return nil
}

// bundleAssets is the StaticHandler for b's stable entries and the files b
// lists under assets/ with a content-type table extension, and those files'
// tree paths: the manifest hash and immutable for each, a BundleClassic or
// stable entry no-cache under a tag bound to policy. SPA and OutagePage both
// serve assets through it.
func bundleAssets(fsys fs.FS, b Bundle, policy string) (http.Handler, map[string]bool, error) {
	stable := stablePaths(b.Entries)
	noCache := maps.Clone(stable)
	for _, e := range b.Entries {
		if e.Kind == BundleClassic {
			noCache[e.Path] = true
		}
	}
	tags := make(map[string]string)
	served := make(map[string]bool)
	for name, hash := range b.Files {
		switch {
		case !stable[name] && (!strings.HasPrefix(name, "assets/") || contentTypes[path.Ext(name)] == ""):
		case noCache[name]:
			served[name], tags[name] = true, policyETag(hash, policy)
		default:
			served[name], tags[name] = true, `"`+hash+`"`
		}
	}
	h, err := StaticHandler(fsys, WithStaticETags(tags), WithStaticCacheControl(func(name string) string {
		if noCache[name] {
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

// CheckShell refuses a shell SPA's policy would block, read as html.Parse
// builds its document: one holding an inline script or style element, a style
// attribute or an on* event-handler attribute, or a script left open at end
// of input, which never runs. It also refuses a shell html.Parse cannot read
// whole or the check cannot bound: past 512 nested elements, a <template> start
// tag while an <svg> or <math> element is open, or over 16 <template> tags past
// the first <svg> or <math>. The error names the element, and the attribute
// where one is the cause. SPA calls it on every shell at start; a bundler too.
func CheckShell(body []byte) error {
	doc, err := parseShell(body)
	if err != nil {
		return err
	}
	if n := openScript(doc, body); n != nil {
		return fmt.Errorf("leaves a <%s> element open at end of input, so it never runs", n.Data)
	}
	for n := range shellElements(doc) {
		if isInlineScript(n) || isInlineStyle(n) {
			return fmt.Errorf("holds an inline <%s> element", n.Data)
		}
		for _, a := range n.Attr {
			switch {
			case a.Namespace != "":
			case a.Key == "style":
				return fmt.Errorf("holds a style attribute on <%s>", n.Data)
			case len(a.Key) > len("on") && strings.HasPrefix(a.Key, "on"):
				return fmt.Errorf("holds the event-handler attribute %q on <%s>", a.Key, n.Data)
			}
		}
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
