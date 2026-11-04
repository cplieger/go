package httpclient_test

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/go/httpclient"
)

// The tests in this file are written from RFC 9110 section 13.1 (the validators a
// client replays), RFC 9111 sections 4.3.4 and 5.2 (what a 304 updates, and
// no-store), and GitHub's statement that a 304 to an authorized conditional request
// does not count against the primary rate limit
// (docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api).

// server answers its body with an ETag, and 304 to a request naming that ETag.
type cacheServer struct {
	header      http.Header
	notModified http.Header
	got         []*http.Request
	body        string
	etag        string
	status      int
	// always304 answers 304 to every request, carrying a validator or not.
	always304 int
	mu        sync.Mutex
}

func newCacheServer(t *testing.T, body string) (*cacheServer, string) {
	t.Helper()
	s := &cacheServer{body: body, etag: `"v1"`, status: http.StatusOK, header: http.Header{}, notModified: http.Header{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func (s *cacheServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, r.Clone(context.Background()))
	w.Header().Set("ETag", s.etag)
	if s.always304 > 0 || (s.status == http.StatusOK && slices.Contains(r.Header.Values("If-None-Match"), s.etag)) {
		if s.always304 > 0 {
			s.always304--
		}
		maps.Copy(w.Header(), s.notModified)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	maps.Copy(w.Header(), s.header)
	w.WriteHeader(s.status)
	_, _ = io.WriteString(w, s.body)
}

func (s *cacheServer) set(f func(*cacheServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *cacheServer) requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

// lastValidator is the If-None-Match the server's last request carried.
func (s *cacheServer) lastValidator() string {
	got := s.requests()
	if len(got) == 0 {
		return ""
	}
	return got[len(got)-1].Header.Get("If-None-Match")
}

func newCacheTransport(t *testing.T, next http.RoundTripper, cfg httpclient.CacheConfig) *httpclient.CacheRoundTripper {
	t.Helper()
	return httpclient.NewCacheRoundTripper(next, cfg)
}

// get sends one request through tr and reads its answer in full.
func cacheGet(t *testing.T, tr http.RoundTripper, method, url string, header http.Header) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatalf("Setup: NewRequest = %v", err)
	}
	maps.Copy(req.Header, header)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatalf("%s %s = %v, want an answer", method, url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the answer to %s %s = %v", method, url, err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

// auth is the header a request carrying token sends, none for no token.
func authorized(token string) http.Header {
	if token == "" {
		return nil
	}
	return http.Header{"Authorization": {token}}
}

func TestAnUnchangedReadIsServedFromTheHeldAnswer(t *testing.T) {
	s, url := newCacheServer(t, `{"n":1}`)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	for i, want := range []string{"", `"v1"`} {
		status, _, body := cacheGet(t, tr, http.MethodGet, url, nil)
		if status != http.StatusOK || body != `{"n":1}` {
			t.Errorf("read %d = %d %q, want 200 and the server's body", i, status, body)
		}
		if got := s.lastValidator(); got != want {
			t.Errorf("read %d carried If-None-Match %q, want %q", i, got, want)
		}
	}
	if got := tr.State(); got.Hits != 1 || got.Misses != 1 || got.Bytes <= 0 {
		t.Errorf("State = %+v, want 1 hit, 1 miss and the answer held", got)
	}
}

func TestEntriesAreSeparatedByCredential(t *testing.T) {
	s, url := newCacheServer(t, `{}`)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	for _, step := range []struct {
		token, want string
	}{
		{token: "Bearer one"},
		{token: "Bearer two"},
		{token: "Bearer one", want: `"v1"`},
		{token: "", want: ""},
	} {
		cacheGet(t, tr, http.MethodGet, url, authorized(step.token))
		if got := s.lastValidator(); got != step.want {
			t.Errorf("a read as %q carried If-None-Match %q, want %q: an answer one credential read never revalidates for another", step.token, got, step.want)
		}
	}
}

// identityServer answers one ETag to every request, and 304 to one naming it, with
// a body naming what the request's identity reads: a held body served to another
// identity reads as that other identity's.
func identityCacheServer(t *testing.T, identity func(*http.Request) string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"shared"`)
		if r.Header.Get("If-None-Match") == `"shared"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, identity(r))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestARequestsIdentityIsKeyedHoweverItIsWritten(t *testing.T) {
	for _, test := range []struct {
		identity func(*http.Request) string
		set      func(r *http.Request, value string)
		name     string
	}{
		{
			name:     "a_lowercase_authorization",
			identity: func(r *http.Request) string { return r.Header.Get("Authorization") },
			set:      func(r *http.Request, v string) { r.Header["authorization"] = []string{v} },
		},
		{
			name:     "a_lowercase_accept",
			identity: func(r *http.Request) string { return r.Header.Get("Accept") },
			set:      func(r *http.Request, v string) { r.Header["accept"] = []string{v} },
		},
		{
			name:     "another_host",
			identity: func(r *http.Request) string { return r.Host },
			set:      func(r *http.Request, v string) { r.Host = v },
		},
		{
			name:     "another_user_agent",
			identity: func(r *http.Request) string { return r.Header.Get("User-Agent") },
			set:      func(r *http.Request, v string) { r.Header.Set("User-Agent", v) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			url := identityCacheServer(t, test.identity)
			tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
			for _, value := range []string{"one.example", "two.example"} {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
				if err != nil {
					t.Fatal(err)
				}
				test.set(req, value)
				resp, err := tr.RoundTrip(req)
				if err != nil {
					t.Fatalf("the read as %q = %v", value, err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(body) != value {
					t.Errorf("the read as %q answered %q, want its own answer", value, body)
				}
			}
		})
	}
}

// A credential next sets is on no request the transport keyed, so an answer to it
// is neither served from nor kept for that key.
func TestACredentialNextSetsIsNeverShared(t *testing.T) {
	for _, test := range []struct {
		report func(*http.Response)
		name   string
	}{
		{name: "an_answer_reporting_its_request", report: func(*http.Response) {}},
		{name: "an_answer_naming_no_request", report: func(resp *http.Response) { resp.Request = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			url := identityCacheServer(t, func(r *http.Request) string { return r.Header.Get("Authorization") })
			var token atomic.Value
			next := cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				r.Header.Set("Authorization", token.Load().(string))
				resp, err := http.DefaultTransport.RoundTrip(r)
				if err == nil {
					test.report(resp)
				}
				return resp, err
			})
			tr := newCacheTransport(t, next, httpclient.CacheConfig{})
			for _, value := range []string{"Bearer one", "Bearer two"} {
				token.Store(value)
				if _, _, body := cacheGet(t, tr, http.MethodGet, url, nil); body != value {
					t.Errorf("the read next sent as %q answered %q, want its own answer", value, body)
				}
			}
			if got := tr.State(); got.Hits != 0 || got.Bytes != 0 {
				t.Errorf("State = %+v, want no hit and nothing held for a request next rewrote", got)
			}
		})
	}
}

func TestALowercaseNoStoreIsNeverKept(t *testing.T) {
	s, url := newCacheServer(t, `{}`)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	for range 2 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header["cache-control"] = []string{"no-store"}
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if got := s.lastValidator(); got != "" || tr.State().Bytes != 0 {
		t.Errorf("a lowercase no-store read carried If-None-Match %q and left %d bytes held, want none and nothing", got, tr.State().Bytes)
	}
}

func TestAServedAnswerCarriesNoCodingOverItsHeldBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Encoding", "gzip")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, `{"n":1}`)
		_ = zw.Close()
	}))
	t.Cleanup(srv.Close)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	for i := range 2 {
		status, header, body := cacheGet(t, tr, http.MethodGet, srv.URL, nil)
		if status != http.StatusOK || body != `{"n":1}` {
			t.Fatalf("read %d = %d %q, want 200 and the decoded body", i, status, body)
		}
		if got := header.Values("Content-Encoding"); len(got) != 0 {
			t.Errorf("read %d carries Content-Encoding %q over its decoded body, want none", i, got)
		}
	}
	if got := tr.State().Hits; got != 1 {
		t.Errorf("the reads hit %d time(s), want the second served", got)
	}
}

func TestAHeldFieldKeepsEachOfItsLines(t *testing.T) {
	s, url := newCacheServer(t, `[]`)
	s.header = http.Header{"Set-Cookie": {"a=1; Path=/", "b=2; Path=/"}}
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{Held: []httpclient.HeldField{{Name: "set-cookie"}}})
	cacheGet(t, tr, http.MethodGet, url, nil)
	for i := range 2 {
		_, header, _ := cacheGet(t, tr, http.MethodGet, url, nil)
		if got, want := header.Values("Set-Cookie"), []string{"a=1; Path=/", "b=2; Path=/"}; !slices.Equal(got, want) {
			t.Errorf("served read %d's Set-Cookie lines = %q, want %q", i, got, want)
		}
		// A caller may rewrite the header it receives; the held lines stay its own.
		header["Set-Cookie"][0] = "rewritten"
	}
	if got := tr.State().Hits; got != 2 {
		t.Errorf("the reads hit %d time(s), want both repeats served", got)
	}
}

func TestOnlyTheDeclaredFieldsAreHeld(t *testing.T) {
	for _, test := range []struct {
		name        string
		held        []httpclient.HeldField
		notModified http.Header
		// later is what a third read's 304 carries; nil sends no third read.
		later      http.Header
		wantLink   string
		wantCustom string
	}{
		{name: "a_declared_field_the_304_omits_keeps_the_held_value", held: []httpclient.HeldField{{Name: "Link"}}, wantLink: "<page2>"},
		{
			name: "a_declared_field_the_304_carries_takes_the_304s_value", held: []httpclient.HeldField{{Name: "Link"}},
			notModified: http.Header{"Link": {"<page3>"}}, wantLink: "<page3>",
		},
		{
			name: "a_declared_field_one_304_replaced_and_the_next_omits", held: []httpclient.HeldField{{Name: "Link"}},
			notModified: http.Header{"Link": {"<page3>"}}, later: http.Header{}, wantLink: "<page3>",
		},
		{name: "an_undeclared_field_is_never_held"},
		{name: "an_undeclared_field_the_304_carries_is_the_304s", notModified: http.Header{"X-Custom": {"live"}}, wantCustom: "live"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newCacheServer(t, `[]`)
			s.header = http.Header{"Link": {"<page2>"}, "X-Custom": {"held"}}
			s.notModified = test.notModified
			tr := newCacheTransport(t, nil, httpclient.CacheConfig{Held: test.held})
			cacheGet(t, tr, http.MethodGet, url, nil)
			status, header, body := cacheGet(t, tr, http.MethodGet, url, nil)
			wantHits := 1
			if test.later != nil {
				s.set(func(s *cacheServer) { s.notModified = test.later })
				status, header, body = cacheGet(t, tr, http.MethodGet, url, nil)
				wantHits = 2
			}
			if status != http.StatusOK || body != `[]` {
				t.Fatalf("the revalidated read = %d %q, want the held answer served", status, body)
			}
			if got := header.Get("Link"); got != test.wantLink {
				t.Errorf("the served Link = %q, want %q", got, test.wantLink)
			}
			if got := header.Get("X-Custom"); got != test.wantCustom {
				t.Errorf("the served X-Custom = %q, want %q", got, test.wantCustom)
			}
			if got := tr.State().Hits; got != wantHits {
				t.Errorf("the reads hit %d time(s), want %d", got, wantHits)
			}
		})
	}
}

func TestSavedFollowsWhatTheServerIsDeclaredToBill(t *testing.T) {
	for _, test := range []struct {
		name      string
		token     string
		free      bool
		wantSaved int
	}{
		{name: "free_and_authorized", free: true, token: "Bearer t", wantSaved: 1},
		{name: "billed", free: false, token: "Bearer t"},
		{name: "free_but_anonymous", free: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, url := newCacheServer(t, `{}`)
			tr := newCacheTransport(t, nil, httpclient.CacheConfig{FreeNotModified: test.free})
			for range 2 {
				cacheGet(t, tr, http.MethodGet, url, authorized(test.token))
			}
			if got := tr.State(); got.Hits != 1 || got.Saved != test.wantSaved {
				t.Errorf("State = %+v, want 1 hit and %d saved", got, test.wantSaved)
			}
		})
	}
}

// statusRecorder is an attempt observer wrapped around the wire: it sees each
// request that reaches the server, and the status the server answered.
type statusRecorder struct {
	next http.RoundTripper
	got  []int
	mu   sync.Mutex
}

func (o *statusRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := o.next.RoundTrip(r)
	if err == nil {
		o.mu.Lock()
		o.got = append(o.got, resp.StatusCode)
		o.mu.Unlock()
	}
	return resp, err
}

func TestAnObserverBeneathSeesEveryWireAttemptAsTheServerAnswered(t *testing.T) {
	for _, test := range []struct {
		name      string
		always304 int
		want      []int
	}{
		{name: "a_revalidated_read", want: []int{http.StatusOK, http.StatusNotModified}},
		{name: "a_validatorless_304_resent", always304: 1, want: []int{http.StatusNotModified, http.StatusOK, http.StatusNotModified}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newCacheServer(t, `{}`)
			s.always304 = test.always304
			observer := &statusRecorder{next: http.DefaultTransport}
			tr := newCacheTransport(t, observer, httpclient.CacheConfig{})
			for i := range 2 {
				if status, _, body := cacheGet(t, tr, http.MethodGet, url, nil); status != http.StatusOK || body != `{}` {
					t.Errorf("read %d = %d %q, want 200 and the server's body", i, status, body)
				}
			}
			if !slices.Equal(observer.got, test.want) || len(observer.got) != len(s.requests()) {
				t.Errorf("the observer saw %v over %d request(s), want %v, one per request", observer.got, len(s.requests()), test.want)
			}
		})
	}
}

func TestOnlyAGetIsCached(t *testing.T) {
	s, url := newCacheServer(t, `{}`)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	for range 2 {
		cacheGet(t, tr, http.MethodPost, url, nil)
	}
	if got := s.lastValidator(); got != "" {
		t.Errorf("a repeated POST carried If-None-Match %q, want none", got)
	}
	if got := tr.State(); got != (httpclient.CacheState{}) {
		t.Errorf("State after two POSTs = %+v, want nothing counted or held", got)
	}
}

func TestAnAnswerNotKeptForgetsTheHeldOne(t *testing.T) {
	for _, test := range []struct {
		name   string
		second func(*cacheServer)
	}{
		{name: "an_error", second: func(s *cacheServer) { s.status = http.StatusInternalServerError }},
		{name: "a_no_store_answer", second: func(s *cacheServer) { s.etag = `"v2"`; s.header.Set("Cache-Control", "no-store") }},
		{name: "an_answer_with_no_validator", second: func(s *cacheServer) { s.etag = "" }},
		{name: "an_encoded_body", second: func(s *cacheServer) { s.etag = `"v2"`; s.header.Set("Content-Encoding", "br") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newCacheServer(t, `{}`)
			tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
			cacheGet(t, tr, http.MethodGet, url, nil)
			s.set(test.second)
			cacheGet(t, tr, http.MethodGet, url, nil)
			if got := tr.State().Bytes; got != 0 {
				t.Errorf("after %s the cache holds %d bytes, want none", test.name, got)
			}
			s.set(func(s *cacheServer) { s.status, s.etag = http.StatusOK, `"v1"` })
			cacheGet(t, tr, http.MethodGet, url, nil)
			if got := s.lastValidator(); got != "" {
				t.Errorf("the read after %s carried If-None-Match %q, want none", test.name, got)
			}
		})
	}
}

func TestARequestForbiddingStorageIsNeverRevalidated(t *testing.T) {
	s, url := newCacheServer(t, `{}`)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	for range 2 {
		cacheGet(t, tr, http.MethodGet, url, http.Header{"Cache-Control": {"no-store"}})
	}
	if got := s.lastValidator(); got != "" {
		t.Errorf("a no-store read carried If-None-Match %q, want none", got)
	}
	if got := tr.State(); got.Bytes != 0 {
		t.Errorf("after no-store reads the cache holds %d bytes, want none", got.Bytes)
	}
}

func TestAnUnvouchedNotModifiedIsSentOnceMore(t *testing.T) {
	for _, test := range []struct {
		name       string
		header     http.Header
		cfg        httpclient.CacheConfig
		always304  int
		wantStatus int
		wantSent   int
	}{
		{name: "a_304_to_no_validator", always304: 1, wantStatus: http.StatusOK, wantSent: 2},
		{name: "a_second_304", always304: 2, wantStatus: http.StatusNotModified, wantSent: 2},
		{name: "with_the_cache_off", always304: 1, cfg: httpclient.CacheConfig{MaxBytes: -1}, wantStatus: http.StatusOK, wantSent: 2},
		{name: "on_a_no_store_read", always304: 1, header: http.Header{"Cache-Control": {"no-store"}}, wantStatus: http.StatusOK, wantSent: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newCacheServer(t, `{}`)
			s.always304 = test.always304
			tr := newCacheTransport(t, nil, test.cfg)
			status, _, _ := cacheGet(t, tr, http.MethodGet, url, test.header)
			if status != test.wantStatus || len(s.requests()) != test.wantSent {
				t.Errorf("the read = %d over %d request(s), want %d over %d", status, len(s.requests()), test.wantStatus, test.wantSent)
			}
			if got := s.requests()[len(s.requests())-1].Header.Get("If-None-Match"); got != "" {
				t.Errorf("the resend carried If-None-Match %q, want none", got)
			}
		})
	}
}

// A conditional request is one with a validator line carrying text, whatever its
// other lines: an empty list element names nothing (RFC 9110 section 5.6.1).
func TestACallersOwnConditionalRequestPassesThrough(t *testing.T) {
	for _, test := range []struct {
		header http.Header
		name   string
		held   bool
	}{
		{name: "one_validator", header: http.Header{"If-None-Match": {`"v1"`}}},
		{name: "a_validator_after_an_empty_line", header: http.Header{"If-None-Match": {"", `"v1"`}}},
		{name: "a_validator_after_an_empty_line_over_a_held_answer", header: http.Header{"If-None-Match": {"", `"v1"`}}, held: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newCacheServer(t, `{}`)
			tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
			if test.held {
				cacheGet(t, tr, http.MethodGet, url, nil)
			}
			before, sent := tr.State(), len(s.requests())
			status, _, _ := cacheGet(t, tr, http.MethodGet, url, test.header)
			got := s.requests()
			if status != http.StatusNotModified || len(got) != sent+1 {
				t.Fatalf("the caller's conditional read = %d over %d request(s), want its own 304 over one", status, len(got)-sent)
			}
			if lines := got[sent].Header.Values("If-None-Match"); !slices.Equal(lines, test.header.Values("If-None-Match")) {
				t.Errorf("the caller's conditional read reached the server with If-None-Match %q, want its own %q", lines, test.header.Values("If-None-Match"))
			}
			if after := tr.State(); after != before {
				t.Errorf("State after the caller's own conditional read = %+v, want %+v: nothing counted or changed", after, before)
			}
		})
	}
}

func TestAnAnswerOverItsShareOfTheBoundIsNotKept(t *testing.T) {
	big := strings.Repeat("x", 4096)
	s, url := newCacheServer(t, big)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{MaxBytes: 8192})
	for i := range 2 {
		if _, _, body := cacheGet(t, tr, http.MethodGet, url, nil); body != big {
			t.Errorf("read %d answered %d bytes, want the server's %d", i, len(body), len(big))
		}
	}
	if got := s.lastValidator(); got != "" {
		t.Errorf("the read after an answer over a quarter of the bound carried If-None-Match %q, want none", got)
	}
	if got := tr.State().Bytes; got != 0 {
		t.Errorf("the cache holds %d bytes, want none", got)
	}
}

func TestANegativeBoundSwitchesTheCacheOff(t *testing.T) {
	s, url := newCacheServer(t, `{}`)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{MaxBytes: -1})
	for range 2 {
		cacheGet(t, tr, http.MethodGet, url, nil)
	}
	if got := s.lastValidator(); got != "" {
		t.Errorf("a read with the cache off carried If-None-Match %q, want none", got)
	}
	if got := tr.State(); got != (httpclient.CacheState{}) {
		t.Errorf("State with the cache off = %+v, want nothing counted or held", got)
	}
}

func TestANotModifiedNamingAnotherRepresentationIsReadInFull(t *testing.T) {
	s, url := newCacheServer(t, `{"n":1}`)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	cacheGet(t, tr, http.MethodGet, url, nil)
	s.set(func(s *cacheServer) {
		s.body, s.always304 = `{"n":2}`, 1
		s.notModified = http.Header{"Etag": {`"v2"`}}
	})
	status, _, body := cacheGet(t, tr, http.MethodGet, url, nil)
	if status != http.StatusOK || body != `{"n":2}` {
		t.Errorf("a read answered 304 for another ETag = %d %q, want the server's current body", status, body)
	}
	if got := len(s.requests()); got != 3 {
		t.Errorf("the server saw %d requests, want 3: the 304 naming another representation is sent once more", got)
	}
}

func TestAReadWhoseContextEndsKeepsNothing(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var validators []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		validators = append(validators, r.Header.Get("If-None-Match"))
		first := len(validators) == 1
		mu.Unlock()
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"n":`)
		if first {
			w.(http.Flusher).Flush()
			close(started)
			<-release
		}
		_, _ = io.WriteString(w, `1}`)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a read whose context ended = %v, want context.Canceled", err)
	}
	cacheGet(t, tr, http.MethodGet, srv.URL, nil)
	mu.Lock()
	defer mu.Unlock()
	if got := validators[len(validators)-1]; got != "" {
		t.Errorf("the read after a cancelled one carried If-None-Match %q, want none", got)
	}
}

func TestTheLeastRecentlyUsedEntryIsEvictedFirst(t *testing.T) {
	s, url := newCacheServer(t, `{}`)
	probe := newCacheTransport(t, nil, httpclient.CacheConfig{})
	cacheGet(t, probe, http.MethodGet, url+"/a", nil)
	entry := probe.State().Bytes
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{MaxBytes: 4*entry + entry/2})
	for _, path := range []string{"/a", "/b", "/c", "/d", "/a", "/e"} {
		cacheGet(t, tr, http.MethodGet, url+path, nil)
	}
	for _, test := range []struct {
		path, want string
	}{
		{path: "/b", want: ""},
		{path: "/a", want: `"v1"`},
		{path: "/e", want: `"v1"`},
	} {
		cacheGet(t, tr, http.MethodGet, url+test.path, nil)
		if got := s.lastValidator(); got != test.want {
			t.Errorf("a read of %s after a fifth entry carried If-None-Match %q, want %q: the least recently used entry goes first", test.path, got, test.want)
		}
	}
}

// endingBody ends its read's context as it answers the last of its bytes.
type endingBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (b *endingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		b.cancel()
	}
	return n, err
}

func (*endingBody) Close() error { return nil }

func TestAReadWhoseContextEndsAsItsAnswerIsReadKeepsNothing(t *testing.T) {
	type cancelKey struct{}
	for _, test := range []struct {
		name   string
		status int
		sent   int
	}{
		{name: "a_200", status: http.StatusOK, sent: 1},
		{name: "a_304_twice", status: http.StatusNotModified, sent: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var validators []string
			wire := cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
				validators = append(validators, r.Header.Get("If-None-Match"))
				cancel, _ := r.Context().Value(cancelKey{}).(context.CancelFunc)
				return &http.Response{
					StatusCode: test.status, Header: http.Header{"Etag": {`"v1"`}},
					Body: &endingBody{Reader: strings.NewReader(`{}`), cancel: cancel}, Request: r,
				}, nil
			})
			tr := newCacheTransport(t, wire, httpclient.CacheConfig{})
			for range 2 {
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				ctx = context.WithValue(ctx, cancelKey{}, cancel)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://server.example/x", nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := tr.RoundTrip(req)
				if err == nil {
					resp.Body.Close()
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("a read whose context ended with its answer = %v, want context.Canceled", err)
				}
			}
			if len(validators) != 2*test.sent || slices.ContainsFunc(validators, func(v string) bool { return v != "" }) || tr.State().Bytes != 0 {
				t.Errorf("the reads carried %q and the cache holds %d bytes, want %d request(s) with no validator and nothing held", validators, tr.State().Bytes, 2*test.sent)
			}
		})
	}
}

// A wire attempt that fails once the request's context has ended answers the
// context's error, as an answer read after it ended does, and keeps nothing.
func TestAWireFailureAfterTheContextEndedAnswersTheContextsError(t *testing.T) {
	type cancelKey struct{}
	wire := cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
		if cancel, ok := r.Context().Value(cancelKey{}).(context.CancelFunc); ok {
			cancel()
			return nil, errors.New("wire failed")
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"v1"`}},
			Body: io.NopCloser(strings.NewReader(`{}`)), Request: r,
		}, nil
	})
	tr := newCacheTransport(t, wire, httpclient.CacheConfig{})
	if _, _, body := cacheGet(t, tr, http.MethodGet, "http://server.example/x", nil); body != `{}` || tr.State().Bytes == 0 {
		t.Fatalf("Setup: the first read answered %q holding %d bytes, want its answer kept", body, tr.State().Bytes)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(context.WithValue(ctx, cancelKey{}, cancel), http.MethodGet, "http://server.example/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a wire failure after the context ended = %v, want context.Canceled", err)
	}
	if got := tr.State().Bytes; got != 0 {
		t.Errorf("the cache holds %d bytes after the failed read, want the key forgotten", got)
	}
}

type cacheRoundTrip func(*http.Request) (*http.Response, error)

func (f cacheRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// idleCloser is a round tripper that counts the calls to close its idle connections.
type idleCloser struct {
	http.RoundTripper
	closed int
}

func (c *idleCloser) CloseIdleConnections() { c.closed++ }

func TestClosingTheClientsIdleConnectionsReachesNext(t *testing.T) {
	next := &idleCloser{RoundTripper: http.DefaultTransport}
	(&http.Client{Transport: newCacheTransport(t, next, httpclient.CacheConfig{})}).CloseIdleConnections()
	if next.closed != 1 {
		t.Errorf("next's idle connections were closed %d time(s), want 1", next.closed)
	}
}

// A client following a redirect sends each hop through the transport as a
// request of its own, so each hop is revalidated against what its own URL
// answered.
func TestEachRedirectHopIsARequestOfItsOwn(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, "/old "+r.Header.Get("If-None-Match"))
		mu.Unlock()
		http.Redirect(w, r, "/new", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, "/new "+r.Header.Get("If-None-Match"))
		mu.Unlock()
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tr := newCacheTransport(t, nil, httpclient.CacheConfig{})
	for i := range 2 {
		if status, _, body := cacheGet(t, tr, http.MethodGet, srv.URL+"/old", nil); status != http.StatusOK || body != `{}` {
			t.Errorf("read %d through the hop = %d %q, want 200 and the hop's body", i, status, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"/old ", "/new ", "/old ", `/new "v1"`}; !slices.Equal(sent, want) {
		t.Errorf("the server saw %q, want %q: each hop is keyed by its own URL", sent, want)
	}
	if got := tr.State(); got.Hits != 1 || got.Misses != 3 {
		t.Errorf("State = %+v, want the second hop to /new served and every other hop a miss", got)
	}
}

// A body that fails while the transport reads it to keep reaches the caller as
// the server sent it: the bytes read, then the read's error. Nothing is kept or
// counted.
func TestABodyReadFailureReachesTheCaller(t *testing.T) {
	errReset := errors.New("connection reset")
	wire := cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"v1"`}},
			Body: io.NopCloser(&failOnce{Reader: strings.NewReader(`{"n":`), err: errReset}), Request: r,
		}, nil
	})
	tr := newCacheTransport(t, wire, httpclient.CacheConfig{})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://server.example/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip = %v, want the answer whose body fails", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != `{"n":` || !errors.Is(err, errReset) {
		t.Errorf("the caller read %q, %v, want the bytes the server sent and then its error", body, err)
	}
	if got := tr.State(); got != (httpclient.CacheState{}) {
		t.Errorf("State = %+v, want nothing kept or counted", got)
	}
}

// failOnce reads its Reader, then fails once with err, then ends, as a connection
// that resets does: a caller that reads it again sees EOF, not the failure.
type failOnce struct {
	io.Reader
	err    error
	failed bool
}

func (f *failOnce) Read(p []byte) (int, error) {
	n, err := f.Reader.Read(p)
	if errors.Is(err, io.EOF) && !f.failed {
		f.failed = true
		return n, f.err
	}
	return n, err
}

// A response next returns beside its error, as net/http's client does for a
// redirect its CheckRedirect refuses, reaches the caller with that error, its
// body closed, whether or not the read's context has ended by then.
func TestAResponseBesideAnErrorReachesTheCaller(t *testing.T) {
	errRefused := errors.New("redirect refused")
	for _, test := range []struct {
		want   error
		name   string
		cancel bool
	}{
		{name: "a_live_context", want: errRefused},
		{name: "an_ended_context", cancel: true, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			wire := cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
				if test.cancel {
					cancel()
				}
				return &http.Response{
					StatusCode: http.StatusFound, Header: http.Header{"Location": {"/elsewhere"}},
					Body: http.NoBody, Request: r,
				}, errRefused
			})
			tr := newCacheTransport(t, wire, httpclient.CacheConfig{})
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://server.example/x", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := tr.RoundTrip(req)
			if resp != nil {
				resp.Body.Close()
			}
			if !errors.Is(err, test.want) {
				t.Errorf("RoundTrip = %v, want %v", err, test.want)
			}
			if resp == nil || resp.Header.Get("Location") != "/elsewhere" {
				t.Fatalf("RoundTrip answered %+v, want the refused hop's response and its Location", resp)
			}
			if resp.Request != req {
				t.Errorf("the response reports request %p, want the caller's %p", resp.Request, req)
			}
			if got := tr.State(); got != (httpclient.CacheState{}) {
				t.Errorf("State = %+v, want nothing kept or counted", got)
			}
		})
	}
}
