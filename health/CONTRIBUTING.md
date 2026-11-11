# Contributing to health

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- A change to what degraded mode reports updates the package comment, the `NewStatus`, `WithMarker` and `RunProbe` comments, the warning's `effect` text (`degradedEffect`), the README's degraded-mode section and `docs/file-marker.md` together. A missed one tells callers the opposite of what the code does.
- `/healthz` reads memory only. A change that makes `Healthy`, `Fresh` or `CheckHandler` read the marker file is declined, because an endpoint that depends on a file reports unhealthy whenever the file cannot be written.

## Checks

- After a change to degraded mode, run the tests as a non-root user on Linux. Root can write into the `0500` directory those tests create, so they skip and `go test` still reports a pass.
