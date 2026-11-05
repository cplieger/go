package httpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
)

// CacheReadConfig is what one read begins with.
type CacheReadConfig struct {
	// Discarded takes the header of an answer the read drops: a resent 304's,
	// and a 304 that fails the read. Nil ignores them.
	Discarded func(http.Header)
	// Variant names what the owner keeps of an answer, such as the Go type it
	// decodes into. Reads that differ only in Variant never serve each other.
	Variant string
	// Store looks the read up and keeps its answer, for a GET only: a request
	// sent by another method is neither looked up nor kept. It is false on a
	// cache that holds nothing.
	Store bool
	// Resend sends a 304 nothing held may serve once more without a validator.
	Resend bool
}

// errSentTwice is what a second [CacheRead.Send] fails with.
var errSentTwice = errors.New("httpclient: a CacheRead is sent once")

// CacheRead is one read through a [Cache], begun by [Cache.Begin]. It is not
// safe for concurrent use.
type CacheRead struct {
	err   error
	cache *Cache
	kept  *pageEntry
	cfg   CacheReadConfig
	rv    revalidation
	sent  bool
	done  bool
	hit   bool
}

// Begin starts one read.
func (c *Cache) Begin(cfg CacheReadConfig) *CacheRead {
	cfg.Store = cfg.Store && c.on()
	return &CacheRead{cache: c, cfg: cfg}
}

// Send runs the read's attempts through send: the first, and with Resend the one
// a 304 nothing held may serve forces. send builds the request, calls prepare on
// it after every other header is set, and sends what prepare returns, beneath a
// [CacheGuard]. prepare keys the request and, on the first attempt, sets the held
// entry's validator. Send answers the last attempt's response and error as send
// returned them, a refused redirect's response beside its error included; a 304
// whose body fails to read, with Resend unset or on the resend, fails with that
// read's error. A read is sent once; a second Send sends nothing and fails.
func (r *CacheRead) Send(send func(prepare func(*http.Request) *http.Request) (*http.Response, error)) (*http.Response, error) {
	if r.sent {
		return nil, errSentTwice
	}
	r.sent = true
	resp, rv, err := r.cache.exchange(r.cfg, send)
	r.rv = rv
	return resp, err
}

// Answered records that the owner read the answer Send returned in full, and
// reports whether State counts this read, as a hit or a miss.
func (r *CacheRead) Answered() bool {
	r.rv.answered = r.rv.cacheable
	return r.rv.answered
}

// Serve answers a 304 the held copy serves: the 304's header with each held
// field it omits, a copy of the held body, and ok. ok is false for any other
// answer, a 304 the cache refused included, which the owner treats as the 304 it
// is. A served 304 carrying no-store forgets the entry once the read finishes.
func (r *CacheRead) Serve(live http.Header) (header http.Header, body []byte, ok bool) {
	if r.rv.verdict != verdictServe || r.done {
		return nil, nil, false
	}
	header, body, r.kept = r.cache.serve(&r.rv, live)
	return header, body, true
}

// Keep keeps what kept returns for resp, a 200 the owner read in full as body,
// where the wire vouches for resp, body is the wire's body byte for byte, the
// answer carries a well-formed validator and no no-store, and kept succeeds. It
// keeps nothing otherwise, so nothing is kept from a body a round tripper above
// the guard replaced.
func (r *CacheRead) Keep(resp *http.Response, body []byte, kept func() ([]byte, error)) {
	if r.done || resp == nil || resp.StatusCode != http.StatusOK || !r.rv.vouched(resp, body) {
		return
	}
	r.kept = r.cache.keepable(resp, kept)
}

// Finish settles the read once, whether it succeeded (ok) or not. A context
// ended by then fails the read, which keeps nothing, and its error is returned.
// hit reports whether the held answer served the read. A second call changes
// nothing and returns the first's result.
func (r *CacheRead) Finish(ctx context.Context, ok bool) (hit bool, err error) {
	if r.done {
		return r.hit, r.err
	}
	r.done = true
	r.hit, r.err = r.cache.conclude(ctx, &r.rv, r.kept, ok)
	r.kept = nil
	return r.hit, r.err
}

// cacheMode is how one attempt treats the cache.
type cacheMode int

const (
	// cacheOff sends the request as built: a read not stored.
	cacheOff cacheMode = iota
	// cacheConditional looks the request up and sends the held entry's validator.
	cacheConditional
	// cacheUnconditional keys the request without looking it up, for the resend a
	// 304 nothing held may serve forces ([Cache.exchange]).
	cacheUnconditional
)

// verdict is what [judge] decided an answer does: [Cache.exchange] acts on a
// resend, and its owner on the rest.
type verdict int

const (
	// verdictNone is any answer but a 304, which its status settles.
	verdictNone verdict = iota
	// verdictServe answers a 304 from the held entry.
	verdictServe
	// verdictResend sends the read once more without a validator.
	verdictResend
	// verdictRefuse leaves a 304 for its owner to treat as the 304 it is.
	verdictRefuse
)

// revalidation is what one attempt knows of its request in the cache.
type revalidation struct {
	held      *pageEntry
	check     *wireCheck
	key       string
	gen       uint64
	verdict   verdict
	cacheable bool
	// credentialed says the keyed request carried an Authorization line, which
	// [Cache.serve] counts as saved on a server billing no 304.
	credentialed bool
	// answered says the server's answer was read as its owner reads one, which
	// [Cache.settle] counts.
	answered bool
	// disowned says a resend was not keyed as the attempt it replaced: key is that
	// attempt's, which the read forgets, keeping nothing.
	disowned bool
}

// vouched reports whether the wire vouches for resp and body as the keyed
// exchange's own: only such an answer is served or kept.
func (rv *revalidation) vouched(resp *http.Response, body []byte) bool {
	return rv.cacheable && rv.check.vouches(resp) && rv.check.carried(body)
}

// exchange is one read through the cache, the sequence [CacheRead] and
// [CacheRoundTripper] share: the first attempt in the mode the read takes, in the
// generation current as it begins, each answer judged by [judge], and a 304
// nothing held may serve sent once more without a validator, whether or not the
// read is stored. The resend belongs to the first attempt's read, whose key and
// generation the owner settles once ([Cache.settle]).
func (c *Cache) exchange(cfg CacheReadConfig, send func(func(*http.Request) *http.Request) (*http.Response, error)) (*http.Response, revalidation, error) {
	if cfg.Discarded == nil {
		cfg.Discarded = func(http.Header) {}
	}
	mode, gen := cacheOff, uint64(0)
	if cfg.Store {
		mode, gen = cacheConditional, c.generation()
	}
	resp, rv, err := c.attempt(cfg, send, mode, gen, nil)
	if err != nil || rv.verdict != verdictResend {
		return resp, rv, err
	}
	if rv.check == nil || rv.check.vouches(resp) {
		cfg.Discarded(resp.Header)
	}
	if mode == cacheConditional {
		mode = cacheUnconditional
	}
	return c.attempt(cfg, send, mode, gen, &rv)
}

// attempt is one send of the read and the verdict [judge] gives its answer. first
// is the attempt a resend replaces, nil on the first one, which alone may be
// resent.
func (c *Cache) attempt(cfg CacheReadConfig, send func(func(*http.Request) *http.Request) (*http.Response, error), mode cacheMode, gen uint64, first *revalidation) (*http.Response, revalidation, error) {
	rv := revalidation{gen: gen}
	prepare := func(r *http.Request) *http.Request {
		if mode == cacheOff {
			return r
		}
		return c.revalidate(r, cfg.Variant, mode, &rv)
	}
	resp, err := send(prepare)
	if first != nil && first.cacheable && rv.key != first.key {
		rv.key, rv.disowned = first.key, true
	}
	if err != nil {
		return resp, rv, err
	}
	rv.verdict, err = c.judge(&rv, resp, first == nil && cfg.Resend)
	if err != nil {
		cfg.Discarded(resp.Header)
		return nil, rv, err
	}
	return resp, rv, nil
}

// revalidate keys a stored request by its credential and variant, and in the
// conditional mode sets the held entry's validator on it. It answers the request
// to send, carrying the [wireCheck] [CacheGuard] holds every attempt and hop to.
// A request other than a GET, which [Cache.key] does not separate by method, and
// one carrying no-store (RFC 9111 section 5.2.1.5) are left unkeyed.
func (c *Cache) revalidate(r *http.Request, variant string, mode cacheMode, rv *revalidation) *http.Request {
	if r.Method != http.MethodGet || noStore(r.Header) {
		return r
	}
	cred := credential(r)
	rv.cacheable, rv.credentialed = true, cred != ""
	rv.key = c.key(cred, variant, r)
	if mode == cacheConditional {
		c.condition(r, rv)
	}
	rv.check = newWireCheck(r)
	return r.WithContext(context.WithValue(r.Context(), wireCheckKey{}, rv.check))
}

// condition sets the validator of the entry held for rv's key on r, and notes
// that entry as the one a 304 may serve.
func (c *Cache) condition(r *http.Request, rv *revalidation) {
	e := c.lookup(rv.key)
	if e == nil {
		return
	}
	if e.page.ETag != "" {
		r.Header.Set("If-None-Match", e.page.ETag)
	} else {
		r.Header.Set("If-Modified-Since", e.page.LastModified)
	}
	rv.held = e.page
}

// judge is the one decision of what a 304 does; any other answer is
// [verdictNone]. It reads a 304's body in full and puts it back for the owner.
// Only the wire's own 304, in the generation the read began in, to a validator a
// held entry sent and naming that entry ([pageEntry.answers]) serves. Any other
// is resent where resend allows, and refused otherwise; a body that fails to read
// is resent likewise, and is otherwise the read's failure.
func (c *Cache) judge(rv *revalidation, resp *http.Response, resend bool) (verdict, error) {
	if resp.StatusCode != http.StatusNotModified {
		return verdictNone, nil
	}
	body, err := ReadLimitedBody(resp.Body, maxNotModifiedBytes)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	switch {
	case err == nil && rv.held != nil && rv.vouched(resp, body) && rv.held.answers(c.held, resp.Header) && c.current(rv.gen):
		return verdictServe, nil
	case resend:
		return verdictResend, nil
	}
	return verdictRefuse, err
}

// serve answers a 304 [judge] found the held entry serves: the live header with
// each held field it omits, a copy of the held body, and the entry kept from here
// on, nil where that header says no-store, which forgets the entry after this
// answer (RFC 9111 section 5.2.2.5).
func (c *Cache) serve(rv *revalidation, live http.Header) (http.Header, []byte, *pageEntry) {
	p := rv.held.refreshed(c.held, live)
	if c.freeNotMod && rv.credentialed {
		c.saved.Add(1)
	}
	header := p.served(c.held, live)
	if noStore(header) {
		return header, slices.Clone(p.Body), nil
	}
	return header, slices.Clone(p.Body), p
}

// keepable is the entry a vouched 200 leaves for a later revalidation, its body
// what body answers, nil where the answer carries no well-formed validator, says
// no-store, or body fails.
func (c *Cache) keepable(resp *http.Response, body func() ([]byte, error)) *pageEntry {
	if resp.StatusCode != http.StatusOK || noStore(resp.Header) {
		return nil
	}
	etag, modified := entityTag(resp.Header), httpDate(resp.Header)
	if etag == "" && modified == "" {
		return nil
	}
	b, err := body()
	if err != nil {
		return nil
	}
	return &pageEntry{
		ETag: strings.Clone(etag), LastModified: strings.Clone(modified), Body: b, Fields: c.held.hold(resp.Header),
	}
}

// settle is the one outcome of a read, called once when it ends, whether it
// succeeded (ok) or not: it keeps the entry the read vouched for and forgets the
// key on every other outcome, a disowned resend's included, and counts an
// answered read. It reports whether the held answer served the read.
func (c *Cache) settle(rv *revalidation, kept *pageEntry, ok bool) bool {
	if rv.disowned || !ok {
		kept = nil
	}
	if rv.key != "" {
		c.finish(rv.key, rv.gen, kept)
	}
	if !rv.answered {
		return false
	}
	hit := rv.verdict == verdictServe && ok
	c.tally(hit)
	return hit
}

// conclude ends a read whose answer its owner has read: a context ended by then
// fails the read, which keeps nothing whatever the verdict, and its error is
// returned; [Cache.settle] then settles it.
func (c *Cache) conclude(ctx context.Context, rv *revalidation, kept *pageEntry, ok bool) (bool, error) {
	err := ctx.Err()
	if err != nil {
		ok = false
	}
	return c.settle(rv, kept, ok), err
}
