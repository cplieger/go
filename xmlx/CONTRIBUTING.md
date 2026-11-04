# Contributing to xmlx

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

A new `Limits` field also needs a draw in `limitsGen` and in the `loose` literal of `prop_test.go`. Left out of `limitsGen`, every drawn `Limits` fails validation, so `TestPropPreflightMonotoneInLimits` passes without checking anything.

## Releases

A new `Limits` field whose zero value is rejected is a breaking change and takes `feat!:`. A caller that builds `Limits` with named fields still compiles, then gets `ErrInvalidLimits` from `Preflight` for the field it never set.
