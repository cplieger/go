# The binder

This page describes how httpwire admits, decodes and answers each row of a table, for anyone writing handlers, a guard or an idempotency store.

## Building the API

`New(table, bindings, options...)` checks the table, its bindings and its options together, and its error lists every refusal at once, so an app fails at start and never at a request. `Derive` runs the table checks that need no binding, and fills each row's `Errors` with its union.

`WithSchema` is required. Every operation of the document must match one row by `operationId`, method and path, and every row one operation. Each `Params` and `Query` field must be a declared parameter, required as the field is, and a row has a JSON request body exactly when it has a `Request`.

A row whose class is not `Public` also needs `WithGuard`, `WithCredential` and `WithCursorOwner`. A row whose class admits a key needs `WithKeyRate`. A `Held` or `Keyed` row needs `WithHold`, a row with `Changes` needs `WithFeed`, and a `Keyed` `Raw` row needs `WithSpoolDir`. A table naming a `ClientHeader` needs `WithClientFloor`.

A path or query value decodes into a `TextUnmarshaler`, a string type with a `MaxBytes` method, an integer type with a `Range` method, a `uint64` or a `bool`. `New` refuses any other field type.

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
10. Decoding: the path, the query, the body's media type, the body, `If-Match`, `Idempotency-Key` and the cursor.
11. The idempotency lookup on a `Keyed` row, then the handler.

A path value outside its type or schema answers `not_found`, because it names no resource. A query value answers `bad_query` and a body `bad_body`, each with `fields` naming the refused paths.

A row whose body the binder decodes takes only `application/json`, matched case-insensitively, with any parameters and a charset of `utf-8` if one is given. A missing, unparsable or other media type answers `unsupported_media_type` (415) before the body is read and before any idempotency key is claimed. A `Raw` row with no `Request` reads its own body, held to the row's `MaxBody`, so it takes any media type.

A JSON body is one JSON value no larger than the row's `MaxBody` (the table's, 64 KiB by default), else `body_too_large`. It is checked against the document's schema, which admits no member the type does not declare, and against the bound methods of its field types, and every failing path is listed in `fields`.

## Mounted handlers

A `Mount` serves rows that decode and guard themselves, such as an account library's sign-in routes. The binder registers them on its own mux under one wrap: the client floor, maintenance, the accounts outage (a store outage leaves them served), the wire version on a write, and the `Wire-Version` header on every answer. It adds no decoding, guard, cross-origin check or key rate, since the admitted principal exists only inside the mount.

## Answers and errors

A JSON row answers its `Response` at its `Status`, or at one of its `Successes` through `Status[T]` and `Answer`. A status the row does not declare, or a body that does not encode, answers `internal_error` and logs at ERROR.

A handler's error answers through the row's union: an `*Error` whose code the union holds, else the first `OwnerCode` of the row's `Owners` that the error wraps, matched in order with `errors.Is`, else `internal_error`, logged. `Catalog` supplies each code's status and remediation, so a handler sets only the code, a message and, when it applies, a `Reason`, `Fields` or `RetryAfter`.

Every row's union ends with the codes any row may answer: `accounts_unavailable`, `maintenance`, `host_not_allowed`, `internal_error`, `not_found` and `method_not_allowed`. A `Reasons` entry lists the reasons a code may carry; an `Open` set says a client must accept reasons it does not list, such as a field name.

A `Raw` row's handler writes its own answer, which must use the row's `ContentType`. Its refusals go through `Catalog.WriteError`.

## Person-only actions

A `PersonOnly` row refuses every API key whatever its scopes. An action decided inside a row, such as archiving an account, is checked by the code that knows the stored value: it calls `RequirePerson(ctx, action)` with an action the row lists in `PersonActions`. A key then gets `person_required`, with the action as its reason, and a session that did not authenticate recently gets the guard's step-up refusal.

## Idempotency

A `Keyed` row requires one `Idempotency-Key`, a quoted string of 1 to 255 printable ASCII characters. Any other row refuses the header with `idempotency_key_unsupported`. A key is held per credential, row and key, so one principal never replays another's answer.

The request's fingerprint is SHA-256 over the row name and the decoded request, so a body spelled another way is the same request. A resend with the same key and fingerprint gets the kept answer with this request's `Wire-Version` and `X-Request-Id`; another fingerprint gets `idempotency_key_reused`. A resend while the first run is in flight waits for it through `WithHold`, then gets its answer.

A success is kept, and so is a refusal whose code changes state (its spec's `NoEffect` is false), unless the handler's context had ended. A refusal that changed nothing, a panic, and an answer of a run whose context ended keep nothing, so a resend runs again. An answer over `MaxAnswer` is sent whole and kept without its body, and its resend answers `idempotency_answer_too_large`, which names the `Result` row that reads the result.

The in-memory store bounds kept bodies in total and per credential, and kept answers per credential; a credential at its bound gets `idempotency_store_full`. A store that writes the key in the work's own transaction reads the slot and fingerprint with `Idempotency(ctx)` inside the handler, answers it from `Load`, and makes `Save` a no-op.

A `Keyed` `Raw` row's body is spooled to a file in `WithSpoolDir`'s directory, through SHA-256, and the digest is the fingerprint's body part. Use a private directory beside the app's data, never a tmpfs, so a large upload does not fill memory. `New` removes the regular files named `httpwire-spool-*` there, which a crash left, and nothing else.

## Maintenance

`API.Quiesce(ctx, level)` quiets the API while a database file it reads is replaced. `QuietWrites` answers `maintenance`, with `Retry-After`, on every write row, a mount's included, and every `Stream` row. `QuietAll` also holds every other row: a request waits for `resume` up to 5 seconds, then answers `maintenance`. `Quiesce` returns once every request of that level already in flight has returned, and cancels the context of one still running 10 seconds into `QuietAll`. A cancelled request answers `maintenance`. An open stream is not waited for: its feed ends it.

Any part of an app that writes from the old file implements `Quiescer`. A job runner cancels its running jobs at `QuietAll` and returns when each has returned, each job having written only through the store pin it took at its start, and its open hook marks a record still `running` as interrupted.
