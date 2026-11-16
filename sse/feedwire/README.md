# feedwire

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/sse/v2/feedwire.svg)](https://pkg.go.dev/github.com/cplieger/go/sse/v2/feedwire)

feedwire is the wire of the change feed: the `changed` and `listening` frames and the `Feed-Position` header. A server writes every frame and header value through its encoders, and `@cplieger/sse/feed` in this module's `web/` tree reads them. It uses only the standard library.

## The wire

A `changed` frame announces one commit of one source, naming the kinds it touched, sorted:

```text
event: changed
data: {"source":"store","epoch":"9f2c4e1a7b3d5f60819a2b3c4d5e6f70","rev":1234,"kinds":["budgets","runs"]}
```

A `listening` frame tells a connection from where it hears every frame: per source, its epoch and each kind's floor. `wire` is the server's wire version and is absent on a frame a source publishes when it reopens:

```text
event: listening
data: {"feed":1,"wire":"3f9a0c1d2e4b5a67","sources":[{"source":"store","epoch":"9f2c4e1a7b3d5f60819a2b3c4d5e6f70","floors":{"budgets":1101,"runs":1230}}]}
```

`Feed-Position` is an RFC 9651 dictionary with one member per source, set on a read's response with the position taken before the read:

```text
Feed-Position: store=1234;epoch="9f2c4e1a7b3d5f60819a2b3c4d5e6f70", live=7;epoch="41ab0c9d8e7f6a5b4c3d2e1f00112233"
```

## Bounds

The decoders take untrusted input and refuse anything outside these bounds:

- Frame data is at most 64 KiB.
- A source name is a lowercase letter then at most 31 lowercase letters, digits or underscores, so it is a valid RFC 9651 dictionary key. A kind name is 1 to 32 lowercase letters, digits or underscores.
- An epoch is 32 lowercase hex characters, and `wire` 16.
- A revision or floor is an integer from 1 to `MaxRev` (999,999,999,999,999, the largest RFC 9651 Integer, which a browser number holds exactly). A `Feed-Position` revision may also be 0.
- A `changed` frame names 1 to 64 kinds, sorted, with no repeat. A `listening` frame names each source once and at most 64 kinds over all its sources; a kind at floor 0 is omitted.
- A member that is not part of the format refuses the frame. Numbers are read as JSON numbers, so `1234.0` is `1234`, and a repeated member keeps its last value, as in a browser's `JSON.parse`.

## The frozen members

In every feed format, a `listening` frame is a JSON object with a required top-level `feed` member holding the format's number as a JSON integer, and an optional top-level `wire` member holding 16 lowercase hex. Neither member is ever renamed, retyped or moved. `PeekListening` reads only those two, so a client built for one format can still read the server's wire version from a frame written in another, and show its newer-build notice instead of failing. `DecodeListening` refuses a frame of another format with an error wrapping `ErrOtherFormat`.

## The lockstep rule

A change to any frame member or to the header changes, in one pull request of this repository: feedwire, its fixtures in [`testdata/feed/cases.json`](../testdata/feed/cases.json), `Version`, and the TypeScript parser in `web/src/feed/` with its `FEED_VERSION`. No member is added inside a version. The fixtures carry their own `feed_version`, and the Go and TypeScript tests both fail when it differs from their half's version, so a one-sided change fails CI.

An app pins the encoder through `go.mod` and the parser through `package.json`. `createFeed` in `@cplieger/sse/feed` throws `FeedVersionMismatch` when the app's generated `FEED_VERSION` differs from the package's, so a build that mixes two formats fails its first test.
