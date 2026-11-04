package httpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/go/httpclient"
)

// The owner tests are written from RFC 9110 section 13.1 (the validators a client
// replays), RFC 9111 sections 4.3.4 and 5.2 (what a 304 updates, and no-store),
// and GitHub's statement that a 304 to an authorized conditional request does not
// count against the primary rate limit
// (docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api).

// account is the body every revalidating origin answers with.
type account struct {
	Login string `json:"login"`
}

// origin is a revalidating server: it answers its current body with an ETag
// naming the version, and 304 to a request carrying that ETag.
type origin struct {
	header      http.Header
	got         []http.Header
	body        string
	etag        string
	modified    string
	status      int
	notModified int
	mu          sync.Mutex
}

func newOrigin(body, etag string) *origin {
	return &origin{body: body, etag: etag, status: http.StatusOK, header: http.Header{}}
}

func (o *origin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, r.Header.Clone())
	maps.Copy(w.Header(), o.header)
	if o.etag != "" {
		w.Header().Set("ETag", o.etag)
	}
	if o.modified != "" {
		w.Header().Set("Last-Modified", o.modified)
	}
	w.Header().Set("Content-Type", "application/json")
	conditional := (o.etag != "" && slices.Contains(r.Header.Values("If-None-Match"), o.etag)) ||
		(o.modified != "" && r.Header.Get("If-Modified-Since") == o.modified)
	if o.status == http.StatusOK && (conditional || o.notModified > 0) {
		if o.notModified > 0 {
			o.notModified--
		}
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(o.status)
	_, _ = io.WriteString(w, o.body)
}

func (o *origin) set(f func(*origin)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f(o)
}

func (o *origin) requests() []http.Header {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.got)
}

func (o *origin) last() http.Header {
	got := o.requests()
	if len(got) == 0 {
		return http.Header{}
	}
	return got[len(got)-1]
}

func serveOrigin(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// layer wraps a round tripper, as an observer does.
type layer func(http.RoundTripper) http.RoundTripper

// ownerConfig is how a test owner is built. Its chain, from the client down, is
// request, a one-attempt RetryRoundTripper, attempt, a CacheGuard, and wire.
type ownerConfig struct {
	request layer
	attempt layer
	wire    http.RoundTripper
	token   func(context.Context) (string, error)
	cache   httpclient.CacheConfig
	noGuard bool
}

// owner reads its own answers through a Cache, as a client that decodes them
// does: it builds each request, sends it beneath a CacheGuard, decodes the body
// into a Go value and keeps that value re-encoded.
type owner struct {
	cache     *httpclient.Cache
	client    *http.Client
	token     func(context.Context) (string, error)
	base      string
	discarded []http.Header
	mu        sync.Mutex
}

func newOwner(base string, cfg ownerConfig) *owner {
	rt := http.DefaultTransport
	if cfg.wire != nil {
		rt = cfg.wire
	}
	if !cfg.noGuard {
		rt = httpclient.CacheGuard(rt)
	}
	if cfg.attempt != nil {
		rt = cfg.attempt(rt)
	}
	rt = httpclient.NewRetryRoundTripper(rt, httpclient.TransportConfig{MaxAttempts: -1})
	if cfg.request != nil {
		rt = cfg.request(rt)
	}
	return &owner{cache: httpclient.NewCache(cfg.cache), client: &http.Client{Transport: rt}, token: cfg.token, base: base}
}

// ownerRead is one read an owner makes.
type ownerRead struct {
	header   http.Header
	into     any
	path     string
	maxBytes int64
	unstored bool
	noResend bool
}

// ownerAnswer is what an owner made of one read.
type ownerAnswer struct {
	header     http.Header
	status     int
	served     bool
	answered   bool
	hit        bool
	redirected bool
}

var errStatus = errors.New("unexpected status")

func (o *owner) discard(h http.Header) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.discarded = append(o.discarded, h.Clone())
}

func (o *owner) dropped() []http.Header {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.discarded)
}

// get is one stored read of path, decoded into an account.
func (o *owner) get(t *testing.T, path string) (account, ownerAnswer, error) {
	t.Helper()
	var out account
	answer, err := o.do(t.Context(), ownerRead{path: path, into: &out})
	return out, answer, err
}

func (o *owner) do(ctx context.Context, req ownerRead) (ownerAnswer, error) {
	into := req.into
	if into == nil {
		into = &account{}
	}
	read := o.cache.Begin(httpclient.CacheReadConfig{
		Variant: fmt.Sprintf("%T", into), Store: !req.unstored, Resend: !req.noResend, Discarded: o.discard,
	})
	target := o.base + req.path
	resp, err := read.Send(func(prepare func(*http.Request) *http.Request) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		r.Header.Set("Accept", "application/json")
		if o.token != nil {
			token, err := o.token(ctx)
			if err != nil {
				return nil, err
			}
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
		}
		for name, lines := range req.header {
			r.Header[name] = slices.Clone(lines)
		}
		return o.client.Do(prepare(r))
	})
	answer, err := readAnswer(read, resp, err, req, into)
	hit, ended := read.Finish(ctx, err == nil)
	if err == nil && ended != nil {
		err = ended
	}
	answer.hit = hit
	answer.redirected = resp != nil && resp.Request != nil && resp.Request.URL.String() != target
	return answer, err
}

func readAnswer(read *httpclient.CacheRead, resp *http.Response, err error, req ownerRead, into any) (ownerAnswer, error) {
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return ownerAnswer{}, err
	}
	limit := req.maxBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	wire, err := httpclient.ReadLimitedBody(resp.Body, limit)
	if err != nil {
		return ownerAnswer{}, err
	}
	out := ownerAnswer{header: resp.Header, status: resp.StatusCode, answered: read.Answered()}
	body := wire
	if header, held, ok := read.Serve(resp.Header); ok {
		out.header, out.status, out.served, body = header, http.StatusOK, true, held
	} else if resp.StatusCode >= 300 {
		return out, fmt.Errorf("%w %d", errStatus, resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return out, err
	}
	read.Keep(resp, wire, func() ([]byte, error) { return json.Marshal(into) })
	return out, nil
}

// answerRewrite is an observer that changes the answer its inner transport gave.
func answerRewrite(change func(*http.Response)) layer {
	return func(next http.RoundTripper) http.RoundTripper {
		return cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
			resp, err := next.RoundTrip(r)
			if err == nil {
				change(resp)
			}
			return resp, err
		})
	}
}

// requestRewrite is an observer that changes the request it passes on.
func requestRewrite(change func(*http.Request)) layer {
	return func(next http.RoundTripper) http.RoundTripper {
		return cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
			r = r.Clone(r.Context())
			change(r)
			return next.RoundTrip(r)
		})
	}
}

// observerPositions is the two places an observer sits above the guard: above the
// retry round tripper, and between it and the guard.
var observerPositions = []struct {
	config func(layer) ownerConfig
	name   string
}{
	{name: "request_observer", config: func(l layer) ownerConfig { return ownerConfig{request: l} }},
	{name: "attempt_observer", config: func(l layer) ownerConfig { return ownerConfig{attempt: l} }},
}
