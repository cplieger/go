# The rune policy

This page lists every rune textsafe replaces and why, for a developer who needs the exact set or wants to build a custom policy from the predicates.

## The four classes

textsafe treats four classes of rune as unsafe. Without the matching escape at the sink, the author of upstream text can use them to forge or garble what the operator sees.

- C0 controls (U+0000-U+001F) and DEL (U+007F) carry terminal escape sequences and forge log records. ESC starts CSI and OSC sequences that can retitle the terminal, clear the screen or write to the clipboard. Only the ESC rune is replaced, so the printable rest of a sequence, such as `[2J`, stays as plain text that no terminal acts on. A raw newline splits one record into two and fabricates a whole log line. CR and LF can be kept for a sink whose encoder escapes them, such as JSON.
- C1 controls (U+0080-U+009F) are one-rune escape introducers, such as CSI U+009B and OSC U+009D, with the same terminal powers as ESC sequences. `encoding/json` and slog's `JSONHandler` write them unescaped, so escaping C0 alone does not protect the terminal.
- The Unicode Bidi_Control format characters U+061C, U+200E, U+200F, U+202A-U+202E and U+2066-U+2069 visually reorder rendered text, as in [Trojan Source](https://trojansource.codes) attacks. A link or a verdict then reads differently from how it compares. The set matches `unicode.Bidi_Control` exactly, and a test checks the two rune by rune.
- The line and paragraph separators U+2028 and U+2029 are legal unescaped in JSON. JavaScript and many viewers treat them as line breaks, so they split records like a raw newline.

Under the single-line policy 79 runes are unsafe. They are the 65 C0 and C1 controls with DEL, the 12 bidi controls and the 2 separators. The multi-line policy keeps CR and LF, which leaves 77. Above ASCII, 46 runes are unsafe.

## The policy does not move with Unicode

The classes are written as code points and never looked up in a Unicode table, so a Go or Unicode upgrade cannot change what textsafe replaces. Measured over all 1,114,112 code points, every predicate and both sanitizers give the same answers on Go 1.26.7 (Unicode 15.0.0) and Go 1.27.0 (Unicode 17.0.0).

The tests use the Unicode tables only as an independent check. They fail if a future Unicode adds a member to one of these classes, and they pin each class's size, so following such an addition has to be a deliberate change.

An upgrade can still change how a sink renders textsafe's output. slog's `TextHandler`, for example, decides whether to quote a value with `unicode.IsPrint`. That is the encoder's choice about a printable rune, not a change in what textsafe replaces.

## One policy per sink

Each call site chooses one of two named policies instead of passing a boolean.

| Policy | Functions | Use it for |
| --- | --- | --- |
| Multi-line, CR and LF kept | `Sanitize`, `IsUnsafeMultiLine`, `SanitizeCapped`, `SanitizeBudgeted`, `NewBudget` | a sink whose encoder escapes CR and LF, such as JSON or slog's handlers |
| Single-line, CR and LF replaced | `SanitizeSingleLine`, `IsUnsafeSingleLine`, `SanitizeSingleLineBounded`, `SanitizeSingleLineCapped`, `SanitizeSingleLineBudgeted`, `NewSingleLineBudget` | a plain-text log line, a one-line error message, a rendered table cell |

The predicates expose the policy one rune at a time:

```go
textsafe.IsUnsafeMultiLine('\x1b')  // true:  ESC is always unsafe
textsafe.IsUnsafeMultiLine('\n')    // false: the sink's encoder escapes it
textsafe.IsUnsafeSingleLine('\n')   // true:  single-line sink; a newline forges a record
```

## A custom replacement policy

For a sink that needs a different replacement, such as removing the rune instead of writing a space, compose the predicate for your sink:

```go
// Remove (rather than blank) unsafe runes for a compact identifier.
id = strings.Map(func(r rune) rune {
    if textsafe.IsUnsafeSingleLine(r) {
        return -1
    }
    return r
}, id)
```

An escaper that percent-encodes instead of replacing, such as a Markdown link-URL escaper that must keep the URL usable, can use `IsUnsafeNonASCII`. It covers the part of the policy above ASCII: C1 controls, the bidi controls, and U+2028 and U+2029. A URL encoder already escapes ASCII controls and whitespace itself, and `url.Parse` accepts these runes raw:

```go
// Percent-encode the policy runes url.Parse accepts but a viewer must never see raw.
for _, r := range u {
    if textsafe.IsUnsafeNonASCII(r) {
        for _, b := range []byte(string(r)) {
            fmt.Fprintf(&out, "%%%02X", b)
        }
        continue
    }
    out.WriteRune(r)
}
```

`IsBidiControl` covers only the Bidi_Control set, for a policy about reordering alone.
