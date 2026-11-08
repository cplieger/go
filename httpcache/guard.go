package httpcache

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
)

// wireCheckKey carries a stored read's [wireCheck] to [Guard].
type wireCheckKey struct{}

// wireCheck is the request a stored read keyed, as [Guard] compares every
// attempt and redirect hop against it, and the last answer the wire gave it.
type wireCheck struct {
	header   http.Header
	answered http.Header
	answer   *http.Response
	body     *wireBody
	method   string
	target   string
	host     string
	status   int
	mu       sync.Mutex
	diverged atomic.Bool
}

func newWireCheck(r *http.Request) *wireCheck {
	return &wireCheck{header: r.Header.Clone(), method: r.Method, target: r.URL.String(), host: effectiveHost(r)}
}

// effectiveHost is the Host a request is sent with: net/http sends Request.Host
// over the URL's host, and a hook can set it with the URL and Header unchanged.
func effectiveHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

func (w *wireCheck) matches(r *http.Request) bool {
	return r.Method == w.method && r.URL.String() == w.target && effectiveHost(r) == w.host &&
		maps.EqualFunc(r.Header, w.header, slices.Equal[[]string])
}

// record notes resp as the wire's answer to the latest attempt, its body hashed
// as the owner reads it.
func (w *wireCheck) record(resp *http.Response) {
	body := &wireBody{ReadCloser: resp.Body, sum: sha256.New()}
	resp.Body = body
	w.mu.Lock()
	defer w.mu.Unlock()
	w.answer, w.status, w.answered, w.body = resp, resp.StatusCode, resp.Header.Clone(), body
}

// vouches reports whether every request of the read reached the wire as keyed and
// resp is the wire's last answer, its status and headers, as it gave it. Only
// [Guard] records an answer, so a read that reached no guard, an observer
// that changed a request or those, and a redirect hop are not vouched for;
// [wireCheck.carried] vouches for the body.
func (w *wireCheck) vouches(resp *http.Response) bool {
	if w == nil || w.diverged.Load() {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return resp == w.answer && resp.StatusCode == w.status && maps.EqualFunc(resp.Header, w.answered, slices.Equal[[]string])
}

// carried reports whether body is all the wire's last answer carried, byte for
// byte.
func (w *wireCheck) carried(body []byte) bool {
	w.mu.Lock()
	b := w.body
	w.mu.Unlock()
	if b == nil || !b.done {
		return false
	}
	sum := sha256.Sum256(body)
	return bytes.Equal(b.sum.Sum(nil), sum[:])
}

// wireBody hashes an answer's body as it is read, to the end.
type wireBody struct {
	io.ReadCloser
	sum  hash.Hash
	done bool
}

func (b *wireBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.sum.Write(p[:n])
	if errors.Is(err, io.EOF) {
		b.done = true
	}
	return n, err
}

// Guard is the round tripper a [Cache.Begin] read is sent through, beneath
// every round tripper that may change a request or an answer. A request that
// differs from the one prepare keyed leaves without validators, and an answer it
// did not get from next as next gave it, or that reports another request, is
// neither served nor kept. A read that reaches no guard is never served or kept.
// A [RoundTripper] guards its own next; do not put another guard inside it.
// Nil next means [http.DefaultTransport].
func Guard(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return cacheGuard{next: next}
}

type cacheGuard struct {
	next http.RoundTripper
}

// RoundTrip strips the validators from a request that is not the keyed one,
// because the held entry answers only for the keyed request (RFC 9110 section
// 8.8.1).
func (g cacheGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	check, _ := r.Context().Value(wireCheckKey{}).(*wireCheck)
	if check != nil {
		if check.matches(r) {
			resp, err := g.next.RoundTrip(r)
			if err == nil {
				if !sentAsKeyed(check, resp) {
					check.diverged.Store(true)
				}
				check.record(resp)
			}
			return resp, err
		}
		check.diverged.Store(true)
	}
	if !carriesValidator(r.Header) {
		return g.next.RoundTrip(r)
	}
	stripped := r.Clone(r.Context())
	maps.DeleteFunc(stripped.Header, func(name string, _ []string) bool { return isValidator(name) })
	return g.next.RoundTrip(stripped)
}

// CloseIdleConnections closes next's idle connections where next can.
func (g cacheGuard) CloseIdleConnections() { closeIdle(g.next) }

// sentAsKeyed reports whether resp answers the request check keyed, by the
// request resp says next sent, as net/http's Transport reports it. An answer
// naming no request vouches for none, since next may have rewritten the one it
// was given.
func sentAsKeyed(check *wireCheck, resp *http.Response) bool {
	return resp.Request != nil && check.matches(resp.Request)
}

// closeIdle closes rt's idle connections where rt has a CloseIdleConnections
// method, as [http.Client.CloseIdleConnections] does.
func closeIdle(rt http.RoundTripper) {
	if c, ok := rt.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
