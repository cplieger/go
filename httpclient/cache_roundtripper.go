package httpclient

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
)

// rawVariant is the variant a [CacheRoundTripper] keys every read by: the body
// as sent.
const rawVariant = "[]byte"

// errNotKept is a body a [CacheRoundTripper] reads and does not keep.
var errNotKept = errors.New("body not kept")

// CacheRoundTripper is an [http.RoundTripper] that revalidates the GET requests
// it carries, with a [Cache] of its own. A served answer reaches the caller as a
// 200 with the held body, the 304's headers but its Content-Length and
// Content-Encoding, and the held fields. It is safe for concurrent use.
type CacheRoundTripper struct {
	next  http.RoundTripper
	cache *Cache
}

// NewCacheRoundTripper builds a [CacheRoundTripper] sending through next, which
// an attempt observer wraps to see each request that reaches the server. An
// answer from next is served from or kept only where its Request reports the
// request next was given, as [http.Transport] does, with its method, URL, host
// and every header unchanged, so a round tripper that sets a credential or any
// other header wraps the CacheRoundTripper instead. Nil next means
// [http.DefaultTransport].
func NewCacheRoundTripper(next http.RoundTripper, cfg CacheConfig) *CacheRoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &CacheRoundTripper{next: next, cache: NewCache(cfg)}
}

// RoundTrip revalidates a GET carrying no validator of its own and sends every
// other request through next as given. A read whose context has ended by the
// time its answer is read keeps nothing and fails with the context's error. A
// response a next that breaks the RoundTripper contract returns beside an error
// is returned beside that error with its body closed; [http.Client] ignores it.
func (t *CacheRoundTripper) RoundTrip(orig *http.Request) (*http.Response, error) {
	if orig.Method != http.MethodGet {
		return t.next.RoundTrip(orig)
	}
	r := orig.Clone(orig.Context())
	r.Header = canonicalHeader(orig.Header)
	if carriesValidator(r.Header) {
		return t.next.RoundTrip(orig)
	}
	read := t.cache.Begin(CacheReadConfig{Variant: rawVariant, Store: true, Resend: true})
	guard := cacheGuard{next: t.next}
	resp, err := read.Send(func(prepare func(*http.Request) *http.Request) (*http.Response, error) {
		return guard.RoundTrip(prepare(r.Clone(r.Context())))
	})
	if err != nil {
		if _, ended := read.Finish(r.Context(), false); ended != nil {
			err = ended
		}
		if resp != nil {
			if resp.Body != nil {
				resp.Body.Close()
			}
			resp.Request = orig
		}
		return resp, err
	}
	out, ok := t.answer(read, resp)
	out.Request = orig
	if _, err := read.Finish(r.Context(), ok); err != nil {
		out.Body.Close()
		return nil, err
	}
	return out, nil
}

// answer is what the caller receives for the read's last answer, and whether the
// read succeeded: a served 304 is the held answer, a refused one reaches the
// caller as the 304 it is, and any other answer is the server's, read here only
// where it may be kept.
func (t *CacheRoundTripper) answer(read *CacheRead, resp *http.Response) (*http.Response, bool) {
	stored := read.Answered()
	if header, body, ok := read.Serve(resp.Header); ok {
		return served(resp, header, body), true
	}
	if resp.StatusCode == http.StatusNotModified {
		return resp, false
	}
	if !stored || resp.StatusCode != http.StatusOK {
		return resp, true
	}
	var readErr error
	// The body is read only once keepable has checked the validators, so the
	// vouching that Keep does first happens inside the read here.
	read.kept = t.cache.keepable(resp, func() ([]byte, error) {
		// A served answer carries no coding ([served]), so an encoded body would
		// reach the caller undecodable.
		if len(resp.Header.Values("Content-Encoding")) > 0 {
			return nil, errNotKept
		}
		body, complete, err := readKept(resp, t.cache.max/entryShare)
		if readErr = err; err != nil || !complete || !read.rv.vouched(resp, body) {
			return nil, errNotKept
		}
		return bytes.Clone(body), nil
	})
	if readErr != nil {
		read.rv.answered = false
	}
	return resp, readErr == nil
}

// canonicalHeader is h with every field under its canonical name, its lines in
// sorted-name order, the order HTTP/1.1 writes them in. Every reader of a
// request's header looks a field up by that name and the key folds one name per
// field, while net/http sends a field under any name.
func canonicalHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for _, name := range slices.Sorted(maps.Keys(h)) {
		canonical := http.CanonicalHeaderKey(name)
		out[canonical] = append(out[canonical], h[name]...)
	}
	return out
}

// readKept reads up to limit bytes of resp's body, reporting whether that was
// all of it, and puts back a body that replays them followed by the rest, or by
// the read's error, so the caller reads exactly what the server sent.
func readKept(resp *http.Response, limit int64) (body []byte, complete bool, err error) {
	orig := resp.Body
	body, err = io.ReadAll(io.LimitReader(orig, limit+1))
	var tail io.Reader = orig
	if err != nil {
		tail = &failedReader{err: err}
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), tail), orig}
	return body, int64(len(body)) <= limit, err
}

// failedReader answers its error to every read.
type failedReader struct{ err error }

func (f *failedReader) Read([]byte) (int, error) { return 0, f.err }

// served is the 200 a served 304 reaches the caller as: header, the held body,
// and its length and identity coding in place of the 304's framing. The held
// body is as next delivered it, decoded where next decoded it, and
// [CacheRoundTripper.answer] keeps no body next left coded.
func served(notModified *http.Response, header http.Header, body []byte) *http.Response {
	header.Del("Content-Length")
	header.Del("Content-Encoding")
	return &http.Response{
		Status: "200 OK", StatusCode: http.StatusOK,
		Proto: notModified.Proto, ProtoMajor: notModified.ProtoMajor, ProtoMinor: notModified.ProtoMinor,
		Header: header, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)),
		TLS: notModified.TLS,
	}
}

// State is what the cache has done since it was built, and what it holds now.
func (t *CacheRoundTripper) State() CacheState { return t.cache.State() }

// CloseIdleConnections closes next's idle connections where next can, so
// [http.Client.CloseIdleConnections] reaches them.
func (t *CacheRoundTripper) CloseIdleConnections() { closeIdle(t.next) }
