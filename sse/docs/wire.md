# The wire contract

This page describes the bytes the Go hub writes and the JSON its digest endpoint accepts, for anyone writing a client or reading a stream by hand.

## The cursor

Every frame `Publish` sends carries `id: <epoch>:<offset>`. The epoch is 16 random lowercase hex characters, minted when the hub is created. The offset grows by one with each published frame. A client presents the last id it received as `Last-Event-ID` when it reconnects.

`ParseCursor(s)` parses that value and `Cursor.String()` renders it back. The empty string is the zero `Cursor` with no error. Any other value must be the epoch, a colon and a decimal offset with no leading zero, at most `MaxOffset`, or the error wraps `ErrCursor`.

`MaxOffset` is `2^53 - 1`, so a JavaScript peer that holds an offset as a `Number` never rounds it.

The epoch changes whenever a hub is created. A client that reconnects after a restart, or to another process, presents a cursor from another epoch and is told to reconcile.

## The stream

`Serve` answers with `Content-Type: text/event-stream`, `Cache-Control: no-cache, no-transform` and `X-Accel-Buffering: no`. `no-transform` asks a proxy not to compress or rewrite the stream, and `X-Accel-Buffering: no` turns off nginx response buffering. The stream then carries, in order:

1. `retry: <ms>`, the wait before a client reconnects. It is always written and is never 0. The default is 1500.
2. The hello, `event: sse:hello`.
3. The missed frames, when the hello says `resumed`.
4. Any frames the `WithOnConnect` hook writes. They carry no id.
5. Live frames from `Publish`.

The hub decides the hello, the replay and the start of live delivery under one lock. So the head the hello reports is the offset where the replay ends and live frames begin.

A keepalive is the named frame `event: sse:keepalive` with `data: {}`, every 15 seconds by default. It carries no id, so it moves no cursor. With `WithKeepaliveEvent("")` the hub writes the comment `: keepalive` instead, which an `EventSource` never dispatches.

A connection whose scope is not everything also carries position marks: an id-only block, `id: <epoch>:<offset>` and a blank line, with no `event:` and no `data:`. A narrow cursor otherwise moves only on the frames it receives, so out-of-scope traffic would push the replay floor past it. The hub writes a mark at most once a second, and the first one right after the handshake. It writes one only when nothing for the connection is queued and the head has moved past the last id it wrote, so every frame the connection receives at or below the mark has already been sent. An `EventSource` records the id and dispatches nothing, and `@cplieger/sse` moves its cursor to the mark behind any frames it is holding. A burst larger than the replay buffer within one second still ends as `gap_floor`.

The hub ends a stream with `event: sse:reset`. Its data is `{"reason":"slow"}` for a client whose buffer filled, written under a 250ms deadline, or `{"reason":"shutdown"}` when the hub shuts down.

Event names that start with `sse:` are reserved for these frames. `Wire` is the revision of this contract, and every hello declares it.

## The hello and its verdicts

The hello's data is a JSON object with eight fields, and a ninth, `conn`, on a connection served with `WithRequestScope`:

| Field | Meaning |
| --- | --- |
| `wire` | the contract revision, `Wire` |
| `epoch` | the hub's epoch |
| `floor` | the oldest offset the hub can replay, as a decimal string, `"0"` when it holds nothing |
| `head` | the newest offset published, as a decimal string |
| `resumed` | true when the missed frames follow the hello |
| `verdict` | why, for logs and counters |
| `keepalive_ms` | the keepalive interval in milliseconds |
| `keepalive_event` | the keepalive frame's name, empty for the comment form |
| `conn` | the 22-character id that names this connection to the scope endpoint, absent unless the connection can be rescoped; it is a capability, so never log it |

A client branches on `resumed` only. The verdict is one of seven values:

- `fresh`: the client presented no cursor.
- `resumed`: the hub holds every missed frame within the reply cap, and they follow the hello in order.
- `gap_floor`: the cursor is below the floor, so frames were lost to retention.
- `gap_budget`: the hub holds the gap, but the frames the connection would receive outnumber the reply cap. The cap counts only frames in the connection's scope, and a resume over it is refused whole, never cut to a prefix.
- `gap_ahead`: the cursor is beyond the head.
- `epoch_changed`: the cursor belongs to another hub, such as the process before a restart.
- `cursor_invalid`: the cursor did not parse.

Every verdict other than `resumed` comes with `resumed: false` and no replay, and the client reconciles from the application's state. The hub checks the three gaps in the order ahead, floor, budget, so the verdict names the first reason a resume failed.

## Scopes

A connection receives broadcasts, the events published with an empty topic, plus the topics in its scope. A scope is everything, broadcasts only, or a set of topics. A scope narrows delivery and grants nothing.

A connection served with `WithRequestScope` takes its scope from the `SSE-Topics` request header: a JSON array of topics (`[]` is broadcasts only), or `null`. An absent header or `null` is everything. A topic is 1 to 128 bytes of printable ASCII (`0x21` to `0x7E`) other than `"` and `\`, so the header and its JSON form are the same bytes. Both halves fix the same limits:

| Constant | Value |
| --- | --- |
| `MaxScopeTopics` | 256 topics in one scope |
| `MaxScopeHeaderBytes` | 4096 bytes of `SSE-Topics`; a longer scope connects with no header and narrows by `POST` |

`Serve` answers a header outside the grammar or the limits with 400 `sse_scope`, before it subscribes.

The scope endpoint replaces a live connection's scope:

| Direction | JSON |
| --- | --- |
| Request (`POST`, `application/json`) | `{"conn": "<22 chars>", "seq": 1, "topics": ["run:a"]}`, where `topics` is `null` for everything and must be present |
| Response (200) | `{"epoch": "<hex16>", "offset": "42"}` |

`seq` is a positive integer the client increments for each request on a connection, retries included. The hub applies a request only when its `seq` is above the last one applied, so a request the client abandoned cannot land after a later one and undo it. `offset` is a decimal string: every frame at or below it was matched against the old scope, and the new scope delivers only later offsets.

| Status | Code | When |
| --- | --- | --- |
| 400 | `scope_invalid` | the body does not parse, or lacks `conn`, a positive `seq` or `topics` |
| 400 | `sse_scope` | a topic outside the grammar, or more than `MaxScopeTopics` |
| 403 | `scope_forbidden` | the caller does not own the connection |
| 404 | `scope_conn` | no live rescopable connection has this `conn`, including after `Shutdown` |
| 405 | `scope_method` | not `POST`; `Allow: POST` is set |
| 409 | `scope_stale` | a request with this `seq` or a later one was already applied |
| 413 | `scope_too_large` | the body exceeds 64 KiB |
| 415 | `scope_content_type` | the body is not `application/json` |

`@cplieger/sse` treats a 400 `sse_scope` or `scope_invalid`, a 403, 405, 413 or 415, and a 404 other than `scope_conn` as a refusal, which a retry cannot change: it stops sending scopes until the stream is stopped and reconnects without one, so the connection carries everything. Every other answer, 404 `scope_conn`, 408, 409, 429 and 5xx among them, is transient and retried on the same connection or the next one.

## The digest

`(*Hub).DigestHandler(resolve Resolver, opts ...DigestOption)` returns an `http.Handler` that answers a client back from sleep, or an error wrapping `ErrConfig` for a nil resolver or a refused option. The client sends the versions it holds, pinned to an epoch. The handler answers which items changed or were removed.

`Resolver` is `func(ctx context.Context, held []Held) ([]State, error)`. It answers exactly one `State{Subject, Version, Status}` per requested `(Kind, Ref)` and none the request did not carry. `Status` is `StatusCurrent`, `StatusGone` or `StatusForbidden`; any other value is treated as a resolver mismatch, so the client is told to refetch.

The resolver runs on the request goroutine, and the library puts no bound on it. Bound it in time and in flight yourself. `ExampleHub_DigestHandler` in `example_test.go` wraps the handler in `httpserver.RouteTimeout` for that.

Mount the handler inside your authentication and cross-origin middleware, because it performs neither check. The resolver reads the caller from `ctx`, however your middleware stored it. When several users share a server, the resolver decides whether a caller may learn that an item exists. When it may not, the resolver answers `gone` for both cases.

`WithDigestMaxSubjects(n)` caps the items in one request at 256 by default, and a larger request is 400. `WithDigestMaxBody(n)` caps the body at 512 KiB by default, and a larger body is 413. A non-positive value for either is `ErrConfig`.

| Direction | JSON |
| --- | --- |
| Request (`POST`, `application/json`) | `{"epoch": "<hex16>", "subjects": [{"kind": "chat", "ref": "c1", "version": "7"}]}` |
| Response (200 for every well-formed request) | `{"epoch": "<hex16>", "floor": "0", "head": "42", "checked": 1, "must_refetch": false, "changed": [{"kind": "chat", "ref": "c1", "version": "9"}], "removed": [{"kind": "chat", "ref": "c2", "reason": "gone"}]}` |

A removed item's `reason` is `gone` or `forbidden`. The handler reads `floor` and `head` after the resolver returns, so `head` is at or after every version it compared.

For a non-empty `subjects` list, `must_refetch` is true when the request epoch is absent or not this hub's, when the resolver fails, or when its answer does not match the request by key. `changed` is then empty and the client refetches everything. A request with an empty `subjects` list gets `checked: 0` and `must_refetch: false`.

## Digest refusals

The handler refuses a request with a JSON error body:

- 405 `digest_method`, with `Allow: POST`, for any method other than `POST`.
- 415 `digest_content_type` when `Content-Type` is not `application/json`.
- 413 `digest_too_large` for a body over the cap.
- 400 `digest_invalid` for malformed JSON or for the first broken rule, which the message names.

The rules, checked in field order:

- `epoch`, when present, is 16 lowercase hex characters.
- `subjects` is present and holds no more items than the cap.
- Each `kind` is 1 to 32 bytes and matches `[a-z][a-z0-9_]*`.
- Each `ref` is at most 512 bytes, with no control, line separator or bidi characters.
- Each `version` is 1 to 64 bytes of printable ASCII.
- No `(kind, ref)` pair appears twice.
