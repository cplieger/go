# The health verdict

This page is for a Go developer wiring health into a service. It covers recording the verdict, serving it at `/healthz`, shutdown, a work loop that stops, several work loops, other signals and the log lines.

## Recording the verdict

A service keeps its verdict in one `*health.Status`, created with `NewStatus` in the long-running process. A Status starts unhealthy. Call `Set(true)` once the service is doing its job, and `Set(false)` when it cannot. `Set` is safe to call from any goroutine, and `Healthy` reads the verdict without waiting on a writer.

Healthy should mean that the service is doing its own job now. Never make it depend on an upstream outside the service: a restart cannot fix an upstream, and a healthcheck that fails for one restarts a working container.

The zero `Status` is ready to use, with no marker and `slog.Default`; only its first healthy log line lacks a `duration` (see Logging). A `Status` must not be copied after first use.

## Serving it

`CheckHandler` serves the verdict at `GET /healthz`. Each `Check` pairs a `Signal` with a `Reason`, one plain sentence that the body lists while that signal is unhealthy:

```go
mux.Handle("GET /healthz", health.CheckHandler(
    health.Check{Signal: st, Reason: "the service is starting or shutting down"},
    health.Check{Signal: store, Reason: "the store is not writable"},
))
```

The handler answers 200 while every check is healthy, and 503 otherwise, listing the reason of each failing check in order:

```json
{"status":"ok"}
{"status":"unhealthy","reasons":["the store is not writable"]}
```

Both carry `Content-Type: application/json`, `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`, and no timestamp: the `Date` header carries the time. Registered as `GET /healthz`, the route also answers `HEAD`, and 405 to every other method. A nil `Signal` is unhealthy, and an empty `Reason` reads as "the app reports itself unhealthy".

Write each reason where you wire the check, never from an error's text. The endpoint answers without authentication, and error text can carry paths, hosts or tokens.

Each request reads every signal again. Reading a Status costs one atomic load and no file access, so a busy prober costs the service almost nothing.

## Shutdown

During shutdown, work that finishes late can call `Set(true)` and hide that the service is stopping. Call `BeginDrain` when shutdown begins, before you wait for work in flight:

```go
st.BeginDrain()
// wait for work in flight; a Set(true) from it is dropped
```

`BeginDrain` makes the verdict unhealthy at once and for good. After it, a healthy `Set` is dropped and an unhealthy one still lands. One lock covers the drain check and the write, so shutdown wins a race with a completion. Calling it twice is safe. With a marker, `BeginDrain` also removes the file.

## A work loop that stops

A verdict that a work loop sets once per cycle stays healthy after the loop deadlocks. `Fresh(d)` returns a `Signal` that fails while the verdict is healthy and its last healthy `Set` is older than `d`. List it beside the Status:

```go
lease := health.Lease{Interval: interval, Cycles: 3}.Duration()
mux.Handle("GET /healthz", health.CheckHandler(
    health.Check{Signal: st, Reason: "the last run failed"},
    health.Check{Signal: st.Fresh(lease), Reason: "the work loop has stopped"},
))
```

Every healthy `Set` restarts the age, even when the verdict was already healthy. `Fresh` passes an unhealthy verdict, so each failure gives its own reason: the Status's when the service reports a fault, and `Fresh`'s when a loop stopped refreshing a healthy verdict. During a drain only the Status's reason is listed. A non-positive `d` never fails. Pass `Fresh` the same lease the healthcheck gives `WithMaxAge`; the [freshness deadline](freshness.md) page explains how to build it.

To test the stale case, create the Status inside `synctest.Test`, call `Set(true)`, sleep one nanosecond past the lease, then serve the request with `httptest.NewRecorder` and the handler's `ServeHTTP`. Do not send it through a listener: a goroutine waiting on the network keeps the bubble's clock from moving, so the sleep never returns. Check the marker file there only by whether it exists, never by its age, because its modification time comes from the real clock.

## Several work loops

A service with several work loops gives each loop its own deadline and one owner. Loops that each refresh one shared verdict hide a stopped loop behind a live one. The owner sets the Status on a tick shorter than the lease, healthy only while every loop's deadline holds, and `/healthz` lists one `Check` per loop, so its reasons name the loop that stopped.

The owner sets the verdict on every tick, not only when the value changes, because a healthy `Set` is what restarts the age that `Fresh` and `RunProbe` read. A loop that is idle by design never beats, so the owner treats it as holding its deadline.

## Other signals

Any type with a `Healthy() bool` method is a `Signal`. `SignalFunc` adapts a function, as `http.HandlerFunc` adapts one to a handler, for a condition the service already tracks:

```go
health.Check{
    Signal: health.SignalFunc(func() bool { return queue.Len() < queue.Cap() }),
    Reason: "the job queue is full",
}
```

A nil `SignalFunc` is unhealthy.

## Logging

The Status logs one line for each change of verdict, with the message `health state changed`:

| Change | Level | Attributes |
| --- | --- | --- |
| Turned healthy | Info | `healthy=true`, and `duration`, how long the service was unhealthy |
| Turned unhealthy | Warn | `healthy=false` |
| `BeginDrain` on a healthy verdict | Warn | `healthy=false draining=true` |

A `Set` that repeats the verdict logs nothing, and so does a first `Set(false)`, because a Status starts unhealthy. A lease that lapses under `Fresh` changes no recorded verdict, so it logs nothing either: `/healthz` names it. The first healthy line after start carries how long the service took to turn healthy. A zero `Status` that never ran `NewStatus` has no start time, so its first healthy line has no `duration`.

Lines go to the logger passed with `NewStatus(health.WithLogger(l))`, or to `slog.Default()` at the time of each line. The marker's own lines are on the [marker file](file-marker.md) page.
