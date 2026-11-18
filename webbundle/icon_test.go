package webbundle_test

import (
	"bytes"
	"cmp"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"maps"
	"os"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
	"github.com/cplieger/go/webbundle"
)

// Each icon in testdata/icons must match its reference render within these
// bounds: at most iconChannelTolerance of 255 per channel on at least
// iconWithinShare of the pixels, none over iconPixelLimit. The seadex-scout
// and subflux references are unraid-templates' cairosvg renders at 6f1cdbd;
// marotte (its favicon.svg at 1da7e83ca) and transformed are resvg 0.45.1's
// (`resvg -w 512 -h 512`), since cairosvg bends a rounded rect's corners.
// Measured at 512 px (within 8, worst): seadex-scout 99.82% 46, subflux
// 99.93% 43, marotte 99.52% 63, transformed 99.71% 43.
const (
	iconChannelTolerance = 8
	iconWithinShare      = 0.995
	iconPixelLimit       = 64
)

var iconSizes = []int{32, 180, 192, 512}

func seedIcon(t testing.TB, name string) string {
	t.Helper()
	body, err := os.ReadFile("testdata/icons/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func decodePNG(t testing.TB, body []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	return img
}

func solidPNG(t testing.TB, size int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// pngsBySize maps each PNG b lists under assets/ to its width, failing on a
// PNG that is not square.
func pngsBySize(t testing.TB, out string, b httpserver.Bundle) map[int]string {
	t.Helper()
	got := map[int]string{}
	for _, name := range listed(b, regexp.MustCompile(`^assets/.+\.png$`)) {
		bounds := decodePNG(t, readFile(t, out, name)).Bounds()
		if bounds.Dx() != bounds.Dy() {
			t.Errorf("%s is %dx%d, not square", name, bounds.Dx(), bounds.Dy())
		}
		got[bounds.Dx()] = name
	}
	return got
}

var linkTag = regexp.MustCompile(`(?is)<link\b[^>]*>`)

func attr(tag, name string) string {
	m := regexp.MustCompile(`(?i)\s` + name + `\s*=\s*"([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestIconWritesHashedFilesAndLinks(t *testing.T) {
	t.Parallel()
	icon := seedIcon(t, "subflux.svg")
	c := newFixture(t, files{"icon.svg": icon, "manifest.json": `{"name": "Fixture App"}`})
	c.Icon, c.Manifest = "icon.svg", "manifest.json"
	mustBuild(t, c)
	b := readBundle(t, c.Out)

	svgs := listed(b, regexp.MustCompile(`^assets/[^/]+\.svg$`))
	if len(svgs) != 1 || string(readFile(t, c.Out, svgs[0])) != icon {
		t.Fatalf("listed SVGs %q, want one holding the icon's bytes", svgs)
	}
	pngs := pngsBySize(t, c.Out, b)
	if got := slices.Sorted(maps.Keys(pngs)); !slices.Equal(got, iconSizes) {
		t.Fatalf("listed PNG sizes %v, want %v", got, iconSizes)
	}
	manifests := listed(b, regexp.MustCompile(`^assets/[^/]+\.webmanifest$`))
	if len(manifests) != 1 {
		t.Fatalf("listed manifests %q, want one", manifests)
	}
	got := decodeJSON(t, readFile(t, c.Out, manifests[0]))
	want := map[string]any{
		"name": "Fixture App",
		"icons": []any{
			map[string]any{"src": "/" + pngs[192], "sizes": "192x192", "type": "image/png"},
			map[string]any{"src": "/" + pngs[512], "sizes": "512x512", "type": "image/png"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("manifest %v, want %v", got, want)
	}

	shell := string(readFile(t, c.Out, "index.html"))
	head, _, ok := strings.Cut(shell, "</head>")
	if !ok {
		t.Fatalf("index.html has no </head>:\n%s", shell)
	}
	links := map[string][]string{}
	for _, tag := range linkTag.FindAllString(head, -1) {
		links[attr(tag, "rel")] = append(links[attr(tag, "rel")], attr(tag, "href"))
	}
	slices.Sort(links["icon"])
	for rel, want := range map[string][]string{
		"icon":             slices.Sorted(slices.Values([]string{"/" + svgs[0], "/" + pngs[32]})),
		"apple-touch-icon": {"/" + pngs[180]},
		"manifest":         {"/" + manifests[0]},
	} {
		if !slices.Equal(links[rel], want) {
			t.Errorf("rel=%s links %q, want %q", rel, links[rel], want)
		}
	}
	last := linkTag.FindAllStringIndex(head, -1)
	if len(last) == 0 || strings.TrimSpace(head[last[len(last)-1][1]:]) != "" {
		t.Errorf("the icon links are not the end of <head>:\n%s", head)
	}
	if i := strings.Index(head, `rel="stylesheet"`); i < 0 || i > strings.Index(head, "apple-touch-icon") {
		t.Errorf("the icon links do not follow the shell's own head:\n%s", head)
	}

	cfg := httpserver.SPAConfig{Assets: os.DirFS(c.Out), Pages: c.Pages, APIPrefix: c.APIPrefix}
	h, err := httpserver.SPA(cfg)
	if err != nil {
		t.Fatalf("httpserver.SPA refuses the built tree: %v", err)
	}
	httpservertest.CheckSPA(t, h, cfg)
}

func TestIconRefusesShellWithIconLink(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{"icon", "apple-touch-icon", "manifest"} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{
				"icon.svg":   seedIcon(t, "subflux.svg"),
				"index.html": strings.Replace(fixtureShell, "</head>", `<link rel="`+rel+`" href="/mine">`+"\n</head>", 1),
			})
			c.Icon = "icon.svg"
			buildRefused(t, c, "index.html", rel)
		})
	}
}

func TestIconRenderMatchesReference(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"seadex-scout", "subflux", "marotte", "transformed"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{"icon.svg": seedIcon(t, name+".svg")})
			c.Icon = "icon.svg"
			mustBuild(t, c)
			rendered, ok := pngsBySize(t, c.Out, readBundle(t, c.Out))[512]
			if !ok {
				t.Fatal("no 512 px PNG listed")
			}
			got := decodePNG(t, readFile(t, c.Out, rendered))
			want := decodePNG(t, []byte(seedIcon(t, name+".png")))
			if got.Bounds() != want.Bounds() {
				t.Fatalf("rendered %v, reference %v", got.Bounds(), want.Bounds())
			}
			within, worst, total := 0, 0, 0
			for y := want.Bounds().Min.Y; y < want.Bounds().Max.Y; y++ {
				for x := want.Bounds().Min.X; x < want.Bounds().Max.X; x++ {
					d := channelDelta(got.At(x, y), want.At(x, y))
					total++
					worst = max(worst, d)
					if d <= iconChannelTolerance {
						within++
					}
				}
			}
			share := float64(within) / float64(total)
			if share < iconWithinShare || worst > iconPixelLimit {
				t.Errorf("%.4f of pixels within %d, worst channel off by %d; want at least %.4f and at most %d",
					share, iconChannelTolerance, worst, iconWithinShare, iconPixelLimit)
			}
		})
	}
}

// channelDelta compares premultiplied channels, as the pixels composite: a
// nearly transparent edge pixel's colour weighs as little as its alpha.
func channelDelta(a, b color.Color) int {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	d := 0
	for _, pair := range [][2]uint32{{ar, br}, {ag, bg}, {ab, bb}, {aa, ba}} {
		d = max(d, abs(int(pair[0]>>8)-int(pair[1]>>8)))
	}
	return d
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

const textIcon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="#3372bf"/><text x="8" y="40">S</text></svg>`

func TestIconWithTextFails(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{"brand/icon.svg": textIcon})
	c.Icon = "brand/icon.svg"
	buildRefused(t, c, "brand/icon.svg", "text")
}

func TestIconRefusesWhatTheRendererCannotDraw(t *testing.T) {
	t.Parallel()
	const open = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">`
	cases := []struct{ name, svg, want string }{
		{name: "Mask", svg: open + `<mask id="m"><rect width="8" height="8"/></mask><rect width="64" height="64"/></svg>`, want: "mask"},
		{name: "Image", svg: open + `<image href="a.png" width="64" height="64"/></svg>`, want: "image"},
		{name: "TransformOnRoot", svg: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" transform="scale(2)"><rect width="8" height="8"/></svg>`, want: "<svg transform>"},
		{name: "TransformOnGradient", svg: open + `<defs><linearGradient id="a" transform="scale(2)"><stop offset="0" stop-color="#fff"/></linearGradient></defs><rect width="64" height="64" fill="url(#a)"/></svg>`, want: "<linearGradient transform>"},
		{name: "TransformInStyle", svg: open + `<g style="transform: scale(2)"><rect width="8" height="8"/></g></svg>`, want: "transform"},
		{name: "TransformTwice", svg: open + `<g xmlns:a="urn:a" a:transform="scale(2)" transform="scale(3)"><rect width="8" height="8"/></g></svg>`, want: "two transform"},
		{name: "TransformUnknownFunction", svg: open + `<g transform="spin(4)"><rect width="8" height="8"/></g></svg>`, want: `"spin(4)"`},
		{name: "TransformArgumentCount", svg: open + `<g transform="translate(1 2) scale(1 2 3)"><rect width="8" height="8"/></g></svg>`, want: "scale"},
		{name: "TransformNoArgument", svg: open + `<g transform="scale()"><rect width="8" height="8"/></g></svg>`, want: "scale"},
		{name: "TransformUnclosed", svg: open + `<g transform="translate(1 2"><rect width="8" height="8"/></g></svg>`, want: "transform"},
		{name: "TransformTrailingText", svg: open + `<g transform="scale(2) x"><rect width="8" height="8"/></g></svg>`, want: "transform"},
		{name: "TransformSingular", svg: open + `<g transform="scale(0)"><rect width="8" height="8"/></g></svg>`, want: "transform"},
		{name: "StrokeUnderNonUniformScale", svg: open + `<g transform="scale(2 1)"><path d="M0 0H8" stroke="#fff"/></g></svg>`, want: "stroke"},
		{name: "StrokeUnderSkew", svg: open + `<g stroke="#fff"><path transform="skewX(20)" d="M0 0H8"/></g></svg>`, want: "stroke"},
		{name: "StrokeUnderUsedSkew", svg: open + `<defs><path id="p" d="M0 0H8" stroke="#fff"/></defs><use href="#p" transform="matrix(1 0 .5 1 0 0)"/></svg>`, want: "stroke"},
		{name: "UserSpaceGradient", svg: open + `<defs><linearGradient id="a" gradientUnits="userSpaceOnUse" x2="64"><stop offset="0" stop-color="#fff"/><stop offset="1" stop-color="#000"/></linearGradient></defs><rect width="64" height="64" fill="url(#a)"/></svg>`, want: "userSpaceOnUse"},
		{name: "FilterAttribute", svg: open + `<rect width="64" height="64" filter="url(#f)"/></svg>`, want: "filter"},
		{name: "FilterInStyle", svg: open + `<rect width="64" height="64" style="fill: #fff; filter: blur(1px)"/></svg>`, want: "filter"},
		{name: "Class", svg: open + `<rect class="mark" width="64" height="64"/></svg>`, want: "class"},
		{name: "EvenOdd", svg: open + `<path fill-rule="evenodd" d="M0 0H64V64H0z"/></svg>`, want: "evenodd"},
		{name: "GroupOpacity", svg: open + `<g opacity="0.5"><rect width="64" height="64"/></g></svg>`, want: "opacity"},
		{name: "NotSquare", svg: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 32"><rect width="64" height="32"/></svg>`, want: "64x32"},
		{name: "NotSVGRoot", svg: `<g xmlns="http://www.w3.org/2000/svg"><rect width="64" height="64"/></g>`, want: "<g>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{"icon.svg": tc.svg})
			c.Icon = "icon.svg"
			buildRefused(t, c, "icon.svg", tc.want)
		})
	}
	t.Run("NotSVGFile", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"icon.png": solidPNG(t, 64)})
		c.Icon = "icon.png"
		buildRefused(t, c, "icon.png", ".svg")
	})
	t.Run("PlainAttributesPass", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"icon.svg": `<?xml version="1.0"?>
<!-- <svg> in a comment -->
<svg xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Mark" viewBox="0 0 64 64"><title>Mark</title><rect width="64" height="64" style="fill: #3372bf"/></svg>`})
		c.Icon = "icon.svg"
		mustBuild(t, c)
	})
}

// A stroke with no stroke-width is 1 unit wide, SVG's initial value
// (https://www.w3.org/TR/SVG2/painting.html#StrokeWidth): 8 px at 512 px
// for a 64-unit viewBox.
func TestIconStrokeWidthInitialValue(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{"icon.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><path d="M0 32H64" stroke="#ffffff"/></svg>`})
	c.Icon = "icon.svg"
	mustBuild(t, c)
	img := decodePNG(t, readFile(t, c.Out, pngsBySize(t, c.Out, readBundle(t, c.Out))[512]))
	alpha := func(y int) uint8 { return color.NRGBAModel.Convert(img.At(256, y)).(color.NRGBA).A }
	for _, y := range []int{253, 258} {
		if a := alpha(y); a != 255 {
			t.Errorf("row %d inside the stroke has alpha %d, want 255", y, a)
		}
	}
	for _, y := range []int{250, 261} {
		if a := alpha(y); a != 0 {
			t.Errorf("row %d outside a 1-unit stroke has alpha %d, want 0", y, a)
		}
	}
}

// A stroke's width is in the user space of its element, so a transform
// scales it (https://www.w3.org/TR/SVG2/painting.html#StrokeWidth): each
// case draws a 1-unit stroke along y=32, 8 px at 512 px for a 64-unit viewBox.
func TestIconStrokeUnderTransform(t *testing.T) {
	t.Parallel()
	const open = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">`
	cases := []struct{ name, body string }{
		{name: "GroupScaleOneArgument", body: `<g transform="scale(0.5)"><path d="M0 64H128" stroke="#fff" stroke-width="2"/></g>`},
		{name: "ShapeMatrix", body: `<path transform="matrix(2 0 0 2 0 -32)" d="M0 32H32" stroke="#fff" stroke-width="0.5"/>`},
		{name: "NestedGroups", body: `<g transform="scale(2)"><g transform="translate(0 -8) scale(0.25)"><path d="M0 96H128" stroke="#fff" stroke-width="2"/></g></g>`},
		{name: "RotateAboutAPoint", body: `<g transform="rotate(90 32 32)"><path d="M32 0V64" stroke="#fff"/></g>`},
		{name: "TranslateOneArgument", body: `<g transform="translate(3)"><path d="M0 32H64" stroke="#fff"/></g>`},
		{name: "Use", body: `<defs><path id="l" d="M0 64H128" stroke="#fff" stroke-width="2"/></defs><use href="#l" transform="scale(.5)"/>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{"icon.svg": open + tc.body + `</svg>`})
			c.Icon = "icon.svg"
			mustBuild(t, c)
			img := decodePNG(t, readFile(t, c.Out, pngsBySize(t, c.Out, readBundle(t, c.Out))[512]))
			alpha := func(y int) uint8 { return color.NRGBAModel.Convert(img.At(200, y)).(color.NRGBA).A }
			for _, y := range []int{253, 258} {
				if a := alpha(y); a != 255 {
					t.Errorf("row %d inside the stroke has alpha %d, want 255", y, a)
				}
			}
			for _, y := range []int{250, 261} {
				if a := alpha(y); a != 0 {
					t.Errorf("row %d outside a 1-unit stroke has alpha %d, want 0", y, a)
				}
			}
		})
	}
}

// A rounded <rect> follows SVG's radius rules
// (https://www.w3.org/TR/SVG2/geometry.html#RX): a missing rx or ry takes
// the other's value, and each is clamped to half the side. On a 64-unit
// square at 512 px, a 16-unit radius leaves (8, 8) empty and fills (40, 40);
// a radius clamped to 32 is a circle, which leaves (40, 40) empty, and on a
// 64x32 rect clamped radii draw a 256x128 px ellipse.
func TestIconRoundedRect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, rect    string
		empty, filled [][2]int
	}{
		{name: "OnlyRX", rect: `<rect width="64" height="64" rx="16" fill="#fff"/>`, empty: [][2]int{{8, 8}}, filled: [][2]int{{40, 40}, {256, 2}}},
		{name: "OnlyRY", rect: `<rect width="64" height="64" ry="16" fill="#fff"/>`, empty: [][2]int{{8, 8}}, filled: [][2]int{{40, 40}, {256, 2}}},
		{name: "EndTag", rect: `<rect width="64" height="64" rx="16" ry="16" fill="#fff"></rect>`, empty: [][2]int{{8, 8}}, filled: [][2]int{{40, 40}, {256, 2}}},
		{name: "Clamped", rect: `<rect width="64" height="64" rx="40" fill="#fff"/>`, empty: [][2]int{{8, 8}, {40, 40}}, filled: [][2]int{{256, 2}, {256, 256}}},
		{name: "ClampedToAnEllipse", rect: `<rect y="16" width="64" height="32" rx="40" fill="#fff"/>`, empty: [][2]int{{20, 140}, {8, 136}}, filled: [][2]int{{256, 132}, {40, 200}, {500, 256}}},
		{name: "Offset", rect: `<rect x="32" y="32" width="32" height="32" rx="8" fill="#fff"/>`, empty: [][2]int{{260, 260}, {100, 100}}, filled: [][2]int{{300, 300}, {384, 258}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{"icon.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` + tc.rect + `</svg>`})
			c.Icon = "icon.svg"
			mustBuild(t, c)
			img := decodePNG(t, readFile(t, c.Out, pngsBySize(t, c.Out, readBundle(t, c.Out))[512]))
			alpha := func(p [2]int) uint8 { return color.NRGBAModel.Convert(img.At(p[0], p[1])).(color.NRGBA).A }
			for _, p := range tc.empty {
				if a := alpha(p); a != 0 {
					t.Errorf("pixel %v has alpha %d, want 0", p, a)
				}
			}
			for _, p := range tc.filled {
				if a := alpha(p); a != 255 {
					t.Errorf("pixel %v has alpha %d, want 255", p, a)
				}
			}
		})
	}
	t.Run("NegativeRadius", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"icon.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="-4"/></svg>`})
		c.Icon = "icon.svg"
		buildRefused(t, c, "icon.svg", `rx="-4"`)
	})
}

// committedPNGs adds a solid PNG to tree for each key, drawn at the size
// the key maps to, and returns IconPNGs naming them.
func committedPNGs(t testing.TB, tree files, sizes map[webbundle.IconPNG]int) map[webbundle.IconPNG]string {
	t.Helper()
	names := map[webbundle.IconPNG]string{}
	for k, drawn := range sizes {
		names[k] = "icons/" + cmp.Or(string(k.Purpose), "any") + "-" + strconv.Itoa(k.Size) + ".png"
		tree[names[k]] = solidPNG(t, drawn)
	}
	return names
}

var (
	anyPNGs      = map[webbundle.IconPNG]int{{Size: 32}: 32, {Size: 180}: 180, {Size: 192}: 192, {Size: 512}: 512}
	maskablePNGs = map[webbundle.IconPNG]int{{Purpose: webbundle.PurposeMaskable, Size: 192}: 192, {Purpose: webbundle.PurposeMaskable, Size: 512}: 512}
)

func TestIconPNGs(t *testing.T) {
	t.Parallel()
	with := func(base map[webbundle.IconPNG]int, edit func(map[webbundle.IconPNG]int)) map[webbundle.IconPNG]int {
		m := maps.Clone(base)
		edit(m)
		return m
	}
	t.Run("CopiedNotRendered", func(t *testing.T) {
		t.Parallel()
		tree := files{"icon.svg": textIcon}
		names := committedPNGs(t, tree, anyPNGs)
		c := newFixture(t, tree)
		c.Icon, c.IconPNGs = "icon.svg", names
		mustBuild(t, c)
		pngs := pngsBySize(t, c.Out, readBundle(t, c.Out))
		for k, name := range names {
			if got := readFile(t, c.Out, pngs[k.Size]); string(got) != tree[name] {
				t.Errorf("the %d px PNG %q does not hold %s's bytes", k.Size, pngs[k.Size], name)
			}
			if want := hashedName(path.Base(name)); !want.MatchString(pngs[k.Size]) {
				t.Errorf("the %d px PNG is at %q, want %s", k.Size, pngs[k.Size], want)
			}
		}
	})
	t.Run("MaskableCopiedNotRendered", func(t *testing.T) {
		t.Parallel()
		tree := files{"maskable.svg": textIcon, "manifest.json": `{"name": "Fixture App"}`, "icon.svg": seedIcon(t, "subflux.svg")}
		names := committedPNGs(t, tree, maskablePNGs)
		c := newFixture(t, tree)
		c.Icon, c.MaskableIcon, c.Manifest, c.IconPNGs = "icon.svg", "maskable.svg", "manifest.json", names
		mustBuild(t, c)
		masks := manifestIconsOf(t, c.Out, "maskable")
		for _, size := range []int{192, 512} {
			name := names[webbundle.IconPNG{Purpose: webbundle.PurposeMaskable, Size: size}]
			got, ok := masks[size]
			if !ok {
				t.Errorf("the manifest lists no %d px maskable icon", size)
				continue
			}
			if string(readFile(t, c.Out, got)) != tree[name] {
				t.Errorf("the %d px maskable icon %q does not hold %s's bytes", size, got, name)
			}
		}
	})
	refusals := []struct {
		sizes map[webbundle.IconPNG]int
		edit  func(c *webbundle.Config)
		name  string
		want  []string
	}{
		{name: "WrongSize", sizes: with(anyPNGs, func(m map[webbundle.IconPNG]int) { m[webbundle.IconPNG{Size: 32}] = 16 }), want: []string{"icons/any-32.png", "32"}},
		{name: "SizeMissing", sizes: with(anyPNGs, func(m map[webbundle.IconPNG]int) { delete(m, webbundle.IconPNG{Size: 180}) }), want: []string{"180"}},
		{name: "ExtraSize", sizes: with(anyPNGs, func(m map[webbundle.IconPNG]int) { m[webbundle.IconPNG{Size: 64}] = 64 }), want: []string{"64"}},
		{name: "WithoutIcon", sizes: anyPNGs, edit: func(c *webbundle.Config) { c.Icon = "" }, want: []string{"Icon"}},
		{name: "MaskableWithoutMaskableIcon", sizes: maskablePNGs, edit: func(c *webbundle.Config) { c.MaskableIcon = "" }, want: []string{"MaskableIcon"}},
		{name: "MaskableTabSize", sizes: with(maskablePNGs, func(m map[webbundle.IconPNG]int) {
			m[webbundle.IconPNG{Purpose: webbundle.PurposeMaskable, Size: 32}] = 32
		}), want: []string{"32", "maskable"}},
		{name: "MaskableSizeMissing", sizes: with(maskablePNGs, func(m map[webbundle.IconPNG]int) {
			delete(m, webbundle.IconPNG{Purpose: webbundle.PurposeMaskable, Size: 512})
		}), want: []string{"512", "maskable"}},
		{name: "MaskableWrongSize", sizes: with(maskablePNGs, func(m map[webbundle.IconPNG]int) {
			m[webbundle.IconPNG{Purpose: webbundle.PurposeMaskable, Size: 192}] = 180
		}), want: []string{"icons/maskable-192.png", "192"}},
		{name: "UnknownPurpose", sizes: map[webbundle.IconPNG]int{{Purpose: "any", Size: 192}: 192}, want: []string{`"any"`}},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tree := files{"icon.svg": seedIcon(t, "subflux.svg"), "maskable.svg": seedIcon(t, "seadex-scout.svg"), "manifest.json": `{"name": "Fixture App"}`}
			names := committedPNGs(t, tree, tc.sizes)
			c := newFixture(t, tree)
			c.Icon, c.MaskableIcon, c.Manifest, c.IconPNGs = "icon.svg", "maskable.svg", "manifest.json", names
			if tc.edit != nil {
				tc.edit(&c)
			}
			buildRefused(t, c, tc.want...)
		})
	}
	t.Run("NotPNG", func(t *testing.T) {
		t.Parallel()
		tree := files{"icon.svg": textIcon}
		names := committedPNGs(t, tree, anyPNGs)
		tree[names[webbundle.IconPNG{Size: 180}]] = "not a png"
		c := newFixture(t, tree)
		c.Icon, c.IconPNGs = "icon.svg", names
		buildRefused(t, c, names[webbundle.IconPNG{Size: 180}])
	})
}

// manifestIconsOf reads the built manifest's icons of one purpose ("" for
// those listed with none) by size, failing on a purpose other than
// "maskable", a size listed twice, or an icon not listed in bundle.json.
func manifestIconsOf(t testing.TB, out, purpose string) map[int]string {
	t.Helper()
	b := readBundle(t, out)
	manifests := listed(b, regexp.MustCompile(`^assets/[^/]+\.webmanifest$`))
	if len(manifests) != 1 {
		t.Fatalf("listed manifests %q, want one", manifests)
	}
	var m struct {
		Icons []struct{ Src, Sizes, Type, Purpose string } `json:"icons"`
	}
	if err := json.Unmarshal(readFile(t, out, manifests[0]), &m); err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, i := range m.Icons {
		if i.Purpose != "" && i.Purpose != "maskable" {
			t.Errorf("icon %s has purpose %q; want none or \"maskable\"", i.Src, i.Purpose)
		}
		if i.Purpose != purpose {
			continue
		}
		w, h, _ := strings.Cut(i.Sizes, "x")
		size, err := strconv.Atoi(w)
		if err != nil || w != h || i.Type != "image/png" {
			t.Fatalf("icon %+v: want a square image/png", i)
		}
		file := strings.TrimPrefix(i.Src, "/")
		if _, ok := b.Files[file]; !ok || !strings.HasPrefix(i.Src, "/assets/") {
			t.Errorf("icon src %q is not a listed file under /assets/", i.Src)
		}
		if _, dup := got[size]; dup {
			t.Errorf("two %q icons of %d px", purpose, size)
		}
		got[size] = file
	}
	return got
}

func TestMaskableIcon(t *testing.T) {
	t.Parallel()
	t.Run("RenderedAt192And512", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"icon.svg": seedIcon(t, "subflux.svg"), "brand/mask.svg": seedIcon(t, "seadex-scout.svg"), "manifest.json": `{"name": "Fixture App"}`})
		c.Icon, c.MaskableIcon, c.Manifest = "icon.svg", "brand/mask.svg", "manifest.json"
		mustBuild(t, c)
		plain, masks := manifestIconsOf(t, c.Out, ""), manifestIconsOf(t, c.Out, "maskable")
		for _, size := range []int{192, 512} {
			if plain[size] == "" || masks[size] == "" {
				t.Fatalf("the manifest lists %v with no purpose and %v as maskable; want 192 and 512 px of each", plain, masks)
			}
			if b := decodePNG(t, readFile(t, c.Out, masks[size])).Bounds(); b.Dx() != size || b.Dy() != size {
				t.Errorf("the %d px maskable icon is %v", size, b)
			}
			if bytes.Equal(readFile(t, c.Out, masks[size]), readFile(t, c.Out, plain[size])) {
				t.Errorf("the %d px maskable icon holds Icon's render, not MaskableIcon's", size)
			}
			if !hashedName("mask-" + strconv.Itoa(size) + ".png").MatchString(masks[size]) {
				t.Errorf("the %d px maskable icon is at %q", size, masks[size])
			}
		}
		if len(plain) != 2 || len(masks) != 2 {
			t.Errorf("the manifest lists %v and %v; want two of each", plain, masks)
		}
		shell := string(readFile(t, c.Out, "index.html"))
		for _, p := range masks {
			if strings.Contains(shell, p) {
				t.Errorf("index.html links the maskable icon %s; only the manifest lists it", p)
			}
		}
	})
	t.Run("NoneWithoutMaskableIcon", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"icon.svg": seedIcon(t, "subflux.svg"), "manifest.json": `{"name": "Fixture App"}`})
		c.Icon, c.Manifest = "icon.svg", "manifest.json"
		mustBuild(t, c)
		if masks := manifestIconsOf(t, c.Out, "maskable"); len(masks) != 0 {
			t.Errorf("the manifest lists maskable icons %v with no MaskableIcon", masks)
		}
	})
	refusals := []struct {
		edit func(c *webbundle.Config)
		name string
		want []string
	}{
		{name: "WithoutManifest", edit: func(c *webbundle.Config) { c.Manifest = "" }, want: []string{"MaskableIcon", "Manifest"}},
		{name: "NotSVGFile", edit: func(c *webbundle.Config) { c.MaskableIcon = "mask.png" }, want: []string{"mask.png", ".svg"}},
		{name: "WithText", edit: func(c *webbundle.Config) { c.MaskableIcon = "text.svg" }, want: []string{"text.svg", "text"}},
		{name: "Missing", edit: func(c *webbundle.Config) { c.MaskableIcon = "none.svg" }, want: []string{"none.svg"}},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{
				"icon.svg": seedIcon(t, "subflux.svg"), "mask.svg": seedIcon(t, "seadex-scout.svg"),
				"mask.png": solidPNG(t, 192), "text.svg": textIcon, "manifest.json": `{"name": "Fixture App"}`,
			})
			c.Icon, c.MaskableIcon, c.Manifest = "icon.svg", "mask.svg", "manifest.json"
			tc.edit(&c)
			buildRefused(t, c, tc.want...)
		})
	}
}
