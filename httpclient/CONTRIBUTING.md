# Contributing to httpclient

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- New parsing, classification or redirect logic also gets a property test in `prop_test.go` or a fuzz target. It reads headers and URLs from untrusted servers, and table tests cover only the inputs you thought of.
- `RetryRoundTripper` stores its configuration, the base delay included, but never a delay progression. Each `RoundTrip` keeps its own. One transport serves concurrent requests, and a shared progression would race and mix the delays of unrelated requests.
