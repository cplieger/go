# Contributing to jsonx

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Releases

A new `Policy` field is a breaking change, so commit it as `feat!:`. A policy a caller writes as a struct literal leaves the field at `Reject`, its zero value, and starts rejecting every value that field covers.
