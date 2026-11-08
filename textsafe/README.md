# textsafe

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/textsafe.svg)](https://pkg.go.dev/github.com/cplieger/go/textsafe) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=textsafe%2Fgo.mod)](https://github.com/cplieger/go/blob/main/textsafe/go.mod)

textsafe keeps untrusted text from forging your Go log lines, starting terminal escapes or reordering what a reader sees in a JSON document or report.

It replaces the `strings.Map` over control characters you would otherwise write at each log call, JSON writer and report renderer with one policy they all share. It uses only the standard library, needs Go 1.27.3 or later and is licensed under Apache-2.0.

## Why use it

textsafe is built for Go code that logs or re-emits text it did not write, such as API fields and error messages.

- It replaces C0 and C1 controls, DEL, the 12 bidi controls, and U+2028 and U+2029 with a space.
- `encoding/json` and slog's `JSONHandler` write C1 and bidi controls unescaped, so JSON alone does not stop a terminal escape or a [Trojan Source](https://trojansource.codes) reordering.
- `Sanitize` keeps CR and LF for JSON, and `SanitizeSingleLine` replaces them for a one-line sink.
- Caps cut on a rune boundary, and `SanitizeCapped`, `SanitizeSingleLineCapped` and `Budget` bound the sanitizer's work too.
- An `Untrusted` value emits its sanitized form whenever slog, an encoder or a `fmt` verb writes it, as a field, a slice element or a map key. The exceptions are `%#v`, which prints an ASCII-escaped constructor call, and `%T`, `%p` and a `%w` on a non-error, which `fmt` prints without calling its methods. It stores no raw unsafe rune for such a reflection-based printer to find.
- `Sanitize`, `SanitizeSingleLine` and `Untrusted` in slog and in `fmt`'s `%v` and `%s` do not allocate for clean text.

Consider [`strconv.Quote`](https://pkg.go.dev/strconv#Quote) or `%q` if you want each control shown as an escape such as `\u202e`. Consider [bluemonday](https://github.com/microcosm-cc/bluemonday) for user content in HTML, which it sanitizes against an allowlist.

## Install

```sh
go get github.com/cplieger/go/textsafe@latest
```

## Usage

Sanitize a value where it is logged or encoded, so comparisons and dedupe keys keep the raw value:

```go
slog.Warn("better release available",
    "title", textsafe.Sanitize(upstream.Title),
    "group", textsafe.Sanitize(upstream.Group))
```

Each unsafe rune becomes a space, and each invalid UTF-8 byte becomes U+FFFD, so the result is always valid UTF-8. Sanitizing twice gives the same result as sanitizing once.

Where a raw newline would start a new record, such as a plain-text log line or a one-line error message, use `SanitizeSingleLine`. `SanitizeSingleLineBounded` also caps the sanitized text on a rune boundary and appends `...` when it cuts:

```go
msg := textsafe.SanitizeSingleLine(upstreamErr.Error())

slog.Warn("upstream rejected request",
    "reason", textsafe.SanitizeSingleLineBounded(upstreamErr.Error(), 200))
```

You can also tag a field once, in the struct that decodes the upstream payload. slog, `fmt`'s `%v` and `%s`, and `encoding/json` then emit it sanitized, and `Raw` returns the decoded string unchanged:

```go
type Episode struct {
    Title textsafe.Untrusted `json:"title"`
}

slog.Warn("better release available", "title", ep.Title) // sanitized automatically
if ep.Title.Raw() == stored.Title { /* matching stays raw */ }
```

Tag text that does not come from a decoder with `textsafe.NewUntrusted(s)`. If your program saves a struct and reads it back, store `Raw` in a plain `string` field, because `json.Marshal` writes a tagged field in its sanitized form.

The package has 14 runnable examples on pkg.go.dev, and `go test` keeps them true. [Bounding sanitized text](docs/capping.md) covers hard size limits and bounding the sanitizer's work.

## API

- `Sanitize` and `SanitizeSingleLine` sanitize a whole string, keeping or replacing CR and LF.
- `SanitizeSingleLineBounded` sanitizes the whole value and then cuts it to the cap, marking a cut with `...` outside it.
- `SanitizeCapped` and `SanitizeSingleLineCapped` cut the raw value to the cap before sanitizing, so a huge value costs no more work than a short one. They return whether they cut, and the marker you pass counts toward the cap. A `Budget`, created with `NewBudget` or `NewSingleLineBudget`, does the same for several values that share one cap.
- `RedactSingleLineCapped` removes known secrets from the text and from its sanitized form for a single line, then cuts the result to the cap with the marker inside it. It reads the whole value, so it does not bound the work.
- `CapBytes` keeps the head and `CapBytesTail` keeps the tail, both cut on a rune boundary.
- `IsUnsafeMultiLine`, `IsUnsafeSingleLine`, `IsUnsafeNonASCII` and `IsBidiControl` classify one rune, for a policy of your own.
- `Untrusted`, created with `NewUntrusted` or by decoding, has `Raw` and `SingleLine`, plus the `LogValue`, `String`, `Format`, `MarshalText` and `UnmarshalText` methods that slog, `fmt` and encoders call.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/textsafe).

## Sanitize where text leaves your program

Sanitize text where it is logged or encoded, never when it is parsed, so matching and dedupe keys keep working on the raw value. Use one policy for the whole app, through these functions, the `Untrusted` type or one wrapper around a predicate, so two sinks cannot disagree about what is unsafe.

Some text must be safe in every sink it may ever reach, such as an error body captured into a returned value. Sanitize it once when you build the value, instead of tagging it.

A Markdown table cell, a link URL or an HTML page has injection characters of its own, such as pipes, brackets and angle brackets. Apply that sink's own escaper on top of textsafe.

If you also remove a known secret, such as an API key, from the same text, use `RedactSingleLineCapped`. It replaces each secret with `[redacted]`, matching it in the text and in its sanitized form, then caps:

```go
reason, cut := textsafe.RedactSingleLineCapped(body, 200, "...", apiKey)
```

[Bounding sanitized text](docs/capping.md#redacting-a-known-secret) explains why each step is needed.

## Unsupported by design

textsafe does no HTML or XSS sanitizing, Unicode normalization or case folding. It leaves zero-width and look-alike runes alone, and its sanitizers always replace with a space and never remove a rune. [Unsupported by design](docs/non-goals.md) gives the reason for each and what to use instead.

## Documentation

- [The rune policy](docs/policy.md) lists every rune textsafe replaces, and shows how to build a custom policy from the predicates.
- [Bounding sanitized text](docs/capping.md) covers the byte caps, the marker and truncation rules, the work budget and the redaction order.
- [The Untrusted type](docs/untrusted.md) explains what the type does at each sink and the two rules for using it.
- [Unsupported by design](docs/non-goals.md) lists the features left out on purpose, with the reasons.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
