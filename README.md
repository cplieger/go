# go

These are the general-purpose Go libraries the cplieger projects build on, one Go module per directory. Each module has its own version, its own tags (`<module>/vX.Y.Z`) and its own README, and you import it by its directory:

```sh
go get github.com/cplieger/go/<module>@latest
```

Nothing here imports a cplieger repository outside this one. The other libraries move here from their own repositories, each with its first development release.

## Modules

### Untrusted input

| Module | What it does |
| --- | --- |
| [`textsafe`](textsafe) | Keeps untrusted text from forging your Go log lines, starting terminal escapes or reordering what a reader sees in a JSON document or report. |
| [`urlform`](urlform) | Tells your Go code which host a browser opens for an untrusted URL, or that it cannot vouch for one, before you publish or match the link. |
| [`jsonx`](jsonx) | Decodes the integer fields of someone else's JSON in Go, whether a value arrives as `14`, `"14"`, `null`, `"unknown"` or `9.0`, under one policy you choose, and caps how many elements an untrusted JSON body may decode before it allocates them. |
| [`xmlx`](xmlx) | Caps the memory and work an untrusted XML document can cost your Go program, before and during an `encoding/xml` decode. |
| [`pathinside`](pathinside) | Catches path traversal in Go by checking that an untrusted path stays inside its root, and that a path from a config file was written without `..`. |

### HTTP

| Module | What it does |
| --- | --- |
| [`httpclient`](httpclient) | Makes your Go HTTP calls survive flaky servers, with retries, backoff and Retry-After built in. |
| [`httpserver`](httpserver) | Hardens your Go `net/http` server with access logging, security headers, a client IP that cannot be spoofed and graceful shutdown. |

### Service runtime

| Module | What it does |
| --- | --- |
| [`slogx`](slogx) | Gives every Go service the same `log/slog` setup in one call, with text or JSON output, UTC timestamps, a level you can change at run time and `LOG_LEVEL` parsing. |
| [`yamlenv`](yamlenv) | Loads a YAML config file and fills `${VAR}` references from the environment after parsing, so a value cannot change the file's structure. |
| [`health`](health) | Gives distroless Go services a Docker healthcheck with no shell, curl or wget. Your service writes a marker file, and the same binary checks it when Docker asks. |
| [`measure`](measure) | Gives your Go service a Prometheus `/metrics` endpoint with counters, gauges, histograms and process metrics, using only the standard library. |
| [`scheduler`](scheduler) | Lets your Go container run its job on an interval or an outside trigger, with no overlapping runs and a clean shutdown. |
| [`filesafe`](filesafe) | Writes files from Go so that a crash or a power loss leaves the complete old content or the complete new content, never a partial file. |
| [`keylock`](keylock) | Gives your Go code one lock per key, so work on one file, user or record waits for itself while work on other keys runs. |

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
