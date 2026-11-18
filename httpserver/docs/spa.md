# Single-page apps and the outage page

This page is for a developer serving a browser app's built bundle from a Go binary. It covers `SPA`, the bundle manifest, the page table, the Content-Security-Policy, caching, a build with no web UI, the test helpers and the outage page an app shows while a file it needs cannot open.

## Serving a bundle

`SPA(SPAConfig{...})` serves a bundle tree, typically an `embed.FS`, for `GET` and `HEAD`. The tree holds the shells, which are the HTML files a page loads, the built assets under `assets/`, the licence notices under `licenses/`, and the manifest `bundle.json` that a bundler writes at its root.

```go
//go:embed all:dist
var dist embed.FS

var Pages = []httpserver.Page{
	{Name: "home", Pattern: "/{$}"},
	{Name: "library", Pattern: "/library"},
	{Name: "item", Pattern: "/library/{id}"},
	{Name: "login", Pattern: "/login", Shell: "login.html"},
}

func routes(api http.Handler, errs httpserver.ErrorResponder) (http.Handler, error) {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	ui, err := httpserver.SPA(httpserver.SPAConfig{
		Assets:    assets,
		Pages:     Pages,
		APIPrefix: "/api",
		CSPAdd:    map[string][]string{"img-src": {"blob:"}},
		Errors:    errs,
	})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", ui)
	return mux, nil
}
```

| Field | Meaning |
| --- | --- |
| `Assets` | The bundle tree. Nil, or a tree with no `bundle.json`, is an unbuilt UI |
| `Pages` | The page table, each pattern answering its shell |
| `APIPrefix` | The API's path prefix, such as `/api`. Required. `SPA` answers nothing at or under it |
| `CSPAdd` | Additions to the default Content-Security-Policy, from a short allowed list |
| `Errors` | The `ErrorResponder` every refusal goes through. Nil is `WriteError` |
| `Logger` | Where the unbuilt warning goes. Nil is `slog.Default()` |

`SPA` reads the manifest and every shell once, at start. Its error names the manifest entry, shell, `CSPAdd` value or page it refuses. The bundle is embedded at build time, so an error is a build defect, and the app fails its start on it.

## The manifest

`bundle.json` lists every file the bundler wrote, by its path in the tree, with its SHA-256 in lowercase hex, and every entry it built:

```json
{
  "files": {
    "index.html": "9c1f...",
    "assets/app-3f2a91.js": "3f2a...",
    "assets/app-77d0c4.css": "77d0...",
    "assets/feed-worker-b81e.js": "b81e...",
    "licenses/THIRD_PARTY_NOTICES.txt": "51aa..."
  },
  "entries": [
    {"name": "app", "kind": "module", "path": "assets/app-3f2a91.js"},
    {"name": "app", "kind": "style", "path": "assets/app-77d0c4.css"},
    {"name": "feed-worker", "kind": "classic", "path": "assets/feed-worker-b81e.js"}
  ]
}
```

`Bundle`, `BundleEntry` and `BundleKind` are its Go types, so a bundler writes the file through the same types `SPA` reads it with. An entry's kind is `module` (an ES module with shared chunks), `classic` (a self-contained script, such as a worker host or a script that runs before the first paint) or `style` (CSS). `ReadBundle(fsys)` reads and checks it, and answers found false with no error when the tree has none. It refuses:

- unknown keys, or data after the object
- a listed path that is not a regular file in the tree, or holds a segment starting with `.`
- a file at the root that is not an `.html` shell, or a nested file outside `assets/` and `licenses/`
- a file named `index.html` under `assets/` or `licenses/`, since `http.FileServer` redirects any path ending in `/index.html`
- a hash that is not 64 lowercase hex digits
- an entry with no name, an unknown kind, a path outside `assets/` or not listed, or the same logical file twice (`app` of kind `module` and `app` of kind `classic` are both `/app.js`).

`SPA` serves only the files the manifest lists, so a stray file, a sourcemap or an earlier build's leftover is never served. It takes every ETag from the listed hashes instead of hashing the tree.

## What each path answers

The first row that applies answers:

| Request | Answer |
| --- | --- |
| A path at or under `APIPrefix`, by its raw or its cleaned path | `not_found` (404) |
| A method other than `GET` or `HEAD` | `method_not_allowed` (405) with `Allow: GET, HEAD` |
| A listed file under `assets/` whose extension is in the content-type table | The file through `StaticHandler`, with gzip when offered |
| A listed notice under `licenses/` | The notice as `text/plain; charset=utf-8`, whatever its extension |
| `/favicon.ico` | 204 with no body |
| Any other path under `assets/` or `licenses/` once its dot segments are cleaned, the manifest, or a shell's own file name such as `/index.html` | `not_found` (404) |
| A page path | Its shell, 200, or the 307 redirect `ServeMux` answers it with |
| A path no page matches with a segment starting with `.` | `not_found` (404) |
| Any other path | The default shell, `index.html`, with 404, so the router draws its not-found screen |

Every refusal is the error envelope written through `Errors`, with `Cache-Control: no-store` set before `Errors` runs, so no cache keeps a refusal after the build that fixes it. A missing hashed asset or notice therefore never answers HTML. The bundle's own paths answer before the pages, so a page pattern such as `/` or `/{a}/{b}` never shadows an asset, a notice, the favicon or the manifest. A browser asks for `/favicon.ico` only when a shell names no icon, and the 204 keeps that out of its console.

The dot-segment refusal applies to paths no page matches, so no dot-file in the tree is ever served. A page's capture is the app's to judge: under a page `/library/{id}`, `/library/.env` answers the page's shell, as the browser router matches it, and the app's own route refuses an id it does not accept. A tail page `/file/{path...}` takes every depth the same way: `/file/a/b/c.ts` and `/file/.github/x.yaml` answer its shell, and `/file` redirects to `/file/`. `/.env` and `/assets/.env` answer `not_found`.

## Pages

A `Page` has a `Name`, the route's name in the browser router, a `Pattern` and a `Shell`, the shell file it answers with. An empty `Shell` is `DefaultShell`, `index.html`. An app with a separate sign-in bundle gives its sign-in pages their own shell, so that bundle never loads the app's code.

`CheckPages(pages)` refuses a table `SPA` cannot serve, and a bundler calls it too:

- more than 256 pages or more than 8 distinct shells
- a name that is empty, repeated, or not an ASCII identifier
- a pattern that is not a valid `http.ServeMux` pattern, or names a method or a host
- two patterns that `ServeMux` calls conflicting, because each matches a path the other does and neither is more specific, such as `/a/{x}` beside `/{y}/b` or one pattern twice. The error names both pages.
- a shell that is not an `.html` file name at the bundle's root.

`NewPageMatcher(pages, apiPrefix)` builds the matcher `SPA` uses. It refuses what `CheckPages` refuses, an `apiPrefix` that is not a clean literal path with no trailing slash, and a page whose pattern's literal part is at or under `apiPrefix`. `Match(target)` takes the escaped path and query as the client sent them. It answers a `PageMatch` holding the page's `Name` and its wildcard `Params`, or a `Redirect`.

The matcher registers each pattern on a real `http.ServeMux` and serves the path through it, so matching, path cleaning and the trailing-slash redirect are `ServeMux`'s own. `/library/a%2Fb` matches `/library/{id}` with `id` = `a/b`. `/library/x/../a` and `//library` redirect to their cleaned path, and `/docs` redirects to `/docs/` when `/docs/` is a pattern. `SPA` answers a redirect 307 with its `Location`, the query kept. A `%2e` segment is not cleaned and matches as a literal. A browser never sends one, because its URL parser removes dot segments first.

## The Content-Security-Policy

`SPA` writes one `Content-Security-Policy` on every answer it serves, including assets, notices, the 204 and its own refusals. A worker script runs under the policy of its own script's response, not the page's, so an asset without the policy would let a worker run unrestricted. The default is strict, with no hash and no `'unsafe-inline'`:

```text
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'
```

`CSPAdd` may add `blob:` or `data:` to `img-src`, `font-src` and `media-src`, which then starts from `'self'`, and nothing else. `SPA` refuses another directive, another source, an empty list, and a source the directive already holds. `SPAPolicy(add)` returns the policy `SPA` writes for an addition, and the same refusal.

Because the policy allows no inline script or style, `SPA` refuses at start a shell holding an inline `<script>` or `<style>` element, a `style` attribute, or an `on…` event-handler attribute such as `onload` or `onclick`, whose inline code the policy would also block. It also refuses a shell that ends inside a `<script>`, because a browser never runs a script that the end of the page closed.

The shell is read once, through `html.Parse` from `golang.org/x/net/html`, which implements the HTML5 tree-building algorithm, with scripting on. Only an element in the tree it builds counts. A tag inside a comment, inside the text of a `<title>`, `<textarea>`, `<noscript>` or `<script>`, or inside another attribute's value is no element, so `<!-- <button onclick="x"> -->`, `title="onclick"` and `data-on` pass. An element inside `<svg>` or `<math>` counts, including an `<svg>` `<script>` or `<style>`. Attribute names are matched case-insensitively. A `<script>` whose `type` is not a script type, such as `application/json` or `text/template`, is a data block that never runs, and it passes. The error names the shell, the element and the attribute. `CheckShell(body)` is that check, for a bundler to run before it writes a shell; its error names the element and the attribute, and the bundler adds the shell's name.

In two cases the tree `html.Parse` builds is not the whole shell, and `SPA` refuses the shell, because markup it cannot read it cannot check:

- A shell nested deeper than 512 open elements, which `html.Parse` refuses. The error says the shell cannot be read as HTML.
- A shell holding a `<template>` start tag while an `<svg>` or `<math>` element is open, such as inside `<foreignObject>`, `<desc>`, `<title>` or `<annotation-xml>`. `html.Parse` drops the markup after that tag, which a browser goes on to read. The error names the `<template>` inside `<svg>` or `<math>`. A `<template>` outside them is read as usual, and so is a `<template>` tag in svg markup itself, which is an svg element.

`SPA` owns `Content-Type`, `Cache-Control`, `ETag` and `Content-Security-Policy` on its answers. `X-Content-Type-Options`, `Referrer-Policy`, `X-Frame-Options` and HSTS stay with the app's outer `SecurityHeaders`, as on every other answer.

## Caching

| Answer | `Cache-Control` | `ETag` |
| --- | --- | --- |
| A `module` or `style` output, a chunk, a font, an image | `public, max-age=31536000, immutable` | The manifest hash |
| A `classic` entry, a shell, a notice | `no-cache` | Bound to the policy |
| The default shell answered with 404 | `no-cache` | None |
| A refusal, `ui_unbuilt` included | `no-store` | None |

A file under `assets/` is named by its content hash, so it can be cached for a year. A `classic` entry is the exception, because it runs under the policy on its own response, and `SPA` writes that policy at serve time. A build that changes only the policy keeps the script's name, so a year-long cache would keep the old policy on it.

Every `no-cache` ETag is computed once at start as the first 32 hex digits of SHA-256 over the file's manifest hash, a zero byte and the policy, quoted. A changed policy therefore changes the tag, and the next revalidation answers 200 with the new header. Nothing then depends on a browser merging a 304's headers into its stored response (RFC 9111 section 4.3.4). An unchanged build and policy still costs a 304. The gzip body of each file answers under its tag plus `-gz`.

## Unbuilt

With no `bundle.json`, `SPA` logs one Warn at start naming `go generate ./...`, every page path answers `ui_unbuilt` (503), and every other path answers `not_found`, each with `Cache-Control: no-store`, so the browser asks again once the bundle is built. So `go test` and a backend-only build run without the bundler's toolchain.

## A build with no web UI

`NotFound(errs)` answers every request `not_found` (404) through `errs`, nil being `WriteError`. Mount it at `/` in a build that ships no web UI, so every path outside the API answers the envelope.

## Testing

The `httpservertest` package holds the checks, so `httpserver` itself imports no `testing`.

`CheckSPA(t, h, c)` fails `t` unless `h` serves `c.Assets` as `SPA` built from `c` must. `h` may be the `SPA` handler alone or the app's whole handler with `SPA` at `/`. It requests:

- every page at a sample path, comparing the body with its own shell and running `CheckShell` on it
- every listed file, with and without gzip, checking its body, content type, cache policy and ETag, and for a `classic` entry or a notice the policy-bound tag
- every unlisted file in the tree, a made-up file under `assets/` and `licenses/`, a sourcemap path, dot-files, the manifest and the shells' own file names
- an unknown path, `/favicon.ico`, a same-origin and a cross-site `POST`, and a path under `APIPrefix`.

The bundle's own paths are checked whatever the pages match. Only an unknown-path or dot-file probe outside `assets/` and `licenses/` that a page matches is left out, since that page answers it.

Every answer outside `APIPrefix` must carry exactly the policy `SPAPolicy(c.CSPAdd)` returns, and every refusal `SPA` writes must decode as the envelope with its `request_id` equal to `X-Request-ID` and carry `Cache-Control: no-store`. A missing or unreadable bundle fails at once, so run it on a fixture tree or on the built one.

`CheckNoUI(t, h)` fails `t` unless `h` answers `not_found` (404) to `GET`, `HEAD` and `POST` on the root, a shell, the manifest, an asset, a notice, `/favicon.ico` and an unknown path. Every answer but a `HEAD` one carries the envelope.

## The outage page

`OutagePage(OutageConfig{...})` serves the static page an app shows in place of `SPA` while a file it needs cannot open, such as a damaged accounts database. The app chooses between the two handlers per request.

```go
page, err := httpserver.OutagePage(httpserver.OutageConfig{
	Assets:    assets,
	Style:     "app.css",
	Title:     "myapp cannot start",
	App:       "myapp",
	APIPrefix: "/api",
	Files:     []httpserver.OutageFile{{Name: "ACCOUNTS_PATH, /config/accounts.db", Class: httpserver.OutageDamaged}},
	Notice:    "Sign-in is off until the accounts file opens.",
	Commands: []httpserver.OutageCommand{{
		Run:  "docker exec myapp myapp accounts restore <archive>",
		Note: "Restores the accounts file from an export archive.",
	}},
})
```

| Field | Meaning |
| --- | --- |
| `Assets` | The bundle. Its style entry styles the page when built |
| `Style` | The style entry's logical name, such as `app.css`. Empty is unstyled |
| `Title` | The page's title and heading. Required |
| `App` | The app's name, written into the class sentences. Required |
| `APIPrefix` | The API's path prefix. Required |
| `Files` | Each file that cannot open, as a `Name` and a `Class`. Each `Name` is required |
| `Notice` | One line drawn above the commands. Empty draws none |
| `Commands` | The commands that recover the files, in order. Each is a `Run`, the command line as run in the container and required, and a `Note` drawn under it. An empty `Note` draws none |
| `Restart` | The closing line names a container restart instead of a reload |
| `Errors` | Answers under `APIPrefix` and for a refused method. Nil is `WriteError` |

The page writes each file's `Name` and the one sentence of its `OutageClass`, never an error's text. An error can carry a path, a key id or bytes someone wrote into the file, so it belongs in the log. The classes are a closed set, and any other value is refused at construction:

| Class | Sentence |
| --- | --- |
| `OutageDamaged` | The file is damaged. |
| `OutageFormat` | The file was written by another version of _App_, or is not _App_'s file. |
| `OutageNewer` | The file was written by a newer version of _App_. |
| `OutageUnreadable` | The file cannot be read. |
| `OutageKey` | The key file is missing or does not match this accounts file. |
| `OutageUnwritable` | The file cannot be written. Free space on its disk. |

The closing line is "Reload this page once the command finishes.", since a repair runs in the serving process. With `Restart` it is "Run the command above or restart the container, then reload this page." Each file's `Name`, the `Notice`, and each command's `Run` and `Note` go through `go/textsafe`'s single-line sanitizer, which turns control characters, line breaks, bidi controls and the Unicode line and paragraph separators into spaces. Each is then cut at 512 bytes, with `...` marking the cut. A `Notice` or `Note` left with nothing but spaces draws none.

The page is rendered once, through `html/template`, with no script and no inline style. Every `GET` or `HEAD` outside `APIPrefix` answers it with 503, `Cache-Control: no-store`, `text/html` and `SPA`'s default policy. The exception is a file the manifest lists under `assets/`, which answers exactly as `SPA` serves it, so the stylesheet and the fonts and icons it loads arrive. A path at or under `APIPrefix` answers `not_found`, since the API answers its own outage, and another method answers `method_not_allowed` with `Allow: GET, HEAD`. Both refusals carry `Cache-Control: no-store`, as `SPA`'s do.

`OutagePage` refuses an empty or blank `Title` or `App`, an `APIPrefix` that is not a clean path, a file `Name` or command `Run` left with nothing but spaces after cleaning, a class outside the six, a manifest `ReadBundle` refuses, and, with a built bundle, a `Style` naming no style entry.
