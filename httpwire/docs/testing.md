# Testing an API

This page covers the `httpwiretest` package and the TypeScript fixtures, for anyone writing tests of an httpwire API or of a client that calls one.

## Contract

`httpwiretest.Contract(t, api, cases)` serves each `Case` through the API and fails the test, naming the row, on any answer outside the row's shapes in the document the API was built with:

- a success status the row does not declare;
- a body that does not decode strictly into the status's `Response` type, or breaks its schema;
- a `Raw` answer of a media type outside the row's `ContentTypes`;
- on a `Conditional` row, a 200 with no `ETag` or one that is no entity-tag, and a resend of the case naming that `ETag` in `If-None-Match` that does not answer 304; on any row, a 304 with a body, a 304 to a request with no `If-None-Match`, and a 304 on a row that is not `Conditional`;
- an error that is not an envelope of a code in the row's union, at the catalog's status;
- an answer without this API's `Wire-Version` or a valid `X-Request-Id`.

Each case's request carries its own principal, built by the test. A HEAD answer's body is not read. A case naming no row, a `Stream` row, or a request the API routes to another row fails, so a case always tests the row it names. Mount rows are routed and checked the same way.

It also fails every write row, mount rows included, whose union holds `outcome_unknown` and that is not `Keyed`: that code says the write may have run, and only a kept answer stops a resend from running it twice.

`WithSameShape(SameShape{A, B})` serves every case of a read row with `Changes` and no `Personal` mark as both principals, two of one shape that own different records in the fixture store. It fails the row when the guard gives them two shapes or their answers differ as JSON values, cursors left out. Every such row then needs a case. A read whose rows differ per principal is marked `Personal`, and this check finds the one that is not.

## AccessMatrix

`httpwiretest.AccessMatrix(t, table, guard, creds, options...)` holds an app's guard to every row's `Access`. It builds an API of the table with the guard and options, every row bound to a stub, and serves each row once per `Credential` over HTTPS, with each path wildcard filled. Each credential is a `Kind` and an `Apply` that adds it to a request: `None`, `Session` (a session whose role grants every scope, past its recent-authentication window), `SessionRecent`, `Key` (with the `Scopes` it passes) or `KeyInvalid` (a revoked or expired key). It fails the test, naming the row and the credential, wherever the answer is not the one the row's `Access` gives:

| Credential | Answer |
| --- | --- |
| any, on a `Public` row | the row runs, as an anonymous principal |
| `None` | `auth_required` |
| `KeyInvalid` | `auth_key_invalid` |
| `Key` on a `PersonOnly` row | `person_required` |
| `Key` on a `Session` row | `auth_session_required` |
| `Key` without the row's `Scope` | `auth_scope_required`, with `error="insufficient_scope"` in `WWW-Authenticate` |
| `Session` or `SessionRecent` on a `Key` row | `auth_key_required` |
| `Session` on a `RecentAuth` row | `auth_recent_auth_required` |
| any other | the row runs, as the credential's principal: a session, or a key holding exactly its scopes |

Each refusal must come at the catalog's status, and a 401 must carry `WWW-Authenticate`. A row's `Scope` is checked only where the row names one, and `RecentAuth` steps up sessions only. A row the guard answers without running it fails as not served, and a table `New` refuses fails at once. The package's own test serves eight: no credential, a session, a recently authenticated session, keys holding `read`, `read` and `operate`, and every scope, a key of a scope no row names, and a revoked key. A key's `Scopes` list every scope it passes, those its scopes imply included.

## Sentinels

`httpwiretest.Sentinels(t, owner)` runs in the owner package's own test. It fails unless the exported `Err*` variables the package declares, in its non-test files for this build, are exactly the keys of `owner.Sentinels`, and unless the owner passes every owner check `New` makes but the catalog's. So a new sentinel with no code, or one named in `Outside` without a reason, fails where it is added.

## The TypeScript fixtures

The `@cplieger/httpwire/testing` entry holds what a client's tests need, described in [web/README.md](../web/README.md):

- `ENVELOPE_FIXTURES`, the envelopes both decoders accept or refuse;
- `PAGE_FIXTURES`, three page tables with the answer `httpserver.PageMatcher` gives for each path, which a client-side router is held to;
- `TRANSPORT_FIXTURE` and `serveTransportCase`, which serve each case so a generated client function can be called and its request compared;
- `createFakeServer`, which answers a generated endpoint list by the rules `Contract` checks.

The envelope and page fixtures are written by the Go tests that pin them, `TestEnvelopeFixtures` and `TestPageFixtures`, and `UPDATE_GOLDEN=1 go test . -run <test>` regenerates one; a mismatch names that command.
