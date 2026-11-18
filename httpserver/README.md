# httpserver

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/httpserver/v2.svg)](https://pkg.go.dev/github.com/cplieger/go/httpserver/v2) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=httpserver%2Fgo.mod)](https://github.com/cplieger/go/blob/main/httpserver/go.mod)

httpserver hardens your Go `net/http` server with access logging, security headers, a client IP that cannot be spoofed and graceful shutdown, and serves a single-page app under a strict Content-Security-Policy.

Every piece is a plain `http.Handler` around `http.ServeMux`, so your app keeps its own routes and error codes. It ships no router, depends only on the standard library, `go/ssrf` and `go/textsafe`, and is licensed under Apache-2.0.

## Why use it

httpserver is built for a Go service on `http.ServeMux` that faces a browser or sits behind a proxy.

- `ResolveClient` reads `X-Forwarded-For` only from a proxy you trust, so a client cannot fake its address. `ForwarderCheck` spots a container runtime's forwarder posing as a local client.
- A Host allowlist stops DNS rebinding, and `CrossOrigin` refuses cross-site writes.
- Every refusal is one JSON envelope with a code, a message, a remediation and the request id.
- `SPA` serves only the files your bundle's manifest lists, with the policy on every answer and year-long caching for hashed assets. `httpservertest.CheckSPA` checks every path in a test.
- The access log caps the path and method it writes, and metric labels stay bounded.
- `New` leaves the write timeout off, so streams stay open. `Run` drains in-flight requests within a grace period, and the ops listener keeps `/healthz` answering through the drain.

Consider [chi](https://github.com/go-chi/chi) for route groups and sub-routers, or [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate) for a limiter that waits instead of refusing.

## Install

```sh
go get github.com/cplieger/go/httpserver/v2@latest
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

	"github.com/cplieger/go/httpserver/v2"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Bind first, so a port still in use after Listen's 5 s retry fails here.
	ln, err := httpserver.Listen(ctx, httpserver.ListenSetting{Name: "LISTEN_ADDR", Addr: ":8080"})
	if err != nil {
		panic(err)
	}

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

A service that browsers reach should also refuse unknown `Host` names. Build a `HostPolicy` with `ParseHostList` and put its `Middleware()` in the chain ahead of `CrossOrigin`, which guards the routes that change state. Each refusal logs one Warn line naming the reason and the host. An API that only machines call can skip it. The examples on pkg.go.dev run under `go test`, which keeps them true.

## API

- Middleware: `Chain`, `Recoverer`, `SecurityHeaders` with `HSTS`, `Gzip`, `Logging`, `RouteTimeout`, `RouteDeadline`, `RateLimiter` with its `FailedAuthRateLimit` preset, `NewKeyedRateLimiter` (`KeyedRate`, `KeyedRateLimiter`), `NoStore`.
- Logging and metrics: `Logging`'s options with `WithProbeLogLevel` and `WithRecordRouteMetric` (`RequestMetric`), `AddLogAttrs` for a handler's own access-line attributes, the request-id helpers (`HeaderRequestID`, `NewRequestID`, `ValidRequestID`, `RequestIDFromContext`, `WithRequestID`), `StatusRecorder` and `NewStatusRecorder`.
- Trust checks: `ClientIP`, `ResolveClient` (`ClientResolution`), `ParseCIDRs`, `ParseHostList` and `HostPolicy` (with `WithLoopbackExempt`, `WithIPLiteralHosts`, `WithHostResponder`, `WithHostAllowlistLogger` and `LogRefusal`), `CanonicalHost`, `CrossOrigin`, `LoopbackOnly`, `LoopbackRequest`, `ProxiedRequest`, `ClassifyBind` and `ClassifyBindHost`, `NewForwarderCheck` (`ForwarderCheck`), `NewStaticTokenVerifier`, `NewTokenSet` (`TokenSet`, `TokenSetError` with its `Err` reasons, `MaxTokenBytes`), `NewFailureLimiter` (`FailureLimiter`, `FailureLimits`), `CheckFieldValue` (`FieldValueError` with its `Err` reasons), `SetChallenge` (`Challenge`, `AuthParam`).
- Requests and responses: `WriteJSON`, `WriteJSONStatus`, `OK`, `JSONHeaders`, `WriteError` with `ErrorResponse`, `ErrorCode` and `ErrorResponder`, `CodeSpecs` (`CodeSpec`), `DecodeBody`, `DecodeJSONInto` with `ErrTrailingData` and `MaxJSONBody`, `LimitBody`, `RequireMethod`, `MethodNotAllowed`, `SetAllow`, `CanonicalRequestPath`.
- Static files: `StaticHandler` with `WithStaticCacheControl` and `WithStaticETags`, `ContentTypes`, `InlineScriptHashes`, `InlineStyleHashes`.
- Single-page apps: `SPA` (`SPAConfig`), `Page`, `CheckPages`, `NewPageMatcher` (`PageMatcher`, `PageMatch`), `NotFound`, the manifest (`BundlePath`, `ReadBundle`, `Bundle`, `BundleEntry`, `BundleKind`), `SPAPolicy`, `CheckShell`, and `OutagePage` (`OutageConfig`, `OutageFile`, `OutageClass`, `OutageCommand`).
- Test helpers in `httpservertest`: `CheckSPA`, `CheckNoUI`, `CheckCodeSpecs`.
- Server: `New` with `WithSlogErrorLog`, `Run` with `WithPreDrain`, `WithTeardown`, `WithServeExit` and `WithShutdownGrace`, `ErrShutdownGraceExpired`, `AwaitDone`, `CausedByCancellation`, and the ops listener for `/healthz` and `/metrics`: `ListenAddr`, `ListenOps` (`OpsConfig`, `Ops`), `RunWithOps` (`Served`), and `Listen` and `CheckListenAddrs` over `ListenSetting`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/httpserver/v2).

## Middleware order and safety rules

`Chain(h, A, B, C)` builds `A(B(C(h)))`, so the first entry sees the request first. Any `func(http.Handler) http.Handler` fits. Put `Recoverer` inside `Logging` and `Gzip` inside `Recoverer`, as in `Chain(mux, Logging(), Recoverer(), Gzip())`. Otherwise the access log can show 200 where the client got 500, or a panic can lose its 500.

Place a `HostPolicy` before any cross-origin or CSRF check. In a DNS rebinding attack, the page's `Origin` and the request's `Host` both name the attacker's domain, so a cross-origin check passes it and only the Host allowlist refuses it. Place `NoStore` innermost, so a handler that sets its own `Cache-Control` still wins.

`SecurityHeaders` always sends `X-Content-Type-Options: nosniff`, and by default `X-Frame-Options: DENY` and `Referrer-Policy: strict-origin-when-cross-origin`. It builds no Content-Security-Policy and sends no HSTS until you pass `WithCSP` or `WithHSTS`. Enable HSTS only for a service reached over HTTPS alone. A browser that has seen the header refuses plain-HTTP and untrusted-certificate connections to the host for the whole max-age.

`RouteTimeout` buffers the whole response, so never wrap a streaming or hijacking handler with it. If the handler is still running at its deadline, it answers 503 without stopping it, so never wrap a handler that changes state with it either. Use `RouteDeadline` for that one. [Middleware](docs/middleware.md) has each option and default.

Behind a proxy, `ClientIP` is safe only when the trusted set holds every proxy hop. It reads at most 64 entries of each forwarding header and treats a longer header as invalid. `LoopbackOnly` is not authentication, because a proxy on the same host that sends no forwarding header passes it, and nginx sends none by default. An empty `NewStaticTokenVerifier` secret denies every value. When a page carries inline scripts, treat an empty `InlineScriptHashes` result as a startup error and never fall back to `'unsafe-inline'`.

## Graceful shutdown

When `ctx` is cancelled, `Run` runs the `WithPreDrain` hook, then `srv.Shutdown` drains in-flight requests, then the `WithTeardown` hook runs. All three share one grace budget, 5 seconds unless you set `WithShutdownGrace`. Use the pre-drain hook to mark the instance unready or close long-lived streams, so they do not hold the drain open.

When `Serve` returns before `ctx` is cancelled, for example because the listener failed, neither hook runs. Pass `WithServeExit` to run your teardown in that case too. A `Run` error caused by the grace running out wraps `ErrShutdownGraceExpired`. [Server and shutdown](docs/server.md) has the full sequence.

## Moving from v1

- Change every import of `github.com/cplieger/go/httpserver` to `github.com/cplieger/go/httpserver/v2`.
- Read an error body's `message` key, which replaces `error`. `code`, `remediation` and `request_id` are always present.
- Drop `WithHostAllowlistError`. The refusal is always 403 `host_not_allowed`, and `WithHostResponder` writes it through your own `ErrorResponder`.
- Give every `WriteError` call and every `ErrorResponder` a non-empty code. The empty code is now malformed and answers `invalid_error_code`.
- Replace `ListenMetrics`, `MetricsHandlers` and `MetricsServer` with `ListenOps` and `RunWithOps`, and pass every listener's address to `CheckListenAddrs` at start.
- A client matching `Recoverer`'s 500 message reads `internal error`.
- `ClientResolution.IgnoredPublic` judges an ignored forwarded address as `go/ssrf`'s `IsPublicAddr` does, so a shared (RFC 6598), documentation or other non-routable address no longer sets it.
- A `StaticHandler` file with an extension outside the content-type table, such as `.jpg` or `.woff`, now answers `application/octet-stream`. Convert it to a listed format, or serve it with your own handler.
- Code written against `v2.0.0-dev.1` or `v2.0.0-dev.2` calls `ListenAddr` where it called `OpsAddr`, with the same arguments.
- A shell written against `v2.0.0-dev.1` or `v2.0.0-dev.2` that carries an `on…` event-handler attribute, such as `onclick`, is now refused by `SPA` and `CheckShell`. The policy already blocked the handler in the browser, so attach it from the bundle's script with `addEventListener`.
- Code written against `v2.0.0-dev.1` wraps each `OutageConfig.Commands` string as `httpserver.OutageCommand{Run: s}`, since the field is now `[]OutageCommand`. A line that explained a command moves into its `Note`, and a line about the outage as a whole into `Notice`.

## Related projects

- [httpclient](https://github.com/cplieger/go/tree/main/httpclient) makes the requests your service sends out survive flaky servers. httpserver handles the requests coming in.
- [health](https://github.com/cplieger/go/tree/main/health) is the container liveness probe for a Docker `HEALTHCHECK`, and its `CheckHandler` answers a load balancer's readiness check.
- [access](https://github.com/cplieger/access) handles user logins and sessions. `NewStaticTokenVerifier` checks one configured machine credential, and `TokenSet` several.

## Documentation

- [Middleware](docs/middleware.md) covers ordering, recovery, security headers, compression, timeouts and rate limits.
- [Access logging and metrics](docs/access-logging.md) covers request ids, the log line and the metric hook.
- [Client IP, hosts and credentials](docs/trust-checks.md) covers proxy trust, the Host allowlist, cross-origin writes, loopback checks, credentials and failed-authentication limits.
- [Requests and responses](docs/requests-and-responses.md) covers the JSON helpers, the error envelope and its codes, body decoding and path checks.
- [Static assets and CSP hashes](docs/static-assets.md) covers embedded files, content types, caching and entity-tags.
- [Single-page apps and the outage page](docs/spa.md) covers `SPA`, its manifest, pages, policy and caching, the test helpers and `OutagePage`.
- [Server and shutdown](docs/server.md) covers timeouts, shutdown order and the ops listener.

## Credits

`RateLimiter`'s refill and `Retry-After` math follows [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate), including its guard against a clock that moves backwards.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
