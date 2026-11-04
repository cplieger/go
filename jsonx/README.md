# jsonx

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/jsonx.svg)](https://pkg.go.dev/github.com/cplieger/go/jsonx) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=jsonx%2Fgo.mod)](https://github.com/cplieger/go/blob/main/jsonx/go.mod)

jsonx decodes the integer fields of someone else's JSON in Go, whether a value arrives as `14`, `"14"`, `null`, `"unknown"` or `9.0`, under one policy you choose.

It replaces the custom `UnmarshalJSON` method you would otherwise write for each inconsistent field. The policy decides, shape by shape, whether to keep the value, return 0 or return a typed error. It uses only the standard library at run time, needs Go 1.27.3 or later and is licensed under Apache-2.0.

## Why use it

jsonx is built for Go code that reads integer ids and counts from an API it does not control.

- Three ready-made policies cover the common stances, each as a field type for your struct.
- For a custom policy, copy a shipped one and set a field, such as `MinValue` to 1 for positive ids.
- It never truncates `9.9` to `9` or rounds a large id through `float64`. `Classify` reads `9007199254740993.0` as exactly 9007199254740993.
- A rejection is a `*ParseError` naming the rule that fired, with the offending value cut to 40 bytes.
- Any input gives a value or an error, never a panic, and allocations do not grow with the input.

Consider `json.Number` from `encoding/json` if a field only ever holds a valid number, bare or quoted. It needs no dependency. Consider [go-viper/mapstructure](https://pkg.go.dev/github.com/go-viper/mapstructure/v2) if you want weak typing on every field of generic map data at once, through its `WeaklyTypedInput` option.

## Install

```sh
go get github.com/cplieger/go/jsonx@latest
```

## Usage

### Field types

```go
type record struct {
    AniListID jsonx.TolerantInt `json:"anilist_id"` // odd values become 0, a malformed string is an error
    TvdbID    jsonx.TolerantInt `json:"tvdb_id"`
}

type item struct {
    ID jsonx.StrictInt `json:"id"` // an error unless an integer, null or ""
}
```

### One value

```go
v, err := jsonx.ParseInt64(data, jsonx.Strict())        // an error unless an integer, null or ""
t, terr := jsonx.ParseInt64(data, jsonx.TolerantZero()) // odd values become 0, a malformed string is an error
```

### A custom policy

A policy is a plain struct value. Copy a shipped one and change the fields you need:

```go
// Strict ids: null and "" are errors, and an id must be positive.
p := jsonx.Strict()
p.Null = jsonx.Reject
p.EmptyString = jsonx.Reject
p.MinValue = 1

// Tolerant, with a wider range than TolerantZero's MaxInt32.
t := jsonx.TolerantZero()
t.MaxValue = math.MaxInt64
```

### The facts behind a decision

`Classify` reports what a value looks like without judging it, for logic no policy expresses, such as which wire form a value arrived in or whether it was `null`:

```go
f := jsonx.Classify(data)
// f.Shape, f.Value, f.WasString(), f.FloatForm, f.Fractional,
// f.Negative, f.Overflow, f.Padded
```

The runnable examples on pkg.go.dev show each case, and `go test` keeps them true.

## API

- Parsing: `ParseInt64(data, policy)` returns an `int64` or an error. `Classify(data)` returns the `Facts` of one value.
- Policies: `Policy`, `Disposition` with `Reject`, `Zero` and `Accept`, and the ready-made `TolerantZero()`, `Strict()` and `StrictAbsentZero()`.
- Field types: `TolerantInt`, `StrictInt` and `StrictAbsentZeroInt`, each a `json.Unmarshaler` for one ready-made policy.
- Errors: `*ParseError`, with one `Reason` constant per rule.

Every result is an `int64`. For an `int32` or a non-negative id, set the policy's `MinValue` and `MaxValue`, then convert. The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/jsonx).

## The three policies

`TolerantZero()` keeps a record from failing on one bad field. Every odd shape or out-of-range value becomes 0. The one error is a string that is not valid JSON, such as `"unterminated`. It accepts integral float forms and padded strings. It limits values to 0 through `math.MaxInt32`, so `-3` also becomes 0.

`Strict()` accepts an integer written as a number or as a quoted string, anywhere in the `int64` range. It reads `null` and `""` as 0, and every other value is an error.

`StrictAbsentZero()` behaves like `Strict()` and also reads zero-length input as 0. Use it when your code calls `ParseInt64` with empty bytes for a field the JSON left out.

What each policy returns:

| Input | `TolerantZero` | `Strict` | `StrictAbsentZero` |
| --- | --- | --- | --- |
| `14` / `"14"` | 14 | 14 | 14 |
| `-3` / `"-3"` | 0 | -3 | -3 |
| `null` / `""` | 0 | 0 | 0 |
| zero-length input | 0 | error | 0 |
| `"abc"`, `"unknown"` | 0 | error | error |
| `9.0`, `"9.0"`, `1e3` | 9 / 1000 | error | error |
| `1.5`, `"1.5"` | 0 (never truncated) | error | error |
| `" 12 "` | 12 | error | error |
| `"007"`, `"+5"` | 7 / 5 | 7 / 5 | 7 / 5 |
| `2147483648` | 0 (> MaxInt32) | 2147483648 | 2147483648 |
| `9223372036854775808` | 0 | error | error |
| `{}`, `[1]`, `true`, garbage | 0 | error | error |
| `"unterminated` | error | error | error |

[How jsonx decodes a value](docs/how-it-works.md) gives the order the rules run in, the error type and the number grammar.

## Unsupported by design

| Feature | Reason |
| --- | --- |
| Truncating fractional values | 9.9 truncated to 9 points at a different record. `Fractional` has no `Accept`. |
| Float-valued fields | jsonx decodes integer ids and counts. Decode real floats into `float64` or `json.Number`. |
| Keeping the wire form | The field types marshal as plain numbers, so a value read from `"14"` is written as `14`. |
| Tolerant strings, arrays and objects | jsonx covers the integer that arrives as a number or a string. Tolerant decoding of other shapes stays in your code. |
| Replacing `json.Number` | `json.Number` leaves parsing to each reader. jsonx parses once, under a policy. |

## Capped decoding

A byte cap on the body does not bound the decode: with `json.Unmarshal`, a 300 KB body of empty objects decodes into about 13 MB before your code can count anything. A `Decoder` walks the token stream instead and refuses the element that would cross your cap before it is allocated, while matching `json.Unmarshal` on well-formed input.

- `NewDecoder(r, elementBudget)` starts a walk over one JSON value. `Array`, `Map` and `Object` decode a slice, a Go map and a struct-shaped object with `json.Unmarshal`'s null, empty and repeated-key handling, and `Skip` discards an unknown field without building it.
- `Preflight` rejects a body that repeats an object key, before `json.Unmarshal` can replace or merge the values.
- `ErrElementBudget`, `ErrArrayCap`, `ErrMapCap` and `ErrDuplicateKey` match with `errors.Is`, and none quotes document bytes.

[Capped decoding](docs/capped-decoding.md) shows a schema decoder, every call and the rules it shares with `json.Unmarshal`.

## Documentation

- [How jsonx decodes a value](docs/how-it-works.md) covers the facts, the order the rules run in, the error type, the field types and the number grammar, for a developer writing a custom policy.
- [Capped decoding](docs/capped-decoding.md) shows a schema decoder, the API and what it leaves out, for a developer decoding a whole untrusted body.
- [How capped decoding works](docs/capped-decoding-internals.md) shows the measured cost, the order of the checks and every parity rule, for developers checking the guarantees.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
