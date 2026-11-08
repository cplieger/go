# keyenc

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/keyenc.svg)](https://pkg.go.dev/github.com/cplieger/go/keyenc) [![npm](https://img.shields.io/npm/v/@cplieger/keyenc)](https://www.npmjs.com/package/@cplieger/keyenc) [![JSR](https://jsr.io/badges/@cplieger/keyenc)](https://jsr.io/@cplieger/keyenc)

keyenc joins untrusted strings into one cache, dedupe or map key with no separator collisions, and builds the same key byte for byte in Go and TypeScript.

It replaces keys like `kind + ":" + host`, where `"a:b"` and `"c"` give the same key as `"a"` and `"b:c"`. The Go module uses only the standard library and needs Go 1.27.3 or later. The npm and JSR package has no runtime dependencies and ships its TypeScript source as an ES module, which your own build compiles. Both are licensed under Apache-2.0.

## Why use it

keyenc is built for keys made from fields you do not control, such as host names, paths or upstream text. A separator no field contains today can stop being safe once someone adds a field or widens its source.

- `:` and `\` are escaped inside each field, so `Join("a:b", "c")` and `Join("a", "b:c")` differ.
- A field with neither character is copied unchanged, so today's safe keys keep their bytes, apart from a key of one empty field.
- `Split` gives back the exact fields `Join` encoded and refuses an escape `Join` never writes.
- Above 8 KiB of fields, `Join` returns a 71-character SHA-256 digest, so untrusted input cannot grow a key without limit.

Consider [`encoding/json`](https://pkg.go.dev/encoding/json) if any JSON parser must read your keys back, since it writes RFC 7159 JSON. Consider [`crypto/hmac`](https://pkg.go.dev/crypto/hmac) if a key must also prove who made it, which an HMAC does with a secret key.

## Install

```sh
go get github.com/cplieger/go/keyenc@latest
npx jsr add @cplieger/keyenc  # or: npm install @cplieger/keyenc
```

## Usage

### Building a key

```go
key := keyenc.Join("gitea", "git.example.com:3000") // gitea:git.example.com\:3000
```

```ts
import { join } from "@cplieger/keyenc";

const key = join("streams", userID, ratingKey, audioID, subID);
```

### Reading one back

Use `Split` where a key is parsed again, such as a URL path segment or a value read from a file. `Split` recovers every ordinary key `Join` writes and rejects an invalid escape. It also parses a hand-built key over the 8 KiB limit, which `Join` would have hashed.

```go
parts, err := keyenc.Split(key)
if err != nil {
    return fmt.Errorf("unrecognized key %q: %w", key, err)
}
kind, host := parts[0], parts[1]
```

### Nesting

To put a list inside a key, join the inner list first and pass the result as one field. The outer `Join` escapes it again, so inner separators never read as outer ones at any depth.

```go
languages := keyenc.Join("en", "fr")
key := keyenc.Join(mediaType, mediaID, languages, videoPath)
```

### Oversized keys

When the fields add up to more than `MaxComponentBytes` (8 KiB), `Join` returns `sha256:` and 64 hex digits instead. Field boundaries still count, so `["a", "b"]` and `["ab"]` give different digests. An ordinary key never starts with `sha256:`, because `Join` hashes any field list whose key would. `IsHashed` therefore tells the two apart. A digest is one-way, so check it before `Split`:

```go
if keyenc.IsHashed(key) {
    return errNotRecoverable
}
```

## API

- `Join`, `Split` and `IsHashed` build a key, read it back and tell a digest from an ordinary key.
- `MaxComponentBytes` is the total input size, in bytes, above which `Join` returns a digest.
- `Separator` and `Escape` are the two reserved characters, `:` and `\`.
- `ErrHashed` and `ErrMalformed` say why `Split` refused a key.

The TypeScript package exports the same surface as `join`, `split`, `isHashed`, `MAX_COMPONENT_BYTES`, `SEPARATOR`, `ESCAPE`, `HashedKeyError` and `MalformedKeyError`, and its `split` throws those errors. The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/keyenc) and [JSR](https://jsr.io/@cplieger/keyenc/doc).

## Existing safe keys keep their bytes

A field with neither `:` nor `\` is copied unchanged, so a key whose fields are already separator-free is byte-identical to its plain concatenation:

```go
keyenc.Join("streams", "u-42", "1234", "3", "5") // streams:u-42:1234:3:5
```

The bytes change only where the plain form was already ambiguous. A key of one empty field is the one case. `Join` hashes it, because its plain form is the same empty string as a key of no fields. Adopting keyenc at a key that is safe today needs no re-key, no cache flush and no migration of a stored key. At a key that collides today, keyenc gives the colliding field lists different encodings.

## Go and TypeScript build the same keys

Both halves give identical keys for identical valid UTF-8 input, so a key built in one language can be looked up among keys built in the other. A shared fixture, `conformance/keys.json`, is generated from the Go half and checked by the TypeScript tests. Both count the size limit in UTF-8 bytes, so a key with multibyte text switches to a digest at the same point in each language. The TypeScript half computes SHA-256 in its own code, with no Web Crypto or `node:crypto`, so `join` stays synchronous. [How keyenc encodes a key](docs/how-it-works.md#go-and-typescript-agree) lists the details a reimplementation must match.

## Unsupported by design

- No configurable separator. Two implementations that must agree byte for byte cannot each carry a setting, and a key written with one separator cannot be read with another. Nesting covers what a second separator would.
- No key derivation, message authentication or proof of origin. The digest only bounds a key's size, and it is no substitute for an HMAC. A key tells you two field lists differ, and nothing about who built them.
- No cleanup of fields. `Join` encodes what it is given. Case folding, Unicode normalization and trimming are the caller's choice.
- Arbitrary bytes are Go-only. A Go string is a byte sequence and a JavaScript string is UTF-16, so the two halves agree for valid UTF-8, which is all the shared fixture covers. Go's fuzz tests cover invalid UTF-8 in Go.

The reasons for each are in [How keyenc encodes a key](docs/how-it-works.md#non-goals).

## Documentation

- [How keyenc encodes a key](docs/how-it-works.md) gives the full format and the reasons behind each limit, for anyone reimplementing it or reading a key by hand.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
