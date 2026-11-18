# Configuration

This page lists every field of `Config` and `Entry`, for a developer writing an app's bundle program. Every path below is a slash path under `Src` unless the row says otherwise.

## Config

| Field | Default | Description |
| --- | --- | --- |
| `Src` | required | The source folder: the shells, `package.json` and `node_modules` |
| `Out` | required | The output folder your UI package embeds. It is replaced whole, so it sits beside `Src`, never inside it |
| `Shells` | `["index.html"]` | The shell files at the root of `Src`. The list must hold `index.html` |
| `Entries` | none | The entries to build, 32 at most |
| `Pages` | none | Your UI package's `[]httpserver.Page`, the same table you pass to `SPA` |
| `APIPrefix` | required | The API's path prefix, such as `/api`, as in `httpserver.SPAConfig` |
| `PagesFile` | `pages.gen.ts` | The committed TypeScript page table |
| `PageCorpusFile` | `pages.corpus.gen.json` | The committed page corpus |
| `Packages` | `["@cplieger/*"]` | The npm packages a bundle may include, as `path.Match` patterns |
| `Define` | none | esbuild defines, each value a JSON value. `BUNDLE_URLS` is reserved |
| `Icon` | none | The app's icon, an `.svg` file. See [Icons](icons.md) |
| `MaskableIcon` | none | An `.svg` file drawn for a mask, listed in the manifest as `maskable`. It needs `Manifest` |
| `IconPNGs` | none | Committed PNGs by size and purpose, used instead of rendering `Icon` or `MaskableIcon` |
| `Manifest` | none | The web app manifest, a JSON file. It needs `Icon`. See [The manifest](icons.md#the-manifest) |

`Src` and `Out` may be relative to the working directory. Symbolic links in them are resolved before `Build` checks that `Out` sits beside `Src`. An entry's path may run through a symbolic link, such as a pnpm package folder, as long as its target is under `Src`. `Build` runs `httpserver.NewPageMatcher` over `Pages` and `APIPrefix`, so an empty prefix or a page at or under it fails the build.

## Entry

| Field | Default | Description |
| --- | --- | --- |
| `Name` | required | The logical name a shell uses, `/<Name>.js` or `/<Name>.css`. A lower-case letter, then lower-case letters, digits and `_`, such as `feed_worker`, so a module reads it as `BUNDLE_URLS.<Name>` |
| `Path` | required | The source file, such as `src/main.ts` |
| `Kind` | required | `Module`, `Classic` or `Style` |
| `URL` | hashed | For a classic entry, a root URL such as `/sw.js` to write it at unhashed. See [An entry at its own URL](#an-entry-at-its-own-url) |
| `Forbid` | none | Items the entry's imports may not reach |
| `Allow` | none | When set, the only items the entry's imports may reach |

A script entry and a style entry may share a name, since one is `/app.js` and the other `/app.css`.

## Entry kinds

- `Module` is an ES module. Code that two module entries share, and every dynamic `import()`, goes into a chunk under `assets/chunks/`.
- `Classic` is a script with its own copy of everything it imports and no chunks, such as a script that sets the theme before the first paint, or the script a shared worker loads.
- `Style` is a stylesheet. Its `@import` rules are inlined in order, and the fonts and images its `url()` values name are written as hashed files. See [Stylesheets](stylesheets.md).

Classic entries build first. Every module entry then reads their URLs through the `BUNDLE_URLS` define, such as `new SharedWorker(BUNDLE_URLS.worker)` for an entry named `worker`. A classic entry read this way needs no shell to name it. The dotted read is the one form: an entry named `feed_worker` is `BUNDLE_URLS.feed_worker`.

## An entry at its own URL

A classic entry with a `URL` is written at that path instead of under `assets/` with a hash, and keeps it in every build. A service worker needs one, because the browser looks for a worker's update at the URL it registered, and a hashed URL changes with every build. The scope is not the reason: a `Service-Worker-Allowed` header would let a worker under `/assets/` control `/`.

```go
webbundle.Entry{Name: "sw", Path: "src/sw.ts", Kind: webbundle.Classic, URL: "/sw.js"}
```

The URL is one file at the root ending in `.js`. Its name may differ from the entry's, though one rule still holds: a module entry reads it as `BUNDLE_URLS.sw`, such as `navigator.serviceWorker.register(BUNDLE_URLS.sw)`, or a shell names it. `bundle.json` marks the entry `"stable": true`, and `SPA` serves it with `Cache-Control: no-cache`, so every load asks whether it changed.

## Forbid and Allow

`Forbid` keeps code out of an entry that must not hold it. A shared worker's entry can forbid `@cplieger/ui` and your own `src/dom` folder, so no DOM code reaches the worker. `Allow` holds an entry to the files it names. A script that sets the theme before the first paint blocks rendering, so its entry can allow only `@cplieger/ui/theme`, and any other file it pulls in fails the build. The entry's own file never counts against either list, and neither does a file whose code the entry does not ship: an import of `@cplieger/ui/theme` that pulls in a module whose exports the script never uses stays within an `Allow` of `@cplieger/ui/theme`. [What a build refuses](checks.md#imports) says which files ship.

Both lists take the same items:

| Item | Matches |
| --- | --- |
| `@cplieger/ui`, `lit` | every file of that npm package |
| `@cplieger/ui/theme` | the file an import of `@cplieger/ui/theme` resolves to, through the package's `exports`, in every installed copy of the package. A style entry resolves it as a CSS `@import` does, a script entry as an `import` statement does |
| `src/wire` | that file or folder under `Src` and everything under it, by whole path segments, so `src/wire` does not cover `src/wirelike.ts` |
| `./lit/core` | a path under `Src` whose first segment is also the name of an installed package |

An item that exists under `Src` is a path. Any other item is a package or a file a package exports. An item that can match nothing fails the build before anything is built: an empty item or `.`, a path leaving `Src`, a path under `node_modules` (name the package instead), a package `Packages` does not match, a package file that resolves to no file or to another package's file, and a path that is also a package name, written without `./`.
