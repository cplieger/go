# Unsupported by design

This page lists what textsafe leaves out on purpose, for a developer wondering whether a missing feature is coming. Each section gives the reason and what to use instead.

## HTML and XSS sanitizing

An HTML page is a different sink with a different threat model. textsafe's policy is for logs, JSON and rendered reports. Use an HTML sanitizer such as [bluemonday](https://github.com/microcosm-cc/bluemonday), which sanitizes HTML against an allowlist of elements and attributes.

## Unicode normalization

Normalization, such as NFC or NFKC, changes the identity of text, and a display-safety policy must not. Normalize separately if your app needs it, for example with [`golang.org/x/text/unicode/norm`](https://pkg.go.dev/golang.org/x/text/unicode/norm), which provides the NFC, NFD, NFKC and NFKD forms.

## Case folding and case mapping

Folding does not preserve identity: U+212A, the Kelvin sign, folds to ASCII `K`. Its answers also move with the toolchain's Unicode tables. Go 1.27 folds U+0390 and U+1FD3 together, where Go 1.26 kept them apart. A display-safety policy must not decide what two strings mean. Match on raw bytes, or canonicalize explicitly with an ASCII-only fold or a `strings.ToLower` result used as the key.

## Zero-width and look-alike runes

Removing invisible characters and homoglyphs damages legitimate text, such as the zero-width joiner inside an emoji or real Cyrillic letters. The policy targets runes with control meaning, where replacing them is always correct.

## A configurable replacement rune

The space is the policy. A different replacement is a short `strings.Map` over the predicate for your sink, shown in [The rune policy](policy.md#a-custom-replacement-policy).

## Removing instead of replacing

Deleting a rune changes rune offsets and can join neighbouring fragments into new tokens. Replacing one rune with one space keeps the rune count and the shape of the text. For the rare sink that needs removal, compose `IsUnsafeSingleLine` or `IsUnsafeMultiLine` with `strings.Map`.
