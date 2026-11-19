# Requests and responses

This page is for a developer writing handlers on httpserver. It covers the JSON writers, the error envelope, body limits and decoding, method checks and the path `http.ServeMux` will route.

## JSON responses

- `JSONHeaders(w)` sets `Content-Type: application/json` and `X-Content-Type-Options: nosniff`.
- `WriteJSON(w, v)` writes `v` with status 200.
- `WriteJSONStatus(w, status, v)` sets the headers, writes the status and encodes `v`. An encode failure is logged at `Warn` and not returned.
- `OK(w)` writes 200 with `{"ok":true}`.

## Errors

`WriteError(w, r, status, code, msg)` writes an `ErrorResponse`. It is safe to call with a nil `r`. It is `WriteErrorBody(w, status, NewErrorResponse(r, code, msg))`, the two pieces [an envelope of your own](#an-envelope-of-your-own) builds on.

`ErrorResponse` has four fields, `Code`, `Message`, `Remediation` and `RequestID`, and every one is always present:

```json
{"code":"bad_request","message":"invalid payload","remediation":"Fix the request as the message describes, then resend it.","request_id":"0f6c..."}
```

- `code` is the machine-readable token a client branches on.
- `message` is the human sentence the caller passed.
- `remediation` is the code's default from `CodeSpecs()`, or the empty string for a code outside it, such as your app's own.
- `request_id` is the id `Logging` gave the request, so a client can match a failure to the access log. When the context has none, `WriteError` makes a fresh one, so the field is never empty. It also sets the `X-Request-ID` response header to the same id when the response has none.

`WriteError` sets `Content-Type` and keeps every other header the caller set, such as `Allow` or `Retry-After`. Every error body the library writes goes through it, or through an `ErrorResponder` you pass in its place.

`ErrorCode` is the type of the `code` field. It is a separate type from the human message, so the two cannot be passed in the wrong order. A code uses only lowercase letters, digits and `_`, as in `host_not_allowed`, and has at least one of them.

A code that breaks that grammar, the empty code included, is neither sent nor repaired, and it never panics. The encoder sends `InvalidErrorCode`, `invalid_error_code`, in its place and logs the bad code once per process, because this runs on an error path for every request.

`ErrorResponder` is the signature of `WriteError`: `func(w, r, status, code, msg)`. `WriteError` is its default. Every middleware that writes an error body takes one through an option or an argument, and so do `SPA`, `OutagePage`, `NotFound`, `CrossOrigin` and `ListenOps`. Your app's catalog can then write its own remediation, and an endpoint that does not speak JSON can render its error on its own content type. `DecodeBody`, `RequireMethod` and `MethodNotAllowed` write through `WriteError` itself.

### An envelope of your own

`NewErrorResponse(r, code, msg)` returns the envelope `WriteError` would write: the code checked against the grammar, its `CodeSpecs()` remediation, and the request id from `r`'s context, else a fresh one. `WriteErrorBody(w, status, body)` writes it, sets `Content-Type`, and sets `X-Request-ID` to the body's own `request_id` when the response has none, so the header and the body never carry two different ids. Replace `Remediation` between the two calls to answer with your own advice:

```go
resp := httpserver.NewErrorResponse(r, "host_not_allowed", "this host name is not served")
resp.Remediation = "Open the app at its configured address."
httpserver.WriteErrorBody(w, http.StatusForbidden, resp)
```

Build the envelope with `NewErrorResponse` and change at most its `Message` or `Remediation`. `WriteErrorBody` writes the body it is given as it is, so an `ErrorResponse{…}` literal goes out with its code unchecked and no request id filled.

`body` is an `ErrorBody`: an `ErrorResponse`, or a struct of yours that embeds one by value as its first field. The embedder's own fields are written after the four standard keys, in one flat object:

```go
httpserver.WriteErrorBody(w, http.StatusConflict, struct {
	httpserver.ErrorResponse
	Dependents []string `json:"dependents"`
}{httpserver.NewErrorResponse(r, "has_dependents", "the item is still in use"), []string{"a", "b"}})
```

```json
{"code":"has_dependents","message":"the item is still in use","remediation":"","request_id":"0f6c...","dependents":["a","b"]}
```

The remediation is empty here because `has_dependents` is the app's own code, outside `CodeSpecs()`; set it as above.

### The package's codes

`CodeSpecs()` returns every code a writer in this package answers, each as a `CodeSpec`: the `Status` it is answered with, the default `Remediation`, and `NoEffect`, which is true when the refused request changed nothing, so resending it is safe. The map is a copy, so a caller can add its own codes to it.

| Code | Status | `NoEffect` | Answered by |
| --- | --- | --- | --- |
| `internal_error` | 500 | no | `Recoverer`, with the message `internal error` |
| `not_found` | 404 | no | `NotFound`, `SPA`, `OutagePage`, the ops listener |
| `method_not_allowed` | 405 | no | `MethodNotAllowed`, `RequireMethod`, `SPA`, `OutagePage`, the ops listener |
| `bad_request` | 400 | no | `DecodeBody` |
| `loopback_only` | 403 | no | `LoopbackOnly` |
| `too_many_auth_failures` | 429 | yes | `FailedAuthRateLimit` |
| `rate_limited` | 429 | yes | `RateLimiter`, `KeyedRateLimiter` |
| `host_not_allowed` | 403 | no | `HostPolicy` |
| `timeout` | 503 | no | `RouteTimeout`, whose handler may still commit after the 503 |
| `ui_unbuilt` | 503 | no | `SPA` with no bundle |
| `cross_origin_refused` | 403 | yes | `CrossOrigin` |
| `precondition_failed` | 412 | yes | `SPA`, `OutagePage`, for an `If-Match` the file does not hold |

`invalid_error_code` is not in the map, because no writer in the package passes a malformed code.

`httpservertest.CheckCodeSpecs(t, dir, specs)` holds a package's spec map to its source. It type-checks the package in `dir`, reading the non-test files `go build` selects for the running platform and its imports through `go list -export`, so the package must compile and the `go` command must be on `PATH`, as it is under `go test`. It fails naming each code written but missing from `specs` and each key nothing writes. A code is any constant expression of string value: a literal, a constant of this package or another, a concatenation or a conversion. It is written when it is passed as an `ErrorCode` argument of `WriteError` or `NewErrorResponse`, of a function or method the package declares, or of a func value such as an `ErrorResponder`, or when it fills an `ErrorCode` field of a struct the package declares or of `httpserver.ErrorResponse`. Another package's functions and structs do not count, so registering a code in a catalog is not answering it. A code held in a variable is counted where its constant enters. httpserver's own test runs it over the package with `CodeSpecs()`, and a library with its own codes runs it over its own directory.

## Request bodies

`MaxJSONBody` is the default body cap, 1 MiB.

`LimitBody(w, r, maxBytes)` wraps the body in `http.MaxBytesReader`. A read past the cap fails with a `*http.MaxBytesError`, which you test with `errors.AsType`, and nothing is written, so the status is yours. Detect an over-limit body on the read error, never on the close.

`LimitBody` also asks `net/http` to close the connection instead of reading the rest of an oversized body. It reaches the server's own writer through the `Unwrap` chain, so this is best effort. A middleware that does not unwrap blocks it, and so does `RouteTimeout`, whose buffering writer cannot be unwrapped.

| Function | Cap | On failure |
| --- | --- | --- |
| `DecodeBody(w, r, v, errMsg)` | `MaxJSONBody` | writes 400 with `errMsg` and returns false |
| `DecodeJSONInto(w, r, v, maxBytes)` | `maxBytes` | writes nothing and returns the error |

Each decodes a single JSON value and rejects trailing data. Unknown fields are accepted. For a body that may be absent, use `DecodeJSONInto` and treat `errors.Is(err, io.EOF)` as the absent case.

`DecodeJSONInto` is the mechanism behind `DecodeBody`, for an app with its own error envelope or a cap per endpoint. It returns a `*http.MaxBytesError` for an oversized body, `ErrTrailingData` for a second JSON value, and another error for a malformed body. Map the result to your own status and envelope, for example 413 for the size error.

## Methods

- `RequireMethod(w, r, method)` writes a 405 and returns false when `r.Method` differs.
- `MethodNotAllowed(w, r, allowed...)` writes the 405 on its own, for a route that permits several methods. The `Allow` header names the whole set, such as `GET, POST`, and the body is the standard `method_not_allowed` envelope.
- `SetAllow(w, allowed...)` sets only the `Allow` header, for an `OPTIONS` responder or any other place outside a 405.

RFC 9110 makes `Allow` mandatory on a 405, as a comma-separated list. The value is your set joined with `", "`. Entries are kept as written, because a method token is case-sensitive. Blank entries are dropped and exact duplicates collapse. An empty set renders the empty value, which the RFC defines as "this resource allows no methods".

`HEAD` is never implied by `GET`. `http.ServeMux` serves `HEAD` from a `GET` pattern, so a route whose `GET` has a side effect registers `HEAD` separately to reject it and does not advertise it. Pass `http.MethodHead` when the route really serves it.

## Canonical request path

`CanonicalRequestPath(p)` returns the path `http.ServeMux` will route `p` as, and whether `p` already is that path. The cleaning is `path.Clean` with a non-root trailing slash put back, which is `net/http`'s own `cleanPath`.

`ServeMux` cleans the escaped path before it picks a pattern, and answers 307 when the result differs. No registered route can intercept that redirect. A browser follows it without the user noticing.

A machine sender that does not follow redirects sees it differently. To `curl -fsS` without `-L`, a 307 is success, so the caller exits 0 having never reached the handler. Nothing is recorded, no job runs, and nothing says the URL was malformed. A route whose only purpose is a side effect uses this function to refuse the non-canonical spelling itself.

The verdict decides whether to refuse. The cleaned path tells you whether the request cleans into the namespace you guard, as `//beat/api` does.

Pass `r.URL.EscapedPath()` to reproduce the mux's decision exactly. The decoded `r.URL.Path` gives a stricter verdict, because `%2e%2e` decodes to `..`. An encoded dot segment then reads as non-canonical, while the mux draws no redirect for it. Both are legitimate, so choose on purpose.

`canonical` is the verdict of the cleaning step alone. Two other redirects are outside it. The trailing-slash redirect, from `/tree` to `/tree/` when a `/tree/` subtree is registered, fires on an already-canonical path and depends on the route table. A `CONNECT` request is not cleaned at all.

An empty `p` returns `/` and false. A `p` with no leading slash is rooted before cleaning, so it can never be canonical. The route scope, the refusal's status and body, and any metric for the case stay with your app. The function only reads a string.
