# Behavior contract

This page lists the guarantees envx gives a caller, for a developer deciding how each getter treats a value. The package documentation on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/envx) has the full text of each symbol.

## Keys

Every getter takes the variable's name as a `Key`. A plain `string` variable in a key position does not compile. An untyped string literal converts to `Key` without a cast, so at a literal call site the guard is a check on first use.

That check panics when a `Key` is not an environment-variable name. The grammar is `[A-Za-z_][A-Za-z0-9_]*`, never empty, and the panic message quotes the name, capped at 64 bytes:

```text
panic: envx: "APP LISTEN" is not an environment variable name
```

The grammar is narrower than what an operating system accepts, on purpose. A typo such as `APP LISTEN` or `app.listen`, or a badly built dynamic name, then fails the first time it is read, usually at startup. Without the panic, it would read as a variable nobody can set, and the getter would return its default forever. The value of a variable never causes a panic. Only the name can, and the name comes from the code.

## Values

- Empty equals unset. Compose files and CI matrices often write `KEY=` for a setting left blank, and every getter treats that as absent. Use `os.LookupEnv` when the difference matters. The one blank value envx reports on its own is a blank `KEY_FILE`, described under [Secrets](#secrets).
- `String` returns the value, empty when the variable is unset. It takes no default, so there is no argument order to get wrong. Compose the default with `cmp.Or(envx.String("K"), "default")`. It does not trim, because whitespace can matter in a free-form string, so a whitespace-only value counts as set.
- `Bool`, `Int` and `Duration` take a default whose type differs from the key's, so a swapped call does not compile.
- `Bool` accepts `true`, `1`, `yes`, `on`, `false`, `0`, `no` and `off`, in any letter case, after trimming surrounding whitespace.
- `Int` parses the trimmed value with `strconv.Atoi`.
- `Duration` parses the trimmed value with `time.ParseDuration`, so `30s`, `6h` and `1h30m` work. A bare number without a unit is rejected, because `30` means seconds in one tool and minutes in another.

## Malformed values

A tolerant getter returns its default for a malformed value and logs one warning through `slog.Default()`. The warning carries the `key`, the trimmed `value`, the expected `kind` and the `fallback` used, so a deployment typo shows in the logs instead of silently changing behavior. A malformed value never stops the program. The warning names the value because a config value is not a secret. `Secret` never goes through this warning.

The strict getters hand the result to the caller and never log:

| State | `BoolStrict` | `IntStrict`, `DurationStrict` |
| --- | --- | --- |
| Unset or empty | `(false, false, nil)` | `(0, false, nil)` |
| Malformed | `(false, false, err)` | `(0, false, err)`, where `err` is a `*ParseError` |
| Valid | `(b, true, nil)` | `(v, true, nil)` |

Read `ok`, not the value, to tell a variable set to `false` from one that is not set.

- `IntStrict` and `DurationStrict` return a `*ParseError` carrying the `Key`, the parser's `Err` and the trimmed `Value` it rejected. A caller can quote the rejected input without a second `os.Getenv`, which would return it untrimmed. `Unwrap` keeps `*strconv.NumError` reachable with `errors.As`.
- `BoolStrict` returns an error that names the key and the accepted spellings, never the value. Use it for a key an operator could wire to a secret by mistake, because the `Bool` warning carries the raw value. It does not return a `*ParseError`, because that type carries the value.
- A tolerant getter and its strict variant share one parser, so they accept exactly the same values. Only the policy for a malformed value differs.

## Required values

`Require` returns the value, or a `*MissingError` carrying the `Key` when the variable is unset or empty. Detect it with `errors.As`. It never exits, so a caller can collect every missing variable and fail once.

## Secrets

`Secret` follows the Docker secrets convention. When `KEY_FILE` is set and not empty, it reads the file that variable names. Otherwise it returns `KEY`, or a `*MissingError` when `KEY` is unset or empty too.

- The file is opened once, without waiting, then checked and read through that handle, so replacing the path cannot redirect the read. Only a regular file is read: a FIFO, a directory or a device is refused at once as unreadable, so a FIFO with no writer cannot hang startup. Symlinks are followed, so a Kubernetes secret's link reaches its file. The read is capped at 1 MB. A file that grows past the cap during the read is refused rather than cut short.
- The path must already be clean and must have no `..` component, a rule from [pathinside](https://github.com/cplieger/go/tree/main/pathinside). Two dots inside a name, as in `/run/secrets/key..v2`, are allowed.
- `KEY` comes back exactly as set. The file's content comes back with at most one trailing line ending, `\n` or `\r\n`, removed, because an editor or `kubectl create secret --from-file` adds one. Edge spaces, tabs, non-breaking spaces and a second trailing newline are part of the secret and stay. A caller that checks a credential as written gets the same answer from either channel.
- A file whose content is empty or only whitespace returns `ErrBlankSecretFile`, because that is a broken mount rather than a secret. A caller's allow-empty policy can then treat a blank file and a blank `KEY` alike.
- The secret value never appears in an error or a log line. Errors carry the key. A rejected path, an unreadable file and a blank file also carry the file path; the two size failures do not.

Each way a file can be unusable has its own error value, matched with `errors.Is`: `ErrSecretFilePathRejected`, `ErrSecretFileTooLarge`, `ErrSecretFileGrew`, `ErrSecretFileUnreadable` and `ErrBlankSecretFile`. A caller can report why the file failed without matching error text and without echoing the path. That matters when `KEY_FILE` was set to the secret itself by mistake. `ErrSecretFileUnreadable` keeps an `*os.PathError` reachable with `errors.As`: the operating system's for a refused open, stat or read, and one whose error is `not a regular file` for a FIFO, a directory or a device.

`SecretWithSource` also returns the channel that answered: `SecretSourceFile`, `SecretSourceEnv` or `SecretSourceNone`. It reports the channel on the error paths too, so a caller can warn that a `KEY` it also set was ignored in favor of `KEY_FILE`.

A `KEY_FILE` that is present but blank names no file. An empty one falls through to `KEY` as if it were unset, and a whitespace-only one is opened and fails. `IsBlankSecretFilePath` reports both shapes without changing how the secret resolves, so a caller can refuse a broken secret pointer. That matters most for an optional secret, where falling through to an unset `KEY` can leave a gate open.

## Injected environment

`Source{Get: getenv}` carries `String`, `Require`, `Bool`, `Int`, `Duration` and the three strict getters as methods over the function you inject, such as the `getenv` of a `run(os.Args, os.Getenv)` main. `os.Getenv` fits `Get` as it is. The zero `Source` reads the process environment, and the package-level getters are the zero `Source`'s methods, so both forms run the same parsers, trim rules, warnings and key checks. The secret calls are not on `Source`, because they also read files.

## State

envx keeps no state, starts no goroutines and reads nothing when it is imported. It reads the process environment only when a getter is called.
