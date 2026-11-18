package webbundle

import (
	"bytes"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// shell is one shell read from Src: html.Parse's tree of it and each
// element naming an entry's logical path.
type shell struct {
	name string
	doc  *html.Node
	head *html.Node
	refs []ref
}

// ref is a script or link element naming an entry: its attr'th attribute is
// the logical path.
type ref struct {
	node  *html.Node
	entry int // index into build.entries
	attr  int
}

// readShell refuses a shell httpserver.CheckShell refuses, which is what
// httpserver.SPA's policy would block, then finds the HTML script and link
// elements of html.Parse's tree naming a logical path, so a comment, a
// raw-text body or foreign content holds none. It also refuses a logical
// path named twice, by an element that does not load its entry's kind, or
// one no entry is.
func (b *build) readShell(name string) (shell, error) {
	body, err := os.ReadFile(filepath.Join(b.src, name))
	if err != nil {
		return shell{}, fmt.Errorf("webbundle: shell %s: %w", name, err)
	}
	if bad := httpserver.CheckShell(body); bad != nil {
		return shell{}, fmt.Errorf("webbundle: shell %s %w", name, bad)
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return shell{}, fmt.Errorf("webbundle: shell %s: %w", name, err)
	}
	logical := map[string]int{}
	for i, e := range b.entries {
		logical[logicalPath(&e)] = i
	}
	sh := shell{name: name, doc: doc, head: headOf(doc)}
	r := shellReader{shell: &sh, entries: b.entries, logical: logical, named: map[string]bool{}, iconRels: b.icon != nil}
	for n := range scriptsAndLinks(doc) {
		if err := r.element(n); err != nil {
			return shell{}, err
		}
	}
	return sh, nil
}

// headOf returns the head element html.Parse puts first in every document's
// html element.
func headOf(doc *html.Node) *html.Node {
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Namespace == "" && n.Data == "head" {
			return n
		}
	}
	return nil
}

// scriptsAndLinks yields doc's HTML script and link elements in document
// order.
func scriptsAndLinks(doc *html.Node) iter.Seq[*html.Node] {
	return func(yield func(*html.Node) bool) {
		for n := range doc.Descendants() {
			if n.Type == html.ElementNode && n.Namespace == "" && (n.Data == scriptElement || n.Data == linkElement) && !yield(n) {
				return
			}
		}
	}
}

// shellReader is one pass over a shell's script and link elements.
type shellReader struct {
	*shell
	logical  map[string]int  // entry index by logical path
	named    map[string]bool // the logical paths read so far
	entries  []Entry
	iconRels bool // whether to refuse a link Build writes for the icon
}

// element checks one script or link element and records it when it names
// an entry.
func (r *shellReader) element(n *html.Node) error {
	for i, a := range n.Attr {
		if err := r.iconRel(n, a); err != nil {
			return err
		}
		isRef := n.Data == scriptElement && a.Key == "src" || n.Data == linkElement && a.Key == hrefAttr
		if !isRef || !rootScriptOrStyle(a.Val) {
			continue
		}
		e, err := r.entry(n, a.Val)
		if err != nil {
			return err
		}
		r.refs = append(r.refs, ref{node: n, entry: e, attr: i})
	}
	return nil
}

// iconRel refuses a link relation Build writes itself for the icon.
func (r *shellReader) iconRel(n *html.Node, a html.Attribute) error {
	if n.Data != linkElement || a.Key != relAttr || !r.iconRels {
		return nil
	}
	for rel := range strings.FieldsSeq(lowerASCII(a.Val)) {
		if slices.Contains(iconRels, rel) {
			return fmt.Errorf("webbundle: shell %s carries a <link rel=%q>; Build writes the %s link from Config.Icon", r.name, a.Val, rel)
		}
	}
	return nil
}

// entry returns the index of the entry whose logical path n names as p,
// refusing a path no entry is, one named before and an element that does
// not load the entry's kind.
func (r *shellReader) entry(n *html.Node, p string) (int, error) {
	e, ok := r.logical[p]
	switch {
	case !ok:
		return 0, fmt.Errorf("webbundle: shell %s names %s, which no entry is", r.name, p)
	case r.named[p]:
		return 0, fmt.Errorf("webbundle: shell %s names %s more than once", r.name, p)
	case loads(n) != r.entries[e].Kind:
		return 0, fmt.Errorf("webbundle: shell %s names %s, %s entry %q, in %s, which does not load it as one; use %s",
			r.name, p, r.entries[e].Kind, r.entries[e].Name, startTag(n), kindTag[r.entries[e].Kind])
	}
	r.named[p] = true
	return e, nil
}

func startTag(n *html.Node) string {
	return html.Token{Type: html.StartTagToken, Data: n.Data, Attr: n.Attr}.String()
}

const (
	linkElement   = "link"
	scriptElement = "script"
	hrefAttr      = "href"
	relAttr       = "rel"
	typeAttr      = "type"
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
func loads(n *html.Node) EntryKind {
	if n.Data == linkElement {
		rels := strings.Fields(lowerASCII(attrVal(n, relAttr)))
		if slices.Contains(rels, "stylesheet") && !slices.Contains(rels, "alternate") {
			return Style
		}
		return ""
	}
	typ, hasType := attrOf(n, typeAttr)
	lang, hasLang := attrOf(n, "language")
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

func attrOf(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func attrVal(n *html.Node, key string) string {
	v, _ := attrOf(n, key)
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

// rootScriptOrStyle reports whether v has a logical path's shape:
// "/<name>.js" or "/<name>.css".
func rootScriptOrStyle(v string) bool {
	name, ok := strings.CutPrefix(v, "/")
	return ok && name != "" && !strings.ContainsAny(name, "/?#\\") &&
		(strings.HasSuffix(name, jsExt) || strings.HasSuffix(name, cssExt))
}

// render writes the shell with each element naming a logical path naming
// its entry's built one and icon's link elements appended to the head. It
// edits the tree, so it runs once. html.Render does not promise that every
// tree reads back as itself, so the written shell is parsed again and
// refused unless its script and link elements are the edited tree's, and
// CheckShell runs on it as SPA will.
func (s *shell) render(paths []string, icon [][]html.Attribute) ([]byte, error) {
	for _, r := range s.refs {
		r.node.Attr[r.attr].Val = "/" + paths[r.entry]
	}
	for _, attrs := range icon {
		s.head.AppendChild(&html.Node{Type: html.ElementNode, Data: linkElement, DataAtom: atom.Link, Attr: attrs})
		s.head.AppendChild(&html.Node{Type: html.TextNode, Data: "\n"})
	}
	var out bytes.Buffer
	if err := html.Render(&out, s.doc); err != nil {
		return nil, fmt.Errorf("webbundle: shell %s: %w", s.name, err)
	}
	back, err := html.Parse(bytes.NewReader(out.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("webbundle: shell %s as written: %w", s.name, err)
	}
	if !slices.EqualFunc(slices.Collect(scriptsAndLinks(s.doc)), slices.Collect(scriptsAndLinks(back)), sameElement) {
		return nil, fmt.Errorf("webbundle: shell %s does not read back as written: html.Render moves or changes a script or link element", s.name)
	}
	if bad := httpserver.CheckShell(out.Bytes()); bad != nil {
		return nil, fmt.Errorf("webbundle: shell %s as written %w", s.name, bad)
	}
	return out.Bytes(), nil
}

func sameElement(a, b *html.Node) bool {
	return a.Data == b.Data && slices.Equal(a.Attr, b.Attr)
}
