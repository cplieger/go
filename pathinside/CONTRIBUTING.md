# Contributing to pathinside

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Checks

CI runs the tests on Linux only, where a backslash is an ordinary filename byte and `Root.Contains` is case-sensitive. After a change to how a predicate splits, cleans or compares a path, also run `go test ./...` on Windows.
