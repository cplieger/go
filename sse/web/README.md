# @cplieger/sse

> A Server-Sent-Events client that resumes exactly where it stopped, notices a dead stream, and reconciles after a sleep.

The TypeScript half of [github.com/cplieger/go/sse/v2](https://github.com/cplieger/go/tree/main/sse), speaking the wire the Go `sse` package serves: every frame carries an `epoch:offset` cursor, every connection opens with a hello that says whether that cursor was honoured, and the client turns both into a projection an application can trust across a page hide, a phone lock or a network change. A native `EventSource` cannot set a request header, cannot tell a silent stream from an idle one, and reconnects on its own schedule with whatever `Last-Event-ID` it last saw; this client owns the connection over `fetch` and `ReadableStream` instead, presents the cursor it holds, measures liveness on bytes, closes a hidden tab's stream after a minute and reopens it on return, backs off with full jitter, and holds incoming frames while the application asks the server what changed. It has no runtime dependencies, and the DOM readers (`document.visibilityState`, `navigator.onLine`) are injectable, so the same runtime runs in Node against the Go test fixture.

## Install

```sh
npx jsr add @cplieger/sse
# or
npm i @cplieger/sse
```

## Usage

```ts
import { createStream, createVersionMap } from "@cplieger/sse";

const versions = createVersionMap();

const stream = createStream({
  url: "/api/events",
  headers: { "SSE-Client": profileTag },
  alive: { url: "/api/alive" },
  versions,
  onFrame: (frame) => {
    applyToStore(frame.type, frame.data);
  },
  revalidate: async (ctx) => {
    // Runs once per hello and once per wake, never concurrently. Every fetch in
    // here takes ctx.signal; ctx.full means the map was cleared, so skip the digest.
    await reconcile(versions.snapshot(), ctx);
  },
  onLifecycle: (ev) => {
    log.debug("sse", ev);
  },
});

stream.start();
```

The first frame of every connection is a hello: it names the server epoch, the ring's floor and head, the keepalive interval and event name, the server's build when the hub sets one, and whether the cursor the client presented was resumed; a hello whose `wire` is outside `[minWire, maxWire]` (both default to the package's `WIRE`) ends the connection with a `wire_unsupported` record and keeps the cursor.

The client keeps its cursor as `epoch:offset`, presents it as `Last-Event-ID` on every connect, and advances it only as frames are delivered to `onFrame`, so a reconnect after a gap either replays exactly the missed frames or reports through the hello that the gap could not be covered, and the application reconciles instead of trusting the replay.

`stream.state()` returns a `StreamState`: the connection kind (`stopped`, `connecting`, `open`, `backoff`, `offline` or `hidden_closed`), its generation and attempt, and the readings that kind acts on, such as `visible`, `online`, the hello's `keepaliveMs` and when a backoff ends. The package exports the factories, the wire helpers and the types they take and return; its parser, reducer and tab fold are internal.

Each run's `RevalidateContext` carries its `cause`, the map's `epoch`, the connection's `generation`, `full`, `build` and `signal`. For a `hello` run, `build` is the build that hello carries, so a page that reconnects to a redeployed server learns it before its first read; it is `null` when the hello carries none and for every other cause.

While `revalidate` runs, incoming frames are held and delivered in order once it settles; runs are single-flight, a second cause queues at most one more run (a queued `hello` keeps its cause and its `build` when a later wake joins it, even after a revalidate timeout ends that hello's connection), and a held queue past 2000 frames or 64 MiB ends the connection so the next connect resumes from the cursor instead.

With `alive` set, every keepalive (or every `everyBeats`-th) is acknowledged with a `POST` to that URL carrying the stream's headers, so a server-side presence table keyed on `SSE-Client` sees which clients are still reading; a failed acknowledgement is reported as an `alive_ack` record.

A 401 ends the session's stream instead of retrying it: a 401 on the stream request, on an alive acknowledgement, or from a `revalidate` that rejects with `{ status: 401 }` stops the stream and emits an `auth_lost` record whose `route` is `stream`, `alive` or `digest`. A tab's own stream and the worker host behave alike, and the host passes the record to every tab. Any other status backs off and reconnects.

`createWorkerHost` runs one stream per browser profile inside a `SharedWorker`, and `attachToWorker` connects each tab to it over a `MessagePort`, folding the tabs' visibility and network readings into that one connection; where `SharedWorker` is absent or the worker never answers, the tab falls back to its own stream and reports the switch. The `fallback` option returns a `TabFallback`: the `createStream` result together with the `versions` map and the `headers` record it was built on, which is what lets the `TabAttachment` work the same in both modes. `observe(subject, version, epoch)` reports a stamp the tab applied, into the host's version map or the fallback's; `reconnect({ resetCursor })` reconnects the stream the tab is attached to; `setTag(tag)` presents a new `SSE-Client` value with one reconnect, written into the host's headers or the fallback's; and `detach("logout")` ends the profile's stream. The tab writes its `tag` into the fallback's headers before the first connect, so a consumer never fills `SSE-Client` itself.

The host presents the first non-empty tag a tab attaches with and replaces it, reconnecting once, when a later attach or `setTag` carries a different one; the empty tag is ignored.

The host's `revalidate(ctx, tabs)` body performs one digest for the profile and fans the verdict to every tab through `tabs.run(ctx, { changed, removed })`, so each tab's `revalidate` receives a `TabRevalidateContext` carrying the host run's `build`, `changed` and `removed` beside its own `signal`. A tab's own full run when it returns carries the open connection's build, and `null` while the stream is between connections or when the host sends none. A `state` message reaches a tab before the `lifecycle` record of the same transition, so `attachment.state()`, a `StreamState`, is current inside the handler, and a tab attaching to a live stream receives `state` and then a `tab_attached` record naming that state.

### Scopes

Against a hub serving `WithRequestScope` and its `ScopeHandler`, a stream can narrow what it receives. Set `scopeUrl` to the scope endpoint, and optionally `initialScope` for the first connect. Without `scopeUrl` the stream never sends a scope, and a `setScope` settles at once while connected, or at the next hello.

`stream.setScope(scope)` replaces the scope: `null` is everything, `[]` broadcasts only, and a list adds those topics. A topic must pass `validTopic`, and a list holds at most `MAX_SCOPE_TOPICS`; anything else throws a `RangeError` before a byte is sent. The promise resolves once the server is guaranteed to deliver the scope, with the offset after which it does. Read a newly added topic's state after it resolves, and keep applying frames idempotently, because a frame the read already reflects can still arrive. It rejects with a `ScopeError` whose `failure` is `stopped` (on or after `stop()`, though the next start still presents the scope; a call before the first start waits for the first hello), `superseded` (a later call replaced it before it was covered) or `link` (a tab detached).

Each connect presents the current scope in the `SSE-Topics` header, up to `MAX_SCOPE_HEADER_BYTES`, so a reconnect keeps it; a larger scope connects as everything and narrows by `POST`. A refused scope (a 400 `sse_scope` or `scope_invalid`, 403, 405, 413, 415, a 404 other than `scope_conn`, or a 400 to a connect that presented the header) never takes the stream down: the stream emits `scope_refused`, stops sending scopes until `stop()` and falls back to everything, which is always correct. Any other answer is transient and retried. A 401 from the endpoint is `auth_lost` with `route: "scope"`.

In the worker host, each tab draws its own scope: `attachToWorker`'s `scope` option sets it at attach, and `TabAttachment.setScope` replaces it, with the same contract. The host owns the stream's scope, the union of its tabs' scopes, which is why `host.stream()` returns a `HostStream`, a `Stream` without `setScope`. The host's `deliver(frame, scope)` option decides which tab receives a frame; a throw delivers it and emits `deliver_failed`. Against an older host that does not know scopes, `TabAttachment.setScope` resolves with `null`, which means the tab receives everything and should read in full.

The package typechecks under `lib: ["ESNext", "WebWorker"]` as well as under the DOM lib (`tsconfig.worker.json` is the check), so a consumer's worker entry can import `createWorkerHost` from a worker-only program: the DOM visibility and online sources read `document` and `window` through `globalThis` and are inert where they are absent.

`onLifecycle` receives every state change and diagnostic as one `LifecycleEvent` record (`state`, `hello`, `watchdog`, `reset`, `revalidate`, `hidden_closed`, `alive_ack`, `worker_dead` and the rest of the union), which is the surface to log and to count.

### Connection state

`connectionOf(state, lostAt)` reads a `StreamState` as a `Connection`: `{ state: "live", since: null }` while the stream is `open`, and `{ state: "lost", since: lostAt }` in every other kind, the first connect included. It reports facts and applies no delay, so a quick reconnect reads `lost` until the next hello. Set `lostAt` from the `state` record whose `from` is `open`:

```ts
let lostAt: number | null = null;
// in onLifecycle:
if (ev.kind === "state" && ev.from === "open") lostAt = Date.now();
// on each state change:
headerStatus.connection(connectionOf(attachment.state(), lostAt).state);
```

`@cplieger/ui`'s header status takes `state` as it is and owns both delays: the grace before a drop shows "Reconnecting…", and `offlineAfter`.

### The change feed (`@cplieger/sse/feed`)

The `./feed` entry keeps a tab's stores current from the server's `changed` and `listening` frames ([the feed](https://github.com/cplieger/go/blob/main/sse/feedwire/README.md)). Every store is read once in full, its response's `Feed-Position` header seeds its subscription, and from then on each frame either says nothing that store has not seen, or names the kinds to catch up from the store's position. A source whose log the server keeps answers a catch-up with rows; any other kind is read again in full.

```ts
import { createFeed } from "@cplieger/sse/feed";

// The server's feedwire.Version, each kind's source, and the sources that keep a log.
const FEED_VERSION = 1;
const KINDS = { budgets: "ledger", accounts: "ledger", health: "status" };
const LOGGED_SOURCES = ["ledger"];

let lostAt: number | null = null;
const feed = createFeed({
  feedVersion: FEED_VERSION,
  kinds: KINDS,
  logged: LOGGED_SOURCES,
  catchUp: readChanges,
  onWire: noteServerVersion,
  stream: {
    path: "/api/events",
    worker: BUNDLE_URLS.feed_worker,
    onLifecycle: (ev) => {
      if (ev.kind === "state" && ev.from === "open") lostAt = Date.now();
      if (ev.kind === "auth_lost") signIn();
    },
  },
});

const res = await readBudgets();
const sub = feed.watch("budgets", budgetsSink, res.headers);
```

- `feedVersion` is the feed format the app was built for, the server's `feedwire.Version`. `createFeed` throws `FeedVersionMismatch`, naming both numbers, when it is not this package's, so a build that pairs a frame encoder and a parser of different formats fails its first test that creates a feed.
- `catchUp` sends one catch-up request for `{ source, epoch, since, kinds }` and resolves to `{ ok: true, answer }` with the changed rows, or `{ ok: false, failure }`, where `failure` is `"retry"` for a request that may succeed if sent again and `"refused"` otherwise. The feed aborts its signal once every subscription in the request has stopped.
- `stream` attaches the tab before `createFeed` returns, to the `SharedWorker` at `worker` with `path` in its query, or to the tab's own stream at `path` where there is no `SharedWorker`; `feed.attachment` is that `TabAttachment`, and `null` with `reconnect`. `path` must be root-relative, or `createFeed` throws a `RangeError`. `worker` is the served URL of the `@cplieger/sse/feed/worker` entry, resolved against the document's base URL; `createFeed` throws a `RangeError` for one that is not a URL, has a fragment or already has a `stream` parameter, which webbundle never writes, and `type` (default `"classic"`) is how it loads; a dev server that serves the entry as ESM needs `"module"`. `onLifecycle` receives the tab's lifecycle records, from the shared stream or the tab's own: the `state` records that set `lostAt` for the [connection state](#connection-state), and `auth_lost` when the server no longer takes the credential.
- A sink's `stale()` re-reads in full, calls `sub.seed(headers)` with that read's headers before its promise settles, and returns the promise. A read that settles with no seed backs off on the stream's reconnect curve (`timing` and `random` take the stream's). A sink that shows a loading skeleton during that read wraps it in `@cplieger/ui/skeleton`'s `skeletonTiming` itself.
- `onWire` gets every `listening` frame's `wire`, in any feed format. A `listening` frame of another feed format stops the feed applying frames until one of its own format arrives, with no read and no reconnect, so the page shows its reload banner instead of reading in a loop.

`@cplieger/sse/feed/worker` is the `SharedWorker` script `stream` opens, so an app whose stream carries only the feed writes no worker. The app lists it as a `Classic` entry of `go/webbundle` and reads its served URL through `BUNDLE_URLS`:

```go
webbundle.Entry{Name: "feed_worker", Path: "node_modules/@cplieger/sse/src/feed/worker.ts", Kind: webbundle.Classic}
```

The entry opens one stream at its URL's `stream` query parameter and throws a `RangeError` naming the parameter when it is missing or names no root-relative path. Its host takes no revalidation, because each connection's `listening` frame carries what a reconnect needs.

An app whose stream also carries its own events, or whose `revalidate` has work outside the feed, gives `reconnect` in place of `stream`, attaches the tab itself and passes each frame to `feed.onFrame`. `onFrame` returns `false` for an app event, so the same handler can pass it on. It throws on a feed frame that does not parse, so the stream reports `frame_rejected`; the feed has already called `reconnect` once for that episode, through the closure, because the link is made after the feed. Its worker script passes `retainListening` as the host's `retain`, so a tab attaching to a running stream receives the connection's floors before any later frame:

```ts
import { type TabAttachment, attachToWorker, createWorkerHost } from "@cplieger/sse";
import { createFeed, retainListening } from "@cplieger/sse/feed";

// In the tab:
let link: TabAttachment | null = null;
const feed = createFeed({ ...feedOptions, reconnect: () => link?.reconnect() });
link = attachToWorker({
  spawn,
  fallback,
  onFrame: (frame) => {
    if (!feed.onFrame(frame)) onAppEvent(frame);
  },
});

// In the worker script:
const host = createWorkerHost({ url: "/api/events", versions, revalidate, retain: retainListening });
```

`retain` is folded over every frame the host's stream delivers, and its result is posted to each tab inside the attach, before any later frame. The host clears it when the connection ends, and a throw from `retain` drops it, is reported with `reportError`, and reconnects the stream.

The tab id the worker host keys each tab by is a version 4 UUID drawn from `crypto.getRandomValues`, so a tab served over plain HTTP from a LAN address, which is not a secure context and has no `crypto.randomUUID`, attaches like any other. The package calls no `[SecureContext]` member, and a test over its published sources fails on one.

## Browser floor

`Response.body` as a readable stream (Firefox 65, Chrome 43, Safari 10.1), `AbortSignal.reason` (Firefox 97, Chrome 98, Safari 15.4), `visibilitychange` (Chrome 62, Firefox 56, Safari 14.1), `pagehide` and `pageshow`, and `BigInt` (Chrome 67, Firefox 68, Safari 14). The change feed adds `Object.hasOwn` (Chrome 93, Firefox 92, Safari 15.4), and it and the worker host's `retain` add `reportError` (Chrome 95, Firefox 93, Safari 15.4). `AbortSignal.any` is not used; two signals are composed by hand. `SharedWorker` is needed by the worker host only and is not part of the floor: where it is absent (Chrome for Android before 148, Samsung Internet) the tab runs its own stream through the fallback.

## Full documentation

The Go server API is in the [repository README](https://github.com/cplieger/go/tree/main/sse#readme). The hub options and the presence hook are in [Running the hub](https://github.com/cplieger/go/blob/main/sse/docs/hub.md), the change feed's frames and rules are in [The change feed](https://github.com/cplieger/go/blob/main/sse/docs/feed.md), and the hello's verdicts and the digest are in [The wire contract](https://github.com/cplieger/go/blob/main/sse/docs/wire.md). The shared timing constants both halves pin are in [`timing.json`](https://github.com/cplieger/go/blob/main/sse/timing.json).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
