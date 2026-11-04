# keylock

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/keylock.svg)](https://pkg.go.dev/github.com/cplieger/go/keylock) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=keylock%2Fgo.mod)](https://github.com/cplieger/go/blob/main/keylock/go.mod)

keylock gives your Go code one lock per key, so work on one file, user or record waits for itself while work on other keys runs.

It replaces the hand-written map of mutexes behind a mutex. It uses only the standard library, needs Go 1.27 or later and is licensed under Apache-2.0.

## Why use it

keylock is built for services that serialize work per item, where the set of items is large or unbounded.

- A wait ends when its context does. The caller then gets the context's error and holds nothing, so a cancelled request never takes a lock it gave up on.
- A key's entry exists only while a caller holds or waits on it, so memory follows the work in flight, not the number of keys ever seen.
- `LockAll` takes several keys in one fixed order, so two callers naming the same keys in a different order never deadlock.
- Each unlock function is safe to call twice, so a `defer unlock()` beside an early unlock does not release someone else's lock.

Consider [`sync.Mutex`](https://pkg.go.dev/sync#Mutex) if you need one lock for the whole process, because it adds no dependency at all.

## Install

```sh
go get github.com/cplieger/go/keylock@latest
```

## Usage

```go
files := keylock.New(strings.Compare)

unlock, err := files.Lock(ctx, "movie.en.srt")
if err != nil {
    return err // ctx ended while waiting
}
defer unlock()
```

To hold several keys at once, pass them all to one `LockAll` call:

```go
unlock, err := files.LockAll(ctx, []string{"movie.fr.srt", "movie.en.srt"})
```

`LockAll` removes duplicate keys, sorts the rest with the function you gave `New` and takes them one by one. When the context ends partway, it releases every key it took before returning the error. The package examples on pkg.go.dev show the common cases, and `go test` keeps them true.

## The ordering rule

Every caller that takes more than one key must do it through one `LockAll` call. Two nested `Lock` calls can deadlock against another caller taking the same keys the other way round, and keylock cannot see that.

The function given to `New` must be a strict total order that agrees with `==`. It returns 0 for two keys only when they are the same key. `LockAll` panics when it calls two different keys equal, because it could not order them the same way for every caller.

`Lock` and `LockAll` also panic on a key that cannot be found again or cannot be compared: a floating-point NaN, or an `any` key holding a slice. They check every key before taking any, so the panic leaves nothing held.

## API

- `New` builds a `Set` for one key type and its order.
- `Set.Lock` takes one key and `Set.LockAll` takes several. Both return an unlock function, or the context's error.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/keylock).

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
