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

	"github.com/cplieger/go/httpserver/v2/internal/httpfield"
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
// listed asset answers immutable (a BundleClassic or stable entry no-cache), a
// notice text/plain, /favicon.ico 204, any other path under assets/ or
// licenses/ a refusal, whatever the pages match; then a page path answers its
// shell, any other the default shell with 404, no-cache. Each file answers
// from memory under its ETag, with its gzip body when offered; Range is
// ignored and no Last-Modified is sent, so If-Match is the one precondition
// refused (precondition_failed). A path at or under APIPrefix, a dot segment
// no page matches, another method and, unbuilt, a page (ui_unbuilt) answer a
// refusal through Errors, no-store. Every answer carries SPAPolicy's policy
// and every no-cache ETag binds it.
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
	assets    map[string]*spaFile // by tree path: the listed assets and stable entries
	refused   map[string]bool     // root paths that are files, never paths
	policy    string
	apiPrefix string
	built     bool
}

// spaFile is a file SPA and OutagePage write from memory: a shell, a notice,
// an asset or a stable entry.
type spaFile struct {
	contentType  string
	cacheControl string
	body         []byte
	etag         string
	gz           gzAsset
}

func readSPAFile(fsys fs.FS, name, contentType, cacheControl, etag string) (*spaFile, error) {
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	f := &spaFile{contentType: contentType, cacheControl: cacheControl, body: body, etag: etag}
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
		sh, err := readSPAFile(fsys, name, contentTypes[htmlExt], "no-cache", policyETag(hash, s.policy))
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

// loadFiles keeps the listed assets and the notices in memory, every other
// listed root file, a shell, being refused as a path.
func (s *spaServer) loadFiles(fsys fs.FS, b Bundle) error {
	var err error
	if s.assets, err = bundleAssets(fsys, b, s.policy); err != nil {
		return err
	}
	s.refused = map[string]bool{BundlePath: true}
	s.notices = make(map[string]*spaFile)
	for name, hash := range b.Files {
		switch {
		case s.assets[name] != nil:
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

// bundleAssets reads b's stable entries and the files b lists under assets/
// with a content-type table extension, by tree path: each under the manifest
// hash and immutable, a BundleClassic or stable entry no-cache under a tag
// bound to policy. SPA and OutagePage both serve assets from it.
func bundleAssets(fsys fs.FS, b Bundle, policy string) (map[string]*spaFile, error) {
	stable := stablePaths(b.Entries)
	noCache := maps.Clone(stable)
	for _, e := range b.Entries {
		if e.Kind == BundleClassic {
			noCache[e.Path] = true
		}
	}
	assets := make(map[string]*spaFile)
	for name, hash := range b.Files {
		cacheControl, etag := immutableCache, `"`+hash+`"`
		switch {
		case !stable[name] && (!strings.HasPrefix(name, "assets/") || contentTypes[path.Ext(name)] == ""):
			continue
		case noCache[name]:
			cacheControl, etag = "no-cache", policyETag(hash, policy)
		}
		f, err := readSPAFile(fsys, name, contentTypeOf(name), cacheControl, etag)
		if err != nil {
			return nil, err
		}
		assets[name] = f
	}
	return assets, nil
}

func (s *spaServer) loadNotice(fsys fs.FS, name, hash string) error {
	f, err := readSPAFile(fsys, name, contentTypes[".txt"], "no-cache", policyETag(hash, s.policy))
	s.notices[name] = f
	return err
}

// CheckShell refuses a shell SPA's policy would block, read as html.Parse
// builds its document: an inline script or style element, a style or on*
// event-handler attribute, or a script left open at end of input. It refuses a
// shell html.Parse cannot read whole: past 512 nested elements, or a <template>
// in an svg or math integration point such as <foreignObject> (one in svg
// markup itself is read). Its own limit refuses over 16 <template spellings
// after the first <svg or <math in the bytes, comments and attributes included.
// The error names the element, and the attribute where one is the cause.
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
			s.shells[m.Name].serve(w, r, http.StatusOK, s.errs)
		}
		return
	}
	if !s.built || hasDotSegment(strings.TrimPrefix(p, "/")) {
		refuse(s.errs, w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	s.notFound.serve(w, r, http.StatusNotFound, s.errs)
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
	case s.assets[name] != nil:
		s.assets[name].serve(w, r, http.StatusOK, s.errs)
	case s.notices[name] != nil:
		s.notices[name].serve(w, r, http.StatusOK, s.errs)
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

// serve writes f with status. A 200 selects the gzip body when it is kept and
// offered, under f's ETag plus "-gz", and evaluates the preconditions against
// that representation in RFC 9110 section 13.2.2's order: an If-Match it
// fails (strong comparison, section 13.1.1) answers precondition_failed
// through errs, no-store, and a matching If-None-Match answers 304. Range is
// ignored (section 14.2) and no Last-Modified is sent, so If-Unmodified-Since,
// If-Modified-Since and If-Range have nothing to act on. Any other status
// carries no ETag and evaluates nothing (section 13.2.1), so nothing
// revalidates it.
func (f *spaFile) serve(w http.ResponseWriter, r *http.Request, status int, errs ErrorResponder) {
	body, etag, gzipped := f.body, f.etag, false
	if status == http.StatusOK {
		if f.gz.body != nil && acceptsGzip(r) {
			body, etag, gzipped = f.gz.body, gzipETag(f.etag), true
		}
		if httpfield.IfMatchFails(r.Header, etag) {
			refuse(errs, w, r, http.StatusPreconditionFailed, "precondition_failed", "the file does not match If-Match")
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("Cache-Control", f.cacheControl)
	if status == http.StatusOK {
		h.Set("ETag", etag)
		h.Add("Vary", "Accept-Encoding")
		if httpfield.IfNoneMatch(r.Header, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if gzipped {
			h.Set("Content-Encoding", "gzip")
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	writeBody(w, r, body)
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
