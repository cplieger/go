# How the checks behave

This page is the full contract of the four predicates, for a developer who needs to know exactly what each one accepts and refuses. The README has the summary.

## Two questions about a path

Containment needs a root and asks where a path points. `Root.Contains` and `RelEscapes` answer it. Hygiene needs no root and asks how a path is written. `HasDotDot` and `IsCanonical` answer it.

Choose by whether you have a root. An archive entry about to be joined onto an extraction directory is a containment question. A credential path, a backup destination or a cache directory read from a config file or a flag is a hygiene question.

The two disagree, and they disagree on the inputs that matter. Containment cleans first, so a traversal that cleans away is not an escape. `/run/secrets/../../etc/shadow` cleans to `/etc/shadow`, leaves no root, and `RelEscapes` reports `false`. Hygiene never cleans, so `HasDotDot` reports `true`, because a real credential path is not written with two traversals in it. Answering a hygiene question with a containment function turns the refusal you meant into an acceptance, at whatever boundary you were guarding.

Clean input bounds the disagreement. `filepath.Clean` leaves `..` components only at the front of a relative path, so on input already in `filepath.Clean` form `HasDotDot` and `RelEscapes` always agree. They differ only on unclean input, which is the input an attacker supplies.

## Why the usual checks fall short

Code that hands an outside path to the filesystem needs a containment answer somewhere. An archive entry name needs one before extraction, and a file-watcher event path before it extends a watch set. So does a request path before it is read or deleted, and a path read back from a log the program wrote earlier. The correct rule is four lines, and the shapes close to it fail in ways a passing test does not show.

- `strings.HasPrefix(target, root)` accepts a sibling whose name only starts with the root's. With root `/srv/data`, the path `/srv/data-evil` passes and is not inside anything. Adding a separator to the root before the prefix test fixes that case and breaks another. It then refuses the root itself, and it answers differently on unclean input than its author's examples suggest.
- `filepath.Rel` followed by a test for a leading `..` string refuses the valid name `..extras/movie.mkv`, whose first segment happens to begin with two dots.

The rule that is right on both counts is `filepath.Rel` followed by a separator-aware test of the result. The relative path escapes exactly when it is `..` or begins with `..` followed by a separator. `Rel` is what defeats the prefix sibling. `Rel("/srv/data", "/srv/data-evil")` is `../data-evil`, so the target is reached by leaving the root, which is what outside means. The separator is what keeps `..extras` a name rather than a traversal.

## Root.Contains

`Root` is a string type, made by plain conversion: `pathinside.Root("/srv/data")`. It checks and normalizes nothing at conversion time. Make it once, where you decide the boundary, and judge every target against it.

`Contains(target)` reports whether target is the root itself or a path beneath it.

- The root and the target are both cleaned, because `filepath.Rel` cleans both. So `/a/b/`, `/a/./b` and `/a/x/../b` are the same root, and a caller that cleans first gets the same answer as one that does not.
- The root itself is inside. `Root(p).Contains(p)` is true for every non-empty `p`, because a tree includes its own root. A scan that starts there, a watch registered on it and an archive's `./` entry all name the root itself.
- A relative root and a relative target are compared as names under the same starting directory. `Root("a/b").Contains("a/b/c")` is `true`, and `Root("a/b").Contains("a/c")` is `false`.
- A pair that cannot be compared by name is refused rather than guessed. That covers an absolute target against a relative root, the reverse, and two different volumes on Windows. `filepath.Rel` reports those as an error, and `Contains` answers `false`.
- The empty root `Root("")` contains nothing. An empty root is almost always an unset field. Treating it as the current working directory would confine paths to a directory nobody chose, which is the direction a containment bug must not take. Write `Root(".")` when you want the current directory.
- Nothing else is normalized. There is no symlink resolution, no Unicode normalization and no conversion between absolute and relative paths. Case follows the platform, as described in "Case on Windows" below.

## RelEscapes

`RelEscapes(rel)` reports whether a relative name leaves the root it is relative to. It is true when the cleaned name is `..` or begins with `..` followed by a separator.

- `rel` is cleaned first, so a traversal buried in the middle, such as `a/../../etc`, is caught.
- The test needs the separator. `../x` escapes, and `..extras/x` does not.
- It says nothing about whether the name is relative. `/etc/passwd` cleans to itself, is not `..`, and does not begin with `../`, so you refuse absolute names yourself with `filepath.IsAbs`. That refusal matters. `filepath.Clean` stops a traversal at the filesystem root, so `/..` cleans to `/` and is accepted here. `filepath.Join` attaches the traversal to a relative base unchanged, so `filepath.Join("data", "/..")` is `.`, above the root.

Use it before a name is joined onto anything, such as an archive entry name or a configured sub-path:

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

Use it also when you already hold a `filepath.Rel` result for other work, so the containment question costs no second `Rel`:

```go
rel, err := filepath.Rel(root, path)
if err != nil || pathinside.RelEscapes(rel) {
    return false
}
_, err = rootDir.Stat(rel) // os.Root-relative, symlink-safe
return err == nil
```

## A name check is stricter than containment

`RelEscapes` and `Root.Contains` do not always agree, and the difference is deliberate. A name that walks out of the root and back into a directory that shares the root's name, such as `../a` under root `a`, is refused by `RelEscapes`. Its joined result is the root itself, which `Root.Contains` reports as inside.

`RelEscapes` judges the shape of a name, and `Root.Contains` judges where a result lands. Code that checks an untrusted name wants the strict answer, because a valid name has no reason to leave its root. Code that classifies a path it was already handed wants `Root.Contains`. One function for both would pick one answer for both callers.

## HasDotDot

`HasDotDot(p)` reports whether `p` holds a `..` component, examined as written.

- `p` is not cleaned, so a traversal that would clean away is still caught. A `..` anywhere is found, first, last or in the middle.
- Components come from `filepath.ToSlash(p)` split on `/`. On Unix a backslash is a valid filename byte, so `a\..\b` is one component and is not a traversal. On Windows a backslash is a separator, so the same string has three components and is a traversal.
- Only an exact `..` component counts. `...`, `..extras`, `/dumps/a..b` and `key..v2` are names. The empty string has no components and is false.
- It judges nothing else. It does not check whether `p` is absolute, whether it is otherwise cleanly written, or what it resolves to.

## IsCanonical

`IsCanonical(p)` reports whether `p` is already in `filepath.Clean` form, so cleaning it would change nothing.

- It refuses a trailing or doubled separator, a `.` component and a traversal buried in the middle.
- The empty string is not canonical, because it cleans to `.`.
- On Windows a path written with slashes is not canonical either, because `Clean` rewrites `a/b` to `a\b`. Code that accepts slash-written input on Windows converts it with `filepath.FromSlash` before the test.
- `..` and `../dumps` are canonical. Canonical form alone accepts a leading traversal, so pair it with `HasDotDot`.

The rule for a value a person typed takes both halves, because neither implies the other:

```go
if !pathinside.IsCanonical(dir) || pathinside.HasDotDot(dir) {
    return fmt.Errorf("dump directory %q must be written plainly, without %q", dir, "..")
}
```

## Names, not files

All four predicates compare names and resolve nothing. A symlink inside the root is still inside it by name, wherever it points, and a path that passes can be swapped between the check and the system call.

That is the right answer for a decision about a name, such as whether a path is yours to handle. It is the wrong one for a decision about access, such as whether an open may succeed. Code that opens, reads, writes, renames or removes through the path wants confinement the kernel enforces. Use [`os.Root`](https://pkg.go.dev/os#Root) through `os.OpenRoot` or `os.OpenInRoot`. Its methods follow a symlink only when it stays inside the root, and it closes the time-of-check to time-of-use gap that a name check cannot see.

The two work together. The name check gives an early refusal with a clear message for the operator, and the `os.Root` handle makes the operation itself safe.

## Case on Windows

Case is the one place `Root.Contains` is not byte-exact. It calls `filepath.Rel`, which compares path components without regard to case on Windows and byte for byte on Unix and Plan 9. So `Root("/srv/Data").Contains("/srv/data/x")` is `false` on Linux and `true` on Windows. The `filepath.Rel` documentation does not state this, so it is stated here. This package neither adds that folding nor removes it.

On Windows the folding is the Go toolchain's simple Unicode case folding, not the volume's own uppercase table. When the fold table grows, containment accepts more names, never fewer, which is the direction a containment bug takes. The fold table is the one the Go release ships, so a newer Go can fold more pairs. Go 1.27's Unicode 17 tables, for example, fold `U+FB05` with `U+FB06`, `U+0390` with `U+1FD3`, and `U+03B0` with `U+1FE3`, so containment on Windows treats each pair as one name.

Case folding does not affect `RelEscapes`, `HasDotDot` or `IsCanonical` on any platform. They compare against the literal `..`, whose characters have no case-fold partners. Their separator handling still follows the platform, as the `HasDotDot` and `IsCanonical` sections above describe.
