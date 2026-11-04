# HTTP probe

This page covers the `probe` binary and its Go package, for an image whose main process is a server you did not write that already answers HTTP. It explains how to add it to an image, timeouts, several URLs, what it checks and its exit codes.

## Adding it to an image

Nothing in such an image can write a marker, so the endpoint's answer is the health signal. Build the static binary in a builder stage and copy it into the image:

```dockerfile
FROM golang:1.27-alpine AS probe
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/cplieger/go/health/probe/cmd/probe@latest

FROM gcr.io/distroless/static-debian12
COPY --from=probe /out/probe /probe
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/probe", "-timeout", "4s", "http://127.0.0.1:2019/config/"]
```

No prebuilt binary is published, so `go install` in a builder stage is the way to get it. Pin a version in place of `@latest` to keep builds repeatable, written as `@vX.Y.Z`. The probe is its own Go module, `github.com/cplieger/go/health/probe`, versioned separately from the marker library. Its git tags are named `probe/vX.Y.Z`, and Go maps the bare version to them.

## Timeouts

`-timeout` is one budget shared by every URL in a run, 5 seconds by default. Keep it below Docker's `--timeout`. The probe needs that margin to give up, write the failure line to stderr and exit 1 before Docker stops the check. A check Docker stops still counts as failed, but its failure line is lost. A zero or negative `-timeout` fails at once.

## Several URLs

Pass several URLs to check several surfaces in one run. Every URL must answer 2xx within the shared budget:

```dockerfile
CMD ["/probe", "-timeout", "4s", "http://127.0.0.1:80/health", "http://127.0.0.1:2019/config/"]
```

The probe checks every URL even after one fails, and writes one stderr line for each failure, so a single run names every broken surface.

## What it checks

The probe sends a GET to exactly the URLs you pass, with Go's default HTTP client. It follows redirects, and it passes when the final response has a 2xx status. Any other status, a connection error or the end of the budget is a failure. It has no TCP mode and no setting for other status codes.

An `https://` URL works too. The probe verifies the server's certificate against the image's system certificates and has no option to skip that check, so the image needs CA certificates. `gcr.io/distroless/static-debian12` includes them.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Every URL answered 2xx |
| 1 | At least one URL failed. Each failure is a line on stderr, which `docker inspect` shows |
| 2 | Usage error, such as no URL given |

## Using the package directly

The `probe` package exposes the same logic for a Go program that builds its own binary. `probe.URL` checks one URL and returns nil on a 2xx final response. `probe.Check` checks every URL within one budget, writes one line per failure to the writer you pass and returns 0 or 1. It returns 1 for zero URLs, so an empty healthcheck cannot report healthy. `probe.Run` calls `probe.Check` with stderr and exits with its result. `probe.DefaultTimeout` is the 5-second default budget.
