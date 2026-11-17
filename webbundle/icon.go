package webbundle

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// The PNG sizes Build writes from Icon: the browser tab, apple-touch-icon,
// and the manifest's two icons.
const (
	tabIcon   = 32
	touchIcon = 180
	smallIcon = 192
	largeIcon = 512
)

var iconSizes = []int{tabIcon, touchIcon, smallIcon, largeIcon}

var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// checkIcon checks Icon, IconPNGs and Manifest and, when Build renders the
// PNGs, that the renderer draws every element and attribute of the icon.
func (b *build) checkIcon() error {
	if b.icon == "" {
		switch {
		case b.iconPNGs != nil:
			return errors.New("webbundle: Config.IconPNGs is set and Config.Icon is not; the PNGs are the icon's")
		case b.manifest != nil:
			return errors.New("webbundle: Config.Manifest is set and Config.Icon is not; the manifest's icons come from it")
		}
		return nil
	}
	p, err := b.srcFile("icon", b.icon)
	if err != nil {
		return err
	}
	if path.Ext(p) != ".svg" {
		return fmt.Errorf("webbundle: icon %s is not an .svg file", b.icon)
	}
	b.icon = p
	if bad := b.checkManifest(); bad != nil {
		return bad
	}
	if b.iconPNGs != nil {
		return b.checkIconPNGs()
	}
	svg, err := os.ReadFile(filepath.Join(b.src, filepath.FromSlash(p)))
	if err != nil {
		return fmt.Errorf("webbundle: icon %s: %w", p, err)
	}
	if _, bad := readIcon(svg); bad != nil {
		return fmt.Errorf("webbundle: icon %s: %w; commit its PNGs as Config.IconPNGs instead", p, bad)
	}
	return nil
}

func (b *build) checkManifest() error {
	m := b.manifest
	if m == nil {
		return nil
	}
	if m.Name == "" {
		return errors.New("webbundle: Config.Manifest.Name is empty")
	}
	for _, c := range []struct{ field, value string }{{"ThemeColor", m.ThemeColor}, {"BackgroundColor", m.BackgroundColor}} {
		if !hexColour.MatchString(c.value) {
			return fmt.Errorf("webbundle: Config.Manifest.%s is %q, not a #rrggbb colour", c.field, c.value)
		}
	}
	return nil
}

func (b *build) checkIconPNGs() error {
	for _, size := range slices.Sorted(maps.Keys(b.iconPNGs)) {
		if !slices.Contains(iconSizes, size) {
			return fmt.Errorf("webbundle: Config.IconPNGs holds a %d px PNG; the sizes are %v", size, iconSizes)
		}
	}
	pngs := make(map[int]string, len(iconSizes))
	for _, size := range iconSizes {
		name, ok := b.iconPNGs[size]
		if !ok {
			return fmt.Errorf("webbundle: Config.IconPNGs has no %d px PNG; it names one for each of %v", size, iconSizes)
		}
		p, err := b.srcFile("icon PNG", name)
		if err != nil {
			return err
		}
		f, err := os.Open(filepath.Join(b.src, filepath.FromSlash(p)))
		if err != nil {
			return fmt.Errorf("webbundle: icon PNG %s: %w", p, err)
		}
		cfg, err := png.DecodeConfig(f)
		f.Close()
		switch {
		case err != nil:
			return fmt.Errorf("webbundle: icon PNG %s is no PNG: %w", p, err)
		case cfg.Width != size || cfg.Height != size:
			return fmt.Errorf("webbundle: icon PNG %s is %dx%d, not %d px square", p, cfg.Width, cfg.Height, size)
		}
		pngs[size] = p
	}
	b.iconPNGs = pngs
	return nil
}

// iconElements are the SVG elements oksvg draws; anything else (text, a
// filter, a mask, a clip path, an image) would be dropped from the render,
// so the icon is refused instead.
const svgRoot = "svg"

var iconElements = []string{
	svgRoot, "g", "defs", "use", "title", "desc",
	"path", "rect", "circle", "ellipse", "line", "polyline", "polygon",
	"linearGradient", "radialGradient", "stop",
}

// iconPaint are the presentation properties oksvg applies, as attributes or
// in a style attribute.
var iconPaint = []string{
	"fill", "fill-opacity", "stroke", "stroke-opacity", "stroke-width",
	"stroke-linecap", "stroke-linejoin", "stroke-miterlimit",
	"stroke-dasharray", "stroke-dashoffset", "opacity",
}

// iconGeometry are the other attributes oksvg reads; transform is left out
// because oksvg draws a stroke under one at its untransformed width.
var iconGeometry = []string{
	"xmlns", "version", "id", "viewBox", "href",
	"x", "y", "width", "height", "rx", "ry", "cx", "cy", "r", "fx", "fy",
	"x1", "y1", "x2", "y2", "d", "points", "offset", "stop-color", "stop-opacity",
	"gradientTransform", "gradientUnits", "spreadMethod",
}

// checkIconSVG refuses an element or attribute oksvg would not draw as a
// browser does, and returns the byte offset just after the root's "<svg".
// oksvg's strict mode refuses only an unknown element outside defs, and it
// ignores an attribute it does not read.
func checkIconSVG(svg []byte) (int, error) {
	dec := xml.NewDecoder(bytes.NewReader(svg))
	depth, root := 0, -1
	for {
		at := dec.InputOffset()
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := checkIconElement(t, depth); err != nil {
				return 0, err
			}
			if depth == 0 {
				root = rootEnd(svg, int(at))
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if root < 0 {
		return 0, errors.New("no <svg> element written as <svg")
	}
	return root, nil
}

// rootEnd is the offset after "<svg" when the root start tag at at is
// spelled so, else -1.
func rootEnd(svg []byte, at int) int {
	if !bytes.HasPrefix(svg[at:], []byte("<svg")) {
		return -1
	}
	return at + len("<svg")
}

func checkIconElement(t xml.StartElement, depth int) error {
	name := t.Name.Local
	if !slices.Contains(iconElements, name) {
		return fmt.Errorf("the renderer cannot draw <%s>", name)
	}
	if depth == 0 && name != svgRoot {
		return fmt.Errorf("the root element is <%s>, not <svg>", name)
	}
	for _, a := range t.Attr {
		if a.Name.Space == "xmlns" {
			continue
		}
		if err := checkIconAttr(name, a.Name.Local, a.Value); err != nil {
			return err
		}
	}
	return nil
}

func checkIconAttr(elem, name, value string) error {
	switch {
	case name == "style":
		return checkIconStyle(elem, value)
	case name == "fill-rule" || name == "clip-rule":
		if value != "nonzero" {
			return fmt.Errorf("the renderer draws only the nonzero %s, not <%s %s=%q>", name, elem, name, value)
		}
		return nil
	case name == "opacity" && (elem == "g" || elem == svgRoot || elem == "use"):
		// A group's opacity composites the group once; oksvg multiplies it
		// into each shape, which differs wherever shapes overlap.
		return fmt.Errorf("the renderer cannot draw opacity on <%s>; set it on each shape", elem)
	case slices.Contains(iconPaint, name), slices.Contains(iconGeometry, name),
		name == "role", strings.HasPrefix(name, "aria-"), strings.HasPrefix(name, "data-"):
		return nil
	}
	return fmt.Errorf("the renderer cannot draw <%s %s>", elem, name)
}

func checkIconStyle(elem, style string) error {
	for decl := range strings.SplitSeq(style, ";") {
		k, v, _ := strings.Cut(decl, ":")
		if k = strings.TrimSpace(k); k != "" {
			if err := checkIconAttr(elem, k, strings.TrimSpace(v)); err != nil {
				return err
			}
		}
	}
	return nil
}

// svgInitial are SVG's initial stroke values where oksvg's defaults differ
// (a width of 2, a bevel join): https://www.w3.org/TR/SVG2/painting.html.
// Set on the root, an icon's own value on any element still wins.
const svgInitial = ` stroke-width="1" stroke-linejoin="miter"`

// readIcon checks and parses an icon with SVG's initial stroke values.
func readIcon(svg []byte) (*oksvg.SvgIcon, error) {
	at, err := checkIconSVG(svg)
	if err != nil {
		return nil, err
	}
	withInitial := slices.Concat(svg[:at], []byte(svgInitial), svg[at:])
	icon, err := oksvg.ReadIconStream(bytes.NewReader(withInitial), oksvg.StrictErrorMode)
	if err != nil {
		return nil, err
	}
	if icon.ViewBox.W <= 0 || icon.ViewBox.W != icon.ViewBox.H {
		return nil, fmt.Errorf("its viewBox is %gx%g, not square", icon.ViewBox.W, icon.ViewBox.H)
	}
	return icon, nil
}

// renderIcon draws svg at size px square as PNG bytes.
func renderIcon(svg []byte, size int) ([]byte, error) {
	icon, err := readIcon(svg)
	if err != nil {
		return nil, err
	}
	icon.SetTarget(0, 0, float64(size), float64(size))
	// oksvg hands the dasher stroke widths and dashes in device pixels while
	// it scales the path, so a stroke would keep its viewBox width
	// (https://github.com/srwiley/oksvg/pull/54, unmerged).
	k := float64(size) / icon.ViewBox.W
	for i := range icon.SVGPaths {
		p := &icon.SVGPaths[i]
		p.LineWidth *= k
		p.DashOffset *= k
		p.Dash = slices.Clone(p.Dash)
		for j := range p.Dash {
			p.Dash[j] *= k
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	scanner := rasterx.NewScannerGV(size, size, img, img.Bounds())
	icon.Draw(rasterx.NewDasher(size, size, scanner), 1)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// webManifest is manifest.webmanifest in the field order Build writes it.
type webManifest struct {
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name,omitempty"`
	ThemeColor      string         `json:"theme_color"`
	BackgroundColor string         `json:"background_color"`
	StartURL        string         `json:"start_url"`
	Display         string         `json:"display"`
	Icons           []manifestIcon `json:"icons"`
}

type manifestIcon struct {
	Src   string `json:"src"`
	Sizes string `json:"sizes"`
	Type  string `json:"type"`
}

// writeIcons writes the icon, its PNGs and the manifest into t as hashed
// files under assets/ and returns the link lines each shell's head ends with.
func (b *build) writeIcons(t *tree) ([]byte, error) {
	if b.icon == "" {
		return nil, nil
	}
	svg, err := os.ReadFile(filepath.Join(b.src, filepath.FromSlash(b.icon)))
	if err != nil {
		return nil, fmt.Errorf("webbundle: icon %s: %w", b.icon, err)
	}
	stem := strings.TrimSuffix(path.Base(b.icon), ".svg")
	svgPath, err := t.writeHashed(stem, ".svg", svg)
	if err != nil {
		return nil, err
	}
	pngs := make(map[int]string, len(iconSizes))
	for _, size := range iconSizes {
		var body []byte
		name := stem + "-" + strconv.Itoa(size)
		if p, ok := b.iconPNGs[size]; ok {
			body, err = os.ReadFile(filepath.Join(b.src, filepath.FromSlash(p)))
			name = strings.TrimSuffix(path.Base(p), ".png")
		} else {
			body, err = renderIcon(svg, size)
		}
		if err != nil {
			return nil, fmt.Errorf("webbundle: icon %s at %d px: %w", b.icon, size, err)
		}
		if pngs[size], err = t.writeHashed(name, ".png", body); err != nil {
			return nil, err
		}
	}
	var links bytes.Buffer
	fmt.Fprintf(&links, "<link rel=\"icon\" href=\"/%s\" type=\"image/svg+xml\">\n", svgPath)
	fmt.Fprintf(&links, "<link rel=\"icon\" href=\"/%s\" sizes=\"%[2]dx%[2]d\" type=\"image/png\">\n", pngs[tabIcon], tabIcon)
	fmt.Fprintf(&links, "<link rel=\"apple-touch-icon\" href=\"/%s\">\n", pngs[touchIcon])
	if b.manifest != nil {
		p, err := b.writeManifest(t, pngs)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&links, "<link rel=\"manifest\" href=\"/%s\">\n", p)
	}
	return links.Bytes(), nil
}

func (b *build) writeManifest(t *tree, pngs map[int]string) (string, error) {
	m := webManifest{
		Name:            b.manifest.Name,
		ShortName:       b.manifest.ShortName,
		ThemeColor:      b.manifest.ThemeColor,
		BackgroundColor: b.manifest.BackgroundColor,
		StartURL:        "/",
		Display:         "standalone",
	}
	for _, size := range []int{smallIcon, largeIcon} {
		m.Icons = append(m.Icons, manifestIcon{Src: "/" + pngs[size], Sizes: fmt.Sprintf("%[1]dx%[1]d", size), Type: "image/png"})
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return "", fmt.Errorf("webbundle: manifest: %w", err)
	}
	return t.writeHashed("manifest", ".webmanifest", body.Bytes())
}

// iconRels are the link relations Build writes for the icon, so a shell
// carrying one of its own would name a second icon.
var iconRels = []string{"icon", "apple-touch-icon", "manifest"}
