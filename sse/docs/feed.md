# The change feed

This page describes the change feed's wire and how `@cplieger/sse/feed` keeps a tab's stores current with it, for anyone writing a server that emits the frames or a client that reads them. The Go encoders and decoders are in [feedwire](../feedwire/README.md), and the TypeScript client's API is in [web/README.md](../web/README.md#the-change-feed-cpliegerssefeed).

A server keeps one or more **sources**, each with its own epoch and its own revisions: a store with a change log, a settings file, or an in-memory set of parts with no log. Each **kind** belongs to one source. A commit announces only the kinds it touched, never its rows. A tab that holds a kind reads its rows through the server's catch-up endpoint (a source with a log) or reads it again in full (any other kind).

## The frames and the header

The feed rides on the hub's stream as two named events.

`changed` announces one commit of one source. `rev` is the source's revision after the commit, and `kinds` names the kinds it touched, sorted, with no repeat:

```text
event: changed
data: {"source":"store","epoch":"9f2c4e1a7b3d5f60819a2b3c4d5e6f70","rev":1234,"kinds":["budgets","runs"]}
```

`listening` is the first frame a server writes on every connection, fresh or resumed, after the hello and any replay. It carries no id, so it is never replayed. It names, for each source, its epoch and each kind's **floor**: a revision such that every later commit touching the kind reaches this connection as a `changed` frame. A kind no commit has touched yet is omitted. `feed` is the frame format and `wire` is the server's wire version:

```text
event: listening
data: {"feed":1,"wire":"3f9a0c1d2e4b5a67","sources":[{"source":"store","epoch":"9f2c4e1a7b3d5f60819a2b3c4d5e6f70","floors":{"budgets":1101,"runs":1230}}]}
```

When a source gets a new epoch inside one process (its file was replaced and reopened), the server publishes a `listening` frame naming only that source, with no `wire`, before any frame of the new epoch. That frame has an id, so a resume replays it.

`Feed-Position` is set on every read of a kind, as an RFC 9651 dictionary with one member per source. It is the position taken before the read, so it is never above what the read shows:

```text
Feed-Position: store=1234;epoch="9f2c4e1a7b3d5f60819a2b3c4d5e6f70", live=7;epoch="41ab0c9d8e7f6a5b4c3d2e1f00112233"
```

The bounds both halves enforce are listed in [feedwire's README](../feedwire/README.md#bounds). The fixtures in [`testdata/feed/cases.json`](../testdata/feed/cases.json) hold valid and refused cases of each frame and of the header, and both the Go and the TypeScript tests read them.

## Listen first, read second

A tab treats a read's position as complete only once it knows the point from which it hears every frame of that source. For each source it keeps the current epoch and each kind's floor, from its connection's `listening` frame, and every `changed` frame naming a kind raises that kind's floor.

Each `watch` call is a **subscription**: a kind, a sink and its own watermark, the `Feed-Position` of the read that seeded it. A subscription is **covered** when its watermark is on the source's current epoch and at or above its kind's floor. One below its floor has missed a commit: a subscription of a logged source is caught up from its watermark, and any other is read again in full through its sink's `stale()`.

This is enough because a seed at revision R is taken before its read. A commit above R is either at or below the kind's floor, so the subscription is below the floor and is caught up, or above the floor, so it arrives as a frame, which raises the floor and catches the subscription up. Two subscriptions of one kind, a list seeded at 100 and a detail at 200, each compare their own watermark.

## How the client applies frames

1. **Seed.** A seed before the source's first `listening` frame waits, and that frame checks it. A seed on the current epoch is checked at once. A seed on another epoch is read again once per `listening` frame of the source. A read with no position gives no watermark.
2. **Epoch.** Only a `listening` frame moves a source's epoch. When it changes, every subscription of that source on another epoch is read again. Another source's epoch never touches a subscription.
3. **Frames.** A `changed` or `listening` frame on the current epoch raises floors, never lowers them, and marks each subscription below its floor. A subscription with no watermark is marked by the next frame of its kind or source.
4. **One catch-up per source.** After `coalesceMs` (50 ms by default) the client sends one catch-up per source for its marked subscriptions, from the lowest of their watermarks, with the source's current epoch. Frames that arrive while it is in flight queue at most one more.
5. **Apply.** An answer upserts, then removes, then moves each subscription in its request to the answer's revision. A subscription re-seeded while the request was in flight takes none of the answer when the seed is on another epoch or below the request's `since`, since the answer lacks the commits between the two; one below its floor is caught up from its seed. A signal kind, a sink with no `upsert`, or a sink with no `remove` whose kind the answer removes keys of, is read again instead. A sink whose `upsert` or `remove` throws is reported through `reportError` and read again, and the answer still applies to the rest of its request. A `full` answer reads every subscription in the request again.
6. **Sources with no log.** A burst of frames in one `coalesceMs` window gives each marked subscription one full read.
7. **Failure.** A catch-up that may succeed if sent again (`retry`) is sent again on the stream's reconnect curve, 500 ms doubling to 30 s with full jitter by default. Frames during that wait only mark the source's subscriptions, those with no watermark included, and a `listening` frame of the source ends it at once. A refused one reads its subscriptions again in full. A 401 is the stream's `auth_lost`, not the feed's.
8. **Untrusted frames.** Every feed frame is parsed strictly. A kind or source this build does not know is dropped and the rest applied, because a newer server build may name it. A `listening` frame of another feed format stops the client applying feed frames until a frame of its own format arrives, with no read and no reconnect. A frame that fails the parse, or a `changed` frame on another epoch, is lost track: the client forgets its floors, calls `reconnect()` once, and the new connection's `listening` frame checks every subscription. That frame, which carries `wire`, ends the episode even when the new view no longer holds a source; a later bad frame calls `reconnect()` again.
9. **Wire version.** Every `listening` frame's `wire`, in any format, goes to `onWire`, so a page that only listens learns of a new build at its next connection.

A full read the client asks for through `stale()` is **re-reading** until the promise `stale()` returned settles, and nothing else ends it. Frames during the read only mark the subscription, so it never has two reads in flight. At the settling, the newest seed the read gave is checked once against the floor. A read that settles with no seed has failed: its next full read waits on the stream's reconnect curve, a `listening` frame of its source retries it at once, and its next seed resets the curve.

The feed paints nothing while a read is in flight. A sink that shows a loading skeleton while `stale()` reads wraps that read in `skeletonTiming` from `@cplieger/ui/skeleton` itself, so a fast read never flashes one.

## Every path to a seed and a stream position

| Path | Where the floors come from | What covers the seed |
| --- | --- | --- |
| Page load and first connect | the connection's `listening` frame | a seed before the frame waits for it; a seed after it is checked at once |
| A tab attaching to a running `SharedWorker` | the frame the worker host retained, posted before any later frame | the retained floors hold every frame the host received before the attach |
| A hello that resumed | the replayed frames, then the `listening` frame | nothing was lost; the frame catches a subscription up only when a commit landed while the server joined the connection |
| A hello that did not resume | the new connection's `listening` frame | every subscription below its kind's new floor is caught up or read again; a quiet kind costs nothing |
| A screen's own read answered while a catch-up is in flight | the source's floors | a seed at or above the catch-up's `since` takes the answer; one below it is caught up from the seed |
| A read in flight across a reconnect | the new connection's `listening` frame | answered before the frame, the frame checks it; answered after, its seed is checked against the new floors |
| A worker the browser ended, and a tab that spawned a new one | the new worker's first `listening` frame | as a hello that did not resume |
| A server restart or a change of the person's view | the `listening` frame naming each source's new epoch | every subscription of that source on another epoch is read again |
| A source's file replaced inside one process | the `listening` frame published at the reopen | the same, for that source only |
| A seed on another epoch than the current one | the next `listening` frame of that source | one full read per `listening` frame of the source |
| A source started after the stream joined | the `listening` frame the start publishes | the source's subscriptions wait until it arrives |
| A tab with no `SharedWorker` | the tab's own connection's `listening` frame | every row above, with the tab's own stream in the worker's place |
| Frames held during a revalidation and then discarded | the next connection's `listening` frame | the stream ends the connection, so this is a hello that did not resume |
| A tab attaching between two connections | the resumed connection's `listening` frame | a replayed `changed` frame for a source with no current epoch is ignored; the frame's floors cover every replayed commit |
| A server running a newer build | the new connection's `listening` frame, its unknown kinds and sources dropped | the known kinds apply as on any connection, and `wire` reaches `onWire` |
| A server writing another feed format | none | no feed frame applies until a `listening` frame of the tab's own format, which checks every subscription |
| Churn on a source with no log | each `changed` frame | one full read per `coalesceMs` window, and one more only when its seed is below the floor |
| A full read in flight, or one that failed | the frames that arrive during the read | the re-reading state above |
| A frame the tab could not read | none until the reconnect | lost track: one `reconnect()`, then the new `listening` frame checks every subscription |
| A catch-up answered `maintenance` while the server replaces a file | the reopen's `listening` frame | the catch-up is sent again on the curve; the reopen's frame ends the wait and reads each subscription of the source again once |

## Sharing one stream between tabs

`@cplieger/sse/feed/worker` is a ready `SharedWorker` script for an app whose stream carries only the feed, so such an app writes no worker. It opens one stream for every tab, at the root-relative path in its URL's `stream` query parameter, and throws a `RangeError` naming the parameter when it is missing or names no root-relative path, so no stream opens on a default. Its host takes no revalidation, because each connection's `listening` frame carries what a reconnect needs.

The app lists the entry as a `Classic` entry of `go/webbundle`, whose `Path` is the package's file under the static-src root:

```go
webbundle.Entry{Name: "feed_worker", Path: "node_modules/@cplieger/sse/src/feed/worker.ts", Kind: webbundle.Classic}
```

`createFeed`'s `stream` option, given in place of `reconnect`, takes the stream's path and the entry's served URL, `BUNDLE_URLS.feed_worker`. It opens the `SharedWorker` at that URL with the path set as its `stream` query parameter, and throws a `RangeError` for a URL that does not parse, has a fragment or has its own `stream` parameter. A tab with no `SharedWorker` opens its own stream at the path. Its `onLifecycle` receives the tab's lifecycle records either way, so the app hears `state` for its connection status and `auth_lost` for a credential the server no longer takes.

An app whose stream also carries its own events, or whose `revalidate` has work outside the feed, writes its own worker script instead, and passes `retainListening` as `createWorkerHost`'s `retain` option. It folds every frame the host's stream delivers into one retained `listening` frame: a new epoch replaces its source's entry, the same epoch keeps each kind's higher floor, and a `changed` frame raises the floors it names. The host posts the retained frame to each tab inside the attach, before any later frame, and clears it when the connection ends. A frame that fails the parse marks the retained frame as lost track, so a tab attaching to it reconnects once.

## The lockstep rule

A change to any frame member or to the header changes, in one pull request of this repository: `feedwire`, the fixtures, `feedwire.Version`, and the TypeScript parser with its `FEED_VERSION`. No member is added inside a version. In every format, `listening` keeps a required top-level `feed` integer and an optional top-level `wire` of 16 lowercase hex, so a client of one format can read the server's version from a frame of another.

An app pins the encoder through `go.mod` and the parser through `package.json`. `createFeed` throws `FeedVersionMismatch` when the app's generated `FEED_VERSION` is not the package's, so a build that pairs two formats fails its first test that creates a feed.
