# Testing an API

This page covers the `httpwiretest` package and the TypeScript fixtures, for anyone writing tests of an httpwire API or of a client that calls one.

## Contract

`httpwiretest.Contract(t, api, cases)` serves each `Case` through the API and fails the test, naming the row, on any answer outside the row's shapes in the document the API was built with:

- a success status the row does not declare;
- a body that does not decode strictly into the status's `Response` type, or breaks its schema;
- a `Raw` answer of another media type;
- an error that is not an envelope of a code in the row's union, at the catalog's status;
- a refusal whose `request_id` is not its `X-Request-Id`, except on a `Keyed` row, where a replayed answer keeps the `request_id` an earlier case with the same credential and `Idempotency-Key` was answered with;
- an answer without this API's `Wire-Version` or a valid `X-Request-Id`.

Each case's request carries its own principal, built by the test. A HEAD answer's body is not read. A case naming no row, a `Stream` row, or a request the API routes to another row fails, so a case always tests the row it names. Mount rows are routed and checked the same way.

`WithSameShape(SameShape{A, B})` serves every case of a read row with `Changes` and no `Personal` mark as both principals, two of one shape that own different records in the fixture store. It fails the row when the guard gives them two shapes or their answers differ as JSON values, cursors left out. Every such row then needs a case. A read whose rows differ per principal is marked `Personal`, and this check finds the one that is not.

## Sentinels

`httpwiretest.Sentinels(t, owner)` runs in the owner package's own test. It fails unless the exported `Err*` variables the package declares, in its non-test files for this build, are exactly the keys of `owner.Sentinels`, and unless the owner passes every owner check `New` makes but the catalog's. So a new sentinel with no code, or one named in `Outside` without a reason, fails where it is added.

## The TypeScript fixtures

The `@cplieger/httpwire/testing` entry holds what a client's tests need, described in [web/README.md](../web/README.md):

- `ENVELOPE_FIXTURES`, the envelopes both decoders accept or refuse;
- `PAGE_FIXTURES`, three page tables with the answer `httpserver.PageMatcher` gives for each path, which a client-side router is held to;
- `TRANSPORT_FIXTURE` and `serveTransportCase`, which serve each case so a generated client function can be called and its request compared;
- `createFakeServer`, which answers a generated endpoint list by the rules `Contract` checks.

The envelope and page fixtures are written by the Go tests that pin them, `TestEnvelopeFixtures` and `TestPageFixtures`, and `UPDATE_GOLDEN=1 go test . -run <test>` regenerates one; a mismatch names that command.
