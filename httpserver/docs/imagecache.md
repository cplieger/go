# Upstream images

This page is for a developer whose web UI shows images that live on another service, such as the posters a media manager holds behind its API key. Package `imagecache` serves those images from your own origin. It checks each one, keeps it in a bounded directory and answers browsers with an ETag, so the page never needs the upstream's address or credential.

## Usage

The app supplies two things. Its `Fetch` calls the upstream client with the app's own credential. Its route handler checks the request's id against its own library before calling `Serve`. The package makes no HTTP request, holds no credential and checks no id.

```go
root, err := os.OpenRoot("/config/cache/covers")
if err != nil {
	return err
}
covers, err := imagecache.New(imagecache.Options{
	Root: root,
	Fetch: func(ctx context.Context, k imagecache.Key) (imagecache.Image, bool, error) {
		return upstream.Cover(ctx, k.Space, k.ID, k.File) // found false for the upstream's 404
	},
	Errors: catalog.Responder(),
	Logger: logger,
})
if err != nil {
	return err
}

mux.HandleFunc("GET /api/covers/{id}", func(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !library.Has(id) {
		httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "no such item")
		return
	}
	covers.Serve(w, r, imagecache.Key{Space: "movies", ID: id, File: "poster.jpg"})
})

return httpserver.Run(ctx, srv, ln,
	httpserver.WithTeardown(func(ctx context.Context) {
		if err := covers.Shutdown(ctx); err != nil {
			logger.Warn("covers: fetches still running", "err", err)
		}
		_ = root.Close()
	}),
)
```

A `Key` is three strings, `Space`, `ID` and `File`, each at most 128 bytes, built from the app's stored ids only. `Space` groups keys for `ForgetSpace`, for example one upstream instance. `Fetch` returns `found` false with a nil error when the upstream has no such image, and the image is then ignored. A non-nil error wins over `found`.

## Options

A zero numeric field takes its default. Any other value outside its range makes `New` return an error naming the field, its value and its range, such as `imagecache: MaxImage 1000 is outside 64 KiB to 16 MiB`. A nil `Root` or `Fetch` is an error too.

| Field | Range | Default | Meaning |
| --- | --- | --- | --- |
| `Root` | | required | The cache directory. The cache owns every file in it named by 64 lowercase hex digits. |
| `Fetch` | | required | The app's upstream call. |
| `MaxImage` | 64 KiB to 16 MiB | 2 MiB | The largest image kept. |
| `MaxBytes` | 1 MiB to 4 GiB | 256 MiB | The total size of the cache's files on disk. |
| `Refresh` | 1 hour to 30 days | 7 days | The age past which a kept image is refetched in the background. |
| `Fetches` | 1 to 16 | 4 | The `Fetch` calls in flight across the cache, foreground and background together. |
| `Errors` | | `httpserver.WriteError` | Writes `Serve`'s error answers. |
| `Logger` | | `slog.Default()` | Where the log lines below go. |

Use one `Cache` per directory, and keep the app's own files out of it or under other names.

## What is kept

An image is kept only when all of these hold:

- its declared type is `image/jpeg`, `image/png`, `image/gif` or `image/webp`;
- that type equals what `http.DetectContentType` sniffs from its bytes;
- its size is 1 byte to `MaxImage`.

SVG is never kept, since it can carry script. An image that fails a check is dropped, logged at Warn and answered `image_unavailable`.

Each file is named by the SHA-256 of the key's three parts, each prefixed with its length, so two different keys never share a file and no request byte reaches a path. Every write goes through `filesafe.WriteFileInRoot`, so a crash leaves the old file or the new one, never a torn one.

## Serving

`Serve(w, r, k)` answers:

- a kept image at once, with its type, `Content-Length`, a strong `ETag` (the SHA-256 of the bytes) and `Cache-Control: private, max-age=86400`. `HEAD` gets the same headers and no body;
- `304 Not Modified` with the `ETag` and `Cache-Control` when `If-None-Match` matches, as RFC 9110 section 13.1.2 reads it;
- a stale image, one older than `Refresh`, at once too, while one background refetch per key replaces it. A refetch that fails, answers not found or returns an image the checks drop keeps the old image and its old age, so the next stale request tries again;
- with nothing kept, an image fetched under the request's context and kept before it is answered. Requests for one key share one fetch, which is cancelled only when every one of them has left. At most `Fetches` calls run at once across the cache.

`X-Content-Type-Options: nosniff` comes from your chain's `SecurityHeaders`, not from `Serve`.

## Codes

`CodeSpecs()` returns both codes for your app's code table, as a copy. `httpservertest.CheckCodeSpecs` holds them to the package's source.

| Code | Status | `NoEffect` | When |
| --- | --- | --- | --- |
| `image_not_found` | 404 | yes | `Fetch` answered `found` false, or a key part is over 128 bytes |
| `image_unavailable` | 503 | yes | the fetch failed, timed out, panicked or returned an image the checks drop, and nothing is kept; or the cache is shut down. It carries `Retry-After: 60` |

The message is fixed, `image not found` or `image unavailable`, so no fetch error text reaches the client.

## Eviction

`MaxBytes` bounds the sum of the cache's own files, each one its header plus its bytes. After each image is written, the images fetched longest ago are removed until the total fits, so the directory exceeds `MaxBytes` by at most the one image being written. Serving an image does not change its place; refetching it moves it to the newest. An image whose file alone is larger than `MaxBytes` is answered and not kept, with a Warn line. A removal that fails logs Warn and leaves an orphan (see [Orphans](#orphans)).

`Forget(k)` drops one key's image and any fetch of it in flight, and `ForgetSpace(space)` does so for every key in a space, for example when the app removes an item or an upstream instance. The next `Serve` fetches anew. `Forget(k)` removes the key's file whether or not it is kept. `ForgetSpace` also removes every orphan, and returns an error for a space over 128 bytes after doing so. Both return the joined removal errors.

## Restart

Each file carries its key, type, digest and the time it was fetched, so `New` rebuilds the cache from the directory with no index file. It keeps every file that still passes the checks under the current options. It removes a hex-named file that does not, such as one over a lowered `MaxImage`, and logs one Warn line with the count. A file it cannot read, for an I/O error, is left in place as an orphan with a Warn line naming it. It also removes temp files over a minute old that an interrupted write left. Every other name, directory or symlink is left alone.

The fetch time is the one recorded in the file, never its modification time, so a restart changes no image's age: a file older than `Refresh` is served on the first request after the restart and refetched in the background. If `MaxBytes` was lowered, the oldest are evicted until the total fits.

## Orphans

An orphan is a cache file on disk that the cache does not vouch for: one `New` or `Serve` could not read, or one whose removal failed in eviction, `Forget` or `ForgetSpace`. `Serve` never answers an orphan; its key is a miss, and the fetch's write replaces the file. An orphan does not count toward `MaxBytes`. `Forget` of its key removes it, and so does any `ForgetSpace`, since its space is unknown until it is read; removing it costs one refetch.

The cache keeps no record of a forget across a restart, and `New` reads an orphan again like any other file. So a removal error from `Forget` or `ForgetSpace` means the forgotten image may still be on disk and may be served after a restart: retry the call. The error `ForgetSpace` returns for a space over 128 bytes comes after its removals and does not go away on retry.

## Shutdown

`Shutdown(ctx)` cancels every fetch the cache runs, background refetches included, and waits for each to return. It returns `ctx.Err()` when `ctx` ends first, and is safe to call more than once. Call it from `httpserver.Run`'s `WithTeardown` hook, after the drain and before you close `Root`.

After it, `Serve` still answers kept images, stale ones included, without refetching. A miss answers `image_unavailable`, and `Fetch` is never called. `Forget` and `ForgetSpace` still work.

## The fetch budget

A foreground fetch runs under the first request's deadline, or 30 seconds when the request has none. A background refetch always gets 30 seconds. The budget covers the wait for a fetch slot and the `Fetch` call. Each fetch keeps the request's context values, such as its request id, but not its cancellation.

## Log lines

| Level | Line |
| --- | --- |
| Warn | `image dropped`, with the reason, the declared and sniffed types and the size |
| Warn | `key part over 128 bytes`, `image larger than MaxBytes not kept` |
| Warn | `write failed`, `remove failed`, `open failed`, `stat failed`, `read failed`, `cache file unreadable`, `cache file not read` |
| Warn | `cache files it cannot keep` and `stale temps not removed`, from `New` |
| Debug | `fetch failed` and `fetch not started`, because the app's upstream client already logs its failures and a line per request would flood while the upstream is down |
| Error | `Fetch panicked`, with the stack. The panic is recovered and answered as a failed fetch |

Each line starts with `imagecache:` and names the key's `Space` and `File`, or the file name, never the `ID`.
