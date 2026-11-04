# What xmlx leaves out

This page gives the reason behind each case xmlx does not handle, for a developer deciding whether their documents are in scope.

## XML directives

`Preflight` refuses every `<!` form that is not a comment or a CDATA section: `<!DOCTYPE`, `<!ENTITY`, `<!ATTLIST` and `<!NOTATION`. There is no option to allow them.

`encoding/xml` reads a directive by tracking nested `<` and `>` pairs, with quoting and nested comments, until it reaches a `>` at depth zero. A scan that stopped at the first unquoted `>` would report a short token where the decoder keeps one the size of the whole body. That bound would read as protection and not be one.

A copied tokenizer can drift from `encoding/xml` without any sign. Your code cannot detect that difference at the call site, while a refusal shows up the first time it happens. Rejecting the class by default is also the usual practice for untrusted XML.

Legacy RSS 0.91 did require a DOCTYPE, so such documents still exist. A document that carries one cannot use `Preflight`. Decode it under a byte cap and a `Budget` instead.

## XXE and entity expansion

These are outside the library's concern, because `encoding/xml` does not resolve external entities and does not expand a DTD's internal entities. The directive rejection closes that surface as well.

## Round-trip stability

Round-trip stability is a different kind of hardening. Go's XML parser accepts leading and trailing garbage around the document element, which was the mechanism behind a real authentication bypass, CVE-2020-16250. If your security depends on the document's shape rather than its cost, pair `Preflight` with a round-trip validator such as [mattermost/xml-roundtrip-validator](https://github.com/mattermost/xml-roundtrip-validator). The two work side by side, and neither replaces the other.

## Schema validation, namespaces, encodings and well-formedness

All of these stay with `encoding/xml`. `Preflight` reads surface structure only, enough to know where one token ends and the next begins. It never validates.

The reverse also holds. Malformed input can still trip a bound, so a rejection means the document was outside your contract, not necessarily that it was too large.

## Streaming

`Preflight` takes the whole body as a byte slice. Its value is refusing a document before any decoding starts, and your code already holds the bytes from a capped read.

## Per-name limits

A rule such as "at most N `<item>` elements" needs your schema's vocabulary, and it reads better with a name from it. It is one comparison at your decode site. `MaxElements` covers the schema-free total.

## Depth inside DecodeText

`Budget.DecodeText` does not inherit the decoder's recursion ceiling of 10000 open elements, or 5000 on wasm, because it never enters that recursion. Above that depth it returns a value `DecodeElement` would refuse. A `Budget` bounds bytes kept, and nesting is the bound `Preflight` owns. To enforce the ceiling, set `MaxDepth` at or below it. [How xmlx bounds a document](design.md#how-decodetext-differs-from-decodeelement) has the measured detail.

## Decoder settings other than the defaults

The bounds model the default decoder: `Strict` enabled, no `AutoClose`, no caller-supplied `Entity` map and no `CharsetReader`.

- With `Strict` disabled, a bare attribute name becomes an attribute the lexical count cannot see.
- An `Entity` map can expand a short reference after the raw bound has passed.

Either change keeps the token, depth and element bounds, and makes the attribute and text bounds advisory.
