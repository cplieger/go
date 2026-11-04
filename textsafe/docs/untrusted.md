# The Untrusted type

This page explains the `Untrusted` type, for a developer deciding whether to tag decoded fields instead of sanitizing at each log call.

Sanitizing at each call site repeats one decision, that this text is untrusted, everywhere the text is emitted, and a forgotten call leaves no trace. `Untrusted` records the decision once, in the struct that decodes the upstream payload, where the decision is known.

```go
type Episode struct {
    Title textsafe.Untrusted `json:"title"`
}
```

Tag text that does not come from a decoder with `textsafe.NewUntrusted(s)`. The zero value holds the empty string.

## Decoding keeps the string value

`UnmarshalText` stores the decoded text unchanged, and textsafe's sanitizer does not run. A tagged JSON field or map key, and a tagged XML element, attribute or character-data field, decodes exactly as a plain `string` would: the same bytes, a JSON `null` handled the same way, and a non-string value or malformed document rejected the same way. `Raw` returns that decoded string.

A few modes key off a string type rather than `UnmarshalText`, so they do not work on `Untrusted`. An XML `,innerxml` or `,comment` field decodes to empty with no error, and a `,comment` field fails to marshal. A JSON `,string` option keeps the outer quotes. An embedded `Untrusted` promotes its methods, so the outer struct marshals as that one text value and rejects a JSON object on decode. Use a plain `string` field for those modes, and give an `Untrusted` field a name rather than embedding it.

`Untrusted` is a struct, so `encoding/json`'s `omitempty` never omits it. Use `omitzero` to leave out an empty value. Code that handles the value by reflection rather than through its methods also sees a struct: `encoding/gob` cannot encode it, `database/sql` rejects it as an argument or `Scan` destination, and a template's `if` and `eq` do not treat it as a string. Pass `Raw` to those instead.

## Every standard sink emits it sanitized

- slog: `LogValue` implements `slog.LogValuer`, so a tagged value resolves to its `Sanitize` form in every handler, inside groups too.
- `fmt` and errors: `Format` implements `fmt.Formatter` and `String` implements `fmt.Stringer`, so every verb `fmt` hands to them, including those in `fmt.Errorf("upstream said %s", v)`, renders sanitized text with the directive's flags, width and precision. The exception is `%#v`, which prints Go syntax that rebuilds the raw value, with every non-ASCII rune escaped, and ignores width and precision. `fmt` calls no method for `%T`, which prints the type name, or for `%p`, which it formats by reflection as described below. An error built this way carries no escape introducers from the moment it is created.
- Encoders: `MarshalText` implements `encoding.TextMarshaler`, so `encoding/json`, `encoding/json/v2`, `encoding/xml` and any other encoder that honours the interface emit the sanitized form at any nesting depth. This includes a map key, whether the map is keyed by `Untrusted`, `*Untrusted` or an interface holding one.

All three use `Sanitize`, which keeps CR and LF. That is correct for JSON, and slog's `TextHandler` quotes a value that holds them. For an error bound for a hand-built single-line sink that escapes nothing, build the message from `SingleLine` instead.

textsafe ships no slog handler or `ReplaceAttr` hook. slog handlers turn an error into text inside the encoder, after any attribute rewriting. So an error is covered only when it was built from sanitized text, and the type does that for you.

Some printers call no method at all: `fmt` formats an unexported struct field, and any `%p` operand, by reflection. `Untrusted` stores the `Sanitize` form and, only when that differs from the raw bytes, an ASCII-escaped copy of them, so such a printer finds no raw unsafe rune either.

`NewUntrusted` and decoding run `Sanitize` once, so a tagged field costs one scan per decode and allocates only for text with something to replace. After that, `LogValue`, `String` and `fmt`'s `%v` and `%s` allocate nothing for any text, and `SingleLine` and `Raw` allocate nothing for clean text. `MarshalText` copies once, because it returns a `[]byte`.

## Raw for matching

Compute paths read the unsanitized value through `Raw`:

```go
slog.Warn("better release available", "title", ep.Title) // sanitized automatically
if ep.Title.Raw() == stored.Title { /* matching stays raw */ }
```

Use `Raw` for matching, dedupe keys and context-aware escapers. It is the only way to read the raw bytes, so every intentional unwrapping is easy to search for.

Matching on `Raw` compares bytes, so two values that differ only in an unsafe rune stay different. A cap that bounds the emitted form belongs on the sanitized text, `CapBytes(v.String(), n)`, because sanitizing can grow the raw bytes.

## Untrusted text as a map key

`Untrusted` is comparable, so it can key a map directly, and every `encoding.TextMarshaler`-aware encoder writes the key sanitized:

```go
counts := map[textsafe.Untrusted]int{}
counts[ep.Title]++

out, _ := json.Marshal(counts) // every key sanitized
```

`Untrusted` is a struct rather than a string type for this reason. `encoding/json` writes a map key of string kind as it is, without calling `MarshalText`, and Go 1.27.2 does the same for a pointer or interface key that holds one ([golang/go#81355](https://go.dev/issue/81355)). A struct key always goes through `MarshalText`.

The map compares keys by their raw bytes, like `Raw`. Two keys that differ only in an unsafe rune therefore stay separate entries but marshal to the same name: `encoding/json` writes that name twice, and `encoding/json/v2` returns a duplicate-name error. Decoding a JSON object into a `map[Untrusted]V` keeps each key's raw bytes.

## Two rules for using it

- Store `Raw` in plain `string` fields in any struct your program saves and reads back. `MarshalText` runs inside every `json.Marshal`, so a tagged field in a state file would come back sanitized, not raw.
- Keep sanitizing at construction for text that must be safe in every future sink, such as a captured error body inside a returned value. A Markdown cell, a URL or an HTML page still needs its own escaping, built on `Raw`.
