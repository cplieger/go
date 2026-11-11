package httpserver

import (
	"net/http"
	"slices"
)

// LoopbackOnly is middleware that admits a request only when LoopbackRequest
// passes and ProxiedRequest finds no forwarding or browser header, and answers
// every other request 403 through WriteError with the code "loopback_only".
// The header check refuses a reverse proxy on this host's network stack, which
// rewrites Host to its upstream address and so passes LoopbackRequest. It is
// not authentication: a same-host proxy that strips every such header passes.
// WithLoopbackError sets the code and message, and WithLoopbackResponder the
// renderer.
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
