package webbundle

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
	"golang.org/x/net/html"
)

// shell is one shell read from Src: its bytes and each tag naming an entry's
// logical path, with the span to replace.
type shell struct {
	name  string
	body  []byte
	links []link
	head  int // the offset of </head>; -1 is none

	iconRels bool // whether to refuse a link Build writes for the icon
}

// link is a start tag naming an entry: the tag's byte span in the shell and
// the token it is written back from, its attr'th attribute the logical path.
type link struct {
	tok        html.Token
	entry      int // index into build.entries
	start, end int
	attr       int
}

// readShell reads a shell as the HTML tokenizer does, so a comment or a
// raw-text element's body holds no tags, and refuses what httpserver.SPA's
// policy would block: an inline script or style element, a style attribute,
// and an inline event handler (an on* attribute, which script-src 'self'
// blocks as it does an inline script). It also refuses a logical path named
// twice, by a tag that does not load its entry's kind, or one no entry is.
func (b *build) readShell(name string) (shell, error) {
	body, err := os.ReadFile(filepath.Join(b.src, name))
	if err != nil {
		return shell{}, fmt.Errorf("webbundle: shell %s: %w", name, err)
	}
	if bad := httpserver.CheckShell(body); bad != nil {
		return shell{}, fmt.Errorf("webbundle: shell %s %w", name, bad)
	}
	logical := map[string]int{}
	for i, e := range b.entries {
		logical[logicalPath(&e)] = i
	}
	sh := shell{name: name, body: body, head: -1, iconRels: b.icon != nil}
	r := shellReader{shell: &sh, entries: b.entries, logical: logical, named: map[string]bool{}}
	if err := r.walk(); err != nil {
		return shell{}, err
	}
	if sh.iconRels && sh.head < 0 {
		return shell{}, fmt.Errorf("webbundle: shell %s has no </head> to end with the icon links", name)
	}
	return sh, nil
}

// shellReader is one walk over a shell's tokens.
type shellReader struct {
	*shell
	logical map[string]int  // entry index by logical path
	named   map[string]bool // the logical paths read so far
	entries []Entry
}

// walk reads every token of the shell, tracking each one's offset as the sum
// of the raw bytes before it.
func (r *shellReader) walk() error {
	z := html.NewTokenizer(bytes.NewReader(r.body))
	var f foreignStack
	for at := 0; ; {
		z.AllowCDATA(len(f) > 0)
		tt := z.Next()
		start := at
		at += len(z.Raw())
		switch tt {
		case html.ErrorToken:
			return nil
		case html.EndTagToken:
			tok := z.Token()
			f.end(tok.Data)
			if tok.Data == "head" && r.head < 0 {
				r.head = start
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			inHTML := f.start(z, tok, tt == html.SelfClosingTagToken)
			if err := r.tag(tok, inHTML, start, at); err != nil {
				return err
			}
		}
	}
}

// tag checks one start tag and records it when it names an entry.
func (r *shellReader) tag(tok html.Token, inHTML bool, start, end int) error {
	if err := r.handlers(tok); err != nil || !inHTML {
		return err
	}
	for i, a := range tok.Attr {
		if err := r.iconRel(tok, a); err != nil {
			return err
		}
		ref := tok.Data == "script" && a.Key == "src" || tok.Data == linkElement && a.Key == "href"
		if !ref || !rootScriptOrStyle(a.Val) {
			continue
		}
		e, err := r.entry(tok, a.Val)
		if err != nil {
			return err
		}
		r.links = append(r.links, link{tok: tok, entry: e, start: start, end: end, attr: i})
	}
	return nil
}

// handlers refuses an on* attribute, an inline event handler.
func (r *shellReader) handlers(tok html.Token) error {
	for _, a := range tok.Attr {
		if len(a.Key) > len("on") && strings.HasPrefix(a.Key, "on") {
			return fmt.Errorf("webbundle: shell %s: <%s> carries %s, an inline event handler the policy blocks", r.name, tok.Data, a.Key)
		}
	}
	return nil
}

// iconRel refuses a link relation Build writes itself for the icon.
func (r *shellReader) iconRel(tok html.Token, a html.Attribute) error {
	if tok.Data != linkElement || a.Key != "rel" || !r.iconRels {
		return nil
	}
	for rel := range strings.FieldsSeq(lowerASCII(a.Val)) {
		if slices.Contains(iconRels, rel) {
			return fmt.Errorf("webbundle: shell %s carries a <link rel=%q>; Build writes the %s link from Config.Icon", r.name, a.Val, rel)
		}
	}
	return nil
}

// entry returns the index of the entry whose logical path tok names as p,
// refusing a path no entry is, one named before and a tag that does not load
// the entry's kind.
func (r *shellReader) entry(tok html.Token, p string) (int, error) {
	e, ok := r.logical[p]
	switch {
	case !ok:
		return 0, fmt.Errorf("webbundle: shell %s names %s, which no entry is", r.name, p)
	case r.named[p]:
		return 0, fmt.Errorf("webbundle: shell %s names %s more than once", r.name, p)
	case loads(tok) != r.entries[e].Kind:
		return 0, fmt.Errorf("webbundle: shell %s names %s, %s entry %q, in %s, which does not load it as one; use %s",
			r.name, p, r.entries[e].Kind, r.entries[e].Name, tok, kindTag[r.entries[e].Kind])
	}
	r.named[p] = true
	return e, nil
}

const (
	linkElement = "link"
	mathRoot    = "math"
)

var kindTag = map[EntryKind]string{
	Module:  `<script type="module" src>`,
	Classic: `<script src> with no type or a JavaScript one`,
	Style:   `<link rel="stylesheet" href>`,
}

// loads returns the kind of entry a <script src> or <link href> loads, or ""
// for none: a script's type is read as
// https://html.spec.whatwg.org/multipage/scripting.html#prepare-the-script-element
// reads it, and only a link whose rel holds stylesheet and not alternate
// applies its stylesheet.
func loads(tok html.Token) EntryKind {
	if tok.Data == linkElement {
		rels := strings.Fields(lowerASCII(attrVal(tok, "rel")))
		if slices.Contains(rels, "stylesheet") && !slices.Contains(rels, "alternate") {
			return Style
		}
		return ""
	}
	typ, hasType := attrOf(tok, "type")
	lang, hasLang := attrOf(tok, "language")
	switch {
	case hasType && typ != "":
	case !hasType && hasLang && lang != "":
		typ = "text/" + lang
	default:
		return Classic
	}
	switch typ = lowerASCII(strings.Trim(typ, " \t\n\f\r")); {
	case typ == "module":
		return Module
	case slices.Contains(javaScriptTypes, typ):
		return Classic
	}
	return ""
}

// javaScriptTypes are the JavaScript MIME type essences of
// https://mimesniff.spec.whatwg.org/#javascript-mime-type.
var javaScriptTypes = []string{
	"application/ecmascript", "application/javascript", "application/x-ecmascript",
	"application/x-javascript", "text/ecmascript", "text/javascript",
	"text/javascript1.0", "text/javascript1.1", "text/javascript1.2",
	"text/javascript1.3", "text/javascript1.4", "text/javascript1.5",
	"text/jscript", "text/livescript", "text/x-ecmascript", "text/x-javascript",
}

func attrOf(tok html.Token, key string) (string, bool) {
	for _, a := range tok.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func attrVal(tok html.Token, key string) string {
	v, _ := attrOf(tok, key)
	return v
}

// lowerASCII lower-cases ASCII letters only, as HTML compares keywords.
func lowerASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// foreignStack holds the open SVG and MathML elements, so the walk switches
// the tokenizer's raw-text reading off inside them as html.Parse does: there
// a <title> or <style> holds markup.
type foreignStack []foreignElement

type foreignElement struct {
	ns, name string
	html     bool // an integration point: its content is read as HTML
}

// html reports whether the next token is read as HTML content.
func (f foreignStack) html() bool { return len(f) == 0 || f[len(f)-1].html }

// start records a start tag and reports whether it is an HTML element,
// following https://html.spec.whatwg.org/multipage/parsing.html#parsing-main-inforeign.
func (f *foreignStack) start(z *html.Tokenizer, tok html.Token, selfClosing bool) bool {
	var ns string
	switch {
	case f.html():
		if tok.Data != svgRoot && tok.Data != mathRoot {
			return true
		}
		ns = tok.Data
	case breakout(tok):
		for len(*f) > 0 && !f.html() {
			*f = (*f)[:len(*f)-1]
		}
		return f.html()
	default:
		top := (*f)[len(*f)-1]
		ns = top.ns
		if tok.Data == svgRoot && top.name == "annotation-xml" {
			ns = svgRoot
		}
		z.NextIsNotRawText()
	}
	if !selfClosing {
		*f = append(*f, foreignElement{ns: ns, name: tok.Data, html: integrationPoint(ns, tok)})
	}
	return false
}

// end closes the innermost open foreign element named name, if any.
func (f *foreignStack) end(name string) {
	for i, e := range slices.Backward(*f) {
		if e.name == name {
			*f = (*f)[:i]
			return
		}
	}
}

func integrationPoint(ns string, tok html.Token) bool {
	switch ns {
	case svgRoot:
		return slices.Contains([]string{"foreignobject", "desc", "title"}, tok.Data)
	case mathRoot:
		if tok.Data == "annotation-xml" {
			enc := lowerASCII(attrVal(tok, "encoding"))
			return enc == "text/html" || enc == "application/xhtml+xml"
		}
		return slices.Contains([]string{"mi", "mo", "mn", "ms", "mtext"}, tok.Data)
	}
	return false
}

var breakoutTags = []string{
	"b", "big", "blockquote", "body", "br", "center", "code", "dd", "div", "dl", "dt",
	"em", "embed", "h1", "h2", "h3", "h4", "h5", "h6", "head", "hr", "i", "img", "li",
	"listing", "menu", "meta", "nobr", "ol", "p", "pre", "ruby", "s", "small", "span",
	"strong", "strike", "sub", "sup", "table", "tt", "u", "ul", "var",
}

// breakout reports whether a start tag in foreign content returns to HTML.
func breakout(tok html.Token) bool {
	if tok.Data == "font" {
		return slices.ContainsFunc(tok.Attr, func(a html.Attribute) bool {
			return a.Key == "color" || a.Key == "face" || a.Key == "size"
		})
	}
	return slices.Contains(breakoutTags, tok.Data)
}

// rootScriptOrStyle reports whether v has a logical path's shape:
// "/<name>.js" or "/<name>.css".
func rootScriptOrStyle(v string) bool {
	name, ok := strings.CutPrefix(v, "/")
	return ok && name != "" && !strings.ContainsAny(name, "/?#\\") &&
		(strings.HasSuffix(name, jsExt) || strings.HasSuffix(name, cssExt))
}

// render returns the shell with each tag naming a logical path written back
// naming its entry's hashed one (every attribute double-quoted, as
// html.Token renders it) and head inserted before </head>.
func (s *shell) render(hashed []string, head []byte) []byte {
	out := make([]byte, 0, len(s.body)+len(s.links)*16+len(head))
	at := 0
	inserted := len(head) == 0
	for _, l := range s.links {
		if !inserted && s.head < l.start {
			out = append(append(out, s.body[at:s.head]...), head...)
			at, inserted = s.head, true
		}
		tok := l.tok
		tok.Attr = slices.Clone(tok.Attr)
		tok.Attr[l.attr].Val = "/" + hashed[l.entry]
		out = append(append(out, s.body[at:l.start]...), tok.String()...)
		at = l.end
	}
	if !inserted {
		out = append(append(out, s.body[at:s.head]...), head...)
		at = s.head
	}
	return append(out, s.body[at:]...)
}
