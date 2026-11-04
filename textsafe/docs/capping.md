# Bounding sanitized text

This page covers textsafe's byte caps, for a developer putting a size limit on untrusted text. It says which function to pick, what each one promises, and the order to use when you also redact a secret.

## Cap after sanitizing

Sanitizing can grow a string, because each invalid UTF-8 byte becomes the three-byte U+FFFD. So a byte cap belongs after sanitizing.

A plain `s[:n]` can then split a multi-byte rune. The partial rune left at the end has raw 0x80-0x9F bytes, which a terminal that does not decode UTF-8 reads as C1 escape introducers, the class the sanitizer just removed. `CapBytes` cuts on a rune boundary instead:

```go
body := textsafe.CapBytes(textsafe.Sanitize(raw), maxBodyBytes)
```

For valid UTF-8, such as any sanitizer output, `CapBytes` drops at most three bytes below the cap and returns a valid UTF-8 prefix. A cap of zero or less returns `""`.

## Keep the tail

When the identifying part of a value sits at its end, such as a file name in a path or the last lines of a log, `CapBytesTail` keeps the last bytes instead:

```go
tail := "..." + textsafe.CapBytesTail(textsafe.Sanitize(raw), maxBodyBytes)
```

If the cut would land inside a multi-byte rune, `CapBytesTail` drops that partial rune and starts at the next whole one. The result stays within the cap, and it never starts with the U+FFFD that `encoding/json` writes for a partial rune. The cut is rune-safe, not grapheme-safe, so it may fall between a base rune and its combining mark. A marker in front of the tail is yours to add. A cap of zero or less returns `""`.

## A marked log attribute

`SanitizeSingleLineBounded` covers the common log attribute, which is single-line, capped and visibly marked. It runs `SanitizeSingleLine`, then `CapBytes` on the sanitized form, then appends `"..."` after the cap.

```go
slog.Warn("upstream rejected request",
    "reason", textsafe.SanitizeSingleLineBounded(upstreamErr.Error(), 200))
```

The cap `n` is for the text it keeps, so a cut result is at most `max(n, 0) + 3` bytes. A result within the cap comes back byte for byte, with no marker. A cut result always ends in the marker, but a value can also end in `...` on its own, so the marker does not prove a cut. When you need to know, use one of the functions in the next section. A cap of zero or less gives `"..."` for non-empty input, and `""` stays `""`.

## A hard size limit

Some caps are real limits, such as a record stored under a write limit, a payload under a vendor byte cap or a fixed-width column. For those, `SanitizeCapped` keeps CR and LF for a JSON sink and `SanitizeSingleLineCapped` replaces them. Both take the marker from you and count it inside the cap, so the text never exceeds `max(n, 0)` bytes. Both also return whether they cut:

```go
attr, cut := textsafe.SanitizeSingleLineCapped(key, maxLoggedKeyBytes, "...")
slog.Warn("unknown upstream keys", "key", attr, "key_truncated", cut)
```

`cut` is true exactly when the sanitized form did not fit. It says nothing about whether sanitizing rewrote a rune. A cap too small to hold the marker drops the marker rather than writing part of it, and `cut` still reports the cut. An empty marker caps silently. An empty input returns `""` and `false` under any cap.

The marker is written as given and never sanitized. Build it from your program's own text, not from untrusted input.

## Bounding the work

Everything above bounds what comes back. When the caller does not control a value's size, such as an upstream response field, an error message that quotes one or a file name, the bound has to cover the work too. Walking a multi-megabyte value with `strings.Map` in a memory-limited process is a work-amplification denial of service ([CWE-400](https://cwe.mitre.org/data/definitions/400.html)), whatever the output cap is.

`SanitizeBudgeted` and `SanitizeSingleLineBudgeted` move the cap ahead of the sanitizer. They cut the raw bytes on a rune boundary first, sanitize only that chunk, cap again if sanitizing grew it, and count your marker inside the cap.

```go
label, cut := textsafe.SanitizeSingleLineBudgeted(upstreamLabel, 64, "...(truncated)")
slog.Warn("skipped block", "type", label, "type_truncated", cut)
```

For a valid UTF-8 value with no unsafe rune, both orders give the same bytes and the same `cut`. Moving a call site to the budgeted form therefore changes nothing an honest value emits.

The orders can differ when the raw value past the cap holds multi-byte unsafe runes, because each one shrinks to a one-byte space. The budgeted form reads no bytes past the raw cap, so it can return fewer sanitized bytes than `SanitizeCapped`, or cut and mark a value that `SanitizeCapped` returns whole. The mark is honest, because bytes really were dropped before sanitizing.

## Several values under one budget

A `Budget` shares one byte budget across several untrusted values and reports one truncation fact for all of them. Joining the values first and capping afterwards would hold the whole untrusted aggregate in memory before the bound applies.

```go
b := textsafe.NewBudget(maxAttrBytes, "...")
for i, group := range upstream.Groups {
    if i > 0 && !b.Write(", ") {
        break
    }
    if !b.Write(group) { // false once anything has been dropped
        break
    }
}
attr, cut := b.Result()
slog.Warn("better release available", "groups", attr, "groups_truncated", cut)
```

- Each `Write` caps its value before sanitizing it, like `SanitizeBudgeted`. `NewBudget` keeps CR and LF, and `NewSingleLineBudget` replaces them.
- Separators go through `Write` too, so a hostile number of values cannot grow the attribute past the budget either.
- `Write` returns false once anything has been dropped. It does not report whether room is left. Keep writing until a write returns false, because the refused write is what records the cut. If you stopped when the budget was full, the skipped values would be lost and `Result` would report no cut.
- After a cut, later writes add nothing, and a non-empty write is refused whole. A write of `""` never records a cut.
- `Result` adds the marker once, inside the budget, so the text never exceeds `max(n, 0)` bytes. It does not spend the budget, so calling it twice returns the same pair.
- Create a `Budget` with `NewBudget` or `NewSingleLineBudget`. The zero value has no budget and treats every write as a cut. Do not copy a `Budget` after its first write, and do not share one between goroutines.

## Redacting a known secret

`RedactSingleLineCapped` removes known secrets, such as API keys, from text bound for a single-line sink, and caps the result like `SanitizeSingleLineCapped`:

```go
reason, cut := textsafe.RedactSingleLineCapped(body, maxReasonBytes, "...", apiKey, token)
slog.Warn("upstream refused", "reason", reason, "reason_truncated", cut)
```

It finds every match of a secret in the text and in its sanitized form, writes the sanitized text with `[redacted]` in place of every rune a match touches, and caps last. Each part is needed:

- Match the text as given, because the sanitizer rewrites an unsafe rune inside a value. A key with one control rune inside it would reach the sink as a near-complete fragment that a full-value match no longer finds.
- Match the sanitized form, because the sanitizer produces spaces and U+FFFD, the only runes it can write. A secret that contains either can be assembled from text that did not match before.
- Find both kinds of match before replacing anything, because they can overlap. Replacing one kind first would break a match of the other kind and leave part of that secret behind.
- Cap last, so a secret that crosses the cut is already gone rather than sliced into a surviving prefix.

Pass every secret the text may carry in one call. Empty and repeated secrets are ignored, and overlapping or adjacent matches become one `[redacted]`, so a short secret inside a longer one cannot leave the rest of the longer one behind. After a cut, a tail that matches the start of a secret is dropped too.

No match of a secret in the text, or in its sanitized form, survives. A match replaces the whole runes it touches and nothing else, so a secret that is not valid UTF-8 cannot leave part of a rune behind. The `[redacted]` mark itself is fixed text: a secret that shares text with `[redacted]`, such as one holding a bracket, can still be spelled by a mark and the text around it, and the marker you pass is written as is.

Pass the whole value. If you cut it yourself first, a secret split by your cut can survive as its first bytes whenever the shortened value then fits the cap. Register each wire form a secret takes in the text, such as its JSON-escaped or percent-encoded form, as a secret of its own, because matching is byte for byte.

`SanitizeSingleLineBounded`, the Capped and Budgeted pairs and `Budget` all contain a cap, and `Budget.Write` caps before it sanitizes. A redaction after one of them may miss a secret the cap split. A redaction built from separate passes around a sanitizer can also leave part of a secret behind, because a match in the text and one that only exists after sanitizing can overlap. `RedactSingleLineCapped` finds both in one pass. It has no multi-line form.
