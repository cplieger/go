# httpserver

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/httpserver.svg)](https://pkg.go.dev/github.com/cplieger/go/httpserver) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=httpserver%2Fgo.mod)](https://github.com/cplieger/go/blob/main/httpserver/go.mod)

httpserver hardens your Go `net/http` server with access logging, security headers, a client IP that cannot be spoofed and graceful shutdown.

It replaces the middleware, proxy-header parsing and shutdown sequence you would otherwise write around `http.ServeMux`, and every piece stays a plain `http.Handler`. It ships no router, and your app keeps its own routes and error codes. It uses only the standard library, needs Go 1.27 or later and is licensed under Apache-2.0.

## Why use it

httpserver is built for a Go service on `http.ServeMux` that faces a browser or sits behind a proxy.

- `ResolveClient` and `ClientIP` read `X-Forwarded-For` only from a proxy you trust, so a client cannot fake its address. `ResolveClient` also reports whether the peer is a proxy you trust, and whether an unlisted proxy sent a forwarding header.
- A Host allowlist answers 403 to a `Host` you did not list, which stops DNS rebinding attacks.
- The access log writes `log/slog` lines and caps the path at 512 bytes and the method at 24. Metric labels stay within ten method values and your registered routes.
- `New` leaves the write timeout off, so SSE and WebSocket streams stay open. `Run` drains in-flight requests within a 5-second grace period by default.
- `StaticHandler` serves an `embed.FS` with content-hash ETags and gzip computed once at startup.

Consider [chi](https://github.com/go-chi/chi) if you want a router with route groups, inline middleware and sub-router mounting. Consider [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate) if you need a token bucket per client or one that waits instead of refusing.

## Install

```sh
go get github.com/cplieger/go/httpserver@latest
```

## Usage

```go
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/cplieger/go/httpserver"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /things", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if !httpserver.DecodeBody(w, r, &body, "invalid thing payload") {
			return
		}
		httpserver.WriteJSONStatus(w, http.StatusCreated, body)
	})

	// The first middleware listed is the outermost. Logging sits outside
	// Recoverer so a recovered panic is logged as its 500.
	handler := httpserver.Chain(mux,
		httpserver.Logging(
			httpserver.WithSkipPaths("/events"), // a long-lived stream
			httpserver.WithRecordRouteMetric(func(m httpserver.RequestMetric) {
				// m.Method and m.Path are bounded labels. Feed your metrics here.
			}),
		),
		httpserver.Recoverer(),
		httpserver.SecurityHeaders(),
	)

	// Only header reads and idle connections time out, so streams stay open.
	// Every other http.Server field is a plain assignment.
	srv := httpserver.New(handler, httpserver.WithSlogErrorLog(slog.LevelError))
	streams, endStreams := context.WithCancel(context.Background())
	srv.BaseContext = func(net.Listener) context.Context { return streams }

	// Bind first, so a port already in use fails here.
	ln, err := net.Listen("tcp", ":8080")
	if err != nil {
		panic(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := httpserver.Run(ctx, srv, ln,
		// End long-lived streams so they do not hold the drain open.
		httpserver.WithPreDrain(func(context.Context) { endStreams() }),
		httpserver.WithTeardown(func(context.Context) {
			// close what the handlers used, after the drain
		}),
	); err != nil {
		panic(err)
	}
}
```

A service that browsers reach should also refuse unknown `Host` names. Build a `HostPolicy` with `ParseHostList` and put its `Middleware()` in the chain ahead of any cross-origin or CSRF check. Each refusal logs one Warn line naming the reason and the host. An API that only machines call can skip it. The examples on pkg.go.dev run under `go test`, which keeps them true.

## API

- Middleware: `Chain`, `Recoverer`, `SecurityHeaders` with `HSTS`, `Gzip`, `Logging`, `RouteTimeout`, `RouteDeadline`, `RateLimiter` with its `FailedAuthRateLimit` preset, `NoStore`.
- Logging and metrics: `Logging`'s options with `WithProbeLogLevel` and `WithRecordRouteMetric` (`RequestMetric`), the request-id helpers (`HeaderRequestID`, `NewRequestID`, `ValidRequestID`, `RequestIDFromContext`), `StatusRecorder` and `NewStatusRecorder`.
- Trust checks: `ClientIP`, `ResolveClient` (`ClientResolution`), `ParseCIDRs`, `ParseHostList` and `HostPolicy` (with `LogRefusal`), `CanonicalHost`, `LoopbackOnly`, `LoopbackRequest`, `ProxiedRequest`, `ClassifyBind` and `ClassifyBindHost`, `NewStaticTokenVerifier`.
- Requests and responses: `WriteJSON`, `WriteJSONStatus`, `OK`, `JSONHeaders`, `WriteError` with `ErrorCode` and `ErrorResponder`, `DecodeBody`, `DecodeJSONInto` with `ErrTrailingData` and `MaxJSONBody`, `LimitBody`, `RequireMethod`, `MethodNotAllowed`, `SetAllow`, `CanonicalRequestPath`.
- Static files: `StaticHandler`, `InlineScriptHashes`, `InlineStyleHashes`.
- Server: `New` with `WithSlogErrorLog`, `Run` with `WithPreDrain`, `WithTeardown`, `WithServeExit` and `WithShutdownGrace`, `ErrShutdownGraceExpired`, `AwaitDone`, `CausedByCancellation`, and `ListenMetrics` (`MetricsHandlers`, `MetricsServer`) for an app's opt-in metrics and health listener.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/httpserver).

## Middleware order and safety rules

`Chain(h, A, B, C)` builds `A(B(C(h)))`, so the first entry sees the request first. Any `func(http.Handler) http.Handler` fits. Put `Recoverer` inside `Logging` and `Gzip` inside `Recoverer`, as in `Chain(mux, Logging(), Recoverer(), Gzip())`. Otherwise the access log can show 200 where the client got 500, or a panic can lose its 500.

Place a `HostPolicy` before any cross-origin or CSRF check. In a DNS rebinding attack, the page's `Origin` and the request's `Host` both name the attacker's domain, so a cross-origin check passes it and only the Host allowlist refuses it. Place `NoStore` innermost, so a handler that sets its own `Cache-Control` still wins.

`SecurityHeaders` always sends `X-Content-Type-Options: nosniff`, and by default `X-Frame-Options: DENY` and `Referrer-Policy: strict-origin-when-cross-origin`. It builds no Content-Security-Policy and sends no HSTS until you pass `WithCSP` or `WithHSTS`. Enable HSTS only for a service reached over HTTPS alone. A browser that has seen the header refuses plain-HTTP and untrusted-certificate connections to the host for the whole max-age.

`RouteTimeout` buffers the whole response, so never wrap a streaming or hijacking handler with it. If the handler is still running at its deadline, it answers 503 without stopping it, so never wrap a handler that changes state with it either; use `RouteDeadline` for that one. [Middleware](docs/middleware.md) has each option and default.

Behind a proxy, `ClientIP` is safe only when the trusted set holds every proxy hop. It reads at most 64 entries of each forwarding header and treats a longer header as invalid. `LoopbackOnly` is not authentication, because a proxy on the same loopback interface that removes every forwarding header passes it. An empty `NewStaticTokenVerifier` secret denies every value. When a page carries inline scripts, treat an empty `InlineScriptHashes` result as a startup error and never fall back to `'unsafe-inline'`.

## Graceful shutdown

When `ctx` is cancelled, `Run` runs the `WithPreDrain` hook, then `srv.Shutdown` drains in-flight requests, then the `WithTeardown` hook runs. All three share one grace budget, 5 seconds unless you set `WithShutdownGrace`. Use the pre-drain hook to mark the instance unready or close long-lived streams, so they do not hold the drain open.

When `Serve` returns before `ctx` is cancelled, for example because the listener failed, neither hook runs. Pass `WithServeExit` to run your teardown in that case too. A `Run` error caused by the grace running out wraps `ErrShutdownGraceExpired`. [Server and shutdown](docs/server.md) has the full sequence.

## Related projects

- [httpclient](https://github.com/cplieger/go/tree/main/httpclient) makes the requests your service sends out survive flaky servers. httpserver handles the requests coming in.
- [health](https://github.com/cplieger/go/tree/main/health) is the container liveness probe for a Docker `HEALTHCHECK`, and its `CheckHandler` answers a load balancer's readiness check.
- [access](https://github.com/cplieger/access) handles user logins and sessions. `NewStaticTokenVerifier` checks one configured machine credential.

## Documentation

- [Middleware](docs/middleware.md) covers ordering, recovery, security headers, compression, timeouts, rate limiting and `NoStore`.
- [Access logging and metrics](docs/access-logging.md) covers request ids, the log line, its bounds and the metric hook.
- [Client IP, hosts and credentials](docs/trust-checks.md) covers proxy trust, the Host allowlist and loopback checks.
- [Requests and responses](docs/requests-and-responses.md) covers the JSON helpers, the error envelope, body decoding and path checks.
- [Static assets and CSP hashes](docs/static-assets.md) covers embedded files, caching and hash-pinned inline scripts.
- [Server and shutdown](docs/server.md) covers timeouts, shutdown order and the metrics listener.

## Credits

`RateLimiter`'s refill and `Retry-After` math follows [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate), including its guard against a clock that moves backwards.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
