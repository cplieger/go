# @cplieger/httpwire

> Typed error envelope, cursor pages and idempotency keys for the cplieger web APIs. The TypeScript half of httpwire.

The TypeScript half of [github.com/cplieger/go/httpwire](https://github.com/cplieger/go/tree/main/httpwire). It reads the wire a Go `httpwire` API answers: the error envelope every refusal carries, list pages and their cursors, and the instants in a body. It writes the request headers a write sends, `Idempotency-Key` and `If-Match`. The `./testing` entry holds the fixtures both halves are tested against and a fake server that answers only what a generated endpoint list can answer. It has no runtime dependencies.

## Install

```sh
npx jsr add @cplieger/httpwire
# or
npm i @cplieger/httpwire
```

## Usage

```ts
import {
  decodeEnvelope,
  IDEMPOTENCY_HEADER,
  idempotencyKeyHeader,
  newIdempotencyKey,
  pages,
  parseInstant,
} from "@cplieger/httpwire";

// Send the same key with every retry of this one action.
const keyHeader = idempotencyKeyHeader(newIdempotencyKey());
if (keyHeader === undefined) throw new Error("a fresh key always encodes");
const res = await fetch("/api/items", {
  method: "POST",
  headers: { "Content-Type": "application/json", [IDEMPOTENCY_HEADER]: keyHeader },
  body: JSON.stringify({ name }),
});
if (!res.ok) {
  const env = decodeEnvelope(await res.json().catch(() => undefined));
  showRefusal(env?.code ?? "unknown", env?.remediation, env?.request_id);
}

for await (const page of pages(readItems, { signal })) {
  for (const item of page.items) {
    addRow(item.name, parseInstant(item.created_at));
  }
}
```

## The main entry

- `decodeEnvelope(v)` reads an error body by the rules the Go `DecodeEnvelope` holds, and answers `undefined` for anything else, so a body from a proxy or an older server is never read as a refusal. `code`, `message`, `remediation` and `request_id` are always present; `reason`, `fields`, `retry_after_ms` and `upstream` only when the server sent them. `upstream.message` is another service's words: show it only as quoted text under your own sentence. `WIRE_CODES` lists the codes the Go catalog holds at its start, and `SCHEMA_FIELD_CODES` the codes its decoder writes into `fields[].code`.
- `pages(page, { signal, maxPages })` reads a list from its first page through each `next_cursor`, yields each page, and returns the first failure your `page` function answers. `checkListAnswer(a)` holds one page to the rules the server checks before it writes it: `offset` and `total` integers from 0 to 999,999,999,999,999, the page inside `total`, `prev_cursor` present exactly when `offset` is above 0 and `next_cursor` exactly when rows follow. A page that breaks them throws a `TypeError`, and a list with pages left after `maxPages` (default 1,000) a `RangeError`.
- `newIdempotencyKey()` draws a version 4 UUID from `crypto.getRandomValues`, so a page served over plain HTTP from a LAN address, which has no `crypto.randomUUID`, sends keys like any other. `idempotencyKeyHeader(key)` writes the header value for a key of 1 to 255 printable ASCII characters and answers `undefined` for any other key.
- `ifMatchHeader(tag)` writes the `If-Match` value for a tag a read answered, `<16 lowercase hex>.<revision>`, and answers `undefined` for anything else, so a tag that was never a server's is never sent.
- `parseInstant(text)` reads an RFC 3339 `date-time` as a Go server writes it (uppercase `T`, 0 to 9 fraction digits, `Z` or a `±hh:mm` offset) as Unix milliseconds, truncating past the third fraction digit; any other text throws a `SyntaxError` naming it. `Date.parse` is specified only for exactly three fraction digits, so compare instants through `parseInstant`, never as text, and give a `<time datetime>` `new Date(parseInstant(text)).toISOString()`.
- `WIRE_VERSION_HEADER`, `IDEMPOTENCY_HEADER` and `REQUEST_ID_HEADER` are the header names both halves use.
- `checkChecked(a)` holds an open check's answer (`Checked<T>`, one `SourceCheck<T>` per source) to the rules the server checks before it writes one: each source named once, the members each answer carries, and `position` exactly when `stored` (`CHECK_ANSWERS` lists the answers, `NOT_STORED` the reasons an answer was not stored). An answer that breaks them throws a `TypeError`. [The wire](https://github.com/cplieger/go/blob/main/httpwire/docs/wire.md#open-checks) lists the rules.

The package calls no member a non-secure context lacks (`crypto.randomUUID`, `crypto.subtle`, `navigator.locks`, `navigator.clipboard`), and a test over its published sources fails on one.

## Testing a client (`@cplieger/httpwire/testing`)

- `ENVELOPE_FIXTURES` holds the envelopes both decoders accept or refuse, each `{ name, body, valid }`.
- `PAGE_FIXTURES` holds three page tables with the answer `httpserver.PageMatcher` gives for each path, which a client-side router is held to. A case marked `serverOnly` holds a dot segment a browser's URL parser removes before a router sees the path.
- `createFakeServer({ endpoints, wireVersion, errorCodes })` returns a `fetch` that answers only what the generated rows can answer. `answer(ep, data, { status })` runs the row's own decoder on `data` first, `refuse(ep, code)` writes an envelope for a code of the row's union, `stream()` pushes frames to the `events.stream` row's connections, and `calls` lists what the client sent. A request to a row with no answer, or to no row, rejects and is reported through `reportError` where the runtime has it (browsers, Deno and Bun, not Node.js), so a client that swallows the rejection still fails its test.
- `TRANSPORT_FIXTURE` is the contract every transport over this wire passes: each case names a row, the arguments its generated function takes, the request the client must send (or none), what the server answers, and the result the client must give. `serveTransportCase(c)` serves one case and records the requests:

```ts
import { serveTransportCase, TRANSPORT_FIXTURE } from "@cplieger/httpwire/testing";

for (const c of TRANSPORT_FIXTURE.cases) {
  it(c.name, async () => {
    const served = serveTransportCase(c);
    const result = await callRow(served.fetch, c); // your client's function for c.row
    expect(result).toEqual(c.want);
    expect(served.sent).toHaveLength(c.request === undefined ? 0 : 1);
    const req = served.sent[0];
    if (c.request !== undefined && req !== undefined) {
      expect(req.method).toBe(c.request.method);
      expect(new URL(req.url).pathname).toBe(c.request.path);
      for (const [name, value] of Object.entries(c.request.headers)) {
        expect(req.headers.get(name)).toBe(value);
      }
    }
  });
}
```

## Runtime compatibility

The package ships TypeScript source and calls only web-platform APIs, so it runs wherever that source is compiled or read as TypeScript:

- **Browsers**: its tests run in Chromium, Firefox and WebKit. It needs `Object.hasOwn` (Chrome 93, Firefox 92, Safari 15.4) and `BigInt` (Chrome 67, Firefox 68, Safari 14).
- **Node.js**: both entries run under a TypeScript-aware toolchain (a bundler, vitest, tsx), and a test runs both entries in Node.js with no DOM. It needs the global `crypto`, which Node.js has from version 19. Node.js does not strip types from a file under `node_modules`, so plain `node` cannot load the npm package.
- **Deno**: install from JSR, which rewrites the source's `.js` import specifiers to its `.ts` files at publish.
- **Bun**: reads the source from either registry as it is.
- **Cloudflare Workers**: not tested, and listed as unsupported on JSR.

## Full documentation

The Go API is in the [module README](https://github.com/cplieger/go/tree/main/httpwire#readme). The headers, the envelope's bounds, the cursor and the tag are in [The wire](https://github.com/cplieger/go/blob/main/httpwire/docs/wire.md), and the fixtures and the Go test helpers are in [Testing](https://github.com/cplieger/go/blob/main/httpwire/docs/testing.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
