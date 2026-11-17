# httpwire

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/httpwire.svg)](https://pkg.go.dev/github.com/cplieger/go/httpwire) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=httpwire%2Fgo.mod)](https://github.com/cplieger/go/blob/main/httpwire/go.mod)

httpwire declares your Go app's JSON API once, as a table of rows, and binds typed handlers to it. The TypeScript half, `@cplieger/httpwire` in [`web/`](web), decodes the same wire in the browser.

It uses the standard library plus this repository's `httpserver` and `textsafe`, and is licensed under Apache-2.0.

## Install

```sh
go get github.com/cplieger/go/httpwire@latest
```
