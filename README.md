# go

These are the general-purpose Go libraries the cplieger projects build on, one Go module per directory. Each module has its own version, its own tags (`<module>/vX.Y.Z`) and its own README, and you import it by its directory:

```sh
go get github.com/cplieger/go/<module>@latest
```

Nothing here imports a cplieger repository outside this one. The other libraries move here from their own repositories, each with its first development release.

## Modules

### Service runtime

| Module | What it does |
| --- | --- |
| [`slogx`](slogx) | Gives every Go service the same `log/slog` setup in one call, with text or JSON output, UTC timestamps, a level you can change at run time and `LOG_LEVEL` parsing. |
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
