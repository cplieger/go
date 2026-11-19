# Rules and matching

This page covers the shape of a rule list, the four kinds of field and how a compiled list matches and counts items, for a developer declaring an app's fields and answering its items' values.

## A rule list

A rule list is a JSON array your settings document holds:

```json
[
  {"conditions": [{"field": "author", "value": "renovate"}, {"field": "title", "value": "^chore\\(deps\\)"}]},
  {"conditions": [{"field": "kind", "value": "pull_request"}, {"field": "draft", "value": "true"}]}
]
```

A rule's conditions are ANDed: a rule matches an item only when every one of its conditions holds. The rules are ORed: an item is left out when any one rule matches it. So each rule names one kind of item to leave out, and removing a rule brings back only the items no other rule matches.

## Fields

Your app declares the fields a condition can name, each with a wire name and a kind:

| Kind | A condition holds when one of the item's values | Example |
| --- | --- | --- |
| `Text` | equals the condition's value under Unicode simple case folding (`strings.EqualFold`), the whole value and never a part of it | an author, a repository, a label |
| `Pattern` | contains a match of the condition's value, an RE2 pattern in Go's `regexp` syntax | a title |
| `Bool` | equals the condition's value, `true` or `false` | a draft state |
| `Choice` | equals the condition's value, which is one of the field's `Choices` | an item kind, a connection |

A `Pattern` is unanchored and case-sensitive, so `deps` matches `chore(deps): update go`. Write `^` and `$` to anchor it, and `(?i)` to ignore case. A `Text` value is compared as given: `" renovate "` with its spaces does not match `"renovate"`.

A field declared twice keeps its first declaration. A field whose `Kind` is not one of the four is treated as not declared, so a condition naming it answers `unknown`.

## Item values

`Item` answers an item's values for one field. A field with several values, such as labels, answers every one, and a condition holds when any value meets it. A field the item has no value for answers none, and then no condition on that field holds: a `draft` condition never matches an issue, which has no draft state.

Values reach `Item` already canonical. Trim, lowercase or normalize them as your source needs before you answer them, so that matching stays one comparison. For example, a forge app answers a bot's login both with and without its `[bot]` suffix, and a media app answers language tags in their normalized form.

## Matching and counting

`Compile` checks a list as `Validate` does and answers a `Set`, or nil and the field errors. A `Set` is safe for concurrent use, and the zero `Set` and a nil `*Set` hold no rule.

- `Set.Match(item)` answers the index of the first rule the item matches and `true`, or `-1` and `false`. An empty set matches nothing.
- `Set.Count(items)` answers one count per rule, in rule order. An item that two rules match counts in both, so the counts can add up to more than the number of items. A set with no rule answers an empty list, never nil.

Recompile the set when the rules change, for example in your settings section's change hook, and swap it in for the next read.
