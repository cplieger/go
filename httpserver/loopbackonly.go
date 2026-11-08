package httpserver

import (
	"net/http"
	"slices"
)

// LoopbackOnly is the MIDDLEWARE half of the loopback-only admission decision,
// beside the LoopbackRequest predicate this package already exports.
//
// The predicate answers "did this request come from inside" from the two fields
// that cannot be spoofed at this layer. This adds the second half every real
// consumer of that predicate turned out to need: a refusal on proxy and browser
// PROVENANCE headers. LoopbackRequest deliberately ignores those headers in both
// directions — they can never admit and never refuse — and its documentation
// tells an app that wants the deny to "compose that deny around this predicate
// rather than finding it folded in". That guidance stands; this is that
// composition, written once. The predicate is untouched, so nothing that calls
// it directly changes behaviour.
//
// Why the deny is needed at all, and why it is not paranoia: the consumer of a
// loopback-only surface is an in-container CLI client, which sends none of these
// headers, so their presence is positive evidence the request did NOT originate
// inside the container. It matters because a reverse proxy SHARING the server's
// loopback interface (host networking, a shared network namespace) rewrites Host
// to its upstream address by default in both nginx and Apache — which satisfies
// LoopbackRequest's Host leg while the peer leg is satisfied by the proxy itself.
// Both legs pass and the request is remote. The provenance headers are what
// distinguishes it.
//
// It has a known ceiling, stated because a gate whose limits are unwritten gets
// trusted past them: a same-loopback proxy that strips every provenance header
// is indistinguishable from an in-container caller and is admitted. Closing that
// needs authentication, not a header rule.
//
// A refused request is answered 403 through WriteError with the code
// "loopback_only" and a message telling the caller to call from inside the
// container. WithLoopbackError replaces the code and message and
// WithLoopbackResponder the renderer, as WithRateLimitError and
// WithRateLimitResponder do for RateLimiter: the refusal is the whole of what a
// rejected caller is told, so its wording belongs to the app.
func LoopbackOnly(opts ...LoopbackOption) Middleware {
	c := &loopbackConfig{
		code:      loopbackRefusalCode,
		msg:       loopbackRefusalMsg,
		responder: WriteError,
	}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !LoopbackRequest(r) || ProxiedRequest(r.Header) {
				c.responder(w, r, http.StatusForbidden, c.code, c.msg)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LoopbackOption configures LoopbackOnly.
type LoopbackOption func(*loopbackConfig)

// loopbackConfig holds resolved LoopbackOnly configuration.
type loopbackConfig struct {
	responder ErrorResponder
	code      ErrorCode
	msg       string
}

// WithLoopbackError sets the code and message of LoopbackOnly's 403. code is
// the machine token and msg the sentence; they are separately typed (see
// ErrorCode) so the pair cannot be supplied transposed. An empty code omits the
// field.
func WithLoopbackError(code ErrorCode, msg string) LoopbackOption {
	return func(c *loopbackConfig) { c.code, c.msg = code, msg }
}

// WithLoopbackResponder sets the ErrorResponder that writes LoopbackOnly's
// 403, in place of WriteError's JSON envelope; it receives the status, code and
// message, so it composes with WithLoopbackError. A responder is also the place
// to log a refusal. A nil fn is ignored, keeping the default.
func WithLoopbackResponder(fn ErrorResponder) LoopbackOption {
	return func(c *loopbackConfig) {
		if fn != nil {
			c.responder = fn
		}
	}
}

// loopbackRefusalCode and loopbackRefusalMsg are the default refusal's
// envelope code and message. The default code is shared across services
// because it is what an operator's log queries and alert rules key on.
const (
	loopbackRefusalCode ErrorCode = "loopback_only"
	loopbackRefusalMsg  string    = "this endpoint is loopback-only; call it from inside the container"
)

// proxyProvenanceHeaders are the headers a request acquires by travelling through
// a browser or a reverse proxy.
//
// Sec-Fetch-Site and Origin are in the set because a BROWSER is not an
// in-container CLI client either: a page that reached a loopback-shared server
// has provenance a curl does not. Note that this makes the set broader than
// "proxied" alone, which is why the exported predicate is named for requests
// that came from somewhere rather than for proxies specifically.
var proxyProvenanceHeaders = []string{
	"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
	"X-Real-Ip", "Sec-Fetch-Site", "Origin",
}

// ProxiedRequest reports whether h carries any evidence the request was forwarded
// by a proxy or issued by a browser.
//
// Exported alongside the middleware because the header SET is the drifting
// knowledge: two consumers had hand-rolled it, and a consumer that cannot adopt
// the middleware wholesale (one with its own admission pipeline) should still be
// able to share the list rather than maintain a third copy. It is a positive
// signal only — never treat a missing header as evidence of anything.
func ProxiedRequest(h http.Header) bool {
	for _, name := range proxyProvenanceHeaders {
		if slices.ContainsFunc(h.Values(name), func(v string) bool { return v != "" }) {
			return true
		}
	}
	return false
}
