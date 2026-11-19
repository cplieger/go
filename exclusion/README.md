# exclusion

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/exclusion.svg)](https://pkg.go.dev/github.com/cplieger/go/exclusion) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=exclusion%2Fgo.mod)](https://github.com/cplieger/go/blob/main/exclusion/go.mod)

exclusion lets the person running your Go app write rules that leave matching items out of every screen and count, and shows how many items each rule matches while they edit it.

It replaces the hand-written rule list, validator and matcher each app would otherwise keep. It depends on this repository's [httpwire](https://github.com/cplieger/go/tree/main/httpwire), [httpserver](https://github.com/cplieger/go/tree/main/httpserver) and [textsafe](https://github.com/cplieger/go/tree/main/textsafe), and is licensed under Apache-2.0.

## Why use it

exclusion is built for an app whose settings hold a list of exclusion rules over its own items, edited on a page that counts each rule's matches live.

- A rule is conditions ANDed, and rules are ORed, so each rule names one kind of item to leave out.
- One validator answers the settings save and the draft count with the same field errors, at the same paths.
- Patterns are Go's RE2, which runs in time linear in its input, so no pattern can stall a count.

## Install

```sh
go get github.com/cplieger/go/exclusion@latest
```

## Usage

Declare the fields your items have, answer each item's values through `Item`, and compile the rule list your settings hold:

```go
fields := []exclusion.Field{
	{Name: "kind", Kind: exclusion.Choice, Choices: []string{"pull_request", "issue"}},
	{Name: "author", Kind: exclusion.Text},
	{Name: "title", Kind: exclusion.Pattern},
	{Name: "draft", Kind: exclusion.Bool},
}
set, errs := exclusion.Compile("rules", settings.Rules, fields)
if len(errs) != 0 {
	return errs // the same entries a settings save answers
}
if _, left := set.Match(item); left {
	// leave item out of every list and count
}
```

`Example` and `ExampleValidate` in the package examples run the whole case, and `go test` keeps them true.

### Validating a settings save

The settings section that holds the rules calls `Validate("rules", rules, fields)` and answers its entries unchanged. Each entry carries the path of the condition it refuses, such as `rules[3].conditions[0].value`, and one of the codes `unknown`, `enum`, `pattern` or `range`. Set the `MaxBody` of the settings row that saves this section to at least `exclusion.MaxBody`, 2 MiB, or that row refuses a list at every bound as too large before `Validate` sees it.

### Counting a draft list on the page

Mount the matches row in your API table, and bind it with the count over your store:

```go
table.Endpoints = append(table.Endpoints, exclusion.Endpoint("exclusion.matches", settingsRead))
bindings = append(bindings, exclusion.Binding("exclusion.matches", fields, countOverStore))
if err := catalog.AddOwned(exclusion.CodeSpecs()); err != nil {
	return err
}
```

The row is `POST /exclusion/matches` under the table's prefix. Its body is `{"rules": [...]}`, a draft list or the saved one, and it answers `{"counts": [...]}`, one count per rule in order. A list `Validate` refuses answers `exclusion_invalid` (422) with the same entries a settings save would answer. The [matches row](docs/matches-row.md) page covers the count, its errors and the body cap.

## API

- The rule list: `Rule`, `Condition`, `Field` and its `Kind` (`Text`, `Pattern`, `Bool`, `Choice`), with the bounds `MaxRules`, `MaxConditions`, `MaxValueBytes` and `MaxPatternRunes`.
- Checking and matching: `Validate`, `Compile`, `Set`, `Set.Match`, `Set.Count` and the `Item` an app answers.
- The matches row: `Endpoint`, `Binding`, `CountFunc`, `MatchesRequest`, `MatchesResponse`, `CodeSpecs` and the body cap `MaxBody`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/exclusion).

## Bounds

A list holds at most 100 rules of at most 8 conditions each. A value is 1 to 200 bytes, and a pattern 1 to 200 characters. A value past its bound, a rule with no condition and a list past its bound each answer `range`. A request carrying a list at every bound is about 1 MiB once JSON-escaped, so the matches row takes a body of up to `MaxBody`, 2 MiB. The [validation](docs/validation.md) page lists every code and message.

## Unsupported by design

exclusion leaves these out on purpose. The [non-goals](docs/non-goals.md) page gives the reason for each.

- Cleaning item values. Your `Item` answers values already trimmed, lowercased or normalized as your source needs.
- Checks that read your domain or two fields together, such as a repository path's shape. Your settings validator runs them beside `Validate`.
- Keep lists. A rule only leaves items out.
- Storing which items are left out. Your store marks them and your `CountFunc` counts over it.
- The rules page. It is `@cplieger/ui`'s `.cp-rules`.

## Documentation

- [Rules and matching](docs/rules.md) covers the rule shape, the four kinds of field, how an item's values are compared and how a count is made.
- [Validation](docs/validation.md) covers the field codes, their paths and messages, and the bounds.
- [The matches row](docs/matches-row.md) covers the row, its binding, the count over your store and its errors.
- [Security model](docs/security-model.md) covers why no pattern can stall a count and what reaches the page from a refused pattern.
- [Non-goals](docs/non-goals.md) lists what the library leaves out and why.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
