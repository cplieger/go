# jsonx

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/jsonx.svg)](https://pkg.go.dev/github.com/cplieger/go/jsonx) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/jsoncap)](https://github.com/cplieger/jsoncap/blob/main/go.mod) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/jsoncap/badges/mutation.json)](https://github.com/cplieger/jsoncap/issues?q=label%3Agremlins-tracker)

jsonx decodes untrusted JSON in Go and refuses the element that would cross your cap before it is allocated, while matching `json.Unmarshal` on well-formed input.

A byte cap on the body does not bound the decode. With `json.Unmarshal`, a 300 KB body of empty objects decodes into about 13 MB before your code can count anything. The package uses only the standard library, needs Go 1.27.1 or later and is licensed under Apache-2.0.

## Why use it

jsonx is built for Go code that decodes JSON from a third-party API or service.

- Each array read through `Array` and map read through `Map` gets its own cap. Both share one element budget, checked before each entry is allocated.
- On that 300 KB body, jsonx with a cap of 16 allocates under 2 KB and refuses it.
- A fuzz test checks that each accepted value equals what `json.Unmarshal` returns.
- `Preflight` rejects repeated object keys before `json.Unmarshal` can replace or merge their values.
- Cap errors name the container and bound, budget errors name the bound, and none quotes document bytes.

You write one decode function per type, without code generation.

Consider [`encoding/json/v2`](https://pkg.go.dev/encoding/json/v2) if you decode with its semantics and duplicate keys are your only concern. It rejects duplicate object names by default. Consider [xmlx](https://github.com/cplieger/go/tree/main/xmlx) if you decode XML, because it bounds the work an untrusted XML document can cost.

## Install

```sh
go get github.com/cplieger/go/jsonx@latest
```

## Usage

A schema decoder replaces `json.Unmarshal` with a token walk. This one caps each `items` array at 500 elements and all entries decoded through `Array` or `Map` at 10,000:

```go
type report struct {
	Name  string
	Items []item
}

type item struct {
	ID   int
	Kind string
}

func decodeReport(r io.Reader) (report, error) {
	dec := jsonx.NewDecoder(r, 10_000) // 10,000 entries across Array and Map calls

	var rep report
	err := dec.Object(func(key string) error {
		switch {
		case strings.EqualFold(key, "name"):
			return dec.Decode(&rep.Name)
		case strings.EqualFold(key, "items"):
			var err error
			rep.Items, err = dec.Array(rep.Items, 500, "items", func(it *item) error {
				return dec.Decode(it)
			})
			return err
		default:
			return dec.Skip() // unknown field, never built into a Go value
		}
	})
	if err != nil {
		return report{}, err
	}
	return rep, dec.End() // rejects trailing data, as json.Unmarshal does
}
```

`Array` refuses the element that would cross either bound, so the slice never grows past what you allowed. Match the bound that fired with `errors.Is`:

```go
if errors.Is(err, jsonx.ErrArrayCap) {
	// one array went over its own cap, and the error names it
}
if errors.Is(err, jsonx.ErrElementBudget) {
	// the Array and Map calls went over the shared budget
}
```

To keep only part of a large array, walk it element by element instead of through `Array`, which builds every element into one slice:

```go
if ok, err := dec.Open('['); err != nil || !ok {
	return err
}
for dec.More() {
	var g group
	if err := dec.Decode(&g); err != nil {
		return err
	}
	// keep what you need, drop the rest
}
return dec.Close()
```

Run `Preflight` over the whole body before you decode it to reject a repeated object key:

```go
if err := jsonx.Preflight(bytes.NewReader(body)); err != nil {
	return err // wraps jsonx.ErrDuplicateKey on a repeated key
}
```

`Preflight` treats keys that differ only in letter case as repeats, because `json.Unmarshal` can fill one struct field from either. A `Decoder` walk accepts a repeated key and handles it as `json.Unmarshal` does, so `Preflight` is the step that refuses one. [How jsonx works](docs/how-it-works.md#duplicate-keys) explains the split.

## API

- `NewDecoder(r, elementBudget)` starts a walk over one JSON value. A budget of zero or less turns the aggregate bound off, so pass a real one. A `Decoder` is not safe for concurrent use.
- `Array`, `Map` and `Object` decode a slice, a Go map and a struct-shaped object with `json.Unmarshal`'s null, empty and repeated-key handling.
- `Decode` reads one value through `json.Decoder.Decode`, outside the caps and the budget. Decode a nested array or map through `Array` or `Map` to bound it. `Skip` discards an unknown field without building it.
- `Open`, `More`, `Key`, `Close`, `End` and `Elements` are the lower-level walk, for shapes the helpers do not cover.
- `Preflight` rejects a body that repeats an object key.
- `ErrElementBudget`, `ErrArrayCap`, `ErrMapCap` and `ErrDuplicateKey` match with `errors.Is`. `MaxDepth` is the nesting ceiling of 10000 that `encoding/json` enforces.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/jsonx).

## It matches json.Unmarshal on well-formed input

A schema decoder built from these calls returns what `json.Unmarshal` would return. You can rely on these rules:

- A JSON null leaves a struct untouched and sets a slice or a map to nil. An empty array gives an empty slice that is not nil.
- A repeated key merges into a struct field by field and replaces a map entry with a fresh value.
- Matching keys with `strings.EqualFold` gives `json.Unmarshal`'s case-insensitive field matching.
- The decoder runs with `UseNumber`, so skipping a field never rejects a valid number such as `1e1000`. Decoding into an untyped `any` gives a `json.Number`.
- The walk is never looser than the standard library. A fuzz test asserts that it rejects every body `json.Unmarshal` rejects.
- `encoding/json` itself bounds nesting at `MaxDepth` for every walk, `Preflight` included.
- A repeated-key error quotes at most 64 bytes of the key, so a hostile key cannot fill a log line.

[How jsonx works](docs/how-it-works.md) covers each rule and the measured cost.

## Unsupported by design

- An unbounded stream of separate JSON values, such as NDJSON. A `Decoder` reads tokens without holding the whole body, but its caps and budget cover one top-level value, and `End` rejects anything after it. To process a stream, loop over `Decode` yourself.
- Schema validation. Whether a field is required, or a number is in range, stays your check at your decode site.
- Per-key counts. A rule such as "at most N of key X" stays in your decode function, and `Map` passes each key to it for that. The per-container caps and the budget cover the totals.
- Byte limits. The caps count elements and entries, not bytes. Keep a byte cap on the body to bound one long string, key or number.
- Invalid UTF-8. `json.Unmarshal` replaces invalid bytes with the replacement character U+FFFD, and so does jsonx. Check the bytes yourself when you need byte-exact text.

## Documentation

- [How jsonx works](docs/how-it-works.md) shows the measured cost, the order of the checks and every parity rule, for developers checking the guarantees.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
