# Server and shutdown

This page is for a developer starting and stopping a httpserver server. It covers the server defaults, routing `net/http`'s own errors into `slog`, the shutdown sequence `Run` follows and the metrics listener of an app that opens no port of its own.

## New

`New(handler, opts...)` returns an `*http.Server` with defaults that suit streaming:

| Field | Default |
| --- | --- |
| `ReadHeaderTimeout` | 10s, a slowloris guard |
| `IdleTimeout` | 120s |
| `MaxHeaderBytes` | 1 MiB |
| `ReadTimeout` | unset |
| `WriteTimeout` | unset |
| `ErrorLog` | the standard logger, or `WithSlogErrorLog` |

To change one, assign the field on the returned server, such as `srv.ReadTimeout = 30 * time.Second`. `New` has an option only where the option does more than assign a field, so `WithSlogErrorLog` is its one option.

With no read or write timeout, SSE, WebSocket and other long-lived responses work. A streaming app never sets `WriteTimeout`, because a write deadline cuts off a stream in progress.

Only header reading is bounded by time, so a slow request body is not. A handler that does not stream sets `ReadTimeout`. A streaming handler sets per-request deadlines with `http.ResponseController.SetReadDeadline` and `SetWriteDeadline` instead. `LimitBody` bounds the size of a body, not the time it takes to send.

`srv.MaxHeaderValueCount = 64` caps how many header values a request may carry. A request refused by that cap, or by `MaxHeaderBytes`, gets its 431 below the handler. It therefore produces no access line and no security headers.

### net/http's own errors

`WithSlogErrorLog(level)` sends `net/http`'s connection-level lines into `slog` at `level`. The main one is `http: Accept error: ...; retrying`, the sign that the process has run out of file descriptors, which no request log reports.

The level is your choice. An accept failure is fatal for a service whose only job is answering probes, and a recoverable problem for one that retries.

The option reads `slog.Default()` when `New` applies it. Install your process logger first, because a later `slog.SetDefault` does not change a server already built. Any other `*log.Logger` is an assignment to `srv.ErrorLog`. With neither, `net/http`'s standard logger is used.

## Run

`Run(ctx, srv, ln, opts...)` serves `srv` on `ln` until `ctx` is cancelled, then shuts down. Bind `ln` before you call it, for example with `net.ListenConfig.Listen`, so a port already in use fails at once. `srv` and `ln` must not be nil.

When `ctx` is cancelled, `Run` sets one deadline, now plus the grace, and runs three steps against it:

1. The `WithPreDrain` hook, if one is registered.
2. `srv.Shutdown`, which drains in-flight requests.
3. The `WithTeardown` hook, if one is registered, for your application's teardown: closing what the handlers used.

Each step gets what the earlier steps left of the one budget, not a fresh window. `WithShutdownGrace(d)` sets the grace, 5 seconds by default.

Once `srv.Shutdown` starts, `Run` closes every connection that has not sent a request yet. `net/http` would otherwise count each one as active for up to 5 seconds ([golang/go#22682](https://go.dev/issue/22682)), so one silent client would hold the drain open. `Run` tracks this through `srv.ConnState`, and a hook you set there before calling `Run` still sees every state change.

`Run` treats `http.ErrServerClosed` as a clean stop. It returns the first other error it sees, a serve error before a shutdown error, or nil after a clean stop.

### The pre-drain hook

`WithPreDrain(fn)` runs after `ctx` is cancelled and strictly before the drain starts. Use it to mark the instance unready so a load balancer stops sending traffic (the [health](https://github.com/cplieger/go/tree/main/health) library's `CheckHandler` serves that signal), and to end long-lived connections. That can mean cancelling the server's `BaseContext` or closing an SSE hub. Otherwise `Shutdown` waits the whole grace for those connections. A nil `fn` is ignored, here and in `WithTeardown` and `WithServeExit`, so a nil never clears a hook set earlier.

### When Serve stops on its own

`Run` has two exits, and only one is the graceful sequence. When `Serve` returns before `ctx` is cancelled, because the accept loop died or you called `Shutdown` or `Close` yourself, the listener is already gone. Neither `WithPreDrain` nor `WithTeardown` runs, and `Run` returns the serve error.

`WithServeExit(fn)` registers the teardown for that path. `fn` gets the whole grace, because no drain spent any of it, and `Run` does not call `srv.Shutdown` behind it. Exactly one of the two paths runs per call. Without the option, nothing runs on that path.

`ctx` is still live there. So a teardown that waits on a goroutine stopped by the same signal context must cancel that context inside `fn`, or it waits out the whole grace. A teardown that does not depend on cancellation can go in both places: `Run(ctx, srv, ln, WithTeardown(teardown), WithServeExit(teardown))`.

### Telling errors apart

`Run`'s error can carry a deadline from two places. One is your own deadline inside a serve error, and the other is the shutdown grace running out. Both satisfy `errors.Is(err, context.DeadlineExceeded)`. A grace expiry is also wrapped in `ErrShutdownGraceExpired`, so `errors.Is(err, httpserver.ErrShutdownGraceExpired)` identifies it. A real serve error takes precedence and is never marked.

`CausedByCancellation(ctx, err)` reports whether `err` is this context's cancellation, so a boundary can tell a routine stop from a fault that happened at the same moment. A cancelled context alone is not proof, because a bind that really failed while a signal arrived must still read as a failure.

It matches `context.Cause(ctx)` as well as `ctx.Err()`. A cause passed to `context.WithCancelCause` need not wrap `context.Canceled`, and `net/http` reports the cause as it is. A nil context, a nil error or a context that is not cancelled report false.

### Waiting in a teardown

`AwaitDone(ctx, done)` waits until `done` closes or `ctx` expires, and reports whether `done` closed in time. It creates no timeout of its own and logs nothing. The `WithTeardown` hook already receives what is left of the grace, so a fresh deadline would be a budget the shutdown does not have.

After `ctx` fires, it checks `done` once more. A drain that used the whole grace hands the teardown an expired context. A `select` with both cases ready picks one at random, so the plain two-case wait sometimes reports a finished teardown as still running. A nil `done` never closes, so the wait then ends with `ctx`.

What to log, at which level, and what exit code to use stay your decisions for all three helpers.

## The metrics listener

An app that opens no port of its own can still offer an opt-in page for Prometheus and a health endpoint. `ListenMetrics(ctx, addr, MetricsHandlers{Metrics: ..., Healthz: ...}, opts...)` builds that listener in one call:

```go
ms, err := httpserver.ListenMetrics(ctx, cfg.MetricsListenAddr, httpserver.MetricsHandlers{
	Metrics: reg.Handler(),
	Healthz: health.CheckHandler(checks...),
})
if err != nil {
	slog.Error("cannot listen on METRICS_LISTEN_ADDR; refusing to start", "error", err)
	os.Exit(1)
}
done := make(chan error, 1)
go func() { done <- ms.Serve(ctx) }()
// ... the app's work; cancel ctx on the way out ...
if err := <-done; err != nil {
	slog.Error("metrics listener stopped", "error", err)
}
```

- It binds `addr` before it returns, so a port in use fails there, naming the address, before the app starts any work. A startup that gives up after that calls `Close` to release the port.
- An empty `addr` opens nothing. `Serve` then returns nil at once, without running `Run` or its options, and `Addr` is nil, so the app needs no branch for a listener that is off.
- It routes only `/metrics` and `/healthz`, for `GET` and the `HEAD` a `GET` pattern also matches. Any other path is 404, after an unclean path is first redirected to its clean form, and any other method 405, so the listener never serves the app's own routes.
- Both paths log under `WithProbeLogLevel`, and `Recoverer` turns a panicking handler into a 500.
- `ReadTimeout` is 10s and `WriteTimeout` 30s, because both routes answer in one short response. `New`'s other defaults apply.
- `opts` reach `New`, so `WithSlogErrorLog` routes net/http's own error lines into `slog`.
- It reads no environment. The app owns its setting's name, parsing and edge trimming.
- `Serve(ctx, opts...)` runs `Run` until `ctx` ends and takes the same options. A second `Serve`, or a `Serve` after `Close`, on a listener that is on returns an error. Once `Serve` runs, stop it by cancelling `ctx`: `Close` then returns an error and leaves it serving.
