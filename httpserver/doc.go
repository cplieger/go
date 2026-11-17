// Package httpserver is server-side HTTP plumbing on top of net/http:
// request-id and bounded access logging (Logging), a transparent
// StatusRecorder, composable middleware (Chain, Recoverer, SecurityHeaders,
// Gzip, RouteTimeout, RouteDeadline, RateLimiter and its FailedAuthRateLimit
// preset, KeyedRateLimiter, NoStore, LoopbackOnly), a spoof-aware ClientIP and
// ResolveClient, a Host allowlist against DNS rebinding (HostPolicy,
// CanonicalHost), a cross-origin write guard (CrossOrigin), one JSON error
// envelope (WriteError, ErrorResponse, CodeSpecs), request preludes
// (LimitBody, RequireMethod, DecodeBody), an embedded StaticHandler,
// single-page app serving from a bundle manifest (SPA, PageMatcher,
// OutagePage), a constant-time NewStaticTokenVerifier, ClassifyBind, a
// graceful server (New, Run) and the ops listener (ListenOps, RunWithOps,
// CheckListenAddrs). Package httpservertest checks an app's use of them.
//
// Middleware is func(http.Handler) http.Handler and Chain lists the outermost
// first, so Chain(mux, Logging(), Recoverer(), SecurityHeaders()) logs a
// recovered panic as its 500. The package depends on nothing beyond the
// standard library and go/textsafe.
package httpserver
