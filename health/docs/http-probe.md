# HTTP probe

This page covers the `probe` package and its binary. A service that always listens uses the package in its own `health` subcommand to ask its `/healthz`. An image whose main process is a server you did not write uses the binary against that server's endpoint. The page covers both, then timeouts, several URLs, what the probe checks and its exit codes.

## Your own service's health subcommand

Derive the probe's URL from the address the server listens on, with `probe.LocalURL`, and pass it to `probe.Run`:

```go
if len(os.Args) > 1 && os.Args[1] == "health" {
    u, err := probe.LocalURL(listenAddr, "/healthz")
    if err != nil {
        fmt.Fprintln(os.Stderr, "health: LISTEN_ADDR:", err)
        os.Exit(1)
    }
    probe.Run(probe.DefaultTimeout, u) // exits 0 or 1
}
```

Dispatch the subcommand first in `main` and return before any other start-up work, such as loading the configuration, opening a store or taking a lock, so a probe never contends with the running server. Read the listen address from the same constant or parsed setting the server binds. In the image, call the binary by its absolute path: `HEALTHCHECK CMD ["/app", "health"]`.

`LocalURL` takes a `net.Listen` address such as `:8080`:

- An empty or unspecified host (`0.0.0.0`, `::`) becomes `127.0.0.1`, which a listener on network `tcp` or `tcp4` answers on. For a `tcp6` listener, pass `[::1]:port`.
- Any other host is kept, because a listener bound to one address answers on no other. An IPv6 host is bracketed.
- A missing port, a port that is not a number from 1 to 65535, a host that makes no valid URL, or a path that does not start with a slash is an error. Wrap it with the name of your own setting.

The probe never uses `HTTP_PROXY`, `HTTPS_PROXY` or `NO_PROXY`, and it opens a new connection for every URL, so it always reaches the listener itself.

A host allowlist in front of `/healthz` sees the probe like any other client. When the listener binds every address, the probe's request has a loopback `Host` and a loopback peer, so an allowlist that exempts loopback requests admits it. When the listener binds one non-loopback address, the probe's `Host` and peer are that address, which no loopback exemption covers, so that address must be on the allowlist, or the service adds its own bind host to its policy.

## A server you did not write

Nothing in such an image records a verdict, so the endpoint's answer is the health signal. Build the static binary in a builder stage and copy it into the image:

```dockerfile
FROM golang:1.27-alpine AS probe
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/cplieger/go/health/v2/probe/cmd/probe@latest

FROM gcr.io/distroless/static-debian12
COPY --from=probe /out/probe /probe
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/probe", "-timeout", "4s", "http://127.0.0.1:2019/config/"]
```

No prebuilt binary is published, so `go install` in a builder stage is the way to get it. Pin a version in place of `@latest` to keep builds repeatable, written as `@vX.Y.Z`. The probe is a package of the health module, so it shares health's versions: pin the same `@vX.Y.Z` you use for `github.com/cplieger/go/health/v2`.

## Timeouts

The timeout is one budget shared by every URL in a run: `-timeout` for the binary, 5 seconds by default, and the first argument of `probe.Check` and `probe.Run`. Keep it below Docker's `--timeout`. The probe needs that margin to give up, write the failure line to stderr and exit 1 before Docker stops the check. A check Docker stops still counts as failed, but its failure line is lost. A zero or negative timeout fails at once.

## Several URLs

Pass several URLs to check several surfaces in one run. Every URL must answer 2xx within the shared budget:

```dockerfile
CMD ["/probe", "-timeout", "4s", "http://127.0.0.1:80/health", "http://127.0.0.1:2019/config/"]
```

The probe checks every URL even after one fails, and writes one stderr line for each failure, so a single run names every broken surface.

## What it checks

The probe sends a GET to exactly the URLs you pass, with its own HTTP client. It follows up to ten redirects, stops when the shared deadline passes, and passes when the final response has a 2xx status. Any other status, a connection error or the end of the budget is a failure. It has no TCP mode and no setting for other status codes.

An `https://` URL works too. The probe verifies the server's certificate against the image's system certificates and has no option to skip that check, so the image needs CA certificates. `gcr.io/distroless/static-debian12` includes them.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Every URL answered 2xx |
| 1 | At least one URL failed. Each failure is a line on stderr, which `docker inspect` shows |
| 2 | The binary's usage error, such as no URL given |

## Using the package directly

`probe.Check` checks every URL within one budget, writes one line per failure to the writer you pass and returns 0 or 1. It returns 1 for zero URLs, so an empty healthcheck cannot report healthy. `probe.Run` calls `probe.Check` with stderr and exits with its result. `probe.DefaultTimeout` is the 5-second default budget.
