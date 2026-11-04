# slogx

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/slogx.svg)](https://pkg.go.dev/github.com/cplieger/go/slogx) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=slogx%2Fgo.mod)](https://github.com/cplieger/go/blob/main/slogx/go.mod) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/slogx/badges/mutation.json)](https://github.com/cplieger/go/issues?q=label%3Agremlins-tracker)

slogx gives every Go service the same `log/slog` setup in one call, with text or JSON output, UTC timestamps, a level you can change at run time and `LOG_LEVEL` parsing.

It replaces the handler, level and timestamp code each Go service otherwise writes at startup, and it installs the standard library's own `TextHandler` or `JSONHandler`. Its `capture` subpackage records log output for tests. It uses only the standard library, needs Go 1.27 or later and is licensed under Apache-2.0.

## Why use it

slogx is built for Go services and containers that log through `log/slog` and want the same logger setup in every app.

- `Setup` installs logfmt text or JSON, with each record's timestamp in UTC whatever the container's time zone.
- It returns the `*slog.LevelVar` behind the level. You can install the logger before config is read and set the level later, or turn debug logging on and off at run time.
- `ParseLevel` accepts `warning` as well as slog's own level names and offsets such as `warn+1`. It reports a bad value for your app to warn about.
- `capture` passes the `testing/slogtest` handler checks and folds `Logger.With` attributes into each record. `capture.Default` restores the default logger and the `log` package's output after each test.

Consider [tint](https://github.com/lmittmann/tint) if you want colorized logs in a terminal. It is a zero-dependency `slog.Handler` that writes tinted output and takes options in the shape of `slog.HandlerOptions`.

## Install

```sh
go get github.com/cplieger/go/slogx@latest
```

## Usage

The common case parses `LOG_LEVEL` and installs the default logger. Parse first, install, then warn about a bad value, so the warning goes through the new handler:

```go
lvl, ok := slogx.ParseLevel(os.Getenv("LOG_LEVEL"), slog.LevelInfo)
slogx.Setup(slogx.Options{Level: lvl})
if !ok {
	// Log the variable's name, never its value, which a bad env expansion could fill with a secret.
	slog.Warn("invalid LOG_LEVEL, using default", "var", "LOG_LEVEL", "default", "info")
}
```

An app that ships its log events to a log store such as Loki can write JSON to stdout:

```go
slogx.Setup(slogx.Options{Format: slogx.JSON, Output: os.Stdout})
```

Install a handler before config is read, so warnings from loading config still print, then set the level once it is known. The returned `*slog.LevelVar` also changes the level at run time for a debug toggle:

```go
lv := slogx.Setup(slogx.Options{}) // Info default, on stderr

cfg := loadConfig() // may log warnings, which emit at Info
lvl, _ := slogx.ParseLevel(cfg.LogLevel, slog.LevelInfo)
lv.Set(lvl)

// later, from a settings toggle:
func setDebug(on bool) {
	if on {
		lv.Set(slog.LevelDebug)
	} else {
		lv.Set(slog.LevelInfo)
	}
}
```

When you build your own handler options, add `UTCTime` to get UTC timestamps:

```go
h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: slogx.UTCTime})
```

The examples for `ParseLevel`, `ParseFormat` and `capture.New` on pkg.go.dev run as tests.

## API

- `Setup(Options) *slog.LevelVar` builds a handler, installs it as slog's default and returns its `LevelVar`. Each call replaces the default logger. It also routes the `log` package's output through the new handler. `NewHandler(Options) (slog.Handler, *slog.LevelVar)` does the same without installing it.
- `Options{Output, Format, Level, AddSource}` has a usable zero value, text at Info. One `Output` takes every level, and a nil `Output` means stderr for both formats.
- `Format` is `Text`, the logfmt default, or `JSON`. Any other value makes `NewHandler` and `Setup` panic, and `ParseFormat` returns only those two.
- `ParseLevel(raw, def)` and `ParseFormat(raw, def)` trim the input and ignore case. An empty string returns `(def, true)` and an unrecognized value `(def, false)`. `ParseLevel` maps `warning` to `warn`, so `warning+1` parses like `warn+1`.
- `UTCTime(groups, attr)` is the `ReplaceAttr` that renders a record's time in UTC. A `time` attribute inside a group keeps its own time zone.
- The `capture` types are covered below.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/slogx). Releases follow semantic versioning.

## Testing with capture

The `slogx/capture` subpackage records log output so a test can assert on it without a hand-written buffer handler. Import it only from `_test.go` files. It is a separate package, so its `testing` import never reaches a production build.

For code that logs through `slog.Default()`, `capture.Default(t)` installs a recorder. When the test ends, it restores the previous default logger and the `log` package's output and flags. `capture.SwapDefault(t, logger)` does the same for a logger you build yourself, such as one writing to a buffer. A test that uses either must not call `t.Parallel`.

```go
func TestWarnsWhenFull(t *testing.T) {
	rec := capture.Default(t)

	checkDisk() // logs through slog.Default()

	if rec.Count("disk almost full") != 1 {
		t.Errorf("want one warning, got %d", rec.Count("disk almost full"))
	}
}
```

For code that takes an injected `*slog.Logger`, `capture.New()` returns a logger and its recorder and leaves the global default alone, so the test can run in parallel:

```go
logger, rec := capture.New()
c := NewComponent(WithLogger(logger))
// ... exercise c, then assert on rec.Contains / Count / CountExact / Messages / Records
```

The recorder has message checks such as `Count`, `CountExact` and `CountLevel`, attribute checks such as `AttrValue` and `HasAttr`, and `Records()` for anything else. Each stored record matches what the standard `TextHandler` or `JSONHandler` would write, including attributes added with `Logger.With` and groups added with `Logger.WithGroup`. [Testing log output with capture](docs/capture.md) explains which check to use and how messages and values match.

## Unsupported by design

These are deliberate non-goals. The library does one job, installing the standard slog handler, and stays small on purpose.

| Feature | Rationale |
| --- | --- |
| A custom `slog.Handler` implementation | `slogx` composes the standard `Text` and `JSON` handlers. For different formatting, write your own handler and put `UTCTime` in its options. |
| Secret redaction or attribute scrubbing | Each call site keeps secrets out. Log `token_set=true` instead of the token. A blanket redacting ReplaceAttr gives false confidence. |
| Audit-event schemas | An audit log with actor, action and outcome is domain policy for the app. |
| `LOG_LEVEL` or any other env-var name | `ParseLevel` takes a string. The app owns which environment variable it reads and its default. |
| Per-app attribute conventions | Base attributes such as `slog.With("service", ...)`, key names and message wording are the app's choice. Call `.With` on the logger `Setup` installs. |
| A logging facade or leveled wrapper types | `slog` is the interface. `slogx` configures it and leaves `slog.Logger` unwrapped. |

## Documentation

- [Testing log output with capture](docs/capture.md) is for a developer writing assertions on log output.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
