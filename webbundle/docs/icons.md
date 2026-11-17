# Icons

This page covers the icon files and the web app manifest `Build` writes from one SVG, for a developer giving an app its favicon and home screen icon.

## What Build writes

With `Config.Icon` set to an `.svg` file under `Src`, `Build` writes these files under `assets/`, each with a content hash in its name and listed in `bundle.json`:

- the SVG itself, as it is in `Src`
- PNGs of 32 px for the browser tab, 180 px for `apple-touch-icon`, and 192 and 512 px for the manifest
- with `Config.Manifest`, `manifest.webmanifest`

It then adds these lines at the end of each shell's `<head>`, right before `</head>`:

```html
<link rel="icon" href="/assets/icon-63110c2b911e935f.svg" type="image/svg+xml">
<link rel="icon" href="/assets/icon-32-10206ba89b65252f.png" sizes="32x32" type="image/png">
<link rel="apple-touch-icon" href="/assets/icon-180-e8a9809e7ea91485.png">
<link rel="manifest" href="/assets/manifest-71b66b32fde31b8c.webmanifest">
```

A shell that already carries an `icon`, `apple-touch-icon` or `manifest` link fails the build, and so does a shell with no `</head>`. With no `Icon`, the shells are left as they are, and `SPA` answers `/favicon.ico` with an empty 204.

## The manifest

```json
{
  "name": "Example",
  "short_name": "Example",
  "theme_color": "#1d4ed8",
  "background_color": "#ffffff",
  "start_url": "/",
  "display": "standalone",
  "icons": [
    {"src": "/assets/icon-192-ad7f663711a8f9c0.png", "sizes": "192x192", "type": "image/png"},
    {"src": "/assets/icon-512-f2fd495ad549f315.png", "sizes": "512x512", "type": "image/png"}
  ]
}
```

`short_name` is left out when `ShortName` is empty.

## Which SVGs render

The PNGs are rendered in Go by [oksvg](https://github.com/srwiley/oksvg) and [rasterx](https://github.com/srwiley/rasterx), which draw part of SVG 2. `Build` accepts an icon only when they draw it as a browser does:

- The root is `<svg>` with a square `viewBox`.
- The elements are `g`, `defs`, `use`, `title`, `desc`, `path`, `rect`, `circle`, `ellipse`, `line`, `polyline`, `polygon`, `linearGradient`, `radialGradient` and `stop`.
- Paint is set by fills, strokes with their caps, joins, miter limits and dashes, and opacity on a shape, as attributes or in a `style` attribute.
- Fills use the `nonzero` rule.

So `<text>`, a filter, a mask, a clip path, an embedded image, a `transform` attribute, a `class` attribute, the `evenodd` rule and `opacity` on a group each fail the build, naming the element or attribute. Where oksvg's defaults differ from SVG's, `Build` applies SVG's stroke width of 1 and miter join.

webbundle's tests render two icons drawn with a filled rectangle, arcs and round-capped strokes at 512 px. Each matches its cairosvg render within 8 of 255 on each channel for at least 99.5% of pixels, and no channel is off by more than 64.

## Committed PNGs

For an icon the renderer cannot draw, render the PNGs with another tool and commit them. Set `Config.IconPNGs` to their paths under `Src` by size, one each for 32, 180, 192 and 512. `Build` then checks that each file is a PNG of that size and copies it, hashed, instead of rendering. The SVG is still copied and linked.
