# Testing log output with capture

This page explains the checks in the `slogx/capture` subpackage and how each one matches messages and values. It is for a Go developer who asserts on log output in tests.

## Install a recorder

Use `capture.Default(t)` for code that logs through `slog.Default()`. It installs a fresh recorder as the default logger. When the test ends, `t.Cleanup` restores the previous default logger and the `log` package's output writer and flags, because `slog.SetDefault` changes all three. A test that uses it must not call `t.Parallel`. `capture.SwapDefault(t, logger)` installs a logger you build yourself with the same restore, under the same rule.

Use `capture.New()` for code that takes an injected `*slog.Logger`. It returns a logger and its recorder and leaves the global default alone, so the test can run in parallel.

A `Recorder` also works without either function. `&capture.Recorder{}` is a ready `slog.Handler` that you can pass to `slog.New` or embed. A recorder captures every level and is safe for concurrent use.

## Count messages

- `Count(sub)` counts the records whose message contains `sub`, and `Contains(sub)` reports whether there is at least one.
- `CountExact(msg)` counts the records whose message equals `msg`.
- `CountLevel(level, sub)` counts the records at exactly that level whose message contains `sub`. An empty `sub` counts every record at that level.
- `Messages()` returns every message in capture order, and `Len()` returns the number of records.

Use `CountExact` when something outside the code depends on the exact message, such as a log-based alert rule that matches the whole `msg` value. A substring count would also pass on a longer message that contains it.

Use `CountLevel` when a test pins the level as well as the message. For a log site that moves from WARN to ERROR past a threshold, the assertion "one ERROR and zero WARN of this message" needs it, because the other counters ignore the level.

## Check attributes

The attribute checks read a record's top-level attributes, with attributes from `Logger.With` already included:

- `AttrValue(msgSub, key)` returns the rendered value of the first match.
- `HasAttr(msgSub, key, rendered)` reports whether a matching record has exactly that rendered value.
- `AttrContains(msgSub, key, sub)` reports whether a matching record has a value that contains `sub`.

A record matches when its message contains `msgSub`. Values compare by their rendered form, `slog.Value.String()`, so an `Int64` 7 and a string `"7"` both match `"7"`. The comparison ignores the kind of the value on purpose.

An empty string matches everything for both parameters. A `msgSub` of `""` matches every record, and a `key` of `""` matches every attribute. Values nested inside groups are out of scope, so walk `Records()` for those.

## Match the whole message

`AttrValueExact(msg, key)` and `AttrValuesExact(msg, key)` match a record only when its message equals `msg`. They relate to `AttrValue` the way `CountExact` relates to `Count`.

Use them when the message is fixed by something outside the code. With substring matching, `AttrValue("cycle complete", "files")` also reads from a `"cycle completed with errors"` record, so the assertion can inspect a record the contract never named.

`AttrValuesExact` returns the value from every matching record in capture order, and `nil` when none matched. Use it for a log site that repeats, such as one record per retry, per pruned file or per polled item. `AttrValueExact` returns only the first match and `HasAttr` only says whether any record matched, so neither can check the sequence.

For these two, `msg` is a whole message. An empty `msg` matches only a record with an empty message. An empty `key` still matches every attribute.

## Check the kind of a value

`Attr(msgSub, key)` returns the `slog.Value` itself instead of its rendered text. Use it when the test must pin the kind of a value. `slog.Time("at", t)` and `slog.String("at", t.String())` render the same text, so no rendered check can tell which one the code used, while a JSON handler writes them differently.

`AttrValue` is `Attr` rendered, so the two always agree about what a record carries. A group value is copied out of the recorder's storage before `Attr` returns it, at every level of nesting, so you can keep it across later captures. A value of kind `Any` is your own object and comes back as it is, so changing that object also changes what a later read returns.

Use `AttrValue` when only the text matters. Walk `Records()` when the test checks a whole record, such as its level, its full set of keys or several values at once.

## What a stored record holds

A stored record holds what the standard `TextHandler` or `JSONHandler` would write, not the raw call-site input:

- `Logger.With` attributes and `Logger.WithGroup` nesting are folded into each record.
- A value that implements `slog.LogValuer` is stored resolved.
- An attribute whose key and value are both zero is dropped, and so is a group with no attributes. A group with an empty key has its attributes moved into its parent. These follow the `slog.Handler` rules.

The time, level, message and source location are stored as the call site passed them, so a custom level keeps its value.

`Records()` returns a copy of the captured records in order. Changing or extending that copy does not affect later captures.

The package's own tests run the standard library's `testing/slogtest` checks against the recorder. They also replay captured records through real text and JSON handlers, and the output must match what those handlers write directly, byte for byte.
