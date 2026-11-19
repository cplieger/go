package httpservertest

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

const (
	cspHeader      = "Content-Security-Policy"
	immutableCache = "public, max-age=31536000, immutable"
	noCache        = "no-cache"
	noStore        = "no-store"
	sampleSegment  = "checkspa"
	crossOrigin    = "https://cross-site.invalid"
)

// CheckSPA fails t unless h, httpserver.SPA built from c (alone or at "/" of
// an app handler), serves c.Assets as SPA must: every page, shell file name,
// listed file (with and without gzip, with a Range it ignores, and with an
// If-Match it holds and one it fails), unlisted file, sourcemap, dot-file, the
// manifest, /favicon.ico, a same-origin and a cross-site POST, a path under
// c.APIPrefix and an unknown path. Each answer outside c.APIPrefix carries
// exactly SPAPolicy(c.CSPAdd) and its status, body, table content type,
// Cache-Control and ETag; a refusal SPA writes decodes as the envelope with
// no-store. A missing or unreadable bundle is fatal.
func CheckSPA(t testing.TB, h http.Handler, c httpserver.SPAConfig) { //nolint:gocritic // hugeParam: SPA's own value config, read once
	t.Helper()
	b, found, err := httpserver.ReadBundle(c.Assets)
	if err != nil || !found {
		t.Fatalf("CheckSPA: no bundle to check against (found %v): %v", found, err)
	}
	policy, err := httpserver.SPAPolicy(c.CSPAdd)
	if err != nil {
		t.Fatalf("CheckSPA: %v", err)
	}
	pages, err := httpserver.NewPageMatcher(c.Pages, c.APIPrefix)
	if err != nil {
		t.Fatalf("CheckSPA: %v", err)
	}
	s := &spaCheck{t: t, h: h, c: c, b: b, policy: policy, pages: pages, classic: map[string]bool{}, stable: map[string]bool{}}
	for _, e := range b.Entries {
		if e.Kind == httpserver.BundleClassic {
			s.classic[e.Path] = true
		}
		if e.Stable {
			s.stable[e.Path] = true
		}
	}
	s.checkPages()
	s.checkFiles()
	s.checkUnlisted()
	s.checkRefusals()
}

type spaCheck struct {
	t       testing.TB
	h       http.Handler
	pages   *httpserver.PageMatcher
	classic map[string]bool
	stable  map[string]bool
	b       httpserver.Bundle
	policy  string
	c       httpserver.SPAConfig
}

// fileWant is what a 200 for one file must carry.
type fileWant struct {
	contentType string
	cache       string
	etag        string
	body        []byte
}

func (s *spaCheck) serve(method, target string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(s.t.Context(), method, target, http.NoBody)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return w
}

// mayBePage reports whether a page answers name, a path outside the bundle's
// own (assets/, licenses/, the manifest and listed files), so it is no probe
// of the unknown path or a dot-file refusal.
func (s *spaCheck) mayBePage(name string) bool {
	if inTree(name) || name == httpserver.BundlePath {
		return false
	}
	if _, listed := s.b.Files[name]; listed {
		return false
	}
	_, ok := s.pages.Match(escaped(name))
	return ok
}

func inTree(name string) bool {
	return strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "licenses/")
}

func (s *spaCheck) checkPages() {
	for _, p := range s.c.Pages {
		target := samplePath(p.Pattern)
		if m, ok := s.pages.Match(target); !ok || m.Name != p.Name || m.Redirect != "" {
			s.t.Errorf("CheckSPA: page %q: its sample path %q answers %+v, not the page", p.Name, target, m)
			continue
		}
		shell := p.Shell
		if shell == "" {
			shell = httpserver.DefaultShell
		}
		want, ok := s.listed(shell)
		if !ok {
			continue
		}
		where := "GET " + target
		rec := s.checkFile(where, target, want, shell)
		if err := httpserver.CheckShell(rec.Body.Bytes()); err != nil {
			s.t.Errorf("CheckSPA: %s: the shell is refused: %v", where, err)
		}
	}
}

// listed returns what the listed file name must answer, failing when the
// manifest does not list it or the tree cannot read it.
func (s *spaCheck) listed(name string) (fileWant, bool) {
	hash, ok := s.b.Files[name]
	if !ok {
		s.t.Errorf("CheckSPA: %s does not list %q", httpserver.BundlePath, name)
		return fileWant{}, false
	}
	body, err := fs.ReadFile(s.c.Assets, name)
	if err != nil {
		s.t.Errorf("CheckSPA: read %q: %v", name, err)
		return fileWant{}, false
	}
	w := fileWant{body: body, contentType: httpserver.ContentTypes()[path.Ext(name)], cache: noCache, etag: policyETag(hash, s.policy)}
	switch {
	case strings.HasPrefix(name, "licenses/"):
		w.contentType = httpserver.ContentTypes()[".txt"]
	case strings.HasPrefix(name, "assets/") && !s.classic[name]:
		w.cache, w.etag = immutableCache, `"`+hash+`"`
	}
	return w, true
}

// checkFiles requests every listed file other than a shell at its own path.
func (s *spaCheck) checkFiles() {
	for _, name := range slices.Sorted(maps.Keys(s.b.Files)) {
		target := escaped(name)
		if !strings.Contains(name, "/") && !s.stable[name] {
			continue
		}
		if _, ok := httpserver.ContentTypes()[path.Ext(name)]; !ok && strings.HasPrefix(name, "assets/") {
			s.checkRefusal("GET "+target, s.serve(http.MethodGet, target), http.StatusNotFound, "not_found")
			continue
		}
		if want, ok := s.listed(name); ok {
			s.checkFile("GET "+target, target, want, name)
		}
	}
}

// checkFile requests target without and with gzip, with a Range SPA ignores,
// with its own tag in If-Match and with another one, and checks each answer
// against want, returning the identity answer.
func (s *spaCheck) checkFile(where, target string, want fileWant, name string) *httptest.ResponseRecorder {
	rec := s.serve(http.MethodGet, target)
	s.checkAnswer(where, rec, want, name)
	s.checkAnswer(where+" (Range)", s.serve(http.MethodGet, target, "Range", "bytes=1-1", "If-Range", want.etag), want, name)
	s.checkAnswer(where+" (If-Match its tag)", s.serve(http.MethodGet, target, "If-Match", want.etag), want, name)
	s.checkRefusal(where+" (If-Match another tag)", s.serve(http.MethodGet, target, "If-Match", `"`+sampleSegment+`"`), http.StatusPreconditionFailed, "precondition_failed")
	gz := s.serve(http.MethodGet, target, "Accept-Encoding", "gzip")
	if gz.Header().Get("Content-Encoding") != "gzip" {
		s.checkAnswer(where+" (gzip offered)", gz, want, name)
		return rec
	}
	zr, err := gzip.NewReader(gz.Body)
	if err == nil {
		gz.Body = new(bytes.Buffer)
		_, err = io.Copy(gz.Body, zr) //nolint:gosec // G110: the bundle's own file, bounded by the tree
	}
	if err != nil {
		s.t.Errorf("CheckSPA: %s (gzip): the body does not decode: %v", where, err)
		return rec
	}
	want.etag = strings.TrimSuffix(want.etag, `"`) + `-gz"`
	s.checkAnswer(where+" (gzip)", gz, want, name)
	return rec
}

func (s *spaCheck) checkAnswer(where string, rec *httptest.ResponseRecorder, want fileWant, name string) {
	s.checkPolicy(where, rec)
	if rec.Code != http.StatusOK {
		s.t.Errorf("CheckSPA: %s: status = %d, want 200", where, rec.Code)
		return
	}
	if !bytes.Equal(rec.Body.Bytes(), want.body) {
		s.t.Errorf("CheckSPA: %s: the body (%d bytes) is not %s (%d bytes)", where, rec.Body.Len(), name, len(want.body))
	}
	h := rec.Header()
	if got := h.Get("Content-Type"); got != want.contentType {
		s.t.Errorf("CheckSPA: %s: Content-Type = %q, want %q from the content-type table", where, got, want.contentType)
	}
	if got := h.Get("Cache-Control"); got != want.cache {
		s.t.Errorf("CheckSPA: %s: Cache-Control = %q, want %q", where, got, want.cache)
	}
	if got := h.Get("ETag"); got != want.etag {
		s.t.Errorf("CheckSPA: %s: ETag = %q, want %q", where, got, want.etag)
	}
}

// checkUnlisted requests every file in the tree the manifest does not list,
// and one synthetic file under each served directory: none is served.
func (s *spaCheck) checkUnlisted() {
	names := []string{"assets/" + sampleSegment + "-unlisted.js", "licenses/" + sampleSegment + "-unlisted.txt"}
	err := fs.WalkDir(s.c.Assets, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if _, ok := s.b.Files[name]; !ok && name != httpserver.BundlePath {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		s.t.Errorf("CheckSPA: walk the tree: %v", err)
	}
	for _, name := range names {
		if s.mayBePage(name) {
			continue
		}
		target := escaped(name)
		where := "GET " + target
		rec := s.serve(http.MethodGet, target)
		if refusedPath(name) {
			s.checkRefusal(where, rec, http.StatusNotFound, "not_found")
			continue
		}
		s.checkUnknown(where, rec)
	}
}

// refusedPath reports whether SPA answers name, when it is not served, with
// the not_found envelope rather than the 404 shell.
func refusedPath(name string) bool {
	if inTree(name) {
		return true
	}
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func (s *spaCheck) checkRefusals() {
	probes := []string{httpserver.BundlePath, ".checkspa", "assets/.checkspa", sourcemapPath(s.b)}
	for name := range s.b.Files {
		if !strings.Contains(name, "/") && !s.stable[name] {
			probes = append(probes, name)
		}
	}
	slices.Sort(probes)
	for _, name := range probes {
		if !s.mayBePage(name) {
			target := escaped(name)
			s.checkRefusal("GET "+target, s.serve(http.MethodGet, target), http.StatusNotFound, "not_found")
		}
	}
	if unknown := sampleSegment + "-unknown/x"; !s.mayBePage(unknown) {
		s.checkUnknown("GET /"+unknown, s.serve(http.MethodGet, "/"+unknown))
	}
	rec := s.serve(http.MethodGet, "/favicon.ico")
	s.checkPolicy("GET /favicon.ico", rec)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		s.t.Errorf("CheckSPA: GET /favicon.ico: status = %d with %d bytes, want 204 and no body", rec.Code, rec.Body.Len())
	}
	page := "/"
	if len(s.c.Pages) > 0 {
		page = samplePath(s.c.Pages[0].Pattern)
	}
	s.checkRefusal("POST "+page, s.serve(http.MethodPost, page, "Sec-Fetch-Site", "same-origin"),
		http.StatusMethodNotAllowed, "method_not_allowed")
	cross := s.serve(http.MethodPost, page, "Sec-Fetch-Site", "cross-site", "Origin", crossOrigin)
	if cross.Code == http.StatusForbidden {
		s.checkEnvelope("POST "+page+" (cross-site)", cross, http.StatusForbidden, "cross_origin_refused")
	} else {
		s.checkRefusal("POST "+page+" (cross-site)", cross, http.StatusMethodNotAllowed, "method_not_allowed")
	}
	s.checkAPIPrefix()
}

// checkAPIPrefix requests a path under APIPrefix, which the API answers: any
// refusal envelope passes, never a shell.
func (s *spaCheck) checkAPIPrefix() {
	target := s.c.APIPrefix + "/" + sampleSegment
	where := "GET " + target
	rec := s.serve(http.MethodGet, target)
	if rec.Code < http.StatusBadRequest {
		s.t.Errorf("CheckSPA: %s: status = %d, want a refusal: SPA answers nothing under APIPrefix", where, rec.Code)
		return
	}
	if err := decodeEnvelope(rec); err != nil {
		s.t.Errorf("CheckSPA: %s: the answer is not the error envelope: %v", where, err)
	}
}

// checkUnknown checks the answer to a path that is no page and no file: the
// default shell with 404, no-cache and no ETag.
func (s *spaCheck) checkUnknown(where string, rec *httptest.ResponseRecorder) {
	s.checkPolicy(where, rec)
	if rec.Code != http.StatusNotFound {
		s.t.Errorf("CheckSPA: %s: status = %d, want 404 with the default shell", where, rec.Code)
		return
	}
	shell, err := fs.ReadFile(s.c.Assets, httpserver.DefaultShell)
	if err != nil {
		s.t.Errorf("CheckSPA: read %q: %v", httpserver.DefaultShell, err)
		return
	}
	if !bytes.Equal(rec.Body.Bytes(), shell) {
		s.t.Errorf("CheckSPA: %s: the 404 body is not the default shell %s", where, httpserver.DefaultShell)
	}
	h := rec.Header()
	if got, want := h.Get("Content-Type"), httpserver.ContentTypes()[".html"]; got != want {
		s.t.Errorf("CheckSPA: %s: Content-Type = %q, want %q", where, got, want)
	}
	if got := h.Get("Cache-Control"); got != noCache {
		s.t.Errorf("CheckSPA: %s: Cache-Control = %q, want %q", where, got, noCache)
	}
	if got := h.Get("ETag"); got != "" {
		s.t.Errorf("CheckSPA: %s: ETag = %q on a 404, want none", where, got)
	}
}

// checkRefusal checks a refusal SPA writes, which carries no-store; a refusal
// the app's middleware writes in front of SPA is checkEnvelope's.
func (s *spaCheck) checkRefusal(where string, rec *httptest.ResponseRecorder, status int, code httpserver.ErrorCode) {
	s.checkEnvelope(where, rec, status, code)
	if got := rec.Header().Get("Cache-Control"); got != noStore {
		s.t.Errorf("CheckSPA: %s: Cache-Control = %q, want %q", where, got, noStore)
	}
}

func (s *spaCheck) checkEnvelope(where string, rec *httptest.ResponseRecorder, status int, code httpserver.ErrorCode) {
	s.checkPolicy(where, rec)
	if rec.Code != status {
		s.t.Errorf("CheckSPA: %s: status = %d, want %d %s", where, rec.Code, status, code)
	}
	env, err := envelope(rec)
	switch {
	case err != nil:
		s.t.Errorf("CheckSPA: %s: the answer is not the error envelope: %v", where, err)
	case env.Code != code:
		s.t.Errorf("CheckSPA: %s: code = %q, want %q", where, env.Code, code)
	}
}

func (s *spaCheck) checkPolicy(where string, rec *httptest.ResponseRecorder) {
	got := rec.Header().Values(cspHeader)
	if len(got) != 1 || !samePolicy(got[0], s.policy) {
		s.t.Errorf("CheckSPA: %s: %s = %q, want exactly %q", where, cspHeader, got, s.policy)
	}
}

// CheckNoUI fails t unless h answers every path and method with not_found
// (404) as the error envelope: the root of a build with no web UI.
func CheckNoUI(t testing.TB, h http.Handler) {
	t.Helper()
	probes := []struct{ method, target string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/" + httpserver.DefaultShell},
		{http.MethodGet, "/" + httpserver.BundlePath},
		{http.MethodGet, "/assets/app.js"},
		{http.MethodGet, "/licenses/index.txt"},
		{http.MethodGet, "/favicon.ico"},
		{http.MethodGet, "/" + sampleSegment + "/page"},
		{http.MethodHead, "/"},
		{http.MethodPost, "/"},
	}
	for _, p := range probes {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), p.method, p.target, http.NoBody))
		where := p.method + " " + p.target
		if w.Code != http.StatusNotFound {
			t.Errorf("CheckNoUI: %s: status = %d, want 404", where, w.Code)
		}
		if p.method == http.MethodHead {
			continue
		}
		if env, err := envelope(w); err != nil {
			t.Errorf("CheckNoUI: %s: the answer is not the error envelope: %v", where, err)
		} else if env.Code != "not_found" {
			t.Errorf("CheckNoUI: %s: code = %q, want not_found", where, env.Code)
		}
	}
}

// envelope decodes rec's body as httpserver.ErrorResponse: a JSON object
// holding exactly its four string members, served as application/json, with
// request_id matching the response's request id header.
func envelope(rec *httptest.ResponseRecorder) (httpserver.ErrorResponse, error) {
	var env httpserver.ErrorResponse
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		return env, errors.New("Content-Type is " + ct + ", not application/json")
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		return env, err
	}
	for _, k := range []string{"code", "message", "remediation", "request_id"} {
		if _, ok := raw[k].(string); !ok {
			return env, errors.New("member " + k + " is missing or not a string")
		}
	}
	if len(raw) != 4 {
		return env, errors.New("the object holds members beyond code, message, remediation and request_id")
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		return env, err
	}
	if env.Code == "" || env.RequestID == "" || rec.Header().Get(httpserver.HeaderRequestID) != env.RequestID {
		return env, errors.New("the code or request_id is empty, or request_id is not the " + httpserver.HeaderRequestID + " header")
	}
	return env, nil
}

func decodeEnvelope(rec *httptest.ResponseRecorder) error {
	_, err := envelope(rec)
	return err
}

// samePolicy compares two policies directive by directive, ignoring the order
// of directives and of sources.
func samePolicy(a, b string) bool {
	return maps.EqualFunc(parsePolicy(a), parsePolicy(b), slices.Equal)
}

func parsePolicy(v string) map[string][]string {
	out := map[string][]string{}
	for d := range strings.SplitSeq(v, ";") {
		if f := strings.Fields(d); len(f) > 0 {
			out[f[0]] = slices.Sorted(slices.Values(f[1:]))
		}
	}
	return out
}

// policyETag is the tag SPA gives a no-cache file: the first 32 hex digits of
// SHA-256 over its manifest hash, a zero byte and the policy, quoted.
func policyETag(fileHash, policy string) string {
	sum := sha256.Sum256([]byte(fileHash + "\x00" + policy))
	return `"` + hex.EncodeToString(sum[:])[:32] + `"`
}

// samplePath returns a path pattern matches: each wildcard filled with a
// sample segment, {$} with nothing.
func samplePath(pattern string) string {
	segs := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	for i, seg := range segs {
		switch {
		case seg == "{$}":
			segs[i] = ""
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "...}"):
			segs[i] = sampleSegment + "/x"
		case strings.HasPrefix(seg, "{"):
			segs[i] = sampleSegment
		default:
			segs[i] = url.PathEscape(seg)
		}
	}
	return "/" + strings.Join(segs, "/")
}

func sourcemapPath(b httpserver.Bundle) string {
	for _, e := range b.Entries {
		return e.Path + ".map"
	}
	return "assets/" + sampleSegment + ".js.map"
}

func escaped(name string) string { return (&url.URL{Path: "/" + name}).EscapedPath() }
