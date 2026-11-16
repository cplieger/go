# pathinside

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/pathinside.svg)](https://pkg.go.dev/github.com/cplieger/go/pathinside) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=pathinside%2Fgo.mod)](https://github.com/cplieger/go/blob/main/pathinside/go.mod)

pathinside catches path traversal in Go by checking that an untrusted path stays inside its root, and that a path from a config file was written without `..`.

It replaces the `strings.HasPrefix` or `filepath.Rel` check you would otherwise write before an archive entry, a request path or a file-watcher event reaches the filesystem. It reads names only and never touches the disk. It uses only the standard library and is licensed under Apache-2.0.

## Why use it

pathinside is built for code that receives a path from outside and must decide whether that path belongs inside its boundary before using it.

- `Root.Contains` refuses `/srv/data-evil` under root `/srv/data`, a sibling that a `strings.HasPrefix` test accepts.
- `RelEscapes` refuses `../x` and `a/../../etc`, and accepts the ordinary name `..extras/movie.mkv`.
- `HasDotDot` and `IsCanonical` judge a path as written, so they refuse `/run/secrets/../../etc/shadow` even though it cleans to a path with no `..`.
- An empty root contains nothing, and a pair it cannot compare, such as an absolute path against a relative root, is refused.

Consider [`os.Root`](https://pkg.go.dev/os#Root) if your code opens, writes or removes the file, because its methods refuse any name or symlink that leads outside the directory. Consider [`filepath.IsLocal`](https://pkg.go.dev/path/filepath#IsLocal) if you want one call that refuses an empty, absolute or escaping name and, on Windows, a reserved name such as `NUL`.

## Install

```sh
go get github.com/cplieger/go/pathinside@latest
```

## Usage

Make the root once, where you decide the boundary, and judge each path against it:

```go
root := pathinside.Root(cfg.WatchDir)

if !root.Contains(event.Name) {
    slog.Warn("refusing to extend the watch set outside the watched root", "path", event.Name, "root", string(root))
    return
}
```

`Root` is a plain string conversion. The root and the target are both cleaned, and the root itself counts as inside.

Check an archive entry name before you join it onto the extraction directory. `RelEscapes` does not judge whether a name is absolute, so refuse that yourself:

```go
switch {
case name == "":
    return fmt.Errorf("archive holds an entry with an empty name")
case filepath.IsAbs(name):
    return fmt.Errorf("archive entry %q is an absolute path", name)
case pathinside.RelEscapes(name):
    return fmt.Errorf("archive entry %q escapes the extraction directory", name)
}
```

Check a path from a config file or a flag as written, with no root. Use both predicates, because `IsCanonical` alone accepts `..` and `../dumps`, which are already in clean form:

```go
if !pathinside.IsCanonical(dir) || pathinside.HasDotDot(dir) {
    return fmt.Errorf("dump directory %q must be written plainly, without %q", dir, "..")
}
```

`ExampleRoot_Contains`, `ExampleRelEscapes`, `ExampleHasDotDot` and `ExampleIsCanonical` on pkg.go.dev are runnable, and `go test` keeps them true.

## API

- Containment needs a root. It is the `Root` type and its `Contains` method, plus `RelEscapes` for a relative name or a `filepath.Rel` result.
- Hygiene needs no root. `HasDotDot` finds a `..` component as written, and `IsCanonical` reports whether a path is already in `filepath.Clean` form.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/pathinside).

## Containment and hygiene answer different questions

Containment cleans a path before it judges it. `/run/secrets/../../etc/shadow` cleans to `/etc/shadow`, which holds no `..`, so `RelEscapes` reports `false`. Hygiene judges the path as written, so `HasDotDot` reports `true`. Use `HasDotDot` and `IsCanonical` for a value a person typed, such as a credential path or a backup folder in a config file. A containment check there accepts the path you meant to refuse. `HasDotDot` and `RelEscapes` give different answers only on a path that is not already in `filepath.Clean` form.

`RelEscapes` is also stricter than `Root.Contains`. Under root `a`, the name `../a` leaves the root and comes back to it. `RelEscapes` refuses it, because a valid name has no reason to leave its root. `Root.Contains` judges only where the joined path lands, so it accepts the result.

[How the checks behave](docs/contract.md) has the full contract of each predicate.

## The checks read names and resolve nothing

All four predicates compare names and never touch the filesystem. A symlink inside the root is still inside it by name, wherever it points, and a path can change between the check and its use. That fits a decision about a name, such as whether a path is yours to handle. When your code opens, reads, writes, renames or removes through the path, also use `os.Root` through `os.OpenRoot` or `os.OpenInRoot`. The name check gives an early refusal with a clear message, and `os.Root` makes the operation itself safe.

Case is the one place `Root.Contains` is not byte-exact. It follows `filepath.Rel`, which ignores case on Windows and compares bytes on Unix and Plan 9. So `Root("/srv/Data").Contains("/srv/data/x")` is `false` on Linux and `true` on Windows. On Windows, Go folds case with its own Unicode tables rather than the volume's, so a newer Go release can make containment accept more names, never fewer. Case folding does not change what `RelEscapes`, `HasDotDot` or `IsCanonical` report on any platform, because `..` has no case-fold partners.

## Unsupported by design

pathinside has no symlink resolution, no helper that validates and joins, no variant that excludes the root, and no case folding or Unicode normalization of its own. [Unsupported by design](docs/non-goals.md) gives the reason for each and what to use instead.

## Documentation

- [How the checks behave](docs/contract.md) gives the full contract of each predicate, for code that relies on an edge case.
- [Unsupported by design](docs/non-goals.md) lists the features left out on purpose, with the reasons.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
