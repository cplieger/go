# Contributing to urlform

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- Adding or removing a case in `testdata/whatwg-fixtures.json` means updating its case counts in `README.md`, `docs/contract.md` and `THIRD_PARTY_NOTICES.md`. Moving to a newer web-platform-tests commit means updating the commit `THIRD_PARTY_NOTICES.md` names. No test reads those files, so a stale value passes CI.
