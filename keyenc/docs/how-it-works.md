# How keyenc encodes a key

This page gives keyenc's full key format and the reasons behind each limit. It is for anyone reimplementing keyenc, reading a key by hand or deciding whether a site needs it.

## Why a fixed separator is not enough

A key built as `a + ":" + b` is correct only while no field can contain `:`. Whether that holds depends on the content of every field except the last, which is not visible where the key is built. Adding a field, reordering two, or widening one field's source to free-form upstream text can each make two field lists share a key. None of those edits looks like it touches key building.

When two field lists share a key, nothing reports an error. A cache returns the wrong entry, a dedupe token drops a distinct event as already seen, and a map treats two identities as one. A NUL byte as the separator makes a clash less likely without ruling it out. The key can then no longer go in a URL or be read in a log.

## The grammar

Two characters are reserved: `:` separates fields and `\` escapes. Inside each field, `Join` writes `\` as `\\` first and then `:` as `\:`, and it joins the escaped fields with `:`. The separator between fields can therefore always be told from a separator inside one.

```go
keyenc.Join("a:b", "c")      // a\:b:c
keyenc.Join("a", "b:c")      // a:b\:c
keyenc.Join("a", "b", "c")   // a:b:c
```

A field with neither character is written unchanged, so a key made of such fields equals its plain concatenation.

Two cases do not follow the plain escaped join:

- No fields at all gives the empty string, and `Split("")` returns no fields.
- A single empty field gives a digest. Without that rule, `Join()` and `Join("")` would both give the empty string.

## Reading a key back

For a field list under the size limit, `Split(Join(fields...))` returns `fields`, except for the single empty field above. For a key under the limit that `Split` accepts, `Join(Split(key)...)` returns `key`. `Split` does not repair a key it cannot read:

- It returns `ErrMalformed` when an escape comes before a character other than `:` or `\`, or when the key ends in a lone `\`. `Join` never writes either.
- It returns `ErrHashed` for a digest, `sha256:` followed by 64 lowercase hex characters, because a digest holds no fields. Any other key that starts with `sha256:` returns `ErrMalformed`, because `Join` never writes one.

In TypeScript, `split` throws `MalformedKeyError` and `HashedKeyError` for the same inputs.

## Nesting

A key that holds a list nests by composition. Join the inner list first, then pass the result as one field of the outer key:

```go
languages := keyenc.Join("en", "fr")
key := keyenc.Join("episode", "tvdb-1-s01e02", languages) // episode:tvdb-1-s01e02:en\:fr
```

The outer `Join` escapes the inner result again, so its separators cannot be read as outer ones at any depth. A second reserved character would give the same result for exactly two levels and fail at three. Every level of a nested key must use `Join`, because one plain concatenation at any level lets fields collide at that level.

## Oversized keys

Escaping grows a key with its input. A caller that folds unbounded upstream data into a key can then be made to allocate without limit, the weakness [CWE-400](https://cwe.mitre.org/data/definitions/400.html) describes. When the fields add up to more than `MaxComponentBytes`, 8192 bytes, `Join` returns a digest instead:

- The digest is `sha256:` followed by 64 lowercase hex digits, 71 characters in all.
- It hashes each field as an 8-byte big-endian length followed by the field's UTF-8 bytes. The length keeps field boundaries, so `["a", "b"]` and `["ab"]` give different digests.
- The limit is measured on the raw fields, not on the escaped output. A field of 8192 separators escapes to twice the limit and still encodes as an escaped join. The shape of a key therefore does not depend on how many escapes it needed.
- An in-bound field list whose escaped join would itself start with `sha256:` is hashed too. Ordinary keys and digests therefore never overlap, and `IsHashed` never mistakes one for the other.

A digest is one-way. A site that builds oversized keys and also parses them back has to keep the fields it needs somewhere else.

## Go and TypeScript agree

The two halves give identical keys for identical valid UTF-8 input. `conformance/keys.json` is generated from the Go half and asserted by the TypeScript tests. It holds escaped and hashed cases on both sides of the limit. Two details carry that agreement and are the likely cause of any difference in a reimplementation:

- The size limit counts UTF-8 bytes, not UTF-16 code units. JavaScript's `String.length` crosses the limit at a different point for multibyte text. The fixture has a case that is under the limit in code units and over it in bytes.
- The digest uses the 8-byte big-endian length prefix per field described above. The fixture has a hashed case with two fields.

The TypeScript half computes SHA-256 itself, because `crypto.subtle.digest` is asynchronous and `join` returns its key synchronously.

## Non-goals

- No configurable separator. Two implementations that must agree byte for byte cannot each carry a setting, and a key written with one separator cannot be read with another. Nesting covers what a second separator would.
- No key derivation, message authentication or proof of origin. The digest bounds a key's size and nothing more. It does not bind a key to a secret, it is not computed in constant time, and it is no substitute for an HMAC. A key tells you two field lists differ, and nothing about who produced them.
- No cleanup of fields. `Join` encodes what it is given. Case folding, Unicode normalization and trimming are the caller's decisions. Doing them here would add a second "normalize" step that both languages would have to keep identical.
- Arbitrary bytes are Go-only. A Go string is a byte sequence and a JavaScript string is a UTF-16 sequence. The two halves agree for all valid UTF-8, and the shared fixture covers only that. Go's own fuzz targets cover invalid UTF-8 for the Go half.
