# health

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/health/v2.svg)](https://pkg.go.dev/github.com/cplieger/go/health/v2) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=health%2Fgo.mod)](https://github.com/cplieger/go/blob/main/health/go.mod)

health gives a Go service one health verdict, kept in memory, served at `GET /healthz`, and checked by the same binary when Docker asks. It needs no shell, curl or wget in the image.

It replaces the `curl` call a `HEALTHCHECK` usually runs. For an image that wraps a server you did not write, its `probe` package ships a static binary that checks that server's HTTP endpoint instead. The module uses only the standard library at run time and is licensed under Apache-2.0.

## Why use it

health is built for container images with no shell, whether the main process is your own Go service or a server you did not write.

- Your code decides what healthy means. `Set(true)` records a healthy verdict and `Set(false)` an unhealthy one.
- `/healthz` reports the verdict your code recorded, because it reads memory and never a file.
- Shutdown cannot be hidden by late work. After `BeginDrain`, a healthy `Set` is dropped.
- A work loop that stops is caught. One lease, passed to `Fresh` and `WithMaxAge`, fails both `/healthz` and the healthcheck once the loop stops refreshing its verdict.
- A read-only root filesystem needs no extra mount. A service with no port keeps its marker file in `/dev/shm`, which stays writable.
- `CheckHandler` serves `GET /healthz`: 200 with `{"status":"ok"}`, or 503 listing one plain reason for each failing check.
- The `probe` binary checks a server you did not write: it passes only when every URL answers 2xx within one shared timeout, and it writes each failure to stderr.

Consider [alexliesenfeld/health](https://github.com/alexliesenfeld/health) if you want an HTTP health endpoint that runs checks against your database and other dependencies, with caching, periodic checks and a status for each component.

## Install

```sh
go get github.com/cplieger/go/health/v2@latest
```

## Choosing the pattern

| Your image | Verdict | The `health` subcommand or `HEALTHCHECK` |
| --- | --- | --- |
| Your service always listens | `health.NewStatus()` | GETs `/healthz` with `probe.Run` at the URL `probe.LocalURL` derives from the listen address |
| Your service opens no port by default | `health.NewStatus(health.WithMarker(health.MarkerPath("<name>")))` | Runs `health.RunProbe` on the same path, with the same lease |
| The image runs a server you did not write | none | The `probe` binary against that server's own endpoint |

## Usage

### A service that always listens

Handle the `health` subcommand first, before any other start-up work. Then record the verdict and serve it:

```go
const listenAddr = ":8080"

func main() {
    if len(os.Args) > 1 && os.Args[1] == "health" {
        u, err := probe.LocalURL(listenAddr, "/healthz")
        if err != nil {
            fmt.Fprintln(os.Stderr, "health:", err)
            os.Exit(1)
        }
        probe.Run(probe.DefaultTimeout, u) // exits 0 or 1
    }

    st := health.NewStatus()
    mux := http.NewServeMux()
    mux.Handle("GET /healthz", health.CheckHandler(
        health.Check{Signal: st, Reason: "the service is starting or shutting down"},
    ))

    // ... start serving mux on listenAddr, then:
    st.Set(true)

    // ... when shutdown begins, before waiting for work in flight:
    st.BeginDrain()
}
```

Point the image's healthcheck at the same binary by its absolute path:

```dockerfile
HEALTHCHECK --interval=30s --timeout=10s --retries=3 CMD ["/app", "health"]
```

The probe asks the listener itself, so a listener that stopped answering fails it, whatever the verdict says. Keep the probe's budget, `probe.DefaultTimeout` here, below Docker's `--timeout`, so the probe can write its failure line before Docker stops the check.

### A service with no port

A service that opens no port mirrors its verdict to a marker file, and the `health` subcommand reads that file:

```go
const name = "myservice"

const interval = time.Minute

func main() {
    lease := health.Lease{Interval: interval, Cycles: 3}.Duration()
    if len(os.Args) > 1 && os.Args[1] == "health" {
        health.RunProbe(health.MarkerPath(name), health.WithMaxAge(lease)) // exits 0 or 1
    }

    st := health.NewStatus(health.WithMarker(health.MarkerPath(name)))
    t := time.NewTicker(interval)
    defer t.Stop()
    for range t.C {
        st.Set(runOnce() == nil)
    }
}
```

`MarkerPath(name)` is `/dev/shm/myservice.healthy`. Every `Set(true)` refreshes the file, so `RunProbe` with `WithMaxAge(lease)` reads the same age that `Fresh(lease)` reads in memory. The two ages match while the wall clock does not step. `Fresh` measures with the monotonic clock and `RunProbe` with the file's modification time, so a forward step larger than the lease fails the file probe while `Fresh` still passes, and a backward step makes the file probe catch a stopped loop later by the size of the step.

When the same service also serves `/healthz`, for example on an opt-in metrics listener, list the Status and its freshness as two checks:

```go
mux.Handle("GET /healthz", health.CheckHandler(
    health.Check{Signal: st, Reason: "the last run failed"},
    health.Check{Signal: st.Fresh(lease), Reason: "the work loop has stopped"},
))
```

### A server you did not write

Point the `probe` binary at the server's endpoint. No prebuilt binary is published, so build it with `go install` in a builder stage:

```dockerfile
FROM golang:1.27-alpine AS probe
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/cplieger/go/health/v2/probe/cmd/probe@latest

FROM gcr.io/distroless/static-debian12
COPY --from=probe /out/probe /probe
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/probe", "-timeout", "4s", "http://127.0.0.1:2019/config/"]
```

Keep the probe's `-timeout` below Docker's `--timeout`, so the probe can write its failure line before Docker stops the check.

## API

- The verdict: `Status` from `NewStatus`, with the `WithLogger` and `WithMarker` options (`StatusOption`), and the `Set`, `BeginDrain`, `Healthy` and `Fresh` methods. `Signal` is the one-method interface a check reads, and `SignalFunc` adapts a function to it.
- The endpoint: `CheckHandler` with `Check`.
- The marker side: `MarkerPath`, `RunProbe` and its `WithMaxAge` option (`ProbeOption`), with `Lease` to build the deadline.
- The HTTP probe package: `probe.Check`, `probe.Run`, `probe.DefaultTimeout`, `probe.LocalURL` and the `probe/cmd/probe` binary.

The full reference is on pkg.go.dev for [health](https://pkg.go.dev/github.com/cplieger/go/health/v2) and [health/probe](https://pkg.go.dev/github.com/cplieger/go/health/v2/probe). Its `Example` functions are runnable, and `go test` keeps them true.

## Degraded mode

When `NewStatus` cannot write the marker, the Status enters degraded mode and logs one warning that names the fix. `RunProbe` then reports healthy, because the service still works and only its marker is broken, which a restart would not fix. `Healthy`, `Fresh` and `/healthz` keep reporting the verdict your code recorded.

A service that keeps calling `Set` needs no restart once the cause is fixed: the first `Set` that can write leaves degraded mode. One that sets its verdict only at startup makes no later `Set`, so once the obstacle is gone its probe reads the path as it stands until the service restarts: absent when nothing is there, and present when a regular file already is. The [marker file](docs/file-marker.md) page lists the causes.

## Unsupported by design

health leaves these out on purpose. The [non-goals](docs/non-goals.md) page gives the reason for each.

- Running dependency checks on each request. The Status is a verdict your code records, and `CheckHandler` never calls a database or an upstream itself.
- Separate liveness and readiness signals. Serve two `CheckHandler`s over two Status values.
- A `context.Context` or a shutdown hook. `BeginDrain` is the shutdown action.
- Status-change callbacks. Wrap `Set` to add your own.
- A staleness check by default. `Fresh` and `WithMaxAge` are opt-in for each service.
- Prometheus metrics. A gauge over `Healthy` adds one.
- Health written by a second process. Only the long-running process writes its verdict.

## Documentation

- [The health verdict](docs/status.md) covers recording the verdict, serving it, shutdown, a work loop that stops, several work loops and the log lines.
- [The marker file](docs/file-marker.md) covers a service with no port: where the marker lives, a shared `/dev/shm`, one writer, degraded mode and write failures.
- [Freshness deadline](docs/freshness.md) covers arming one lease for `Fresh` and `WithMaxAge`, building it with `Lease`, and when to leave it off.
- [HTTP probe](docs/http-probe.md) covers your own service's `health` subcommand and the `probe` binary for a server you did not write.
- [Non-goals](docs/non-goals.md) lists what the library leaves out and why.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
