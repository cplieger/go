package httpserver

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
)

// StaticOption configures StaticHandler.
type StaticOption func(*staticConfig)

// staticConfig holds resolved StaticHandler configuration.
type staticConfig struct {
	cacheControl func(assetPath string) string
	tags         map[string]string
	callerTags   bool
}

// WithStaticCacheControl sets the per-asset Cache-Control policy, "no-cache"
// for every asset by default. fn receives the asset path with no leading
// slash, "index.html" for "/", and returns the header value; an empty value
// omits the header. It chooses the value only; WithStaticETags sets the tags.
// A nil fn keeps the default.
func WithStaticCacheControl(fn func(assetPath string) string) StaticOption {
	return func(c *staticConfig) {
		if fn != nil {
			c.cacheControl = fn
		}
	}
}

// WithStaticETags gives each file its entity-tag by path in fsys (no leading
// slash), each a quoted strong entity-tag such as `"3f2a"`, and StaticHandler
// then hashes no file. A named file answers its tag and its gzip body the tag
// plus "-gz"; an unnamed file is served with no ETag, Cache-Control or gzip
// body. A nil map names no file. StaticHandler refuses a path naming no file
// and a tag that is not a strong entity-tag (RFC 9110 section 8.8.3).
func WithStaticETags(tags map[string]string) StaticOption {
	return func(c *staticConfig) {
		c.tags = maps.Clone(tags)
		c.callerTags = true
	}
}

// StaticHandler serves fsys, typically an embed.FS, whose zero ModTime leaves
// a bare http.FileServer nothing to revalidate on. It reads the tree once and
// gives each file a sha256 ETag (or the WithStaticETags tag) and, when
// smaller, a gzip body. Every file answers its type from ContentTypes. A
// tagged file carries its ETag, Cache-Control and Vary: Accept-Encoding; a
// GET or HEAD offering gzip without Range gets the gzip body under its own
// "-gz" ETag. Everything else falls through to http.FileServer. The error is
// non-nil when walking fsys fails or WithStaticETags is refused.
func StaticHandler(fsys fs.FS, opts ...StaticOption) (http.Handler, error) {
	c := &staticConfig{
		cacheControl: func(string) string { return "no-cache" },
	}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	files, err := buildStaticFiles(fsys, c)
	if err != nil {
		return nil, err
	}
	return &staticServer{files: files, cacheControl: c.cacheControl, fileServer: http.FileServer(http.FS(fsys))}, nil
}

type staticServer struct {
	files        map[string]staticFile
	cacheControl func(assetPath string) string
	fileServer   http.Handler
}

func (s *staticServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := staticName(r.URL.Path)
	f, known := s.files[name]
	if !known {
		s.fileServer.ServeHTTP(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.gz.contentType)
	if f.etag != "" {
		h.Set("ETag", f.etag)
		if cc := s.cacheControl(name); cc != "" {
			h.Set("Cache-Control", cc)
		}
		// Asset bodies vary by Accept-Encoding (some carry a gzip
		// representation), so shared caches must key on it on every path.
		h.Add("Vary", "Accept-Encoding")
	}
	if f.gz.body != nil && serveGzip(w, r, f.etag, f.gz) {
		return
	}
	s.fileServer.ServeHTTP(w, r)
}

// staticName is the tree path http.FileServer answers urlPath from: a
// directory path answers its index.
func staticName(urlPath string) string {
	name := strings.TrimPrefix(path.Clean(urlPath), "/")
	switch {
	case name == "":
		return "index.html"
	case strings.HasSuffix(urlPath, "/"):
		return name + "/index.html"
	}
	return name
}

// gzAsset is a precomputed gzip representation of an embedded asset.
type gzAsset struct {
	contentType string
	body        []byte
}

// staticFile is one file of the tree: its tag ("" untagged) and its type,
// with the gzip body when one is kept.
type staticFile struct {
	etag string
	gz   gzAsset
}

// buildStaticFiles walks the static tree once, tagging each file (by its
// sha256, or from c.tags) and keeping a gzip body for each tagged file that
// compresses smaller.
func buildStaticFiles(fsys fs.FS, c *staticConfig) (map[string]staticFile, error) {
	files := make(map[string]staticFile)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		f, err := readStaticFile(fsys, p, c)
		files[p] = f
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := checkStaticTags(files, c.tags); err != nil {
		return nil, err
	}
	return files, nil
}

func readStaticFile(fsys fs.FS, p string, c *staticConfig) (staticFile, error) {
	f := staticFile{gz: gzAsset{contentType: contentTypeOf(p)}}
	if c.callerTags {
		if f.etag = c.tags[p]; f.etag == "" {
			return f, nil
		}
	}
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		return f, err
	}
	if !c.callerTags {
		sum := sha256.Sum256(b)
		f.etag = fmt.Sprintf(`"%x"`, sum[:])
	}
	if gz, ok := gzipAsset(b, p); ok {
		f.gz = gz
	}
	return f, nil
}

func checkStaticTags(files map[string]staticFile, tags map[string]string) error {
	for _, p := range slices.Sorted(maps.Keys(tags)) {
		if _, ok := files[p]; !ok {
			return fmt.Errorf("httpserver: WithStaticETags names %q, which is no file in the tree", p)
		}
		if !strongEntityTag(tags[p]) {
			return fmt.Errorf("httpserver: WithStaticETags tag %q for %q is not a quoted strong entity-tag", tags[p], p)
		}
	}
	return nil
}

// strongEntityTag reports whether s is DQUOTE *etagc DQUOTE (RFC 9110
// section 8.8.3), the form whose "-gz" sibling serveGzip derives.
func strongEntityTag(s string) bool {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return false
	}
	for i := 1; i < len(s)-1; i++ {
		if c := s[i]; c != 0x21 && (c < 0x23 || c == 0x7f) {
			return false
		}
	}
	return true
}

// gzipAsset returns the gzip representation of b, or ok=false when gzip does
// not shrink it: already-compressed assets (woff2, which embeds Brotli) and
// small files whose gzip framing outweighs the savings. Plain otf/ttf outline
// fonts are not pre-compressed and do shrink (~30%), so they are stored and
// served gzipped.
func gzipAsset(b []byte, name string) (gzAsset, bool) {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression) // level is constant-valid
	if _, err := zw.Write(b); err != nil {
		return gzAsset{}, false
	}
	if err := zw.Close(); err != nil {
		return gzAsset{}, false
	}
	if buf.Len() >= len(b) {
		return gzAsset{}, false
	}
	return gzAsset{contentType: contentTypeOf(name), body: bytes.Clone(buf.Bytes())}, true
}

// serveGzip writes the precompressed representation of an asset and reports
// true when it handled the response. It returns false — leaving the caller to
// fall back to the identity http.FileServer — for Range requests, non-GET/HEAD
// methods, or clients that do not offer gzip.
func serveGzip(w http.ResponseWriter, r *http.Request, etag string, gz gzAsset) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.Header.Get("Range") != "" || !acceptsGzip(r) {
		return false
	}
	h := w.Header()
	// A gzip body is a distinct representation, so it carries its own ETag;
	// Vary: Accept-Encoding (set by the caller) keeps caches from crossing it
	// with the identity body.
	gzEtag := `"` + strings.Trim(etag, `"`) + `-gz"`
	h.Set("ETag", gzEtag)
	if ifNoneMatch(r.Header, gzEtag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Type", gz.contentType)
	h.Set("Content-Length", strconv.Itoa(len(gz.body)))
	if r.Method == http.MethodHead {
		return true
	}
	_, _ = w.Write(gz.body)
	return true
}

// ifNoneMatch reports whether h's If-None-Match, over every field line, is
// "*" as the whole field or lists an entity-tag weakly equal to etag, a
// strong entity-tag (RFC 9110 section 13.1.2).
func ifNoneMatch(h http.Header, etag string) bool {
	members, wildcard := 0, false
	for member := range fieldList(h, "If-None-Match", entityTagList) {
		members++
		wildcard = member == "*"
		if strings.TrimPrefix(member, "W/") == etag {
			return true
		}
	}
	return wildcard && members == 1
}
