# The output tree

This page describes what `Build` writes to `Out`, for a developer serving it with `httpserver.SPA` or shipping it in an image.

## Layout

| Path | What it holds |
| --- | --- |
| `<shell>.html` | Each shell, with its logical paths replaced by hashed ones and, with an icon, the icon links, written by `html.Render`: implied tags such as `<head>` are written out, attributes are double-quoted and void elements end in `/>` |
| `<file>.js` | Each classic entry with a `URL`, such as `sw.js`, unhashed |
| `assets/<name>-<hash>.<ext>` | Each other entry's output, the fonts and images it uses, and the icon files |
| `assets/chunks/<name>-<hash>.js` | The code module entries share, and each dynamic `import()` |
| `licenses/` | The licence notices of the bundled npm packages and of the bundled files under `Src` |
| `bundle.json` | Every file above with its SHA-256, and every entry |
| `.gitignore` | Your committed file, kept byte for byte, when `Out` had one |

Every file under `assets/` carries a content hash in its name, so `SPA` can cache it for a year. A classic entry with a `URL` keeps its name in every build, so `SPA` serves it `no-cache`. Built scripts, stylesheets and their files take esbuild's hash. The icon files and `manifest.webmanifest` take the first 16 hex digits of their SHA-256. Every URL the tree writes is a root path, such as `/assets/logo-KQ3W7N2X.svg`, so a page at `/library/a` loads the same files as `/`.

## Scripts and stylesheets

The output is minified UTF-8 with no sourcemap. A sourcemap would publish your sources to anyone who reaches the port, because `SPA` serves every file in the tree.

The browser versions `Engines` returns are pinned in webbundle's source. In this release they are Chrome 157, Firefox 160 and Safari 27. For syntax one of them lacks, such as a `using` declaration for Safari, esbuild writes older syntax or fails the build naming the feature and the browser. Lowering adds no runtime code, so an API a browser lacks still fails in that browser. An app cannot set its own versions.

## bundle.json

`bundle.json` is the manifest `SPA` reads at start. It lists every file in the tree, except itself and `.gitignore`, with its SHA-256 in lowercase hex, and every entry with its logical name, kind and path. An entry with a `URL` is marked `"stable": true`, and the key is left out for the others:

```json
{
  "files": {
    "assets/app-DMFFLALO.css": "dcfc11b5ef9a11de...",
    "assets/app-XHG7BGA7.js": "8aba6dbacb27b943...",
    "assets/prepaint-FWXNCLNH.js": "0f3c41d0a6e2b7a9...",
    "index.html": "5be0c7e43f1d92a8...",
    "licenses/@cplieger/ui@1.3.0/LICENSE": "9a1e6f20c4d3b857...",
    "licenses/index.txt": "e2c4a7f9013b6d58...",
    "sw.js": "96568d665ef6b61a..."
  },
  "entries": [
    {"name": "sw", "kind": "classic", "path": "sw.js", "stable": true},
    {"name": "prepaint", "kind": "classic", "path": "assets/prepaint-FWXNCLNH.js"},
    {"name": "app", "kind": "module", "path": "assets/app-XHG7BGA7.js"},
    {"name": "app", "kind": "style", "path": "assets/app-DMFFLALO.css"}
  ]
}
```

The hashes above are cut short, and the file holds all 64 digits. Its Go types are `httpserver.Bundle` and `httpserver.BundleEntry`, so a test can read it with `httpserver.ReadBundle`. To find the built stylesheet of the entry named `app`, take the `path` of the entry whose `name` is `app` and whose `kind` is `style`.

## What each script holds

`Result.Outputs` maps every script in the tree, entries and chunks, by its path under `Out`, to an `Output`: `Packages`, the sorted npm packages whose code it holds, and `Own`, whether it holds code from a file under `Src` outside `node_modules`. A file that puts no code in a script, such as a module of types, does not count. `Own` tells a chunk built only from packages from one that also holds the app's code, though both list the same packages. An app's bundle program can read it, for example to choose which chunks a service worker caches ahead. `bundle.json` does not record it, because `SPA` has no use for it.

## Licence notices

For each npm package version the bundle holds, `Build` copies its notice files to `licenses/<package>@<version>/` and writes one `<package> <version>` line per directory to `licenses/index.txt`, sorted:

```text
@cplieger/reactive 1.4.0
@cplieger/ui 1.3.0
```

A notice file is a regular file whose name, upper-cased, starts with `LICENSE`, `LICENCE`, `COPYING`, `NOTICE` or `THIRD_PARTY_NOTICES`. A file of a type the bundle reads, such as `license.js`, `NOTICE.css` or `license.json`, is code and is left out. So `LICENSE.md`, `NOTICE`, `COPYING.LESSER` and `LICENSE-OFL-1.1` are copied, and `README.md` is not.

`Build` reads the package root and every folder between the root and a file it bundles. A notice in such a folder covers the files beside it, so it keeps its path inside the package, such as `licenses/@cplieger/ui@1.3.0/fonts/LICENSE-atkinson-hyperlegible-next`. Only the root must hold one, and a package whose root holds none fails the build.

The version comes from the package's `package.json`. A package with no version, or a version that is not semver, fails the build. A package bundled at two versions gets two directories. Two installed copies of one version, such as pnpm's copies for two sets of peers, share a directory, and fail the build when their notices differ.

Your own files under `Src`, outside `node_modules`, follow the same folder rule. For each file the bundle holds, including the icon, its committed PNGs, the manifest and the files the manifest names, `Build` copies the notice files in its folder and in every folder above it up to `Src` to `licenses/<path under Src>`. Each gets one `./<path under Src>` line in `licenses/index.txt`, sorted with the package lines:

```text
./fonts/LICENSE-monaspace
@cplieger/ui 1.3.0
```

To ship the licence of a font or a script you vendored, put its file beside it under a notice name, such as `fonts/LICENSE-monaspace`. Only a font must have one: a bundled font file under `Src` (a type `httpserver` serves as `font/`, today `.woff2`) with no notice file in its folder or a folder above it fails the build, as a package with none does, because a font licence such as the OFL requires its text with every copy.

The runtime stage of your Dockerfile copies the folder beside the notices of your Go modules:

```dockerfile
COPY --from=build /src/internal/webui/dist/licenses/ /usr/share/licenses/example-web/
```

## How a build replaces the tree

`Build` writes the new tree into a temporary folder beside `Out`, named `.<Out>.webbundle-<random>`. Once the tree is whole, it renames `Out` aside, renames the new tree to `Out` and removes the old one. A build that fails before that point removes its temporary folder and leaves `Out` as it was. A build that was killed leaves its temporary folder behind, and the next build removes it.

`Build` reads `Out/.gitignore` before it starts and writes it into the new tree byte for byte, with its file mode. With no `Out/.gitignore`, none is written. The tree holds at most 64 MiB.
