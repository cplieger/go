# Stylesheets

This page covers style entries, the fonts and images a stylesheet names, and `BuildStyle` with its `cmd/style` command, for a developer building or testing an app's CSS.

## Style entries

A `Style` entry is one CSS file. `Build` inlines its `@import` rules in their order, so an app's stylesheet can start with a design system's sheet and add its own rules after it:

```css
@import "@cplieger/ui/css/system";
@import "./layout.css";
```

A shell links the entry by its logical path, such as `<link rel="stylesheet" href="/app.css">`. A module or classic entry cannot import CSS, since only a style entry builds a stylesheet. That import fails the build, naming the entry.

## Fonts and images

A `url()` in a stylesheet, or an `import` in a script, of a file with one of these extensions writes that file as `assets/<name>-<hash>.<ext>` and puts its root URL in the CSS or the script:

`.woff2`, `.svg`, `.png`, `.webp`, `.avif`, `.ico`

These are the font and image types `httpserver` serves. A `url()` or an import of any other type, such as `.ttf` or `.woff`, fails the build naming the file. No file is inlined into the CSS as a `data:` URL, so the browser caches each file on its own. Two files with one name in two folders get two hashed names.

## One stylesheet alone

`BuildStyle` builds one CSS file exactly as a style entry is built, with the same browser versions, file types, hashed names and minification, and returns its text without writing anything. Its output is byte for byte the style entry's file for the same input.

A TypeScript test that compares the app's CSS with a design system's sheet can get that sheet's built text from the `cmd/style` command, at the webbundle version your `go.mod` pins:

```sh
go run github.com/cplieger/go/webbundle/cmd/style -src web node_modules/@cplieger/ui/css/system.css
```

| Flag or argument | Default | Description |
| --- | --- | --- |
| `-src` | `.` | The source folder the path is under |
| path | required | One `.css` file, a slash path under `-src` |

It prints the stylesheet on standard output and exits 0. A failed build prints the error, naming the file, and exits 1. A usage error prints `usage: style [-src dir] path` and exits 2. To read the app's own built stylesheet, take the `path` of its style entry from `bundle.json`, as [The output tree](output-tree.md) shows.
