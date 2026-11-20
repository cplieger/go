# Icons

This page covers the icon files and the web app manifest `Build` writes from one SVG, for a developer giving an app its favicon and home screen icon.

## What Build writes

With `Config.Icon` set to an `.svg` file under `Src`, `Build` writes these files under `assets/`, each with a content hash in its name and listed in `bundle.json`:

- the SVG itself, as it is in `Src`
- PNGs of 32 px for the browser tab, 180 px for `apple-touch-icon`, and 192 and 512 px for the manifest
- with `Config.Manifest`, `manifest.webmanifest`, and the files its `src` members name
- with `Config.MaskableIcon`, PNGs of it at 192 and 512 px for the manifest

It then appends these elements to each shell's head element, so they are written right before `</head>`:

```html
<link rel="icon" href="/assets/icon-84ec58577e86f6b6.svg" type="image/svg+xml"/>
<link rel="icon" href="/assets/icon-32-17bfe12faa092c13.png" sizes="32x32" type="image/png"/>
<link rel="apple-touch-icon" href="/assets/icon-180-2fb7d3acde97a5c4.png"/>
<link rel="manifest" href="/assets/manifest-9d84059a25eb421c.webmanifest"/>
```

A shell that already carries an `icon`, `apple-touch-icon` or `manifest` link fails the build. With no `Icon`, no link is added, and `SPA` answers `/favicon.ico` with an empty 204.

## The manifest

`Config.Manifest` names your manifest, a JSON file under `Src` holding every member but `icons`:

```json
{
  "name": "Example",
  "short_name": "Example",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "theme_color": "#1d4ed8",
  "background_color": "#ffffff",
  "shortcuts": [
    {"name": "Library", "url": "/library", "icons": [{"src": "icons/library.png", "sizes": "96x96", "type": "image/png"}]}
  ]
}
```

`Build` passes every member through, so a new member needs no new webbundle release. It writes `icons` itself, and replaces each `src` member, at any depth, with the served URL of that file written as a hashed asset. A `src` is the path of an image file under `Src` from the manifest's folder, as a browser resolves it from the manifest's URL. The manifest above, with `Icon` and `MaskableIcon` set, is written as:

```json
{
  "background_color": "#ffffff",
  "display": "standalone",
  "icons": [
    {"sizes": "192x192", "src": "/assets/icon-192-b94dbe2a375f68a2.png", "type": "image/png"},
    {"sizes": "512x512", "src": "/assets/icon-512-1a88ae90e125883d.png", "type": "image/png"},
    {"purpose": "maskable", "sizes": "192x192", "src": "/assets/maskable-192-ad7f663711a8f9c0.png", "type": "image/png"},
    {"purpose": "maskable", "sizes": "512x512", "src": "/assets/maskable-512-f2fd495ad549f315.png", "type": "image/png"}
  ],
  "name": "Example",
  "scope": "/",
  "short_name": "Example",
  "shortcuts": [
    {"icons": [{"sizes": "96x96", "src": "/assets/library-9c61ea8739f4a0dd.png", "type": "image/png"}], "name": "Library", "url": "/library"}
  ],
  "start_url": "/",
  "theme_color": "#1d4ed8"
}
```

The members come out sorted by name, indented one per line. `Build` checks the members it knows:

- `name` is a string that is not blank.
- `theme_color` and `background_color`, when present, are `#rrggbb` colours.
- `id`, `start_url`, `scope`, each shortcut's `url` and `share_target.action` are root-relative URLs, such as `/library?view=grid`, with no dot segment.
- All of them but `id` lie inside the scope, as the [manifest spec](https://www.w3.org/TR/appmanifest/#dfn-within-scope) defines it: the scope's path is a prefix of theirs. The scope is the `scope` member or, with none, the folder of `start_url`. With neither, the scope is the folder of the page the app is installed from, and `Build` checks only that each URL is root-relative.

## Maskable icons

An installed app's launcher may crop its icon to a circle or another shape. Set `Config.MaskableIcon` to an SVG drawn for that mask, with its art inside the central circle the [manifest spec](https://www.w3.org/TR/appmanifest/#icon-masks) calls the safe zone. `Build` lists its 192 and 512 px PNGs with `"purpose": "maskable"` and `Icon`'s with no purpose. It never lists one icon as `"any maskable"`: art drawn for the safe zone looks small unmasked, and art drawn to the edge loses its edges under a mask. Without `MaskableIcon`, the manifest lists no maskable icon. The shells never link a maskable icon.

## Which SVGs render

The PNGs are rendered in Go by [oksvg](https://github.com/srwiley/oksvg) and [rasterx](https://github.com/srwiley/rasterx), which draw part of SVG 2. `Build` accepts an icon only when they draw it as a browser does:

- The root is `<svg>` with a square `viewBox`.
- The elements are `g`, `defs`, `use`, `title`, `desc`, `path`, `rect`, `circle`, `ellipse`, `line`, `polyline`, `polygon`, `linearGradient`, `radialGradient` and `stop`.
- Paint is set by fills, strokes with their caps, joins, miter limits and dashes, and opacity on a shape, as attributes or in a `style` attribute.
- Fills use the `nonzero` rule.
- A `transform` attribute on `g`, `use` or a shape holds `matrix`, `translate`, `scale`, `rotate`, `skewX` and `skewY` functions. Stroke widths and dashes scale with it, as SVG's do. A stroke under a skew, or under a scale that differs in x and y, has no single width, so it fails the build; a fill under one draws.
- A gradient's `gradientUnits` is `objectBoundingBox`, the default.

So `<text>`, a filter, a mask, a clip path, an embedded image, a `transform` on `<svg>` or a gradient or in a `style` attribute, a singular transform, a `class` attribute, the `evenodd` rule, a `userSpaceOnUse` gradient and `opacity` on a group each fail the build, naming the element or attribute. Where oksvg's defaults differ from SVG's, `Build` applies SVG's stroke width of 1 and miter join, gives a `rect` with only `rx` or `ry` the other at the same value and clamps both to half its sides, and draws a rounded `rect`'s corners as arcs, since rasterx's own rounded rectangle is off by up to 73 of 255 at its corners.

webbundle's tests render four icons at 512 px: two drawn with a filled rectangle, arcs and round-capped strokes, marotte's favicon (a rounded rectangle and a translated, scaled group) and one using each transform function on groups, shapes and `use`. Each matches its reference render within 8 of 255 on each channel, compared premultiplied by alpha, for at least 99.5% of pixels, and no channel is off by more than 64. The first two references come from cairosvg and the other two from resvg, since cairosvg bends two corners of a rounded rectangle. The renderer is less exact on a wide stroked circle and a diagonal round-capped dash: up to 87 of 255 on under 20 edge pixels at 512 px, with or without a transform.

## Committed PNGs

For an icon the renderer cannot draw, render the PNGs with another tool and commit them. Set `Config.IconPNGs` to their paths under `Src`, keyed by `IconPNG{Purpose, Size}`: one each for 32, 180, 192 and 512 px with `PurposeAny` for `Icon`, and for 192 and 512 px with `PurposeMaskable` for `MaskableIcon`. A purpose's PNGs are committed all together or not at all, so `Icon` can render while `MaskableIcon` is committed. `Build` checks that each file is a PNG of its size and copies it, hashed, instead of rendering. `Icon`'s SVG is still copied and linked.
