# masked

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/masked.svg)](https://pkg.go.dev/github.com/cplieger/go/masked) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=masked%2Fgo.mod)](https://github.com/cplieger/go/blob/main/masked/go.mod)

masked gives your Go app a `Secret` type for passwords, tokens and API keys that no log line, error message, JSON body or debug dump can show.

It replaces the `string` field you would otherwise have to remember never to print. It uses only the standard library and is licensed under Apache-2.0.

## Why use it

masked is built for Go services that hold credentials in memory and log, encode or dump the structs that carry them.

- Every `fmt` verb, `%#v`, `%x` and `%p` included, `log/slog` in text and JSON, and `encoding/json` write `[redacted]`, or nothing for the zero value.
- The text is never held in clear: a `Secret` holds an HMAC-SHA256 tag and the text encrypted with AES-256-CTR under keys drawn at process start, so a printer that walks fields by reflection (go-cmp, go-spew, `%v` of a holder) finds only masked bytes.
- `==` on a `Secret` does not compile. `Equal` compares two 32-byte tags in constant time, and `Key()` returns the tag as a fixed-size map key, so a lookup by secret never handles the text.
- A `Secret` has no JSON decoder: it is not a wire or storage type, so a request body cannot fill one by accident. Wrap the decoded string with `NewSecret` where it enters.
- The `maskedtest` package checks your own types and printers in tests: `Unmasked` lists every field of your configuration and client types that can carry text in clear, `SecretNamed` lists every field of your wire types that is named like a credential and holds text, and `Leak` finds a secret's text, its hex and its base64 in any output.

Consider a plain `string` with a custom `String` method if you only print through `fmt` and never log, encode or dump the struct that holds it, because masked adds a dependency and an encryption per value.

## Install

```sh
go get github.com/cplieger/go/masked@latest
```

## Usage

Wrap the secret where it enters, and reveal it only where it is sent:

```go
type Config struct {
    APIToken masked.Secret
}

cfg := Config{APIToken: masked.NewSecret(os.Getenv("API_TOKEN"))}
slog.Info("config loaded", "config", cfg) // config={APIToken:[redacted]}

req.Header.Set("Authorization", "Bearer "+cfg.APIToken.Reveal())
```

Key an in-memory map by `Key()`. The key is the per-process tag: equal texts give equal keys within one process, and it changes at every start, so never store or send it. A durable index keys on `Reveal()` instead.

```go
sessions := map[[32]byte]Session{}
sessions[token.Key()] = s
```

In your tests, check that no field of the types that hold your secrets carries one in clear and that a printer's output shows none:

```go
func TestNoPlainSecrets(t *testing.T) {
    allow := maskedtest.Allow{Types: []reflect.Type{reflect.TypeFor[time.Time]()}}
    if found := maskedtest.Unmasked(allow, reflect.TypeFor[Config]()); len(found) > 0 {
        t.Errorf("fields that can carry a secret in clear:\n%s", strings.Join(found, "\n"))
    }
    cfg := Config{APIToken: masked.NewSecret("the-test-token")}
    out := fmt.Sprintf("%+v", cfg)
    if maskedtest.Leak(out, "the-test-token") {
        t.Errorf("printed the token: %s", out)
    }
}
```

Run `Unmasked` over the types that hold secrets, never over wire types: every title and path on the wire is text, so it would report them all. Check wire types by name instead. `SecretNamed` reports every field whose JSON name contains `key`, `token`, `secret`, `password` or `passkey` and that can hold text, unless you allow it with a reason. Exclude your string enum types so a `token_state` enum field passes:

```go
func TestNoCredentialOnTheWire(t *testing.T) {
    allow := maskedtest.NameAllow{
        Fields: map[string]string{"api.Release.ReleaseKey": "a release's identity, not a credential"},
        Enums:  []reflect.Type{reflect.TypeFor[api.TokenState]()},
    }
    if found := maskedtest.SecretNamed(allow, reflect.TypeFor[api.Release](), reflect.TypeFor[api.Session]()); len(found) > 0 {
        t.Errorf("credential-named wire fields:\n%s", strings.Join(found, "\n"))
    }
}
```

The package examples on pkg.go.dev show the common cases, and `go test` keeps them true.

## API

- `NewSecret` wraps a text, `Reveal` returns it, and `IsZero` reports the zero value, which `NewSecret("")` also gives.
- `Equal` compares two secrets in constant time, and `Key` returns the 32-byte per-process tag for an in-memory map key, 32 zero bytes for the zero value.
- `Format`, `LogValue` and `MarshalJSON` write `[redacted]`, or nothing for the zero value. There is no decoder.
- `maskedtest.Unmasked` walks a type graph and lists every field that can carry text and is not a masked type or an entry of the `maskedtest.Allow` you pass, and reports an allow entry no root reaches, so none goes stale.
- `maskedtest.SecretNamed` walks wire types as `encoding/json` does and lists every field whose JSON name contains `key`, `token`, `secret`, `password` or `passkey` and whose type can hold text (a string that is not one of your enums, or a `[]byte`), unless the `maskedtest.NameAllow` you pass allows it with a reason. It reports an entry with no reason and an entry no root reaches.
- `maskedtest.Leak` reports whether an output contains a text in any form `maskedtest.KeyForms` returns: the text, its hex and base64, and its HMAC pad forms, which a printed `crypto/hmac` value can show.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/masked).

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
