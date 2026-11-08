# health

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/health.svg)](https://pkg.go.dev/github.com/cplieger/go/health) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=health%2Fgo.mod)](https://github.com/cplieger/go/blob/main/health/go.mod)

health gives distroless Go services a Docker healthcheck with no shell, curl or wget. Your service writes a marker file, and the same binary checks it when Docker asks.

It replaces the `curl` call a `HEALTHCHECK` usually runs. For an image that wraps a server you did not write, its `probe` package ships a static binary that checks an HTTP endpoint instead. The module uses only the standard library at run time, needs Go 1.27.3 or later and is licensed under Apache-2.0.

## Why use it

health is built for container images with no shell, whether the main process is your own Go service or a server you did not write.

- Your code decides what healthy means. `Set(true)` writes the marker and `Set(false)` removes it.
- When `/tmp` cannot be written, the check still reports healthy and the service logs one warning with the fix. A read-only filesystem never marks a working container unhealthy.
- An optional deadline fails the check when a work loop stops refreshing the marker.
- `Latch` keeps the container unhealthy once shutdown begins, even when late work succeeds.
- `CheckHandler` serves `GET /healthz`: 200 with `{"status":"ok"}`, or 503 listing one plain reason for each failing check.
- The `probe` binary passes only when every URL answers 2xx within one shared timeout, and it writes each failure to stderr.

Consider [alexliesenfeld/health](https://github.com/alexliesenfeld/health) if you want an HTTP health endpoint that runs checks against your database and other dependencies, with caching, periodic checks and a status for each component.

## Install

```sh
go get github.com/cplieger/go/health@latest
```

## Usage

Handle the `health` subcommand first, then mark the service healthy once it is ready:

```go
func main() {
    if len(os.Args) > 1 && os.Args[1] == "health" {
        health.RunProbe(health.DefaultPath) // exits 0 or 1
    }

    m := health.NewMarker(health.DefaultPath)
    defer m.Cleanup()
    m.Set(true)

    // ... run the service, and call m.Set(false) when it cannot do its work ...
}
```

Point the image's healthcheck at the same binary:

```dockerfile
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/app", "health"]
```

`RunProbe` exits 0 while the marker at `/tmp/.healthy` exists. It exits 1 when the marker is missing from a writable `/tmp`, and writes the reason to stderr.

If a separate `docker exec` process writes the marker, run it as the main process's UID and use `SetChecked` so a failed write fails the job.

For an image whose main process is a server you did not write, point the `probe` binary at an endpoint. No prebuilt binary is published, so build it with `go install` in a builder stage:

```dockerfile
FROM golang:1.27-alpine AS probe
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/cplieger/go/health/probe/cmd/probe@latest

FROM gcr.io/distroless/static-debian12
COPY --from=probe /out/probe /probe
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/probe", "-timeout", "4s", "http://127.0.0.1:2019/config/"]
```

Keep the probe's `-timeout` below Docker's `--timeout`, so the probe can write its failure line before Docker stops the check. The [file marker](docs/file-marker.md) and [HTTP probe](docs/http-probe.md) pages cover shutdown, external triggers and several URLs.

## API

- Marker: `NewMarker` with its `WithLogger` option (`MarkerOption`), `DefaultPath`, and the `Set`, `SetChecked`, `Cleanup`, `Healthy` and `Within` methods, with the `Signal` interface.
- Probe side: `RunProbe` and `Inspect` with the `WithMaxAge` option, with `Lease` to build its deadline, and the `Freshness` and `MarkerState` it reports.
- Reading a marker in-process: `Inspect`, `Freshness` and the `MarkerState` values.
- Shutdown: `Latch`, `NewLatch`, and the `Set` and `BeginDrain` methods.
- HTTP endpoint: `CheckHandler` with `Check`, and `Handler` for one signal.
- HTTP probe package: `probe.Check`, `probe.Run`, `probe.DefaultTimeout` and the `probe/cmd/probe` binary.

The full reference is on pkg.go.dev for [health](https://pkg.go.dev/github.com/cplieger/go/health) and [health/probe](https://pkg.go.dev/github.com/cplieger/go/health/probe). Its `Example` functions are runnable, and `go test` keeps them true.

## Degraded mode keeps the container healthy

`NewMarker` checks that the marker's folder is writable, and that anything already at the marker path is a regular file `Set` can refresh. When either fails, the marker enters degraded mode. The folder fails under compose's `read_only: true` with no tmpfs at `/tmp`; the marker fails when an earlier run as another user left one in a shared `/tmp` that this user cannot write, or when the path holds a directory or a FIFO. `Set` and `Cleanup` then do nothing, `SetChecked` returns nil, and the service logs one warning that names the fix. A marker path that cannot be stat'ed at all, such as a symlink loop, is not degraded mode: the `health` subcommand reports it unhealthy and names the error.

In degraded mode the `health` subcommand reports healthy, because the service still works and only its health reporting is broken. `Handler`, `Healthy` and `Within` report unhealthy, even when a marker was left in the folder, because `Set` cannot change it. So do not use `Handler` as the only liveness probe of a service that may run read-only without a tmpfs at `/tmp`. A platform that restarts on a failed probe, such as Kubernetes, would restart a working container.

To run read-only with a working signal, mount a tmpfs at `/tmp`:

```yaml
read_only: true
tmpfs:
  - /tmp:size=1m,mode=1777,noexec,nosuid,nodev
```

## The freshness deadline is opt-in

By default the check passes for as long as the marker exists, and Docker's `--interval` decides how often it runs. `WithMaxAge` adds a deadline, and a marker older than it fails the check. Every `Set(true)` refreshes the marker's age, so a service that calls it once per work cycle needs no other change.

Arm it only for a service that runs its own work loop at a known interval. Leave it off for a service that a separate `docker exec` triggers, because that service is healthy while it waits between triggers. Build the deadline with `Lease`, which saturates instead of overflowing. The [freshness deadline](docs/freshness.md) page has the details and `Inspect`.

## Unsupported by design

health leaves these out on purpose. The [non-goals](docs/non-goals.md) page gives the reason for each.

- Running dependency checks on each request. `CheckHandler` reads signals your code keeps, and never calls a database or an upstream itself.
- Separate liveness and readiness signals. Use two markers with different paths.
- A `context.Context` or a shutdown hook. `Cleanup` is the shutdown action.
- Status-change callbacks. Wrap `Set` to add your own.
- A staleness check by default. The deadline is opt-in for each service.
- Prometheus metrics. A gauge over `Healthy` adds one.
- Content inside the marker file. The check is one `os.Stat`.

## Documentation

- [The file marker](docs/file-marker.md) covers wiring, degraded mode, external triggers, shutdown and the HTTP handler.
- [Freshness deadline](docs/freshness.md) covers when to arm `WithMaxAge`, building it with `Lease`, and reading a marker in-process with `Inspect`.
- [HTTP probe](docs/http-probe.md) covers the Dockerfile, timeouts, several URLs and exit codes.
- [Non-goals](docs/non-goals.md) lists what the library leaves out and why.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
