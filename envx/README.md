# envx

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/envx.svg)](https://pkg.go.dev/github.com/cplieger/go/envx) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=envx%2Fgo.mod)](https://github.com/cplieger/go/blob/main/envx/go.mod)

envx configures your Go app from environment variables and Docker secrets, with typed getters that fall back to your defaults and log a typo instead of hiding it.

It replaces the `os.Getenv`, `strconv` and `KEY_FILE` code a service otherwise writes at startup. envx depends only on [pathinside](https://github.com/cplieger/go/tree/main/pathinside), which uses only the standard library. It is licensed under Apache-2.0.

## Why use it

envx is built for Go services in containers, configured by compose or Kubernetes through variables and mounted secret files.

- `Bool`, `Int` and `Duration` return your default for an unset, empty or malformed value. A malformed one also logs a warning through `slog.Default()` naming the variable.
- A key name no variable can have, such as `APP LISTEN`, panics on first read. Otherwise the getter would return its default forever.
- `Require` returns a typed error, so startup can report every missing variable at once.
- `Secret` reads `KEY_FILE` before `KEY`, caps the file at 1 MB and keeps the secret out of every error and log line.

Consider [caarlos0/env](https://github.com/caarlos0/env) if you want your whole config parsed into a tagged struct, with float, slice, map and custom-type parsers.

## Install

```sh
go get github.com/cplieger/go/envx@latest
```

## Usage

Read each setting with its default:

```go
addr := cmp.Or(envx.String("APP_LISTEN"), ":8080")
debug := envx.Bool("APP_DEBUG", false)                 // true/1/yes/on · false/0/no/off
retries := envx.Int("APP_RETRIES", 3)
interval := envx.Duration("APP_INTERVAL", 6*time.Hour) // Go duration syntax
```

Fail at startup on a missing variable, and read a Docker secret from `APP_API_KEY_FILE` when it is set, else from `APP_API_KEY`:

```go
token, err := envx.Require("APP_TOKEN") // *envx.MissingError when unset or empty
if err != nil {
	slog.Error("startup", "error", err)
	os.Exit(1)
}

// An empty APP_API_KEY_FILE names no file, so Secret would read APP_API_KEY
// instead. Stop here when a broken secret mount must not fall back:
if envx.IsBlankSecretFilePath("APP_API_KEY") {
	slog.Error("startup", "error", "APP_API_KEY_FILE is set but empty")
	os.Exit(1)
}

apiKey, err := envx.Secret("APP_API_KEY")
```

Keep a `run(os.Args, os.Getenv)` test seam and still get the typed getters:

```go
func main() { os.Exit(run(os.Args, os.Getenv)) }

func run(args []string, getenv func(string) string) int {
	env := envx.Source{Get: getenv}
	timeout := env.Duration("DUMP_TIMEOUT", 5*time.Minute)
	workers := env.Int("DUMP_CONCURRENCY", 2)
	// ...
}
```

The runnable examples on pkg.go.dev show more cases, and `go test` keeps them true.

## API

- Getters: `String`, `Bool`, `Int` and `Duration`, which take the variable name as a `Key`.
- Strict getters that return a malformed value as an error and never log: `BoolStrict`, `IntStrict`, `DurationStrict` and `ParseError`.
- Required values and secrets: `Require`, `Secret`, `SecretWithSource`, `IsBlankSecretFilePath`, `MissingError` and one error value for each way a secret file can fail.
- An injected environment: `Source`, which carries the getters, the strict getters and `Require` as methods.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/envx).

## Behavior contract

- Every getter treats an empty variable as unset. Use `os.LookupEnv` when the difference matters.
- A value never causes a panic or an exit. The one panic is a malformed key name, which comes from the code.
- A tolerant getter and its strict variant share one parser, so they accept exactly the same values.
- `Bool`, `Int`, `Duration` and the strict getters trim surrounding whitespace. `String` returns the value as set.
- `Secret` returns `KEY` as set, and a file's content with at most one trailing line ending removed.
- envx keeps no state, starts no goroutines and reads the environment only when a getter is called.

[Behavior contract](docs/behavior.md) has every rule, including the secret-file checks and the error values.

## Unsupported by design

These are deliberate non-goals:

- Struct tags or reflection-based config loading.
- Loading `.env` files.
- Float, slice and map getters.
- Prefix namespacing such as `WithPrefix("APP_")`.
- `MustX` variants that panic on a missing variable.
- Accepting a malformed key name with a warning.

[Non-goals](docs/non-goals.md) gives the reason for each.

## Documentation

- [Behavior contract](docs/behavior.md) explains how each getter treats unset, empty and malformed values, and how secrets are read.
- For a YAML config file whose values refer to environment variables, see the separate [yamlenv](https://github.com/cplieger/go/tree/main/yamlenv) module.
- [Non-goals](docs/non-goals.md) gives the reason each unsupported feature is left out.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is
intended for personal / self-hosted use. No guarantees of fitness for production
environments. Use at your own risk.

This project was built with AI-assisted tooling using
[Claude](https://claude.com), [GPT](https://openai.com), and
[Kiro](https://kiro.dev). The human maintainer defines architecture,
supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
