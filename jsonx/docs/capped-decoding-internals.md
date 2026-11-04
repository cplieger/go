# How capped decoding works

This page explains how jsonx's capped decoding bounds a decode, what the bound costs and how each call matches `json.Unmarshal`, for developers checking the guarantees before they rely on them.

## Why a byte cap does not bound the decode

`json.Unmarshal` builds the whole decoded value before any count check in your code can run. Compact elements make a small body expensive, because `{}` is two bytes on the wire and a whole struct in a slice once decoded.

`BenchmarkHostileArray` decodes `{"parts":[{},{},...]}` holding 100,000 empty objects, 300,011 bytes in all. One run on Go 1.27.1, linux/amd64, gave these figures:

| Decode | Bytes allocated | Allocations | Time per decode |
| --- | --- | --- | --- |
| `json.Unmarshal` | 12,972,789 | 31 | 9.4 ms |
| jsonx, array cap of 16 | 1,674 | 22 | 2.2 µs |

`json.Unmarshal` allocates 43 times the size of the body. The capped walk allocates about 7,700 times less and refuses the body at element 17. Times depend on the machine. To measure it yourself, run this in the jsonx directory:

```sh
go test -run '^$' -bench HostileArray -benchmem
```

The cost of the capped walk does not grow with the element count the body claims. `TestBoundedAllocationDoesNotScaleWithCardinality` rejects a body of 1,000 elements and one of 1,000,000 under the same cap of 16, and asserts that the second costs less than 1.5 times the allocations of the first.

## The two bounds

A per-container cap is a schema statement, such as "at most 500 items". The element budget given to `NewDecoder` is a document statement. It stops a body that spreads a hostile total across many small containers, each one under its own cap.

Every array element decoded through `Array` and every map entry decoded through `Map` counts against the one budget, whichever container it belongs to. Nested or repeated containers therefore cannot multiply a per-container cap. `Decode` and `Skip` are not charged. A value read with `Decode` goes through `json.Decoder.Decode` whole, so decode a nested array or map through `Array` or `Map` when it needs a bound.

The per-container and budget checks run in a fixed order, before the next entry is allocated:

1. `Array` checks the per-array cap, then charges the budget, then grows the slice and decodes into the new element.
2. `Map` checks the per-map cap, then charges the budget, then reads the key and the value. The charge comes before the key because the key is itself an allocation from the wire.

A cap of zero or less on `Array` or `Map` turns that container's cap off, and the budget still applies. A budget of zero or less on `NewDecoder` turns the budget off, and the per-container caps still apply. Pass a real budget for untrusted input.

`Elements` reports how many elements and entries have been charged so far. A caller reading several paginated bodies can use it to carry one budget across several `Decoder` values.

## Parity with json.Unmarshal

The fuzz target `FuzzParityWithUnmarshal` decodes arbitrary bytes through a schema decoder built from these calls and through `json.Unmarshal`. For every body the walk accepts, `json.Unmarshal` must accept it too and return a value equal under `reflect.DeepEqual`, with nil and empty told apart. The walk may be stricter than `json.Unmarshal`, because rejecting what `json.Unmarshal` would build is what the caps are for. It may never be looser.

### Objects

- `Object` calls your field function once per key, and that function consumes exactly that key's value. It calls `Decode` for a known field, `Skip` for an unknown one, or `Object`, `Array` or `Map` for a container.
- A JSON null in place of the object is a no-op. The field function is never called and your target stays as it was, so a repeated key such as `"expand": null` cannot wipe a value already decoded.
- Match keys with `strings.EqualFold` to reproduce the case-insensitive field matching of `json.Unmarshal`.

### Arrays

- A JSON null gives a nil slice. An empty array `[]` gives an empty slice that is not nil.
- Pass the value already decoded for an earlier occurrence of the same key as `prior` to get the repeated-key behavior of `json.Unmarshal`. Within the retained capacity, `Array` reuses the earlier elements, so a struct element merges field by field. The result is cut to the length of the new array, and an empty re-occurrence replaces the slice with a fresh one.

### Maps

- A JSON null gives a nil map, which wipes an earlier occurrence of the same key.
- Each entry decodes into a fresh zero value, so a repeated key replaces the earlier entry instead of merging into it. A null value stores the zero value.
- An empty object `{}` gives an empty map that is not nil when `prior` is nil, and leaves a non-nil `prior` in place.
- Keys compare exactly, so `"a"` and `"A"` are two entries.
- The value function receives the key as well, so a per-key rule needs no second walk.
- On an error, the returned map holds the entries decoded so far, as a failed `json.Unmarshal` leaves them in your map. The error is the result.

### Trailing data

`End` rejects anything after the top-level value, as `json.Unmarshal` does. A plain `json.Decoder` leaves trailing data unread and unreported.

### Numbers

The underlying `json.Decoder` always runs with `UseNumber`. Skipping an unknown field therefore never converts its numbers through `float64`, which would reject a valid number such as `1e1000` that `json.Unmarshal` accepts when it skips a field. Decoding into a typed `int`, `string` or `bool` field is unaffected. Decoding into an untyped `any` gives a `json.Number`.

### Invalid UTF-8

`json.Unmarshal` replaces invalid bytes inside strings with U+FFFD instead of failing, and jsonx does the same.

## Duplicate keys

`Preflight` and the `Decoder` handle a repeated key in opposite ways, on purpose.

- `Object`, `Array` and `Map` reproduce the repeated-key behavior of `json.Unmarshal`, because a schema decoder must return what the standard library returns.
- `Preflight` rejects the body outright with `ErrDuplicateKey`, for a caller that cannot accept the ambiguity. `json.Unmarshal` processes repeated keys in order. Later values replace or merge into earlier values depending on the destination type, so nothing downstream can tell which occurrence a value came from.

`Preflight` conservatively treats keys equal under `strings.EqualFold` as duplicates, such as `"media"` and `"Media"`. This follows the case-insensitive struct-field fallback of `json.Unmarshal` without needing the destination type. Its key folding gives exactly the matches of `strings.EqualFold`.

`Preflight` decodes nothing into a Go value. Run it over the whole body, then hand the same bytes to `json.Unmarshal` or to a `Decoder` walk. It rejects malformed JSON and trailing data before decoding, as `json.Unmarshal` does. A later schema decode can still reject valid JSON whose type or shape does not match your schema. It takes no view of which keys or values are acceptable, only of whether the structure is unambiguous.

The fuzz target `FuzzPreflight` checks three things. Every body `Preflight` accepts is valid JSON for `json.Valid`. Its answer is the same on every run. A body it accepts stays accepted inside a one-element array.

## Errors

Cap and budget errors carry the bound and never document bytes. `ErrArrayCap` and `ErrMapCap` errors also name the container through the `what` argument you pass, and never a map key, because at this boundary the keys are text an attacker shaped.

An `ErrDuplicateKey` error quotes the repeated key, cut to at most 64 bytes on a rune boundary and rendered with `%q`. Control characters and newlines are escaped, so the message stays on one line and is safe to log.

When `Open` or `Key` meets a token of the wrong kind, its error prints that token, which is text from the document. Errors from `encoding/json` itself, such as a syntax error, are returned as they are, or wrapped by `End` when they follow the top-level value.

Match each class with `errors.Is` through the wrapping error.

## Skipping unknown fields

`Skip` reads one whole value, scalar or container, and builds no Go value from it. It still allocates, because `json.Decoder.Token` returns each token as an `any`, so its cost grows with the number of tokens in the skipped value. `BenchmarkSkip` measures 5.02 allocations per skipped element of a two-field object, 5,023 allocations for 1,000 elements.

## Nesting depth

`encoding/json` bounds nesting for every walk in this package, `Preflight` included. Its decoder refuses to read the token that would open container `MaxDepth+1`, and the error reaches you as a `*json.SyntaxError`, which jsonx does not translate. `MaxDepth` is 10000 and exists because the standard library does not export its threshold.

`TestPreflightDepthMatchesUnmarshal` asserts that `Preflight` accepts exactly the depths `json.Unmarshal` accepts. `TestPreflightAllocationBoundedByDepthCeiling` asserts that 4 MiB of open brackets costs `Preflight` less than 1.5 times the allocations of 64 KiB of them.

## The lower-level walk

`Open`, `More`, `Key`, `Close`, `End`, `Decode` and `Skip` let you decode a shape the helpers do not cover, such as an array you read one element at a time and keep only part of.

- `Open` reads a container's opening delimiter. It returns `ok` as false, without an error, for a JSON null, and returns an error for any other token.
- `json.Delim` is an integer type, so `Open('[')` takes an untyped rune constant, and your code needs no `encoding/json` import of its own.
- `Close` reads the closing delimiter once `More` reports false. `Key` reads the next object key.
- Create every `Decoder` with `NewDecoder`. The zero `Decoder` has no underlying `json.Decoder`, so its methods panic.
- A `Decoder` is not safe for concurrent use.
