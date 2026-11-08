# Contributing to keylock

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- A change to how `Lock` or `LockAll` waits or releases keeps three promises the tests pin: a wait that ends with its context holds nothing, a key's entry exists only while someone holds or waits on it (`Entries` in `export_test.go` reads the count), and `LockAll` takes its keys in the `Set`'s one order.
- Every key is checked before any is taken, so a panic on a NaN, an incomparable key or an order that calls two keys equal leaves nothing held. A new check goes before the first acquire too.
- Run `go test -race ./...` on a change to the locking code.
