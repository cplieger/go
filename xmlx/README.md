# xmlx

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/xmlx.svg)](https://pkg.go.dev/github.com/cplieger/go/xmlx) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=xmlx%2Fgo.mod)](https://github.com/cplieger/go/blob/main/xmlx/go.mod)

xmlx caps the memory and work an untrusted XML document can cost your Go program, before and during an `encoding/xml` decode.

A byte cap on the response body does not bound the decode, because `encoding/xml` builds each token before your code can check it. xmlx adds those bounds and keeps your decoder. It uses only the standard library, needs Go 1.27.3 or later and is licensed under Apache-2.0.

## Why use it

xmlx is built for Go code that decodes XML it did not write, such as an RSS or Atom feed or a third-party API reply.

- `Preflight` scans the raw bytes once, with no allocation, and refuses an oversized text run or tag, too many attributes, elements or nesting levels, and any XML directive such as a DOCTYPE.
- A `Budget` caps each decoded value and the document's total decoded text while the value is still being read.
- Every rejection wraps one sentinel, `ErrLimit`, and names the bound. A `Preflight` rejection also gives the byte offset. No rejection quotes the document.
- No setting means unbounded: a bound left at zero takes its default, and a negative one is a configuration error.

Consider [mattermost/xml-roundtrip-validator](https://github.com/mattermost/xml-roundtrip-validator) if your security depends on the document's shape, such as XML signature validation or SAML. It rejects input that does not survive an `encoding/xml` round trip, and it pairs with `Preflight`.

## Install

```sh
go get github.com/cplieger/go/xmlx@latest
```

## Usage

Run `Preflight` on the body before the decoder sees it:

```go
if err := xmlx.Preflight(body, xmlx.DefaultLimits()); err != nil {
	return err // a *xmlx.LimitError naming the bound and the byte offset
}

var doc feed
if err := xml.Unmarshal(body, &doc); err != nil {
	return err
}
```

With plain `xml.Unmarshal`, `Preflight` is the whole gate. A `Budget` works where you read tokens yourself, in a hand-written decoder or an `UnmarshalXML` method. Create one `Budget` per document and read each text field through it. `DecodeText` replaces `d.DecodeElement(&s, &start)` and refuses the value as soon as it grows past either cap:

```go
budget, err := xmlx.NewBudget(xmlx.BudgetLimits{MaxFieldBytes: 4 << 10, MaxTotalBytes: 4 << 20})
if err != nil {
	return err
}

d := xml.NewDecoder(bytes.NewReader(body))
var title, guid string
for {
	tok, err := d.Token()
	if err == io.EOF {
		break
	}
	if err != nil {
		return err
	}
	start, ok := tok.(xml.StartElement)
	if !ok {
		continue
	}
	switch start.Name.Local {
	case "title":
		title, err = budget.DecodeText(d)
	case "guid":
		guid, err = budget.DecodeText(d)
	}
	if err != nil {
		return err
	}
}
fmt.Println(title, guid, budget.Total())
```

For a value the decoder has already handed you whole, such as an attribute value, call `budget.Charge(a.Value)` before you store it. In custom `UnmarshalXML` methods, give every element's decoder the same `*Budget`, and use `d.Skip()` for children you do not model. A value from `DecodeText` is already charged, so charge each value once. After any error from `DecodeText`, abandon the document. [How xmlx bounds a document](docs/design.md#how-decodetext-differs-from-decodeelement) has the detail.

To add the gate to a working integration, log the `Preflight` error and decode anyway until your bounds are proven, so a mis-sized bound shows up in your logs instead of breaking the feed.

The package examples on pkg.go.dev show the gate, a rejection, a refused directive, the `Budget` loop and a value split across CDATA sections, and `go test` keeps them true.

## API

- `Preflight` is the raw-byte gate, and `Limits` with `DefaultLimits` holds its five bounds. A zero field takes its `DefaultLimits` value.
- `NewBudget` with `BudgetLimits`, and `DefaultBudget`, create the decode-time text accounting for one document, with `DecodeText`, `Charge`, `Total`, `Remaining`, `MaxFieldBytes` and `MaxTotalBytes`.
- `LimitError`, `Kind` and `ErrLimit` describe a rejection. Match the class with `errors.Is(err, xmlx.ErrLimit)` and read `Kind`, `Limit` and `Offset` with `errors.AsType[*xmlx.LimitError](err)`.
- `ConfigError` and `ErrInvalidLimits` report a negative bound. That is a setup mistake, and it never wraps `ErrLimit`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/xmlx).

## Sizing the bounds

The defaults suit a small structured document, such as a feed or an API reply. They are a starting point.

| Bound | Default |
| --- | --- |
| `MaxTextRunBytes` | 64 KiB |
| `MaxTokenBytes` | 128 KiB |
| `MaxTagAttrs` | 16 |
| `MaxDepth` | 64 |
| `MaxElements` | 100,000 |
| `Budget` per value | 4 KiB |
| `Budget` per document | 4 MiB |

Set `MaxTextRunBytes` to at least six times the `Budget` per-value cap. An entity such as `&quot;` is six raw bytes that decode to one, so a smaller raw bound refuses values the `Budget` would keep. When your schema nests as deeply as the document, keep `MaxDepth` at 10000 or below, or 5000 on wasm. A catalogue-scale document will exceed `MaxElements` and the document cap, so set both from its real ceiling. For a compressed body, run `Preflight` on the inflated bytes. [How xmlx bounds a document](docs/design.md) explains each bound.

## Unsupported by design

- XML directives. `Preflight` refuses every `<!DOCTYPE`, `<!ENTITY`, `<!ATTLIST` and `<!NOTATION`. Decode a document that needs one under a byte cap and a `Budget` instead.
- XXE and DTD-defined entity expansion. `encoding/xml` does not resolve external entities or expand entities declared in a DTD.
- Round-trip stability, which a round-trip validator covers.
- Schema validation, namespace policy, character-encoding conversion and well-formedness, which stay with `encoding/xml`.
- Streaming from an `io.Reader`. `Preflight` takes the whole body, so read it under a byte cap first.
- Per-name limits such as "at most N `<item>` elements", which belong at your decode site.
- Decoder settings other than the defaults. If you turn `Strict` off or set an `Entity` map, the token, depth and element bounds still hold. The attribute and text bounds can then miss input, so treat them as a guide.

[What xmlx leaves out](docs/non-goals.md) gives the reason for each.

## Documentation

- [How xmlx bounds a document](docs/design.md) is for choosing bound values and reading a rejection.
- [What xmlx leaves out](docs/non-goals.md) is for deciding whether a case is in scope.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
