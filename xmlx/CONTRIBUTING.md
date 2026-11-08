# Contributing to xmlx

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

A new `Limits` or `BudgetLimits` field takes its default at zero, so `resolve` or `NewBudget` needs its default and its `ConfigError` name. A new `Limits` field also needs a draw in `limitsGen` and in the `loose` literal of `prop_test.go`, from 1 up, since 0 means the default. Left out of `limitsGen`, the field sits at its default in every draw, so `TestPropPreflightMonotoneInLimits` never varies it.

## Releases

A caller's `Limits` literal leaves a new bound at zero, so the bound is not a breaking change only when its zero value rejects no document that some existing `Limits` value accepts, including one that raised other bounds. In practice its zero value either resolves from the existing field it overlaps (a per-tag namespace bound whose zero means the caller's `MaxTagAttrs`) or sits above anything the existing fields can admit. Any other default is a breaking change and takes `feat!:`.
