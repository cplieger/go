# Configuration

This page lists every field of `Config`, `Entry` and `Manifest`, for a developer writing an app's bundle program. Every path below is a slash path under `Src` unless the row says otherwise.

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
| `IconPNGs` | none | Committed PNGs by size, used instead of rendering the icon |
| `Manifest` | none | The web app manifest. It needs `Icon` |

`Src` and `Out` may be relative to the working directory. `Build` runs `httpserver.NewPageMatcher` over `Pages` and `APIPrefix`, so an empty prefix or a page at or under it fails the build.

## Entry

| Field | Default | Description |
| --- | --- | --- |
| `Name` | required | The logical name a shell uses, `/<Name>.js` or `/<Name>.css`. Letters, digits, `-`, `_` and `.`, with no leading dot |
| `Path` | required | The source file, such as `src/main.ts` |
| `Kind` | required | `Module`, `Classic` or `Style` |
| `Forbid` | none | npm packages by name, or paths under `Src` by whole segments, that the entry's imports may not reach |

A script entry and a style entry may share a name, since one is `/app.js` and the other `/app.css`.

## Entry kinds

- `Module` is an ES module. Code that two module entries share, and every dynamic `import()`, goes into a chunk under `assets/chunks/`.
- `Classic` is a script with its own copy of everything it imports and no chunks, such as a script that sets the theme before the first paint, or the script a shared worker loads.
- `Style` is a stylesheet. Its `@import` rules are inlined in order, and the fonts and images its `url()` values name are written as hashed files. See [Stylesheets](stylesheets.md).

Classic entries build first. Every module entry then reads their URLs through the `BUNDLE_URLS` define, such as `new SharedWorker(BUNDLE_URLS.worker)` for an entry named `worker`. A classic entry read this way needs no shell to name it.

`Forbid` keeps code out of an entry that must not hold it. A shared worker's entry can forbid `@cplieger/ui` and your own `src/dom` folder, so no DOM code reaches the worker. The entry's own file never counts against its `Forbid`.

## Manifest

| Field | Default | Description |
| --- | --- | --- |
| `Name` | required | The app's name |
| `ShortName` | none | The name under a home screen icon. Left out of the manifest when empty |
| `ThemeColor` | required | A `#rrggbb` colour |
| `BackgroundColor` | required | A `#rrggbb` colour |
