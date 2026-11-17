# webbundle

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/webbundle.svg)](https://pkg.go.dev/github.com/cplieger/go/webbundle) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=webbundle%2Fgo.mod)](https://github.com/cplieger/go/blob/main/webbundle/go.mod)

webbundle builds your single-page app's web bundle at build time with esbuild: content-hashed scripts and stylesheets, shells that name them, licence notices and the manifest this repository's `httpserver.SPA` serves.

It replaces the bundle command each app writes around esbuild's Go API. It runs from your Dockerfile's builder stage and from `go generate`, never in your app binary, and is licensed under Apache-2.0.

## Install

```sh
go get github.com/cplieger/go/webbundle@latest
```

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
