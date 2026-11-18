# Read state

This page is for a Go developer whose API serves a count or a chart built from something the app read elsewhere, such as an upstream's list or a media root. It covers `Count` and `CountOf`, `Read` and `ReadOf` for a read beside any other figure, a frame's `read` through `WithRead`, and what rule `read` refuses. The six states, the folds and the `/metrics` pair are [measure's](../../measure/docs/how-it-works.md#read-state).

## The rule

A count that was not read whole shows the value of its last whole read, with the state of the latest read, the time of that whole read and the app's reason, never 0 or a dash. The page words it ("Partial, as of 14:05"). Before the first whole read there is no value to show, so the count carries none.

## A count on the API

`CountOf(last, r)` builds the figure an API row carries for a count: `last` is the value the app wrote in the commit that marked its signals whole, nil before the first whole read, and `r` is the scope's `measure.Read`.

```go
c, err := chartdata.CountOf(&open, read) // read is prev.Cut(measure.ReadPartial, "rate_limited")
```

```json
{"value":412,"state":"partial","as_of":"2026-11-18T14:05:00.123456789Z","reason":"rate_limited"}
```

| Field | Holds |
| --- | --- |
| `value` | the last whole read's value, `null` before the first whole read |
| `state` | one of `complete`, `partial`, `blind`, `unread`, `unsupported` and `excluded`; the zero `Read` is `unread` |
| `as_of` | the last whole read's instant, an RFC 3339 string with its fraction as `time.Time`'s JSON writes it, `null` before the first whole read |
| `reason` | the app's code for why the read is not whole, left out when empty |

`value` and `as_of` are set together or not at all: a count has a value exactly when a whole read gave one. The value is a copy, refused as a frame's value is when it is not finite or past `MaxMagnitude`.

## A read beside another figure

A figure that is not the last whole read's value, such as a list's exact `total`, still has a read behind it. `ReadOf(r)` writes that read as `Read`, `{state, as_of, reason?}`, with the fields of a count but no value, for any state, `complete` included:

```go
state, err := chartdata.ReadOf(read) // read is the scope's measure.Read
page := Page{Items: items, Total: total, CountState: state} // CountState is a chartdata.Read, `json:"count_state"`
```

```json
{"state":"partial","as_of":"2026-11-18T14:05:00.123456789Z","reason":"rate_limited"}
```

The page shows the total as read and marks it with the read's state and time.

## A frame's read

`WithRead(r)` marks any frame as built from a read that was not whole: the frame gains `read`, a `Read`. A complete read that rule read does not refuse adds nothing, a zero `AsOf` included, so a frame built from a whole read encodes exactly as one built with no option, and every frame that existed before encodes as it did. A complete read with a reason, or with an `AsOf` that `time.Time` cannot write, is refused. The chart draws the frame as built and ignores `read`; the page marks the figure around it.

`as_of` is a read's time, which the page words and compares, not a place on an axis. A frame's axis values, `x`, `resets`, a note's `at` and a calendar's `start`, stay numbers in the axis unit, Unix milliseconds on a time axis.

A sampled frame carries no read: it samples the exported series, which already hold the last whole value.

## What rule read refuses

On a count, a read alone and a frame's read:

- a state outside the six;
- a `complete` read with a reason;
- a reason past `MaxReasonBytes` (64) or with a byte outside `a-z`, `0-9` and `_`;
- an `as_of` that `time.Time`'s JSON cannot write: a year outside 0 to 9999, or a zone offset of 24 hours or more.

On a count and a read alone:

- a `complete` one with no `as_of`.

On a count only:

- a value with no `as_of`, and an `as_of` with no value.

On the wire, a frame whose `read` names `complete` breaks the rule too, since no constructor writes one. The [refusal fixtures](contract.md) carry one case of each.
