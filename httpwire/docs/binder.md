# The binder

This page describes how httpwire admits, decodes and answers each row of a table, for anyone writing handlers, a guard or an idempotency store.

## Building the API

`New(table, bindings, options...)` checks the table, its bindings and its options together, and its error lists every refusal at once, so an app fails at start and never at a request. `Derive` runs the table checks that need no binding, and fills each row's `Errors` with its union.

A row's `Path` is relative to the table's `Prefix`: the API serves the row at `Prefix` then `Path` (`Prefix: "/api"` and `Path: "/items/{id}"` serve `/api/items/{id}`), and a `Path` that repeats the prefix is not stripped, so `/api/x` under `/api` serves `/api/api/x`. A `Path` starts with `/` and holds literal segments and whole-segment `{name}` wildcards, and two rows whose full paths one request could match are refused.

`WithSchema` is required. Every operation of the document must match one row by `operationId`, method and full path, and every row one operation. Each `Params`, `Query` and `Headers` field must be a declared parameter, required as the field is, and a row has a JSON request body exactly when it has a `Request`. A `Keyed` row's operation carries the `x-idempotency` policy the binder enforces (`docs/wire.md`), which `PolicyOf` answers for the row, and no other operation carries one.

A row whose class is not `Public` also needs `WithGuard`, `WithCredential` and `WithCursorOwner`. A row whose class admits a key needs `WithKeyRate`. A `Held` or `Keyed` row needs `WithHold`, a row with `Changes` needs `WithFeed`, and a `Keyed` `Raw` row needs `WithSpoolDir`. A table naming a `ClientHeader` needs `WithClientFloor`.

A path or query value decodes into a `TextUnmarshaler`, a string type with a `MaxBytes` method, an integer type with a `Range` method, a `uint64` or a `bool`. A query field may also be a slice of one of those types with a `MaxItems() int` method of at least 1, sent as one `name=` per value: it decodes every occurrence in order, and more than `MaxItems` values, or any value its type or the document refuses, answers `bad_query` with a field entry for each refused value (`path[3]`). Its document parameter is an array whose `maxItems` is `MaxItems`, with `style: form` and `explode: true`. `New` refuses any other field type, a list in `Params`, a list behind a pointer (`omitempty` makes it optional), and a document that describes a field otherwise.

## Request headers

A row's `Headers` is a struct of the request headers its handler reads, each field naming its header in a `header:"Name"` tag, and its handler receives them decoded as `Call.Headers`. A field takes the types a query value takes, but never a list, since a header carries one value. It is required unless it is a pointer or tagged `omitempty` or `omitzero`. A name matches in any case and is written canonically (`X-Thing`), as `net/http` keys the header map, and its document parameter is `in: header`, matched in any case too (RFC 9110 section 5.1). A required header missing, a header sent twice, or a value its type or the document refuses answers `bad_header`, with a field entry per refused header; a header no field names is ignored.

`Derive` refuses a field naming a header httpwire, the platform or the connection owns: `Content-Type`, `Content-Length`, `Host`, `Authorization`, `Cookie`, `Idempotency-Key`, `If-Match`, `Wire-Version`, `X-Request-Id`, the table's `ClientHeader`, and the hop-by-hop headers (`Connection`, `Proxy-Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailer`, `Transfer-Encoding` and `Upgrade`). It also refuses an unexported or untagged field, a name that is not a token, and one header named twice. On a `Raw` or `Stream` row the binder checks the headers the same way before the handler runs, and the handler reads `r.Header` itself, as it reads its path and query. A mount row's `Headers` only declares them: the mount decodes its own request, so only the names are checked.

## The order on every row

1. A write (any method but GET and HEAD) meets `httpserver.CrossOrigin`, which answers `cross_origin_refused`.
2. The client version floor answers `upgrade_required` for a client header below the floor.
3. Maintenance refuses or holds the request (below).
4. The outage: while `WithOutage` names `accounts` or `store`, every row not marked `DuringOutage` answers `accounts_unavailable` or `store_unavailable`.
5. The guard's `Wrap` admits the principal.
6. On a row that is not `Public`, a credential of `""` is a wiring bug: it answers `internal_error` and logs at ERROR.
7. The key rate takes one token per request from an API key's bucket and answers `rate_limited` with `Retry-After` when it has none.
8. The held-request cap wraps a `Held` row.
9. A write naming a `Wire-Version` other than this API's answers `wire_mismatch`.
10. Decoding: the path, the query, the headers, the body's media type, the body, `If-Match`, `Idempotency-Key` and the cursor.
11. The idempotency lookup on a `Keyed` row, then the handler.

A path value outside its type or schema answers `not_found`, because it names no resource. A query value answers `bad_query`, a header `bad_header` and a body `bad_body`, each with `fields` naming the refused paths.

A row whose body the binder decodes takes only `application/json`, matched case-insensitively, with any parameters and a charset of `utf-8` if one is given. A missing, unparsable or other media type answers `unsupported_media_type` (415) before the body is read and before any idempotency key is claimed. A `Raw` row with no `Request` reads its own body, held to the row's `MaxBody`, so it takes any media type. A handler that reads past the cap before it starts its answer has that answer replaced by `body_too_large`, with the header the row started with: the handler's headers are undone and the app's middleware's kept.

A JSON body is one JSON value no larger than the row's `MaxBody` (the table's, 64 KiB by default), else `body_too_large`. It is checked against the document's schema, which admits no member the type does not declare, and against the bound methods of its field types, and every failing path is listed in `fields`.

## Mounted handlers

A `Mount` serves rows that decode and guard themselves, such as an account library's sign-in routes. The binder registers them on its own mux under one wrap: the client floor, maintenance, the accounts outage (a store outage leaves them served), the wire version on a write, and the `Wire-Version` header on every answer. It adds no decoding, guard, cross-origin check or key rate, since the admitted principal exists only inside the mount.

## Answers and errors

A JSON row answers its `Response` at its `Status`, or at one of its `Successes` through `Status[T]` and `Answer`. A status the row does not declare, or a body that does not encode, answers `internal_error` and logs at ERROR.

An answer that is or embeds a `Checked[T]` is held to the open check's rules ([The wire](wire.md#open-checks)) before it is written. One that breaks a rule answers `internal_error` and logs at ERROR, naming the row and the rule. Each `upstream` is held to the envelope's bounds, and one whose service is outside the code grammar is left out and logged.

A handler's error answers through the row's union: an `*Error` whose code the union holds, else the first `OwnerCode` of the row's `Owners` that the error wraps, matched in order with `errors.Is`, else `internal_error`, logged. `Catalog` supplies each code's status and remediation, so a handler sets only the code, a message and, when it applies, a `Reason`, `Fields` or `RetryAfter`.

Every slot that holds a wire code is an `httpserver.ErrorCode`: `Error.Code`, `OwnerCode.Code`, `Endpoint.Codes`, `Endpoint.Errors`, the keys of `Endpoint.Reasons`, `Guard.Codes`, `Envelope.Code`, `SourceCheck.Code`, and the code `Catalog.Add`, `SetRemediation` and `Lookup` take. `httpservertest.CheckCodeSpecs` counts a code set in any of those struct fields, so an app's code test sees every code it answers through httpwire. A field code (`FieldError.Code`) is not a wire code and stays a string.

Every row's union ends with the codes any row may answer: `accounts_unavailable`, `maintenance`, `host_not_allowed`, `internal_error`, `not_found` and `method_not_allowed`. A `Reasons` entry lists the reasons a code may carry; an `Open` set says a client must accept reasons it does not list, such as a field name.

A `Raw` row's handler writes its own answer, of one of the row's `ContentTypes`, the media types its 2xx may carry (one or more, each named once, compared without case). A row that negotiates, such as an image served as WebP or JPEG by `Accept`, lists every type it may send. Its refusals go through `Catalog.WriteError`. `New` matches the set with the media types the document's primary response declares, one to one.

A `Conditional` row is a `Raw` GET row whose handler answers `304 Not Modified` with no body when the request's `If-None-Match` matches the current entity-tag, and sends an `ETag` on every 200 (RFC 9110 sections 13.1.2 and 15.4.5). Derive refuses `Conditional` on any other row. The binder logs at ERROR, naming the row and the rule, a 200 with no `ETag` on a `Conditional` row, a 304 to a request with no `If-None-Match`, and a 304 on a row that is not `Conditional`; the answer is written as the handler wrote it. `New` requires the operation of a `Conditional` row to declare a bodiless 304 and an optional `If-None-Match` header parameter, and the operation of any other row to declare neither.

A panic while a row decodes or runs is logged at ERROR with its stack and keeps nothing: a `Keyed` row's claim is released and its spool file removed. While nothing of the answer is written it answers `internal_error` with the header the row started with: every header the handler set or overwrote is undone, and those the app's middleware set before the API are kept. Once the answer has started, the binder panics with `http.ErrAbortHandler`, so net/http cuts the connection and the client never reads a cut answer as a whole one. A handler's own `http.ErrAbortHandler` passes through untouched.

## Person-only actions

A `PersonOnly` row refuses every API key whatever its scopes. An action decided inside a row, such as archiving an account, is checked by the code that knows the stored value: it calls `RequirePerson(ctx, action)` with an action the row lists in `PersonActions`. A key then gets `person_required`, with the action as its reason, and a session that did not authenticate recently gets the guard's step-up refusal.

## Idempotency

A `Keyed` row requires one `Idempotency-Key`, a quoted string of 1 to 255 printable ASCII characters. Any other row refuses the header with `idempotency_key_unsupported`. A key is held per credential, row and key, so one principal never replays another's answer.

The request's fingerprint is SHA-256 over the row name and the decoded request, its `Headers` included, so a body spelled another way is the same request. A resend with the same key and fingerprint gets the kept answer with this request's `Wire-Version` and `X-Request-Id`; another fingerprint gets `idempotency_key_reused`. A resend while the first run is in flight waits for it through `WithHold`, then gets its answer.

A success is kept, and so is a refusal whose code may have changed state (its spec's `NoEffect` is false). That holds after the client leaves too, when the handler names the code from its row's union: a handler that sent a write on a detached context and lost its answer returns `outcome_unknown` (502, httpwire's own code), and the resend replays it. A refusal that changed nothing, a panic, and an answer the binder wrote for a run whose context ended (`internal_error`, or a code outside the union) keep nothing, so a resend runs again. A kept answer lasts the row's `Window`: the row's own when set, else the table's `Window` (10 minutes when zero), each from 1 minute to 7 days. `Derive` writes the table's onto every `Keyed` row with none, so the document wiregen writes publishes each row's window as `x-idempotency`'s `window_s`. An answer over `MaxAnswer` is sent whole and kept without its body, and its resend answers `idempotency_answer_too_large`, which names the `Result` row that reads the result. The rule reads a refusal's code as `Catalog.WriteError` writes it, so a refusal's size never decides whether it is kept; a `Raw` handler that wraps its writer keeps that working by giving the wrap an `Unwrap` method, as `http.ResponseController` expects, else the rule reads the code from the kept body, which it can only do within `MaxAnswer`: a larger refusal whose code it never saw is kept, and its resend answers `idempotency_answer_too_large`.

The in-memory store bounds kept bodies in total and per credential, and kept answers per credential; a credential at its bound gets `idempotency_store_full` (503, `Retry-After: 60`) and runs nothing. Once the store holds 10,000 answers and runs, a credential is admitted only while it holds fewer than its share, 10,000 divided by the number of credentials holding answers or runs, and at 20,000 every new claim gets `idempotency_store_full`. So the store holds at most 20,000, and below that one principal filling the store never refuses another below its share. A resend whose answer is kept still replays. A store that writes the key in the work's own transaction reads the slot and fingerprint with `Idempotency(ctx)` inside the handler, answers it from `Load`, and makes `Save` a no-op. A `Load` error answers `internal_error` and runs nothing. A `Save` error is logged, and the answer is kept in memory until it expires, so a resend to the same process replays it. That memory admits a new key by the default store's bounds over the answers it holds, not the runs in flight, so runs admitted together whose saves all fail can pass those bounds by their number, and no kept answer is dropped to hold them. A store that must survive a restart writes the key in the work's transaction.

A `Keyed` `Raw` row's body is spooled to a file in `WithSpoolDir`'s directory, through SHA-256, and the digest is the fingerprint's body part. Use a private directory beside the app's data, never a tmpfs, so a large upload does not fill memory. `New` removes the regular files named `httpwire-spool-*` there, which a crash left, and nothing else.

## Maintenance

`API.Quiesce(ctx, level)` quiets the API while a database file it reads is replaced. `QuietWrites` answers `maintenance`, with `Retry-After`, on every write row, a mount's included, and every `Stream` row. `QuietAll` also holds every other row: a request waits for `resume` up to 5 seconds, then answers `maintenance`. `Quiesce` returns once every request of that level already in flight has returned, and cancels the context of one still running 10 seconds into `QuietAll`. A cancelled request answers `maintenance`. An open stream is not waited for: its feed ends it.

Any part of an app that writes from the old file implements `Quiescer`. At `QuietAll` a job runner cancels each running job, a yielded one included, and returns when each has returned, each job having written only through the store pin it took at its start. It also ends every queued job `interrupted`, empties its queue and drops its held proposals, since they were admitted against the file being replaced. An app with jobs registers `jobstore.OnOpen` (in `github.com/cplieger/bolttable/jobstore`) as its store's open hook, so every record still queued or running in an opened file ends `interrupted`, as it does at a restart.
