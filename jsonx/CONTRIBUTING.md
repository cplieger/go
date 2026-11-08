# Contributing to jsonx

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Releases

A new `Policy` field must give, at its zero value, the outcome every policy gives that input today, as the `Policy` godoc promises. A policy a caller writes as a struct literal leaves the new field at its zero value, so a `Disposition` field (zero is `Reject`) fits only a fact every policy already rejects. A fact that policies accept today needs a field type whose zero value accepts, and a fact whose outcome differs by policy today needs one whose zero value defers to the gate that decides that input now. A field that changes an outcome at its zero value is a breaking change, committed as `feat!:`.
