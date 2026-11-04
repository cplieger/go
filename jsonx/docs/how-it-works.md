# How jsonx decodes a value

This page describes the two steps of every decode, for a developer who writes a custom policy, reads the facts directly or matches the error type.

`ParseInt64` first calls `Classify`, which records what the value looks like. It then applies the policy's rules to those facts, in a fixed order. The first rule that does not accept the value decides the result.

## The facts

`Classify` returns a `Facts` value for any input, including garbage, and never panics or returns an error. It judges nothing. Its fields:

| Field | Meaning |
| --- | --- |
| `Shape` | The kind of token. `Null`, `Empty`, `EmptyString`, `MalformedString`, `NonNumericString`, `Number`, `NumericString` or `Other` |
| `Value` | The exact integer. It is nonzero only for a numeric shape whose value is integral and fits `int64` |
| `FloatForm` | The literal used a decimal point or an exponent, such as `"9.0"`, `1e3` or `1.5`. The value may still be integral |
| `Fractional` | The value is not integral, such as `1.5` or `1e-999`. `Value` stays 0 |
| `Negative` | The value is below zero. For an overflow, it is the literal's sign. `"-0"` is not negative |
| `Overflow` | The magnitude does not fit `int64`. `Value` stays 0 |
| `Padded` | The string content carried ASCII whitespace around it, such as `" 12 "` |

`WasString()` reports whether the value arrived quoted, in any string shape. `Other` is the zero value of `Shape` and covers objects, arrays, booleans and invalid tokens, so an unset shape reads as the least trusted one.

## The order the rules run in

1. A value that is not a number goes by its `Shape` to one policy field. `Empty` goes to `EmptyInput`, `Null` to `Null`, `EmptyString` to `EmptyString`, `MalformedString` to `MalformedString`, `NonNumericString` to `NonNumericString` and `Other` to `OtherShape`. Each of these is `Zero` or `Reject`.
2. A number then passes `PaddedString`, `FloatForm` and `Fractional`, in that order.
3. Last comes the range check against `MinValue` and `MaxValue`. A value outside it, or one that overflows `int64`, goes to `OutOfRange`.

## What a policy field can say

A `Disposition` is `Reject`, `Zero` or `Accept`. `Zero` turns the value into 0 with no error.

`Reject` is the zero value, so a field you leave unset rejects. `Accept` means something only for `PaddedString` and `FloatForm`, where a usable integer exists. On any other field it is treated as `Reject`, so a misused `Accept` fails with an error instead of inventing a value. `Fractional` can only zero or reject, because jsonx has no truncation path.

`MinValue` and `MaxValue` have no default. Every policy states its range, and `math.MinInt64` with `math.MaxInt64` means unbounded. An empty `Policy{}` therefore rejects every odd shape and allows only the range 0 through 0. It accepts a plain 0 and nothing else.

A policy with every field set to `Zero` over the full `int64` range is the fully lenient decoder. It turns every value it would reject into 0, float forms included.

## Errors

A rejection returns a `*ParseError`. Match it with `errors.As`, or with `errors.AsType`. It carries three fields:

- `Reason`, a constant naming the rule that fired, such as `ReasonFloatForm` or `ReasonOutOfRange`. There is one per policy field.
- `Facts`, the full classification of the rejected value.
- `Snippet`, the first 40 bytes of the offending value, followed by `...` when it is longer.

`Error()` has a stable shape, `jsonx: <reason>: "<snippet>"`, for example `jsonx: float form: "\"9.0\""`.

## The field types

`TolerantInt`, `StrictInt` and `StrictAbsentZeroInt` each reset to 0 before they decode. `encoding/json` reuses one field for a repeated object key, so `{"id":5,"id":null}` decodes as 0 rather than keeping the 5. On an error the field stays 0, never a partial value.

They marshal as plain numbers through their underlying `int64`. The wire form a value arrived in is not kept. Read it from `Classify` when you need it.

## The number grammar

jsonx accepts only decimal number forms, a tighter grammar than `strconv` parsing:

- A quoted hex float (`"0x1p2"`), an `"Inf"` or `"NaN"` word, and digit separators (`"1_000"`) are non-numeric strings, although `strconv.ParseFloat` accepts them.
- Only ASCII JSON whitespace counts as padding. A token padded with a Unicode space such as a no-break space stays a non-numeric string.
- A quoted number may have leading zeros (`"007"`) and a leading `+` (`"+5"`). A bare token must follow the JSON number grammar exactly, so a bare `007` is an `Other` shape.
- Integer literals parse with `strconv.ParseInt` across the whole `int64` range, never through `float64`, whose rounding corrupts ids above 2^53.
- Float-form literals such as `"9.0"` and `1e3` never pass through `float64` either. Integrality, range and the exact value come from the decimal digits.

Three results follow from the last rule. `9007199254740993.0`, which is 2^53+1 and the first integer `float64` cannot hold, decodes exactly. A full underflow such as `1e-999` is fractional, not an integral zero. The `int64` boundary is exact. `"9223372036854775807.0"` is `math.MaxInt64`, and one more overflows.

## Cost

An exponent with too many digits saturates, so the work stays bounded by the input length. Tests pin that the allocation count of `Classify` and `ParseInt64` stays the same from a 512-byte input to a 64 KB one.
