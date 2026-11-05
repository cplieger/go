# The file marker

This page is for a Go developer wiring the file marker into a service. It covers the two roles, degraded mode, a marker written by a separate process, shutdown and the optional HTTP handler.

## Two roles, one binary

The service's main process owns a `*Marker`. It calls `Set(true)` once it is ready, `Set(false)` when it cannot do its work, and `Cleanup` on shutdown. `Set(true)` creates the marker file and `Set(false)` removes it. The probe is the same binary, started by Docker with a `health` argument. It reads the marker and exits. Kubernetes can run the same subcommand as an exec liveness probe.

```go
func main() {
    if len(os.Args) > 1 && os.Args[1] == "health" {
        health.RunProbe(health.DefaultPath) // calls os.Exit
    }

    m := health.NewMarker(health.DefaultPath)
    defer m.Cleanup()
    m.Set(true)

    // ... run the service ...
}
```

`DefaultPath` is `/tmp/.healthy`. The two processes share nothing but that file, and the library starts no goroutine of its own.

`RunProbe` exits 0 when the marker exists. It exits 1 when the marker is absent from a writable folder, or when a stat of the marker fails for another reason, and it writes one line to stderr naming the cause. `ProbeCheck` returns the same 0 or 1 without exiting, for tests.

`Set` is safe to call from any goroutine. Each call compares the value with the marker file as it stands, the state the probe reads without `WithMaxAge`: a file is healthy and no file is unhealthy. A change logs at Info level for healthy and at Warn level for unhealthy, and a call that changes nothing stays silent. So a first `Set(false)` at startup with no file logs nothing, and a `Set(false)` that removes a marker another process wrote logs Warn. It logs through `slog.Default()`, so call `slog.SetDefault` in `main` before `NewMarker`.

When the marker cannot be read, written or removed, `Set` logs the failure once for each distinct error and carries on. A marker it cannot read, such as a symlink loop, is left as it is. The first write that succeeds after a failure logs one Info line. `SetChecked` does the same write and returns the error, for a caller that must act on it.

## Degraded mode

`NewMarker` checks that the marker's folder is writable by creating and deleting a temporary file in it. When that fails, the marker enters degraded mode and logs one warning with the folder, the error and the tmpfs mount to add. Callers do not need to branch on the result.

In degraded mode:

- `Set` and `Cleanup` do nothing, and `SetChecked` returns nil, so a compose mistake never turns into a loop of alerts.
- `RunProbe` and `ProbeCheck` report healthy. The service is alive, and only its way of reporting health is broken, which a restart cannot fix.
- `CheckHealthy`, `Healthy`, `Within` and `Handler` report unhealthy, even when a marker was left in the folder, because `Set` cannot change it.

The usual cause is compose's `read_only: true` with no tmpfs at `/tmp`. Mount one to keep a working signal:

```yaml
read_only: true
tmpfs:
  - /tmp:size=1m,mode=1777,noexec,nosuid,nodev
```

Under Kubernetes, `readOnlyRootFilesystem: true` has the same effect. Mount a memory-backed `emptyDir` volume at `/tmp`.

## A marker written by a separate process

The marker belongs to the user that created it. When a separate `docker exec` process updates it, for example a job scheduler that runs your binary's `run` or `sync` subcommand, run that exec as the same UID as the container's main process. Otherwise, filesystem ownership and mode may make the marker write fail with permission denied. Under `Set` that failure is only logged, so the health signal is lost without any other sign.

When an external scheduler alerts on the subcommand's exit code, call `SetChecked` and turn the returned error into a failing exit code. The scheduler's job then fails instead of losing the update.

## Shutdown with `Latch`

During shutdown, work that finishes late can call `Set(true)` and hide that the service is stopping. `Latch` wraps a marker to prevent it. Send ordinary health decisions through the latch's `Set`, and call `BeginDrain` before you wait for work in flight:

```go
marker := health.NewMarker(health.DefaultPath)
defer marker.Cleanup()

state := health.NewLatch(marker)
state.Set(true)

// Stop admitting work, then make health monotonic toward unhealthy.
state.BeginDrain()

// This is dropped after BeginDrain. state.Set(false) would still land.
state.Set(runSucceeded)
```

`BeginDrain` marks the service unhealthy at once. After it, a healthy value is dropped and an unhealthy one still reaches the marker. One lock covers the drain check and the marker write, so shutdown wins over a completion that races it. The latch only decides which write wins. Your service still decides whether a result means healthy, unhealthy or no write at all. Keep the `*Marker` to call `Cleanup`.

## The HTTP handler

A service that already serves HTTP can also expose its health at `GET /healthz`, for an uptime prober or a Kubernetes HTTP probe:

```go
m := health.NewMarker(health.DefaultPath)
mux.Handle("GET /healthz", health.Handler(m))
```

`Handler` answers 200 when the signal is healthy, and 503 otherwise, a nil signal included:

```json
{"status":"ok"}
{"status":"unhealthy","reasons":["the app reports itself unhealthy"]}
```

Both carry `Content-Type: application/json`, `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`, and no timestamp: the `Date` header carries the time. Registered as `GET /healthz`, the route also answers `HEAD` and answers 405 to every other method.

`CheckHandler` reports several conditions at once. Each `Check` pairs a `Signal` with a `Reason`, one plain sentence that `reasons` lists, in order, while that signal is unhealthy:

```go
mux.Handle("GET /healthz", health.CheckHandler(
	health.Check{Signal: listener, Reason: "the listener has not started"},
	health.Check{Signal: store, Reason: "the store is not writable"},
))
```

Write each reason where you wire the check, never from an error's text: the endpoint answers without authentication, and error text can carry paths, hosts or tokens.

Both take any `Signal`, the one-method interface `*Marker` satisfies, so an in-memory flag works too. With a `*Marker`, each request does one `os.Stat`, so serve it at your platform's probe interval rather than on a busy path. To make the endpoint follow the probe's deadline, pass `m.Within(lease.Duration())` instead of `m`, as [Freshness deadline](freshness.md) describes.

In degraded mode `Handler` always answers 503, even for a marker left in the folder, because `Set` cannot change it, while the `health` subcommand reports healthy. Do not use `Handler` as the only liveness probe of a service that may run read-only without a tmpfs at `/tmp`, or the platform will restart a container that is working.
