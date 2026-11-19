# exclusion

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/exclusion.svg)](https://pkg.go.dev/github.com/cplieger/go/exclusion) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=exclusion%2Fgo.mod)](https://github.com/cplieger/go/blob/main/exclusion/go.mod)

exclusion lets the person running your Go app write rules that leave matching items out of every screen and count, and shows how many items each rule matches while they edit it.

It replaces the hand-written rule list, validator and matcher each app would otherwise keep. It is licensed under Apache-2.0.

## Why use it

exclusion is built for an app whose settings hold a list of exclusion rules over its own items, edited on a page that counts each rule's matches live.

- A rule is conditions ANDed, and rules are ORed, so each rule names one kind of item to leave out.
- One validator answers the settings save and the draft count with the same field errors, at the same paths.
- Patterns are Go's RE2, which runs in time linear in its input, so no pattern can stall a count.

## Install

```sh
go get github.com/cplieger/go/exclusion@latest
```

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
