# webbundle

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/webbundle.svg)](https://pkg.go.dev/github.com/cplieger/go/webbundle) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=webbundle%2Fgo.mod)](https://github.com/cplieger/go/blob/main/webbundle/go.mod)

webbundle builds your single-page app's web bundle with esbuild at build time. It writes content-hashed scripts and stylesheets, the shells that name them, icons, licence notices and the manifest this repository's `httpserver.SPA` serves.

It replaces the bundle program each app writes around esbuild's Go API. It runs from your Dockerfile's builder stage and from `go generate`, never in your app binary, and needs no Node process once your npm packages are installed. It depends on esbuild, `go/httpserver` and the oksvg and rasterx SVG renderers, and is licensed under Apache-2.0.

## Why use it

webbundle is built for a Go server that serves a TypeScript front end through `httpserver.SPA`.

- Every built file has a content-hashed name, and two builds of the same inputs write the same bytes.
- Syntax newer than the pinned browser versions is lowered, or the build fails naming the feature.
- A shell with an inline script, an inline style or an event handler fails the build, so the strict Content-Security-Policy of `SPA` never blocks your page.
- An import of a package you did not allow fails the build with the chain of imports that reached it.
- Each bundled package's licence files, and those beside your own vendored files, ship in the tree; a package or a vendored font with none fails the build.
- One SVG gives the favicon, touch icon and manifest icons.
- A failed build leaves the last good tree in place.

Consider [Vite](https://vite.dev) if you want a dev server with hot module replacement, which webbundle does not have.

## Install

```sh
go get github.com/cplieger/go/webbundle@latest
```

## Usage

Your bundle program fills one `Config` and calls `Build`. The shells name each entry by its logical path, such as `<script type="module" src="/app.js">`, and `Build` writes the hashed path in its place.

```go
// cmd/bundle/main.go
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/cplieger/go/webbundle"

	"example.com/app/internal/webui"
)

func main() {
	pages := flag.Bool("pages", false, "write the page table and corpus, then stop")
	flag.Parse()
	if *pages {
		writePages()
		return
	}
	_, err := webbundle.Build(webbundle.Config{
		Src:       "web",
		Out:       "internal/webui/dist",
		Pages:     webui.Pages,
		APIPrefix: "/api",
		Entries: []webbundle.Entry{
			{Name: "prepaint", Path: "src/prepaint.ts", Kind: webbundle.Classic, Allow: []string{"@cplieger/ui/theme"}},
			{Name: "app", Path: "src/main.ts", Kind: webbundle.Module},
			{Name: "app", Path: "src/app.css", Kind: webbundle.Style},
		},
		Icon:     "icon.svg",
		Manifest: "manifest.json",
	})
	if err != nil {
		log.Fatal(err)
	}
}

func writePages() {
	table, err := webbundle.RenderPages(webui.Pages, "/api")
	if err != nil {
		log.Fatal(err)
	}
	corpus, err := webbundle.RenderPageCorpus(webui.Pages, "/api")
	if err != nil {
		log.Fatal(err)
	}
	for name, body := range map[string][]byte{"pages.gen.ts": table, "pages.corpus.gen.json": corpus} {
		if err := os.WriteFile(filepath.Join("web", name), body, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
```

Run both steps from `go generate` and commit the two page files. `Build` refuses a page file that no longer matches `Pages`, so the browser router and the server always agree on the pages:

```go
//go:generate go run ./cmd/bundle -pages
//go:generate go run ./cmd/bundle
```

Commit an `Out/.gitignore` that ignores everything else in the folder, so `//go:embed` compiles in a clean clone. `Build` keeps that file byte for byte.

## API

- Building: `Build` with `Config`, `Entry`, `EntryKind` (`Module`, `Classic`, `Style`), `IconPNG` (`IconPurpose`: `PurposeAny`, `PurposeMaskable`) and `Result` (with `Output`, what each built script holds), and `Engines` (`Engine`) for the pinned browser versions.
- Page files: `RenderPages` and `RenderPageCorpus`.
- One stylesheet: `BuildStyle` (`Stylesheet`) and the `cmd/style` command.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/webbundle).

## Licence notices

`Build` copies the licence and notice files of each bundled npm package version to `licenses/<package>@<version>/`, and those beside your own bundled files under `Src` (a vendored font's `fonts/LICENSE-monaspace`) to `licenses/<path under Src>`, and lists them in `licenses/index.txt`. Ship them with one line in your Dockerfile's runtime stage:

```dockerfile
COPY --from=build /src/internal/webui/dist/licenses/ /usr/share/licenses/example-web/
```

A bundled package whose root folder holds no `LICENSE`, `LICENCE`, `COPYING`, `NOTICE` or `THIRD_PARTY_NOTICES` file fails the build, because shipping its code without its licence would breach it. So does a font under `Src` with no such file in its folder or a folder above it. [The output tree](docs/output-tree.md) lists which files count.

## What a build refuses

`Build` checks the whole configuration before it writes anything, and each error names the entry, file or package at fault. It refuses a shell `SPA` would refuse, an import outside the allowed packages, a file type `SPA` does not serve, a stale page file and an icon the renderer cannot draw faithfully. [What a build refuses](docs/checks.md) lists every case.

## Documentation

- [Configuration](docs/configuration.md) lists every field with its default.
- [The output tree](docs/output-tree.md) covers file names, `bundle.json`, the notices and how a build replaces the tree.
- [Pages](docs/pages.md) covers the page table and the page corpus.
- [Icons](docs/icons.md) covers the icon files and the web app manifest.
- [Stylesheets](docs/stylesheets.md) covers style entries, their fonts and images, and `cmd/style`.
- [What a build refuses](docs/checks.md) lists every refusal.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
