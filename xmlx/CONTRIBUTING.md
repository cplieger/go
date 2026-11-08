# Contributing to xmlx

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

A new `Limits` or `BudgetLimits` field takes its default at zero, so `resolve` or `NewBudget` needs its default and its `ConfigError` name. A new `Limits` field also needs a draw in `limitsGen` and in the `loose` literal of `prop_test.go`, from 1 up, since 0 means the default. Left out of `limitsGen`, the field sits at its default in every draw, so `TestPropPreflightMonotoneInLimits` never varies it.

## Releases

A new bound is not a breaking change when its default is loose enough for every document the other defaults accept, because a caller's `Limits` literal leaves it at zero, which means that default. A default that rejects documents callers accept today is a breaking change and takes `feat!:`.
