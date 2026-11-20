# httpwire

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/httpwire.svg)](https://pkg.go.dev/github.com/cplieger/go/httpwire) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=httpwire%2Fgo.mod)](https://github.com/cplieger/go/blob/main/httpwire/go.mod) [![npm](https://img.shields.io/npm/v/@cplieger/httpwire)](https://www.npmjs.com/package/@cplieger/httpwire) [![JSR](https://jsr.io/badges/@cplieger/httpwire)](https://jsr.io/@cplieger/httpwire)

httpwire declares your Go app's JSON API once, as a table of rows, and binds typed handlers to it, with a matching TypeScript package that decodes the same wire in the browser.

The Go module depends on this repository's [httpserver](https://github.com/cplieger/go/tree/main/httpserver) and [textsafe](https://github.com/cplieger/go/tree/main/textsafe), which use only the standard library. Both halves are licensed under Apache-2.0.

## Why use it

httpwire is built for a Go server whose browser client is generated from the same table, where any disagreement between the two must fail in a test.

- `New` checks the table against its handlers and the committed OpenAPI document at start, listing every refusal at once.
- Every refusal is one error envelope whose code is in the row's declared union, at the catalog's status.
- A request is guarded before it is decoded, and decoded strictly: unknown fields, a body not sent as `application/json` and a value outside its schema are refused before the handler runs.
- `Idempotency-Key` replay, `If-Match` tags, sealed list cursors that page both ways, and quiescing for a file replace are built in.
- `httpwiretest.Contract` holds every answer to the document, and `httpwiretest.AccessMatrix` holds the guard to every row's `Access`.

Consider [huma](https://github.com/danielgtaylor/huma) if you want the OpenAPI document generated from your handlers at run time across several routers.

## Install

```sh
go get github.com/cplieger/go/httpwire@latest
npx jsr add @cplieger/httpwire  # or: npm i @cplieger/httpwire
```

## Usage

```go
table := &httpwire.Table{
	Prefix:  "/api",
	Catalog: httpwire.NewCatalog(),
	Endpoints: []httpwire.Endpoint{{
		Name: "items.get", Method: http.MethodGet, Path: "/items/{id}",
		Summary: "Read an item", Description: "Reads one item.",
		Params: reflect.TypeFor[itemRef](), Response: reflect.TypeFor[item](),
		Access: httpwire.Access{Class: httpwire.Public},
	}},
}
get := func(_ context.Context, c httpwire.Call[itemRef, httpwire.None, httpwire.None, httpwire.None]) (item, error) {
	return item{ID: string(c.Params.ID), Name: "first"}, nil
}
api, err := httpwire.New(table, []httpwire.Binding{httpwire.Bind("items.get", get)},
	httpwire.WithSchema(openapiJSON))
```

`ExampleNew` in `example_test.go` runs this with its document, and `go test` keeps it true. The document is the one [wiregen](https://github.com/cplieger/go/tree/main/wiregen) writes from the same table, committed beside it. A row that is not `Public` also needs `WithGuard`, `WithCredential` and `WithCursorOwner`, and `New` names each missing option. Pass `WithSealer(keyFile.Sealer())`, a [go/sealed](https://github.com/cplieger/go/tree/main/sealed) key file's sealer, so list cursors outlive a restart.

## API

- `Table`, `Endpoint`, `Access`, `Kind`, `Mount`, `Owner` and `Derive` declare the API. `Bind`, `BindRaw`, `BindStream`, `Call`, `None`, `Status` and `Answer` bind handlers; a `Call` carries the row's path, query, body and request headers. A `Raw` row names the media types it may answer in `ContentTypes`, and a `Conditional` Raw GET row answers 304 to a matching `If-None-Match`.
- `CheckHeaders` checks a `Headers` struct as `Derive` does.
- `New` and its `With*` options build an `API`, which serves the table and quiesces through `Quiesce`.
- `Catalog`, `Error`, `FieldError`, `Upstream`, `Envelope`, `DecodeEnvelope` and `Union` describe refusals. Each wire code is an `httpserver.ErrorCode`, so `httpservertest.CheckCodeSpecs` finds every code an app answers.
- `Page`, `Cursor`, `Limit`, `List`, `Seal`, `Open`, `Tag`, `OpenToken`, `Idempotency`, `IdempotencyStore`, `IdempotencyPolicy` and `PolicyOf` serve pages, handles, tags and replays.
- `Checked`, `SourceCheck`, `CheckAnswer` and `NotStored` answer an open check, a keyed re-read of one item at each of its sources.
- `ParseSchema`, `WireVersion` and the `Pattern*` constants read the document.
- `httpwiretest` holds `Contract`, `WithSameShape`, `AccessMatrix` with its `Credential` kinds, and `Sentinels`.
- The TypeScript package decodes envelopes, pages and tags, and its `./testing` entry holds the fakes and fixtures; [web/README.md](web/README.md) documents it.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/httpwire) and [JSR](https://jsr.io/@cplieger/httpwire/doc).

## Every row runs in one order

A write first meets `httpserver.CrossOrigin`. Then every row meets the client version floor, maintenance, the store outage, the guard, the key rate and the held-request cap, in that order, and only then is the request decoded: path, query, headers, the body's media type, the body, `If-Match`, `Idempotency-Key` and the cursor. A write naming another `Wire-Version` answers `wire_mismatch` before decoding. So a refused principal is never decoded, and an idempotency key is never looked up for a caller the guard has not admitted. A bad path value answers `not_found`, since it names no resource, and a refused request header `bad_header`.

A handler's error answers through the row's union: an `*Error` whose code the union holds, else the first owner sentinel it wraps, else `internal_error`, logged. [The binder](docs/binder.md) covers the order, request headers, idempotency, the spool for keyed uploads, person-only actions and maintenance.

## The wire both halves agree on

Every answer carries `Wire-Version`, the first 16 hex characters of the SHA-256 of the document's normal form, and the request's `X-Request-Id`. Refusals are one envelope (`code`, `message`, `remediation`, `request_id`, optional `reason`, `fields`, `retry_after_ms` and `upstream`), decoded by the same rules in Go and TypeScript. A List row's page carries `offset`, `total` and cursors sealed by [go/sealed](https://github.com/cplieger/go/tree/main/sealed) under `WithSealer`'s sealer, bound to the row, its path values, the query and the owner, that open for 24 hours. [The wire](docs/wire.md) lists the headers, the envelope bounds, the page rules, the open check's answer, the tag form and the schema keywords `ParseSchema` accepts.

## Documentation

- [The binder](docs/binder.md) describes how a row is admitted, decoded and answered, for anyone writing handlers or a guard.
- [The wire](docs/wire.md) describes the headers, envelope, pages, tags and document rules, for anyone writing a client.
- [Testing an API](docs/testing.md) covers `httpwiretest` and the TypeScript fixtures, for anyone writing handler tests.
- [web/README.md](web/README.md) documents the TypeScript package.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
