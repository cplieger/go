# Contributing to httpcache

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- New validator, key or header logic also gets a fuzz target or a property test. It reads headers from untrusted servers, and table tests cover only the inputs you thought of.
- A held answer is served only on the server's `304` in the same exchange. A change that serves an answer on age, or after any check the server did not make, is out of scope (see [docs/caching.md](docs/caching.md)).
