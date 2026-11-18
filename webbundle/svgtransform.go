package webbundle

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/srwiley/rasterx"
)

// transformArgs are the argument counts of each SVG transform function
// (https://www.w3.org/TR/SVG11/coords.html#TransformAttribute).
var transformArgs = map[string][]int{
	"matrix": {6}, "translate": {1, 2}, "scale": {1, 2}, "rotate": {1, 3}, "skewX": {1}, "skewY": {1},
}

// transformable are the elements whose transform the renderer applies.
var transformable = []string{"g", "use", elemPath, elemRect, "circle", "ellipse", "line", "polyline", "polygon"}

const (
	attrTransform = "transform"
	elemPath      = "path"
	elemRect      = "rect"
	nsXMLNS       = "xmlns"
)

var svgNumber = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?`)

// parseTransform reads an SVG transform list as one matrix, refusing a
// singular one, which draws nothing.
func parseTransform(v string) (rasterx.Matrix2D, error) {
	m := rasterx.Identity
	rest := strings.TrimLeft(v, svgSpace)
	for rest != "" {
		f, err := readTransformFunc(rest)
		if err != nil {
			return m, fmt.Errorf("the renderer cannot read transform %q: %w", v, err)
		}
		m = m.Mult(f.m)
		rest = strings.TrimLeft(f.rest, svgSpace)
		if after, ok := strings.CutPrefix(rest, ","); ok {
			if rest = strings.TrimLeft(after, svgSpace); rest == "" {
				return m, fmt.Errorf("the renderer cannot read transform %q: it ends with a comma", v)
			}
		}
	}
	if m.A*m.D-m.B*m.C == 0 {
		return m, fmt.Errorf("transform %q is singular and draws nothing", v)
	}
	return m, nil
}

const svgSpace = " \t\r\n"

type transformFunc struct {
	rest string
	m    rasterx.Matrix2D
}

// readTransformFunc reads the transform function s starts with.
func readTransformFunc(s string) (transformFunc, error) {
	name := s[:len(s)-len(strings.TrimLeft(s, "abcdefghijklmnopqrstuvwxyzXY"))]
	counts, ok := transformArgs[name]
	if !ok {
		return transformFunc{}, errors.New("it holds no transform function the SVG grammar names there")
	}
	rest, ok := strings.CutPrefix(strings.TrimLeft(s[len(name):], svgSpace), "(")
	if !ok {
		return transformFunc{}, fmt.Errorf("%s has no (", name)
	}
	var args []float64
	for {
		rest = strings.TrimLeft(rest, svgSpace)
		if after, done := strings.CutPrefix(rest, ")"); done {
			rest = after
			break
		}
		if len(args) > 0 {
			rest = strings.TrimLeft(strings.TrimPrefix(rest, ","), svgSpace)
		}
		num := svgNumber.FindString(rest)
		if num == "" {
			return transformFunc{}, fmt.Errorf("%s's arguments are not numbers closed by )", name)
		}
		f, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return transformFunc{}, err
		}
		args, rest = append(args, f), rest[len(num):]
	}
	if !slices.Contains(counts, len(args)) {
		return transformFunc{}, fmt.Errorf("%s takes %v numbers, not %d", name, counts, len(args))
	}
	return transformFunc{rest: rest, m: transformMatrix(name, args)}, nil
}

func transformMatrix(name string, a []float64) rasterx.Matrix2D {
	m := rasterx.Identity
	switch name {
	case "matrix":
		return rasterx.Matrix2D{A: a[0], B: a[1], C: a[2], D: a[3], E: a[4], F: a[5]}
	case "translate":
		return m.Translate(a[0], argOr(a, 1, 0))
	case "scale":
		return m.Scale(a[0], argOr(a, 1, a[0]))
	case "rotate":
		cx, cy := argOr(a, 1, 0), argOr(a, 2, 0)
		return m.Translate(cx, cy).Rotate(a[0]*math.Pi/180).Translate(-cx, -cy)
	case "skewX":
		return m.SkewX(a[0] * math.Pi / 180)
	}
	return m.SkewY(a[0] * math.Pi / 180)
}

func argOr(a []float64, i int, or float64) float64 {
	if i < len(a) {
		return a[i]
	}
	return or
}

// svgEdit replaces svg[from:to] with text.
type svgEdit struct {
	text     string
	from, to int
}

func applyEdits(svg []byte, edits []svgEdit) []byte {
	var out bytes.Buffer
	at := 0
	for _, e := range edits {
		out.Write(svg[at:e.from])
		out.WriteString(e.text)
		at = e.to
	}
	out.Write(svg[at:])
	return out.Bytes()
}

// transformEdit rewrites the transform attribute of start, whose raw bytes
// begin at offset at, as one matrix(), the one form oksvg reads as SVG does:
// it reads scale(s) as scale(s 0).
func transformEdit(raw []byte, at int, start xml.StartElement) (*svgEdit, error) {
	var value string
	n := 0
	for _, a := range start.Attr {
		if a.Name.Local == attrTransform && a.Name.Space != nsXMLNS {
			value, n = a.Value, n+1
		}
	}
	switch {
	case n == 0:
		return nil, nil
	case n > 1:
		return nil, fmt.Errorf("<%s> holds two transform attributes", start.Name.Local)
	}
	from, to, ok := transformSpan(raw)
	if !ok {
		return nil, fmt.Errorf("the renderer cannot read <%s transform>", start.Name.Local)
	}
	m, err := parseTransform(value)
	if err != nil {
		return nil, err
	}
	text := `"matrix(` + strings.Join([]string{fmtNum(m.A), fmtNum(m.B), fmtNum(m.C), fmtNum(m.D), fmtNum(m.E), fmtNum(m.F)}, " ") + `)"`
	return &svgEdit{text: text, from: at + from, to: at + to}, nil
}

// rectEdit rewrites a <rect> with rounded corners as a <path> of arcs:
// rasterx's rounded rectangle is off resvg's corner by up to 73 of 255 at
// 512 px, its arcs by 44. The radii follow SVG's auto and clamping rules
// (https://www.w3.org/TR/SVG2/geometry.html#RX). A renamed element's end tag
// needs endTagEdit.
func rectEdit(raw []byte, at int, start xml.StartElement) (*svgEdit, error) {
	if start.Name.Local != elemRect {
		return nil, nil
	}
	r, err := readRect(start)
	if err != nil || r.w <= 0 || r.h <= 0 || r.rx <= 0 || r.ry <= 0 {
		return nil, err
	}
	n := fmtNum
	arc := "A" + n(r.rx) + " " + n(r.ry) + " 0 0 1 "
	d := "M" + n(r.x+r.rx) + " " + n(r.y) + "H" + n(r.x+r.w-r.rx) + arc + n(r.x+r.w) + " " + n(r.y+r.ry) +
		"V" + n(r.y+r.h-r.ry) + arc + n(r.x+r.w-r.rx) + " " + n(r.y+r.h) +
		"H" + n(r.x+r.rx) + arc + n(r.x) + " " + n(r.y+r.h-r.ry) +
		"V" + n(r.y+r.ry) + arc + n(r.x+r.rx) + " " + n(r.y) + "Z"
	end := at + len(tagName.Find(raw))
	return &svgEdit{text: elemPath + ` d="` + d + `"`, from: end - len(elemRect), to: end}, nil
}

type rectBox struct{ x, y, w, h, rx, ry float64 }

// readRect reads a <rect>'s box and its radii after SVG's auto and clamping
// rules.
func readRect(start xml.StartElement) (rectBox, error) {
	v := map[string]float64{}
	given := map[string]bool{}
	for _, a := range start.Attr {
		k := a.Name.Local
		if a.Name.Space == nsXMLNS || !slices.Contains([]string{"x", "y", "width", "height", "rx", "ry"}, k) {
			continue
		}
		f, err := strconv.ParseFloat(strings.Trim(a.Value, svgSpace), 64)
		if err != nil || f < 0 && (k == "rx" || k == "ry") {
			return rectBox{}, fmt.Errorf("the renderer cannot read <rect %s=%q>", k, a.Value)
		}
		v[k], given[k] = f, true
	}
	r := rectBox{x: v["x"], y: v["y"], w: v["width"], h: v["height"], rx: v["rx"], ry: v["ry"]}
	switch {
	case given["rx"] && !given["ry"]:
		r.ry = r.rx
	case given["ry"] && !given["rx"]:
		r.rx = r.ry
	}
	r.rx, r.ry = min(r.rx, r.w/2), min(r.ry, r.h/2)
	return r, nil
}

var endTagName = regexp.MustCompile(`^</[^\s>]+`)

// endTagEdit renames the end tag raw, at offset at, to </path>; a
// self-closing tag has no end tag bytes.
func endTagEdit(raw []byte, at int) *svgEdit {
	end := at + len(endTagName.Find(raw))
	if end == at {
		return nil
	}
	return &svgEdit{text: elemPath, from: end - len(elemRect), to: end}
}

// fmtNum writes f with no exponent, which oksvg's number reader refuses.
func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

var (
	tagName  = regexp.MustCompile(`^<[^\s/>]+`)
	tagAttr  = regexp.MustCompile(`^\s+([^\s=/>]+)\s*=\s*("[^"]*"|'[^']*')`)
	tagClose = regexp.MustCompile(`^\s*/?>$`)
)

// transformSpan finds the quoted value of the one transform attribute in
// the raw start tag raw, reading the tag attribute by attribute so a quoted
// value holding the text transform="…" is never taken for one.
func transformSpan(raw []byte) (from, to int, ok bool) {
	at := len(tagName.Find(raw))
	if at == 0 {
		return 0, 0, false
	}
	from = -1
	for {
		m := tagAttr.FindSubmatchIndex(raw[at:])
		if m == nil {
			break
		}
		name := string(raw[at+m[2] : at+m[3]])
		if name[strings.LastIndex(name, ":")+1:] == attrTransform && !strings.HasPrefix(name, nsXMLNS+":") {
			from, to = at+m[4], at+m[5]
		}
		at += m[1]
	}
	return from, to, from >= 0 && tagClose.Match(raw[at:])
}
