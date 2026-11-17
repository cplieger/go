# Contributing to sse

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- A change to a frame, the cursor grammar, the hello, a reset reason or a timing constant lands in Go and TypeScript in one commit. A one-sided change passes its own suite and breaks tabs talking to the other half.
- When such a change is not additive, raise `wire` in `timing.json`, then `Wire` in `frame.go` and `WIRE` in `web/src/timing.ts` to match. Without the bump, tabs still running the old client accept the new frames and misread them.
- A change to a change-feed frame or to the `Feed-Position` header changes `feedwire`, `testdata/feed/cases.json`, `feedwire.Version` and the TypeScript parser in `web/src/feed/` with its `FEED_VERSION`, in one pull request. Never add a member inside a version, and never rename, retype or move the `listening` frame's `feed` and `wire` members.
- Keep `web/go.mod`, the placeholder module `web-ignore`. It stops `go test ./...` at `web/`, where `node_modules` holds Go files, and keeps the TypeScript tree out of the published Go module.

## Checks

The `*.integration*` suites in `web/src/` drive a Go test server, the nested module `internal/ssefixture`. CI builds it (`sseFixture` in `web/package.json`) and runs every suite: the existing ones in Chromium, and the change feed's (`web/src/feed/`) in Chromium, Firefox and WebKit (`cplieger.browsers`). Locally, a suite whose server is not built is skipped. To run them all:

```sh
(cd internal/ssefixture && go build -o "${TMPDIR:-/tmp}/ssefixture" .)
cd web && SSE_FIXTURE="${TMPDIR:-/tmp}/ssefixture" CPLIEGER_BROWSERS=chromium,firefox,webkit npm test
```

`SSE_FIXTURE_PORT` moves the server off its default port, 45781, when another run holds it. Without `CPLIEGER_BROWSERS`, the feed's suites run in Chromium only.

`npx stryker run` from `web/` mutates the working tree in place, because two suites read files outside `web/`. Start it on a clean tree and edit nothing until it ends.

Stryker leaves `web/stryker-setup-*.js` files behind. Delete them before you run the lint checks.

## Releases

The Go module and `@cplieger/sse` on npm and JSR share one version tag. A breaking change in the TypeScript half is a major release of both.

With such a break, add the new `/vN` to the `go.mod` module path and the module's own imports, or the release fails.

Keep TypeScript changes additive until the next Go major.
