# Unsupported by design

This page lists what pathinside leaves out on purpose, for a developer wondering whether a missing feature is coming. Each section gives the reason and what to use instead.

## Symlink resolution

Resolving symlinks would turn a pure string check into a filesystem call with its own error cases, and it would still lose the race between check and use. [`os.Root`](https://pkg.go.dev/os#Root) in the standard library is the answer.

## A SafeJoin-style helper that validates and joins

An empty name, an absolute name and a traversal deserve distinct messages, and those messages belong to the caller. A helper that returns one error for all three hides which one happened. Compose `RelEscapes` with `filepath.IsAbs` and `filepath.Join` instead, as the README's archive example does.

## A variant that excludes the root

Code that needs "strictly beneath the root" refuses equality and wants its own error for it, which is a different rule. A flag for it would let a caller pick the wrong containment rule with one boolean. Keep the equality test at the call site:

```go
r.Contains(target) && filepath.Clean(target) != filepath.Clean(string(r))
```

## Case-insensitive or Unicode-normalizing comparison

pathinside adds no folding or normalization. On Windows it cannot remove folding either, because `filepath.Rel` already ignores case there and `Root.Contains` inherits that. [How the checks behave](contract.md#case-on-windows) describes it.

Folding and normalization are properties of a filesystem, not of a path, and an error in either direction is a security bug. When you need a mount's own rule for equal names, ask the filesystem rather than a string comparison.
