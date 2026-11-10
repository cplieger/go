# Contributing to health

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- A change to what degraded mode reports updates the package comment, the `Healthy`, `Within`, `Inspect`, `RunProbe` and `Handler` comments, `NewMarker`'s warning text (`degradedEffect`), the `MarkerState` and `Freshness` comments, the README's "Why use it" bullet and degraded-mode section, `docs/file-marker.md` and `docs/freshness.md` together. A missed one tells callers the opposite of what the code does.

## Checks

- After a change to degraded mode, run the tests as a non-root user on Linux. Root can write into the `0500` directory those tests create, so they skip and `go test` still reports a pass.
