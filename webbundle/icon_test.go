package webbundle_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
	"github.com/cplieger/go/webbundle"
)

// The rendered seed icons must match the reference renders within these
// bounds: at most iconChannelTolerance of 255 per channel on at least
// iconWithinShare of the pixels, and no channel off by more than
// iconPixelLimit. The references are unraid-templates' icons/seadex-scout.png
// and icons/subflux.png at 6f1cdbd, which scripts/icons.py renders from
// icons/src/*.svg (the copies in testdata/icons) through cairosvg. Measured
// at 512 px: seadex-scout 99.82% within 8 and worst 46, subflux 99.93% and 43.
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
	c := newFixture(t, files{"icon.svg": icon})
	c.Icon = "icon.svg"
	c.Manifest = &webbundle.Manifest{Name: "Fixture App", ShortName: "Fixture", ThemeColor: "#112233", BackgroundColor: "#f7f5f4"}
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

	var m struct {
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		ThemeColor      string `json:"theme_color"`
		BackgroundColor string `json:"background_color"`
		StartURL        string `json:"start_url"`
		Display         string `json:"display"`
		Icons           []struct {
			Src, Sizes, Type string
		} `json:"icons"`
	}
	dec := json.NewDecoder(bytes.NewReader(readFile(t, c.Out, manifests[0])))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode %s: %v", manifests[0], err)
	}
	if m.Name != "Fixture App" || m.ShortName != "Fixture" || m.ThemeColor != "#112233" || m.BackgroundColor != "#f7f5f4" ||
		m.StartURL != "/" || m.Display != "standalone" {
		t.Errorf("manifest fields %+v", m)
	}
	var icons []string
	for _, i := range m.Icons {
		icons = append(icons, i.Src+" "+i.Sizes+" "+i.Type)
	}
	if want := []string{"/" + pngs[192] + " 192x192 image/png", "/" + pngs[512] + " 512x512 image/png"}; !slices.Equal(icons, want) {
		t.Errorf("manifest icons %q, want %q", icons, want)
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
	for _, name := range []string{"seadex-scout", "subflux"} {
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
					d := channelDelta(color.NRGBAModel.Convert(got.At(x, y)).(color.NRGBA), color.NRGBAModel.Convert(want.At(x, y)).(color.NRGBA))
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

func channelDelta(a, b color.NRGBA) int {
	d := 0
	for _, pair := range [][2]uint8{{a.R, b.R}, {a.G, b.G}, {a.B, b.B}, {a.A, b.A}} {
		d = max(d, abs(int(pair[0])-int(pair[1])))
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
		{name: "Transform", svg: open + `<g transform="scale(2)"><path d="M0 0H8" stroke="#fff"/></g></svg>`, want: "transform"},
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

func TestManifestWithoutShortName(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{"icon.svg": seedIcon(t, "subflux.svg")})
	c.Icon = "icon.svg"
	c.Manifest = &webbundle.Manifest{Name: "Fixture App", ThemeColor: "#112233", BackgroundColor: "#f7f5f4"}
	mustBuild(t, c)
	b := readBundle(t, c.Out)
	manifests := listed(b, regexp.MustCompile(`^assets/[^/]+\.webmanifest$`))
	if len(manifests) != 1 {
		t.Fatalf("listed manifests %q, want one", manifests)
	}
	var m map[string]any
	if err := json.Unmarshal(readFile(t, c.Out, manifests[0]), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["short_name"]; ok {
		t.Errorf("the manifest holds short_name %v with no ShortName set", m["short_name"])
	}
}

func TestIconPNGs(t *testing.T) {
	t.Parallel()
	committed := func(t testing.TB, sizes map[int]int) (files, map[int]string) {
		t.Helper()
		tree, names := files{"icon.svg": textIcon}, map[int]string{}
		for size, drawn := range sizes {
			names[size] = "icons/icon-" + strconv.Itoa(size) + ".png"
			tree[names[size]] = solidPNG(t, drawn)
		}
		return tree, names
	}
	t.Run("CopiedNotRendered", func(t *testing.T) {
		t.Parallel()
		tree, names := committed(t, map[int]int{32: 32, 180: 180, 192: 192, 512: 512})
		c := newFixture(t, tree)
		c.Icon, c.IconPNGs = "icon.svg", names
		mustBuild(t, c)
		pngs := pngsBySize(t, c.Out, readBundle(t, c.Out))
		for _, size := range iconSizes {
			if got := readFile(t, c.Out, pngs[size]); string(got) != tree[names[size]] {
				t.Errorf("the %d px PNG %q does not hold %s's bytes", size, pngs[size], names[size])
			}
			if want := hashedName(path.Base(names[size])); !want.MatchString(pngs[size]) {
				t.Errorf("the %d px PNG is at %q, want %s", size, pngs[size], want)
			}
		}
	})
	t.Run("WrongSize", func(t *testing.T) {
		t.Parallel()
		tree, names := committed(t, map[int]int{32: 16, 180: 180, 192: 192, 512: 512})
		c := newFixture(t, tree)
		c.Icon, c.IconPNGs = "icon.svg", names
		buildRefused(t, c, "icons/icon-32.png", "32")
	})
	t.Run("SizeMissing", func(t *testing.T) {
		t.Parallel()
		tree, names := committed(t, map[int]int{32: 32, 192: 192, 512: 512})
		c := newFixture(t, tree)
		c.Icon, c.IconPNGs = "icon.svg", names
		buildRefused(t, c, "180")
	})
	t.Run("ExtraSize", func(t *testing.T) {
		t.Parallel()
		tree, names := committed(t, map[int]int{32: 32, 64: 64, 180: 180, 192: 192, 512: 512})
		c := newFixture(t, tree)
		c.Icon, c.IconPNGs = "icon.svg", names
		buildRefused(t, c, "64")
	})
	t.Run("NotPNG", func(t *testing.T) {
		t.Parallel()
		tree, names := committed(t, map[int]int{32: 32, 180: 180, 192: 192, 512: 512})
		tree[names[180]] = "not a png"
		c := newFixture(t, tree)
		c.Icon, c.IconPNGs = "icon.svg", names
		buildRefused(t, c, names[180])
	})
	t.Run("WithoutIcon", func(t *testing.T) {
		t.Parallel()
		tree, names := committed(t, map[int]int{32: 32, 180: 180, 192: 192, 512: 512})
		c := newFixture(t, tree)
		c.IconPNGs = names
		buildRefused(t, c, "Icon")
	})
}

func TestManifestRefuses(t *testing.T) {
	t.Parallel()
	good := webbundle.Manifest{Name: "Fixture App", ShortName: "Fixture", ThemeColor: "#112233", BackgroundColor: "#f7f5f4"}
	cases := []struct {
		edit func(m *webbundle.Manifest, c *webbundle.Config)
		name string
		want string
	}{
		{name: "ThemeColorNotHex", edit: func(m *webbundle.Manifest, _ *webbundle.Config) { m.ThemeColor = "red" }, want: "red"},
		{name: "BackgroundShortHex", edit: func(m *webbundle.Manifest, _ *webbundle.Config) { m.BackgroundColor = "#fff" }, want: "#fff"},
		{name: "NoName", edit: func(m *webbundle.Manifest, _ *webbundle.Config) { m.Name = "" }, want: "Name"},
		{name: "NoIcon", edit: func(_ *webbundle.Manifest, c *webbundle.Config) { c.Icon = "" }, want: "Icon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{"icon.svg": seedIcon(t, "subflux.svg")})
			m := good
			c.Icon, c.Manifest = "icon.svg", &m
			tc.edit(&m, &c)
			buildRefused(t, c, tc.want)
		})
	}
}
