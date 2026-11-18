# What a build refuses

This page lists every case `Build` refuses, for a developer reading one of its errors. Each error starts with `webbundle:` and names the field, entry, shell, file or package at fault. A refused build leaves `Out` as it was.

## The configuration

`Build` checks these before it bundles anything:

- an empty `Src` or `Out`, a `Src` that is not a folder, an `Out` that exists and is not a folder, or an `Out` and a `Src` that overlap
- more than 32 entries, or two entries with one logical path, such as two module entries named `app`
- an entry with no name, or a name that is not a lower-case letter followed by lower-case letters, digits and `_`, the identifier form `BUNDLE_URLS.<name>` reads
- an entry whose file is not a regular file under `Src`, or is a symbolic link to a file outside `Src`, a style entry that is not a `.css` file, a module or classic entry that is, or an unknown kind
- a `URL` on an entry that is not classic, a `URL` that is not `/<file>.js` with a file name of ASCII letters, digits, `-`, `.`, `_` and `~` not starting with `.`, or a `URL` that is, ignoring case, another entry's `URL` or logical path
- a `Forbid` or `Allow` item that can match nothing, as [Forbid and Allow](configuration.md#forbid-and-allow) lists
- a `Define` value that is not JSON, or a `Define` of `BUNDLE_URLS` or under it
- `Shells` without `index.html`, a shell that is not an `.html` file at the root of `Src`, a shell listed twice, or a page whose `Shell` is not in `Shells`
- a `PagesFile` or `PageCorpusFile` outside `Src`, or a `Packages` pattern `path.Match` rejects
- an `Icon` or `MaskableIcon` that is not an `.svg` file, a `Manifest` without an `Icon`, a `MaskableIcon` without a `Manifest`, or `IconPNGs` naming PNGs of an icon that is not set
- a manifest that is not one JSON object, has no `name`, holds `icons`, or has a `theme_color` or `background_color` that is not `#rrggbb`
- a manifest `id`, `start_url`, `scope`, shortcut `url` or `share_target.action` that is not a root-relative URL or holds a dot segment, or, but for `id`, lies outside the scope
- a manifest `src` that is not a path from the manifest's folder to an image file under `Src` that `httpserver` serves

## Pages

- a page table `httpserver.NewPageMatcher` refuses, or a page named `__proto__`
- a page table or page corpus under `Src` that is missing, or differs from what `Pages` and `APIPrefix` render

Run `go generate` to write both files again. [Pages](pages.md) describes them.

## Shells

`Build` reads each shell once, with `html.Parse` from `golang.org/x/net/html`, which follows the HTML parsing rules a browser does. Only the HTML `<script>` and `<link>` elements of that tree are read and rewritten: a tag inside a comment, inside an attribute value, or inside the text of a `<title>`, `<textarea>`, `<noscript>` or `<script>` element is text, and a tag inside `<svg>` or `<math>` is an element of that markup, unless the parser returns to HTML there (an svg `<title>` or `<desc>`, or an end tag such as `</p>` that closes the foreign content). `Build` edits the tree's elements, appends the icon links to its head, and writes the shell with `html.Render`, then parses the written shell again. It refuses a shell that `httpserver.SPA`'s Content-Security-Policy would block, that names entries wrongly, or that does not read back as written:

- what `httpserver.CheckShell` refuses, the check `SPA` runs at start: an inline `<script>` or `<style>` element, a `style` attribute, an event handler attribute such as `onclick`, which the policy blocks as it blocks an inline script, and a shell `html.Parse` cannot read whole. A `<script>` of a type that is no script, such as `text/template`, never runs and passes
- a root path ending in `.js` or `.css`, such as `/main.js`, that is no entry's logical path
- one logical path named twice in the shell
- a logical path in a tag that does not load its entry's kind: a module entry needs `<script type="module" src>`, a classic entry a `<script src>` with no type or a JavaScript one, and a style entry a `<link rel="stylesheet" href>` that is not an alternate stylesheet
- a module or style entry no shell names, or a classic entry that no shell names and no module entry reads through `BUNDLE_URLS`
- with an `Icon`, a shell that already carries an `icon`, `apple-touch-icon` or `manifest` link
- a written shell whose `<script>` and `<link>` elements, read again, are not the edited tree's, with the same attributes in the same order, or that `httpserver.CheckShell` refuses. `html.Render` does not promise that every tree reads back as itself: a `<plaintext>` start tag inside a table, which the parser moves before the table, makes every element after it text

## Imports

`Build` reads esbuild's record of every file each entry pulls in, and refuses:

- a file outside `Src`, reached by an import, an `@import` or a `url()`, or through a symbolic link whose target leaves `Src`; a link that stays inside `Src`, such as a pnpm package folder linked into `node_modules/.pnpm`, is followed
- a file from an npm package that `Packages` does not match
- a file an entry's `Forbid` names, or, when the entry has an `Allow`, a file its `Allow` does not name; the items are those of [Forbid and Allow](configuration.md#forbid-and-allow)
- a script entry that imports CSS

Each of these errors ends with the chain of imports from the entry to the refused file.

## Files

- a file esbuild cannot load, such as a `url()` or an import of a `.ttf` font, named with its place in the source
- syntax a pinned browser lacks that esbuild cannot lower, named with the feature and the browser
- an entry whose built file holds no code: empty or only whitespace, such as a stylesheet of comments or a module of types, or a classic script whose files put nothing in it, which esbuild still wraps; a module entry that only loads a shared chunk is not empty
- a written file whose extension `httpserver` does not serve, outside `licenses/`
- more than 64 MiB of output
- a built tree that `httpserver.ReadBundle` refuses, such as a notice under a package folder whose name starts with a dot, which `SPA` never serves

## Licence notices

- a bundled package whose `package.json` is missing, does not parse, has no version, or has a version that is not semver
- a bundled package whose root holds no notice file
- two installed copies of one package version whose notice files differ
- a bundled font under `Src` with no notice file in its folder or a folder above it up to `Src`
- a notice under `Src` whose path under `licenses/` is also a package's notice, such as one in a folder named `@scope/name@1.0.0`

[The output tree](output-tree.md) describes which files are notices.

## Icons

- an SVG the renderer cannot draw as a browser does, naming the element or attribute, such as `<text>`
- a root that is not `<svg>`, or a `viewBox` that is not square
- a `transform` the SVG grammar does not read, a singular one, two on one element, or a stroke under a skew or a scale unequal in x and y
- an `IconPNGs` map that holds a purpose other than `PurposeAny` and `PurposeMaskable`, lacks one of a purpose's sizes (32, 180, 192 and 512 for `PurposeAny`, 192 and 512 for `PurposeMaskable`), holds another size, or names a file that is not a PNG of its size

[Icons](icons.md) lists what the renderer draws.
