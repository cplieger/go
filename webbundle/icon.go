package webbundle

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/net/html"
)

// The PNG sizes Build writes: the browser tab, apple-touch-icon, and the
// manifest's two icons.
const (
	tabIcon   = 32
	touchIcon = 180
	smallIcon = 192
	largeIcon = 512
)

// pngSizes are the PNGs each purpose's SVG gives; a maskable icon is shown
// only by an installed app, from the manifest.
var pngSizes = map[IconPurpose][]int{
	PurposeAny:      {tabIcon, touchIcon, smallIcon, largeIcon},
	PurposeMaskable: {smallIcon, largeIcon},
}

var purposes = []IconPurpose{PurposeAny, PurposeMaskable}

func purposeName(p IconPurpose) string {
	if p == PurposeAny {
		return "any"
	}
	return string(p)
}

var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// iconArt is one SVG under Src and the PNGs Build writes from it.
type iconArt struct {
	pngs    map[int]string // committed PNGs by size; nil renders the SVG
	svg     string
	purpose IconPurpose
}

// checkIcons checks Icon, MaskableIcon, IconPNGs and Manifest and, for each
// SVG Build renders, that the renderer draws every element and attribute.
func (b *build) checkIcons(c *Config) error {
	switch {
	case c.Manifest != "" && c.Icon == "":
		return errors.New("webbundle: Config.Manifest is set and Config.Icon is not; the manifest's icons come from it")
	case c.MaskableIcon != "" && c.Manifest == "":
		return errors.New("webbundle: Config.MaskableIcon is set and Config.Manifest is not; a maskable icon is listed only in the manifest")
	}
	committed, err := splitIconPNGs(c.IconPNGs)
	if err != nil {
		return err
	}
	for _, a := range []struct {
		dst     **iconArt
		field   string
		svg     string
		purpose IconPurpose
	}{
		{dst: &b.icon, field: "Icon", svg: c.Icon, purpose: PurposeAny},
		{dst: &b.maskable, field: "MaskableIcon", svg: c.MaskableIcon, purpose: PurposeMaskable},
	} {
		pngs := committed[a.purpose]
		if a.svg == "" {
			if pngs != nil {
				return fmt.Errorf("webbundle: Config.IconPNGs holds %s PNGs and Config.%s is not set; the PNGs are its", purposeName(a.purpose), a.field)
			}
			continue
		}
		if *a.dst, err = b.checkArt(a.field, a.svg, a.purpose, pngs); err != nil {
			return err
		}
	}
	if c.Manifest != "" {
		b.manifest, err = b.readManifest(c.Manifest)
	}
	return err
}

// splitIconPNGs sorts IconPNGs by purpose, refusing a purpose or size Build
// writes no PNG for and a purpose missing one of its sizes.
func splitIconPNGs(pngs map[IconPNG]string) (map[IconPurpose]map[int]string, error) {
	out := map[IconPurpose]map[int]string{}
	for k, name := range pngs {
		sizes, ok := pngSizes[k.Purpose]
		switch {
		case !ok:
			return nil, fmt.Errorf("webbundle: Config.IconPNGs holds purpose %q; the purposes are PurposeAny and PurposeMaskable", k.Purpose)
		case !slices.Contains(sizes, k.Size):
			return nil, fmt.Errorf("webbundle: Config.IconPNGs holds a %d px %s PNG; the %s sizes are %v", k.Size, purposeName(k.Purpose), purposeName(k.Purpose), sizes)
		case out[k.Purpose] == nil:
			out[k.Purpose] = map[int]string{}
		}
		out[k.Purpose][k.Size] = name
	}
	for _, p := range purposes {
		for _, size := range pngSizes[p] {
			if _, ok := out[p][size]; out[p] != nil && !ok {
				return nil, fmt.Errorf("webbundle: Config.IconPNGs has no %d px %s PNG; it names one for each of %v", size, purposeName(p), pngSizes[p])
			}
		}
	}
	return out, nil
}

// checkArt checks one SVG named by Config.<field> and the PNGs committed for
// it, or, with none, that the renderer can draw it.
func (b *build) checkArt(field, name string, purpose IconPurpose, committed map[int]string) (*iconArt, error) {
	p, err := b.srcFile(field, name)
	if err != nil {
		return nil, err
	}
	if path.Ext(p) != ".svg" {
		return nil, fmt.Errorf("webbundle: %s %s is not an .svg file", field, name)
	}
	art := &iconArt{svg: p, purpose: purpose}
	if committed != nil {
		art.pngs, err = b.checkIconPNGs(purpose, committed)
		return art, err
	}
	svg, err := os.ReadFile(filepath.Join(b.src, filepath.FromSlash(p)))
	if err != nil {
		return nil, fmt.Errorf("webbundle: %s %s: %w", field, p, err)
	}
	if _, _, bad := readIcon(svg); bad != nil {
		return nil, fmt.Errorf("webbundle: %s %s: %w; commit its PNGs as Config.IconPNGs instead", field, p, bad)
	}
	return art, nil
}

// checkIconPNGs checks that each committed PNG is a PNG file of its size
// and returns their resolved paths by size.
func (b *build) checkIconPNGs(purpose IconPurpose, committed map[int]string) (map[int]string, error) {
	pngs := make(map[int]string, len(committed))
	for _, size := range pngSizes[purpose] {
		p, err := b.srcFile("icon PNG", committed[size])
		if err != nil {
			return nil, err
		}
		f, err := os.Open(filepath.Join(b.src, filepath.FromSlash(p)))
		if err != nil {
			return nil, fmt.Errorf("webbundle: icon PNG %s: %w", p, err)
		}
		cfg, err := png.DecodeConfig(f)
		f.Close()
		switch {
		case err != nil:
			return nil, fmt.Errorf("webbundle: icon PNG %s is no PNG: %w", p, err)
		case cfg.Width != size || cfg.Height != size:
			return nil, fmt.Errorf("webbundle: icon PNG %s is %dx%d, not %d px square", p, cfg.Width, cfg.Height, size)
		}
		pngs[size] = p
	}
	return pngs, nil
}

const svgRoot = "svg"

// iconElements are the SVG elements oksvg draws; anything else (text, a
// filter, a mask, a clip path, an image) would be dropped from the render,
// so the icon is refused instead.
var iconElements = slices.Concat([]string{svgRoot, "defs", "title", "desc"}, transformable,
	[]string{"linearGradient", "radialGradient", "stop"})

// iconPaint are the presentation properties oksvg applies, as attributes or
// in a style attribute.
var iconPaint = []string{
	"fill", "fill-opacity", "stroke", "stroke-opacity", "stroke-width",
	"stroke-linecap", "stroke-linejoin", "stroke-miterlimit",
	"stroke-dasharray", "stroke-dashoffset", "opacity",
}

// iconGeometry are the other attributes oksvg reads.
var iconGeometry = []string{
	nsXMLNS, "version", "id", "viewBox", hrefAttr,
	"x", "y", "width", "height", "rx", "ry", "cx", "cy", "r", "fx", "fy",
	"x1", "y1", "x2", "y2", "d", "points", "offset", "stop-color", "stop-opacity",
	"gradientTransform", "gradientUnits", "spreadMethod",
}

// checkIconSVG refuses an element or attribute oksvg would not draw as a
// browser does, and returns the edits that make oksvg draw svg as SVG does:
// SVG's initial stroke values on the root, a rounded rect as a path of arcs
// and each transform as a matrix.
// oksvg's strict mode refuses only an unknown element outside defs, and it
// ignores an attribute it does not read.
func checkIconSVG(svg []byte) ([]svgEdit, error) {
	dec := xml.NewDecoder(bytes.NewReader(svg))
	w := svgWalk{svg: svg, root: -1}
	for {
		at := int(dec.InputOffset())
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := w.token(tok, at, int(dec.InputOffset())); err != nil {
			return nil, err
		}
	}
	if w.root < 0 {
		return nil, errors.New("no <svg> element written as <svg")
	}
	return w.edits, nil
}

// svgWalk collects checkIconSVG's edits token by token.
type svgWalk struct {
	svg     []byte
	edits   []svgEdit
	renamed []bool // per open element, whether its tag becomes <path>
	root    int
}

func (w *svgWalk) token(tok xml.Token, at, end int) error {
	raw := w.svg[at:end]
	switch t := tok.(type) {
	case xml.StartElement:
		if len(w.renamed) == 0 {
			if w.root = rootEnd(w.svg, at); w.root >= 0 {
				w.edits = append(w.edits, svgEdit{text: svgInitial, from: w.root, to: w.root})
			}
		}
		e, rename, err := startEdits(raw, at, t, len(w.renamed))
		if err != nil {
			return err
		}
		w.edits, w.renamed = append(w.edits, e...), append(w.renamed, rename)
	case xml.EndElement:
		if w.renamed[len(w.renamed)-1] {
			if e := endTagEdit(raw, at); e != nil {
				w.edits = append(w.edits, *e)
			}
		}
		w.renamed = w.renamed[:len(w.renamed)-1]
	}
	return nil
}

// startEdits checks the start tag raw at offset at and returns its edits,
// reporting whether it is renamed to <path>.
func startEdits(raw []byte, at int, t xml.StartElement, depth int) ([]svgEdit, bool, error) {
	if err := checkIconElement(t, depth); err != nil {
		return nil, false, err
	}
	var edits []svgEdit
	r, err := rectEdit(raw, at, t)
	if err != nil {
		return nil, false, err
	}
	if r != nil {
		edits = append(edits, *r)
	}
	e, err := transformEdit(raw, at, t)
	if err != nil {
		return nil, false, err
	}
	if e != nil {
		edits = append(edits, *e)
	}
	return edits, r != nil, nil
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
		if a.Name.Space == nsXMLNS {
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
	case name == attrTransform:
		if !slices.Contains(transformable, elem) {
			return fmt.Errorf("the renderer cannot draw <%s transform>", elem)
		}
		if _, err := parseTransform(value); err != nil {
			return fmt.Errorf("<%s>: %w", elem, err)
		}
		return nil
	case name == "gradientUnits" && value == "userSpaceOnUse":
		// rasterx places such a gradient in device pixels, not the viewBox.
		return fmt.Errorf("the renderer cannot draw <%s gradientUnits=%q>; use objectBoundingBox", elem, value)
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
		if k = strings.TrimSpace(k); k == attrTransform {
			return fmt.Errorf("the renderer cannot draw transform in <%s style>; set the transform attribute", elem)
		}
		if k != "" {
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

// readIcon checks and parses an icon, returning with it each path's stroke
// scale: the factor its transform scales lengths by.
func readIcon(svg []byte) (*oksvg.SvgIcon, []float64, error) {
	edits, err := checkIconSVG(svg)
	if err != nil {
		return nil, nil, err
	}
	icon, err := oksvg.ReadIconStream(bytes.NewReader(applyEdits(svg, edits)), oksvg.StrictErrorMode)
	if err != nil {
		return nil, nil, err
	}
	if icon.ViewBox.W <= 0 || icon.ViewBox.W != icon.ViewBox.H {
		return nil, nil, fmt.Errorf("its viewBox is %gx%g, not square", icon.ViewBox.W, icon.ViewBox.H)
	}
	scales := make([]float64, len(icon.SVGPaths))
	for i := range icon.SVGPaths {
		if scales[i], err = strokeScale(&icon.SVGPaths[i]); err != nil {
			return nil, nil, err
		}
	}
	return icon, scales, nil
}

// strokeScale is the factor p's transform scales lengths by, refusing a
// stroke under a transform that scales directions unequally (a skew or
// scale(2 1)), which no single stroke width draws.
func strokeScale(p *oksvg.SvgPath) (float64, error) {
	// oksvg keeps a path's transform and stroke paint unexported.
	style := reflect.ValueOf(&p.PathStyle).Elem()
	mv, liner := style.FieldByName("mAdder").FieldByName("M"), style.FieldByName("linerColor")
	if mv.Type() != reflect.TypeFor[rasterx.Matrix2D]() || liner.Kind() != reflect.Interface {
		return 0, errors.New("the renderer's path transform is unreadable")
	}
	f := func(n string) float64 { return mv.FieldByName(n).Float() }
	a, b, c, d := f("A"), f("B"), f("C"), f("D")
	cols, dot := a*a+b*b-(c*c+d*d), a*c+b*d
	if !liner.IsNil() && (math.Abs(cols) > 1e-9*(a*a+b*b) || math.Abs(dot) > 1e-9*(a*a+b*b)) {
		return 0, errors.New("the renderer cannot draw a stroke under a skew or a scale unequal in x and y")
	}
	return math.Sqrt(math.Abs(a*d - b*c)), nil
}

// renderIcon draws svg at size px square as PNG bytes.
func renderIcon(svg []byte, size int) ([]byte, error) {
	icon, scales, err := readIcon(svg)
	if err != nil {
		return nil, err
	}
	icon.SetTarget(0, 0, float64(size), float64(size))
	// oksvg hands the dasher stroke widths and dashes in device pixels while
	// it transforms the path, so a stroke would keep its user-space width
	// (https://github.com/srwiley/oksvg/pull/54, unmerged).
	for i := range icon.SVGPaths {
		p := &icon.SVGPaths[i]
		k := float64(size) / icon.ViewBox.W * scales[i]
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

// writeIcons writes the icon, the PNGs and the manifest into t as hashed
// files under assets/ and returns the attributes of the link elements each
// shell's head ends with.
func (b *build) writeIcons(t *tree) ([][]html.Attribute, error) {
	if b.icon == nil {
		return nil, nil
	}
	svg, err := os.ReadFile(filepath.Join(b.src, filepath.FromSlash(b.icon.svg)))
	if err != nil {
		return nil, fmt.Errorf("webbundle: icon %s: %w", b.icon.svg, err)
	}
	svgPath, err := t.writeHashed(strings.TrimSuffix(path.Base(b.icon.svg), ".svg"), ".svg", svg)
	if err != nil {
		return nil, err
	}
	pngs, err := b.writePNGs(t, b.icon)
	if err != nil {
		return nil, err
	}
	links := make([][]html.Attribute, 0, len(iconRels)+1)
	links = append(links,
		[]html.Attribute{{Key: relAttr, Val: relIcon}, {Key: hrefAttr, Val: "/" + svgPath}, {Key: typeAttr, Val: "image/svg+xml"}},
		[]html.Attribute{{Key: relAttr, Val: relIcon}, {Key: hrefAttr, Val: "/" + pngs[tabIcon]}, {Key: "sizes", Val: fmt.Sprintf("%[1]dx%[1]d", tabIcon)}, {Key: typeAttr, Val: "image/png"}},
		[]html.Attribute{{Key: relAttr, Val: relTouchIcon}, {Key: hrefAttr, Val: "/" + pngs[touchIcon]}},
	)
	if b.manifest == nil {
		return links, nil
	}
	icons := manifestIcons(pngs, PurposeAny)
	if b.maskable != nil {
		masks, merr := b.writePNGs(t, b.maskable)
		if merr != nil {
			return nil, merr
		}
		icons = append(icons, manifestIcons(masks, PurposeMaskable)...)
	}
	p, err := b.manifest.write(t, b.src, icons)
	if err != nil {
		return nil, err
	}
	return append(links, []html.Attribute{{Key: relAttr, Val: relManifest}, {Key: hrefAttr, Val: "/" + p}}), nil
}

// iconFiles lists every file under Src writeIcons copies or renders into
// the tree: the SVGs, the committed PNGs, the manifest and its src files.
func (b *build) iconFiles() []string {
	var files []string
	for _, art := range []*iconArt{b.icon, b.maskable} {
		if art != nil {
			files = append(files, art.svg)
			files = append(files, slices.Collect(maps.Values(art.pngs))...)
		}
	}
	if b.manifest != nil {
		files = append(files, b.manifest.path)
		for _, s := range b.manifest.srcs {
			files = append(files, s.file)
		}
	}
	return files
}

// writePNGs writes art's PNGs, committed or rendered, and returns their
// paths in the tree by size.
func (b *build) writePNGs(t *tree, art *iconArt) (map[int]string, error) {
	svg, err := os.ReadFile(filepath.Join(b.src, filepath.FromSlash(art.svg)))
	if err != nil {
		return nil, fmt.Errorf("webbundle: icon %s: %w", art.svg, err)
	}
	stem := strings.TrimSuffix(path.Base(art.svg), ".svg")
	pngs := make(map[int]string, len(pngSizes[art.purpose]))
	for _, size := range pngSizes[art.purpose] {
		var body []byte
		name := stem + "-" + strconv.Itoa(size)
		if p, ok := art.pngs[size]; ok {
			body, err = os.ReadFile(filepath.Join(b.src, filepath.FromSlash(p)))
			name = strings.TrimSuffix(path.Base(p), ".png")
		} else {
			body, err = renderIcon(svg, size)
		}
		if err != nil {
			return nil, fmt.Errorf("webbundle: icon %s at %d px: %w", art.svg, size, err)
		}
		if pngs[size], err = t.writeHashed(name, ".png", body); err != nil {
			return nil, err
		}
	}
	return pngs, nil
}

// manifestIcons lists the manifest's 192 and 512 px icons of one purpose;
// an icon is never listed as "any maskable", since art drawn for a mask's
// safe zone looks small unmasked and art drawn to the edge loses its edges
// under a mask (https://www.w3.org/TR/appmanifest/#icon-masks).
func manifestIcons(pngs map[int]string, purpose IconPurpose) []any {
	icons := make([]any, 0, 2)
	for _, size := range []int{smallIcon, largeIcon} {
		icon := map[string]any{srcMember: "/" + pngs[size], "sizes": fmt.Sprintf("%[1]dx%[1]d", size), "type": "image/png"}
		if purpose != PurposeAny {
			icon["purpose"] = string(purpose)
		}
		icons = append(icons, icon)
	}
	return icons
}

// iconRels are the link relations Build writes for the icon, so a shell
// carrying one of its own would name a second icon.
var iconRels = []string{relIcon, relTouchIcon, relManifest}

const (
	relIcon      = "icon"
	relTouchIcon = "apple-touch-icon"
	relManifest  = "manifest"
)
