# httpcache

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/httpcache.svg)](https://pkg.go.dev/github.com/cplieger/go/httpcache) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=httpcache%2Fgo.mod)](https://github.com/cplieger/go/blob/main/httpcache/go.mod)

httpcache makes your Go client's repeated GET requests cost less: when the server answers `304 Not Modified`, the answer comes from memory.

It replaces the hand-written `ETag` bookkeeping a polling client keeps beside its requests. It needs Go 1.27.3 or later, depends only on the [httpclient](https://github.com/cplieger/go/tree/main/httpclient) module, and is licensed under Apache-2.0.

## Why use it

httpcache is built for clients that read the same resources again and again, such as a poller of a rate-limited API.

- It never serves an answer on age. Every read reaches the server, and only the server's `304` in the same exchange serves the held copy, so a read never returns an answer the server has changed.
- Its key includes a digest of the request's `Authorization` lines, so requests with different credentials never share an answer, and the credential itself is never held.
- It holds at most 32 MiB by default and evicts the least recently used answer, and one answer may take at most a quarter of the bound.
- A round tripper between the cache and the wire that changes a request or an answer cannot make the cache serve the wrong copy.

Consider [bartventer/httpcache](https://github.com/bartventer/httpcache) if you want answers served on age from `Cache-Control` freshness under RFC 9111, or a disk-backed store.

## Install

```sh
go get github.com/cplieger/go/httpcache@latest
```

## Usage

```go
cache := httpcache.NewRoundTripper(nil, httpcache.Config{})
client := &http.Client{Transport: cache, Timeout: 30 * time.Second}
```

To retry as well, put httpclient's `RetryRoundTripper` beneath the cache, as its `next`. `ExampleNewRoundTripper` on pkg.go.dev is runnable, and `go test` keeps it true.

## API

- On a client: `NewRoundTripper` and the `RoundTripper` it returns, configured by `Config` with its `HeldField` entries, reporting a `State`.
- For code that reads its own answers: `New` returns a `Cache`, whose `Begin` returns one `Read` configured by `ReadConfig`, sent through a `Guard`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/httpcache).

## Documentation

- [Revalidation cache](docs/caching.md) covers what is kept, when a 304 serves it, the key, the bound and the owner-read API.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
