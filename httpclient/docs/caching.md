# Revalidation cache

This page is for a developer who wants repeated GET requests to cost less. httpclient keeps earlier answers in memory and replays each one's validator, so a server that sees nothing changed can answer `304 Not Modified` instead of the whole body. It covers what is kept, when a `304` serves it, the key, the memory bound and the two ways to use the cache.

## What it does

A GET whose earlier answer the cache holds carries that answer's validator: `If-None-Match` from its `ETag`, or else `If-Modified-Since` from its `Last-Modified`. On a `304` that names the held copy, the read is answered from it.

Nothing is served on age. Every read reaches the server, and only the server's `304` in the same exchange lets the cache answer from what it holds. There is no freshness, `max-age` or heuristic expiry, so a cached read never returns an answer the server has since changed.

What that saves depends on the server. A `304` carries no body, so the transfer is always saved. Some servers also bill a `304` less: GitHub's REST API does not count a `304` to a conditional request carrying an `Authorization` header against its rate limit ([best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api)).

## What is never kept

- A request other than a GET, and a GET that carries its own `If-None-Match` or `If-Modified-Since`. These pass through as given.
- An answer other than a `200`. Any answer that is not kept forgets what was held for its request: an error status, a failure in transit, a body that cannot be read, and every case below.
- An answer with no validator. An `ETag` counts only when it is one entity-tag, weak or strong, and a `Last-Modified` only when it is one HTTP date, each at most 1 KiB ([RFC 9110 section 8.8](https://www.rfc-editor.org/rfc/rfc9110#section-8.8)). A wildcard or a list replayed as `If-None-Match` would match other representations.
- An answer whose `Cache-Control` says `no-store`, in any case. A `304` that says `no-store` serves its read from the held copy, which is then forgotten ([RFC 9111 section 5.2.2.5](https://www.rfc-editor.org/rfc/rfc9111#section-5.2.2.5)).
- A read whose request says `Cache-Control: no-store`. It is sent without a validator and its answer is not kept.
- A read that followed a redirect. A validator names one resource's representation, so a hop leaves without one and its answer is not kept.
- A read whose request or answer a round tripper between the cache and the wire changed. See [Observers](#observers-and-the-guard).

## When a 304 serves the held copy

A `304` serves the held copy only when all of these hold:

- it is the wire's own answer, unchanged, to the validator the held copy sent;
- its `ETag`, where it has one, weakly matches the held one, and its `Last-Modified`, where it has one, is the held date ([RFC 9111 section 4.3.4](https://www.rfc-editor.org/rfc/rfc9111#section-4.3.4));
- it carries every required held field the held copy has (see below);
- the cache was not cleared while the read was in flight.

A `304` that fails any of these, or that answers a request carrying no validator, has nothing to be served from. The read is sent once more without a validator, even when it is not stored. A second `304` reaches you as the `304` it is.

## Held fields

A held copy keeps the body and only the response header fields you name in `CacheConfig.Held`, the ones you read from a served answer, such as `Link` for paging. Every other field of a served answer, rate headers and cookies included, is the `304`'s own.

- A held field the `304` carries takes the `304`'s lines.
- One the `304` omits keeps the held lines, which the `304` did not confirm. A field sent on several lines, such as `Set-Cookie`, keeps each line.
- A field marked `Required` that the `304` omits, where the held copy has it, makes the held copy unservable, and the read is sent once more without a validator. Use it for a field whose stale value would mislead, such as a total count.

A name is matched without regard to case. A name given twice is held once, required if either entry says so.

## The key

An entry's key is a SHA-256 digest of a digest of the request's `Authorization` lines, its host, its URL and every other request header but the validators, however each name is cased. Requests with different credentials never share an entry, and the credential is never held. Each cache has entries of its own, so two caches never share one.

## Memory

A cache holds at most `CacheConfig.MaxBytes`, 32 MiB by default, and evicts the least recently used entry first. An entry is charged everything it holds: its key, its validators, its held fields with the storage each line takes, the full capacity of its body, and 512 bytes of overhead. An entry larger than a quarter of the bound is not kept, so one large answer cannot flush the rest. A negative `MaxBytes` holds nothing and adds no validator; a `304` to a GET is still sent once more without one.

`State` reports `Hits`, the reads a held copy served, `Misses`, every other stored read the server answered in full, error statuses included, `Saved`, and `Bytes` held. `Saved` counts the served `304`s to requests carrying an `Authorization` header, and only where `CacheConfig.FreeNotModified` says the server bills none. A read sent once more after a `304` counts once.

The cache lives in this process only. A tier on disk or in a shared store would hold response bodies outside the process, and would save one full read per answer after a restart.

## On an http.Client

`NewCacheRoundTripper(next, cfg)` puts a cache on any `http.Client`:

```go
cache := httpclient.NewCacheRoundTripper(nil, httpclient.CacheConfig{FreeNotModified: true})
client := &http.Client{Transport: cache, Timeout: 30 * time.Second}
```

- It keeps the body as `next` delivers it, decoded where `next` decoded it, up to a quarter of the bound. It keeps no answer with a `Content-Encoding`, because the `304` that later serves it need not repeat the coding.
- A served answer reaches you as a `200` with the held body, the `304`'s headers but its `Content-Length` and `Content-Encoding`, and the held fields.
- A body that fails while the cache reads it reaches you as the server sent it: the bytes read, then the error.
- Each redirect hop your client follows is a request of its own.
- A read whose context ends before `RoundTrip` returns, even after its answer was read, fails with the context's error and keeps nothing.
- A response that `next` returns beside an error breaks the `http.RoundTripper` contract. `RoundTrip` returns it beside that error with its body closed, and `http.Client` ignores it. A redirect your `CheckRedirect` refuses is not this case: `RoundTrip` answers that hop with no error, and `http.Client.Do` returns the refusal itself.
- `CloseIdleConnections` reaches `next`.

The cache never answers without the server, and it does no retries. To retry, put a `RetryRoundTripper` beneath it, as `next`.

## Observers and the guard

A round tripper you put inside `next` sees every request that reaches the server, and a `304` as the `304` it is. One around the cache round tripper sees a served answer as a `200`.

Each answer from `next` must report the request it answered as its `Request`, as `net/http`'s `Transport` does. An answer to a request whose method, URL, host or any header `next` changed, or one naming no request, is neither served from nor kept. So a round tripper that sets a credential or any other header, such as an OAuth or signing transport, wraps the cache round tripper rather than sitting inside `next`, where the key would not see what it sets.

## Reading your own answers

`NewCache(cfg)` is the same cache for code that sends, reads and decodes its own answers, and keeps something smaller than the raw body, such as the decoded value re-encoded. Each read is one `CacheRead`:

```go
read := cache.Begin(httpclient.CacheReadConfig{Variant: "[]Item", Store: true, Resend: true})
resp, err := read.Send(func(prepare func(*http.Request) *http.Request) (*http.Response, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        return nil, err
    }
    req.Header.Set("Authorization", "Bearer "+token)
    return client.Do(prepare(req)) // client's transport sits over httpclient.CacheGuard
})
var wire []byte
if err == nil {
    wire, err = httpclient.ReadLimitedBody(resp.Body, 10<<20)
}
if err == nil {
    read.Answered()
    body := wire
    if _, held, ok := read.Serve(resp.Header); ok {
        body = held
    }
    if err = json.Unmarshal(body, &items); err == nil {
        read.Keep(resp, wire, func() ([]byte, error) { return json.Marshal(items) })
    }
}
_, ended := read.Finish(ctx, err == nil)
```

- `Send` runs the attempts: the first, and with `Resend` the one a `304` nothing held may serve forces. Your function builds the request and calls `prepare` last, after every other header is set, because `prepare` keys the request and sets the held copy's validator. A `CacheRead` is sent once: a second `Send` sends nothing and fails.
- The request must travel through `CacheGuard`, placed beneath every round tripper that may change a request or an answer, such as a retry round tripper or an observer. The guard sends a request that differs from the keyed one without validators, and marks an answer it did not get from the wire as given. A read that reaches no guard is never served or kept. A `CacheRoundTripper` guards its own `next`.
- `Answered` records that you read the answer in full, and reports whether `State` counts this read. Skip it when the body fails to read, so the read counts nothing.
- `Serve` answers a `304` the held copy serves: the `304`'s header with the held fields, and a copy of the held body. For any other answer it reports false, and a `304` it refuses is yours to treat as the `304` it is.
- `Keep` keeps what its function returns, for a `200` whose body you read in full and pass, where the wire vouches for the answer, the body you pass is the wire's byte for byte, and the answer has a validator and no `no-store`. A body that a round tripper above the guard replaced is not kept.
- `Finish` settles the read once, whether it succeeded or not. A context ended by then fails the read, which keeps nothing, and its error is returned. A second call changes nothing and returns the first call's result.

`Variant` names what you keep of an answer, such as the Go type it decodes into, so reads that differ only in what they keep never serve each other. `Store` false sends the read with no validator and keeps nothing, for a read that must not be cached; only a `GET` is ever looked up or kept, so `Store` on another method does the same. `Discarded` takes the header of an answer the read drops, a resent `304`'s and a `304` that fails the read, for code that tracks rate headers.
