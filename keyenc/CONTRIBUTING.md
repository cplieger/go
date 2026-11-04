# Contributing to keyenc

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Scope

A fix that keeps every key's bytes can go straight to a pull request.

Open an issue first to change any key's bytes. Consumers store and compare these keys, and the README promises that a safe key keeps its bytes.

## Rules

- Change which key the encoder writes, or which keys the decoder accepts, in Go and TypeScript in one commit, and regenerate the fixture after an encoder change. One side alone passes its tests and breaks consumers comparing keys across languages.
- A grammar change also updates the package comment in `doc.go` and [How keyenc encodes a key](docs/how-it-works.md) in the same commit. pkg.go.dev readers and anyone reimplementing keyenc rely on them.
- `web/.prettierrc.json` is a hand-maintained copy of the root `.prettierrc.json`, which comes from cplieger/ci. When the root file changes, copy it into `web/`, and never edit the package copy on its own, because the next copy overwrites the edit.
- `web/go.mod` is a placeholder module. It stops `go test ./...` at `web/`, where `web/node_modules` holds Go files from npm packages, and keeps the TypeScript out of the published Go module. Do not delete it.

## Releases

The Go module and the npm and JSR package share one version tag. A breaking change in either half is a major release of both, and the Go module path takes the new `/vN` suffix.

Changing the key an existing list of fields produces is a breaking change. Its upgrade steps name the stored keys to rebuild.
