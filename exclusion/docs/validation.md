# Validation

This page lists every field error `Validate` and `Compile` answer, with its path, code and message, and the bounds of a rule list, for a developer wiring the rules into a settings section or reading the errors on the page.

## One validator for the save and the draft

`Validate(path, rules, fields)` answers the field errors of a list, each under `path`. A settings section that holds the rules under `rules` calls `Validate("rules", rules, fields)` and answers the entries unchanged in its own refusal. Its settings row's `MaxBody` is at least `exclusion.MaxBody`, so a list at every bound reaches the validator. The matches row calls `Compile` under the same path, so a draft list and a settings save of the same list answer equal entries, and the page shows a bad pattern beside its condition either way.

`Compile` answers the same entries as `Validate`, or a `Set` when there are none.

## The field errors

The codes are httpwire's field codes. A path names the rule and condition by index.

| Case | Path | Code | Message |
| --- | --- | --- | --- |
| A list of more than 100 rules | `rules` | `range` | `at most 100 rules` |
| A rule with no condition | `rules[i].conditions` | `range` | `at least 1 condition` |
| A rule of more than 8 conditions | `rules[i].conditions` | `range` | `at most 8 conditions` |
| A field your app does not declare | `rules[i].conditions[j].field` | `unknown` | `not a field of these rules` |
| An empty value | `rules[i].conditions[j].value` | `range` | `required` |
| A `Text`, `Bool` or `Choice` value over 200 bytes | `rules[i].conditions[j].value` | `range` | `at most 200 bytes` |
| A pattern over 200 characters | `rules[i].conditions[j].value` | `range` | `at most 200 characters` |
| A `Bool` value other than `true` or `false` | `rules[i].conditions[j].value` | `enum` | `true or false` |
| A `Choice` value not among the field's choices | `rules[i].conditions[j].value` | `enum` | `one of`, then the choices separated by commas |
| A pattern Go's `regexp` refuses | `rules[i].conditions[j].value` | `pattern` | the parser's reason and the pattern, such as ``missing closing ): `(wip` `` |

A list over 100 rules answers its one entry, and its rules are not checked. A rule over 8 conditions answers its one entry, and its conditions are not checked. Every other case answers one entry per refused condition, in list order.

A `Choice` value and a `Bool` value are exact: `Issue` is not `issue`, and `True` is not `true`. A pattern's length is counted in characters, and every other value's in bytes.

A refused pattern's message passes through textsafe: it is cut to one line of at most 256 bytes, ending in `...` when cut. The [security model](security-model.md) page says why.

## Checks the library does not make

`Validate` checks only what any app's rules share. A check that knows your domain, such as a repository written as `owner/name`, or one that reads two conditions together, such as `draft` beside `kind: issue`, belongs to your settings validator, which appends its own entries to the ones `Validate` answers.

A rule repeated in the list is accepted: each copy counts the same items, and leaving out an item twice changes nothing.
