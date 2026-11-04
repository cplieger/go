# go

These are the general-purpose Go libraries the cplieger projects build on, one Go module per directory. Each module has its own version, its own tags (`<module>/vX.Y.Z`) and its own README, and you import it by its directory:

```sh
go get github.com/cplieger/go/<module>@latest
```

Nothing here imports a cplieger repository outside this one. The other libraries move here from their own repositories, each with its first development release.

## Modules

### HTTP

| Module | What it does |
| --- | --- |
| [`httpclient`](httpclient) | Makes your Go HTTP calls survive flaky servers, with retries, backoff and Retry-After built in. |
| [`httpserver`](httpserver) | Hardens your Go `net/http` server with access logging, security headers, a client IP that cannot be spoofed and graceful shutdown. |

### Service runtime

| Module | What it does |
| --- | --- |
| [`yamlenv`](yamlenv) | Loads a YAML config file and fills `${VAR}` references from the environment after parsing, so a value cannot change the file's structure. |
| [`keylock`](keylock) | Gives your Go code one lock per key, so work on one file, user or record waits for itself while work on other keys runs. |

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
