# The wire

This page describes what an httpwire API sends and accepts, for anyone writing a client or reading its answers by hand.

## Headers

| Header | Direction | Rule |
| --- | --- | --- |
| `Wire-Version` | answer, and a write's request | Every answer carries it. A write that names another one answers `wire_mismatch` (409); a request naming none passes |
| `X-Request-Id` | both | The context's id, else a valid inbound one, else a fresh one; every answer and envelope carries it |
| `Idempotency-Key` | request | One quoted string of 1 to 255 printable ASCII characters on a `Keyed` row, refused on any other |
| `If-Match` | request | One quoted tag on an `IfMatch` row: missing is `if_match_required` (428), malformed `if_match_invalid`, another tag `stale_version` (412) |
| `Feed-Position` | answer | Every 2xx of a row with `Changes`, from the change feed |
| `Retry-After` | answer | Whole seconds, rounded up, on a refusal that carries a wait; the envelope's `retry_after_ms` gives the exact wait |

A row may also take request headers of its own, each an `in: header` parameter of its operation. A header name matches in any case. Each takes one value, and a required one missing, one sent twice, or a value outside its schema answers `bad_header` (400), with a `fields` entry per refused header named canonically (`X-Thing`). No row takes `Wire-Version`, `X-Request-Id`, `Idempotency-Key`, `If-Match`, a credential, body framing or connection header, or the client version header as its own.

The wire version is the first 16 lowercase hex characters of the SHA-256 of the document's normal form. The normal form drops `summary` and `description` where each is an OpenAPI keyword, the top-level `info` object and each `x-error-codes` entry's `remediation`, then writes the rest with sorted keys and no whitespace. So editing a description never moves the version.

## The error envelope

Every refusal is one JSON object, decoded by the same rules in Go (`DecodeEnvelope`) and TypeScript (`decodeEnvelope`).

| Member | Rule |
| --- | --- |
| `code` | required, one or more lowercase ASCII letters, digits and `_` |
| `message` | required, at most 4 KiB |
| `remediation` | required, at most 4 KiB |
| `request_id` | required |
| `reason` | optional, the same grammar as `code`, at most 64 bytes |
| `fields` | optional, at most 100 entries of `path`, `code` and `message` |
| `retry_after_ms` | optional, an integer from 0 to 2^53 - 1 |
| `upstream` | optional: `service` in the same grammar, `status` from 100 to 599, `message` at most 512 bytes |

A member present as `null` breaks its rule, and an unknown member is ignored. `internal_error` carries the message `internal error` and nothing else. `upstream.message` is another service's text: no code or remediation depends on it.

`outcome_unknown` (502) answers a write whose effect is not known: the server sent it on to another service and lost the answer. It may have run, so it is kept like a success: only a `Keyed` row answers it, and a resend with the same `Idempotency-Key` replays it instead of running the write again. A client reads the result before it sends a new request.

## Request bodies

A body the binder decodes is sent with `Content-Type: application/json` (a charset, if given, is `utf-8`); anything else answers `unsupported_media_type` (415). It must be one JSON value, and it is checked against the document's schema, which admits no member the row's type does not declare.

## Pages

A List row's query takes `cursor` (at most 1,416 bytes) and `limit` (1 to 200, 50 when absent). Its answer is:

```json
{"items": [], "offset": 0, "total": 0, "next_cursor": "...", "prev_cursor": "..."}
```

`offset` is the count of items before the page's first and `total` the count matching the query at this read, at most 999,999,999,999,999. `prev_cursor` is absent exactly when `offset` is 0, and `next_cursor` exactly when the page ends at `total`. A `prev_cursor` answers the `limit` items before the page in list order, or the first page when fewer precede it. Both halves refuse a page that breaks these rules.

A cursor is a [go/sealed](https://github.com/cplieger/go/tree/main/sealed) value that expires in 24 hours: its payload is a direction byte and a position of at most 1,024 bytes, so a cursor is at most `sealed.ExpiringLen(1 + 1,024)`, 1,416 bytes, and a URI holding one stays under the 8,000 octets RFC 9110 section 4.1 asks every recipient to support. A longer value is refused before it is opened. Its purpose binds it to the row, the values of the row's path wildcards, the query without its page parameters, and the owner `WithCursorOwner` names, so every session of one person opens it on the same list and nobody else does. Its purposes are named under `httpwire/`, so an app's own purposes on the same sealer, named under the app's module, never equal one. The sealer is `WithSealer`'s, such as a key file's `Sealer`, so cursors outlive a restart; without it, `New` uses `sealed.NewEphemeral`, and cursors stop opening at a restart. A cursor that does not open answers `cursor_invalid`, and the client reads the list again from its first page. A key `Rotate` leaves open cursors opening, since the previous key still opens what it sealed; after `Retire` they answer `cursor_invalid`. A sealer that holds no key, such as a closed key file's, answers `internal_error`.

`Seal` and `Open` use the same sealer for a handle bound to one purpose and one credential, which opens until the ttl `Seal` was given.

## Tags

A tag is `<16 lowercase hex>.<decimal>`, the decimal with no sign or leading zero and at most 2^64 - 1: the value inside `If-Match`'s quotes and a read answer's `tag` field. Its hex half is the token of what the revision lives in: `OpenToken` of a store file's epoch, read inside the transaction that reads or checks the revision, or the first 8 bytes of a shared file's SHA-256. A revision that an older file puts back never matches a tag read before.

## Open checks

An open check reads one item again at each source it came from, and answers `Checked[T]`, generic over the item:

```json
{"sources": [{"source": "upstream_a", "answer": "updated", "row": {}, "stored": true, "position": "store=1234;epoch=\"9f2c4e1a7b3d5f60819a2b3c4d5e6f70\""}]}
```

Each entry names its `source` once, and its `answer` decides which other members it carries:

| `answer` | Meaning | Members |
| --- | --- | --- |
| `same` | found, and the stored item already matches it | `stored: false`, and no `row`, `not_stored`, `code` or `upstream` |
| `updated` | found, and the item changed | `row` (as stored when `stored`, else as the source answered it), and exactly one of `stored: true` and `not_stored` |
| `gone` | the source no longer has the item | exactly one of `stored: true` and `not_stored`, and no `row` |
| `error` | the read failed | `code` in the envelope's code grammar, `upstream` when the source answered (held to the envelope's `upstream` rules), `stored: false`, and no `row` or `not_stored` |

`not_stored` is `superseded` when the stored item moved while the check read, and `store_unwritable` when the store refused the write. Only `error` carries `code` or `upstream`. A stored answer, and only a stored one, carries `position`: the change feed's `Feed-Position` value right after the write, in printable ASCII of at most 4,096 bytes, so a tab marks seen no further than the write it read, a `gone` answer included. The server never writes an answer that breaks these rules (it answers `internal_error` instead), and the TypeScript `checkChecked` refuses one.

## The document

`ParseSchema` reads the OpenAPI 3.1 document wiregen writes, and refuses any schema keyword outside this set:

- `type`, `properties`, `required`, `additionalProperties`, `propertyNames`, `items`, `enum`, `const`
- `minimum`, `maximum`, `minLength`, `maxLength`, `x-max-bytes`, `pattern`, `minItems`, `maxItems`
- `$ref`, `anyOf`, `oneOf`, `discriminator`
- the annotations `description`, `format`, `contentEncoding` and `default`

A `Keyed` operation carries `x-idempotency`, the policy the binder enforces (`PolicyOf` answers it for a row): `window_s` (the row's window in whole seconds, rounded down), `scope: "credential,operation"`, `concurrent: "join"`, `kept: "success_and_effectful_refusals"`, and `max_kept` and `result` when the row sets them. `New` refuses a document whose policy differs from the row's. It is part of the wire version's normal form, so a changed window is a new wire version. A query parameter that repeats is `type: array` with `items`, `maxItems`, `style: form` and `explode: true`; a client sends one `name=value` per element.

A `pattern` must be one of the six `Pattern*` constants, which both halves match with one verdict. It also refuses an unresolved `$ref`, a `$ref` cycle with no object between, and an OpenAPI member wiregen never writes. An object with `properties` and no `additionalProperties` admits no other member. Numbers are read exactly, an integer as `int64` or `uint64`.
