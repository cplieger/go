package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

// InlineScriptHashes returns a CSP source token 'sha256-<base64>' for each
// inline script element, HTML or SVG, in page's document as html.Parse builds
// it, hashing its text as a browser does. A script a browser never checks
// against the policy is skipped: one with a source, a data block, and one left
// open at end of input. A page CheckShell refuses as one html.Parse cannot
// read whole yields nil, as a script-less page does, which a caller whose page
// holds an inline script treats as a broken build rather than loosening its
// policy. It reads pages the app controls and is not a sanitizer.
func InlineScriptHashes(page []byte) []string {
	doc, err := parseShell(page)
	if err != nil {
		return nil
	}
	open := openScript(doc, page)
	var out []string
	for n := range shellElements(doc) {
		if n != open && isInlineScript(n) {
			out = append(out, cspHash(childText(n)))
		}
	}
	return out
}

// InlineStyleHashes returns a CSP source token for each style element, HTML
// or SVG, in page, as InlineScriptHashes does for scripts. A style left open
// at end of input is hashed, since a browser applies it. It covers style
// elements only: a style attribute needs style-src-attr, which no element
// hash satisfies. It yields nil for the pages InlineScriptHashes does.
func InlineStyleHashes(page []byte) []string {
	doc, err := parseShell(page)
	if err != nil {
		return nil
	}
	var out []string
	for n := range shellElements(doc) {
		if isInlineStyle(n) {
			out = append(out, cspHash(childText(n)))
		}
	}
	return out
}

// parseShell is page's document as html.Parse builds it with scripting on. It
// fails where that tree is not the whole page: past html.Parse's 512 open
// elements, and where html.Parse stops at a <template>. It also fails past
// maxShellTemplates, the template check's own bound, on a whole tree.
func parseShell(page []byte) (*html.Node, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("cannot be read as HTML: %w", err)
	}
	if err := foreignTemplateError(doc, page); err != nil {
		return nil, err
	}
	return doc, nil
}

// maxShellTemplates bounds the template check at two parses per <template
// start-tag spelling after the first <svg or <math in a shell's bytes. A shell
// is a small page with no need for templates, so the limit is generous.
const maxShellTemplates = 16

// templateMark is text a parsed page holds wherever it is put after a
// template start tag, unless the parser drops what follows that tag.
const templateMark = "httpserver-template-probe"

// templateProbe puts templateMark after a <template> start tag, then again
// after closing any quoted attribute value or tag a prefix ends in.
const templateProbe = "<template>" + templateMark + `"'>` + templateMark

// foreignTemplateError refuses a <template> start tag after which html.Parse
// ignores the HTML tokens, as x/net/html v0.63.0's inHeadIM does
// (ignoreTheRemainingTokens) with an <svg> or <math> element open and a
// browser does not. Each <template spelling past the first <svg or <math in
// the bytes, in comments and attribute values too, is probed alone: a
// mark count over the whole page decides nothing, since the parser clones a
// formatting element with any mark in its attributes. An HTML <frameset> pops
// every foreign element and admits none, so a document holding one never stops.
func foreignTemplateError(doc *html.Node, page []byte) error {
	lower := lowerASCIIString(string(page))
	from := strings.Index(lower, "<svg")
	if m := strings.Index(lower, "<math"); m >= 0 && (from < 0 || m < from) {
		from = m
	}
	if from < 0 || hasFrameset(doc) {
		return nil
	}
	var starts []int
	for k := from; ; k += len("<template") {
		i := strings.Index(lower[k:], "<template")
		if i < 0 {
			break
		}
		k += i
		if opensTemplateTag(page[k:]) {
			starts = append(starts, k)
		}
	}
	if len(starts) > maxShellTemplates {
		return fmt.Errorf("spells <template more than %d times after its first <svg or <math, comments and attribute values included, past the check's limit", maxShellTemplates)
	}
	for _, k := range starts {
		if !readsPastTemplate(page[:k]) {
			return errors.New("holds a <template> inside <svg> or <math>, after which html.Parse drops markup a browser reads")
		}
	}
	return nil
}

func hasFrameset(doc *html.Node) bool {
	for n := range shellElements(doc) {
		if n.Data == "frameset" && n.Namespace == "" {
			return true
		}
	}
	return false
}

// opensTemplateTag reports whether page opens with a template start tag.
func opensTemplateTag(page []byte) bool {
	z := html.NewTokenizer(bytes.NewReader(page))
	if tt := z.Next(); tt == html.StartTagToken || tt == html.SelfClosingTagToken {
		name, _ := z.TagName()
		return string(name) == "template"
	}
	return false
}

// readsPastTemplate reports whether html.Parse keeps text after a <template>
// start tag placed after prefix, counting templateMark against prefix alone,
// since a page can spell it, directly or through character references.
func readsPastTemplate(prefix []byte) bool {
	before, err := html.Parse(bytes.NewReader(prefix))
	if err != nil {
		return false
	}
	after, err := html.Parse(bytes.NewReader(append(slices.Clip(prefix), templateProbe...)))
	return err == nil && templateMarks(after) > templateMarks(before)
}

func templateMarks(doc *html.Node) int {
	n := 0
	for d := range doc.Descendants() {
		n += strings.Count(d.Data, templateMark)
		for _, a := range d.Attr {
			n += strings.Count(a.Key, templateMark) + strings.Count(a.Val, templateMark)
		}
	}
	return n
}

// shellElements yields every element under doc in document order.
func shellElements(doc *html.Node) iter.Seq[*html.Node] {
	return func(yield func(*html.Node) bool) {
		for n := range doc.Descendants() {
			if n.Type == html.ElementNode && !yield(n) {
				return
			}
		}
	}
}

// openScript returns doc's script element that end of input closed, or nil.
// A browser never runs it (HTML "text" insertion mode, end-of-file token), and
// html.Parse builds the same tree whether or not the end tag came, so the
// script is found as the one whose text grows when a byte is appended to page:
// a closed element takes nothing after its end tag. A tag left unfinished at
// end of input, such as "</script ", is cut first, since the tokenizer drops
// it and an appended byte would extend it instead.
func openScript(doc *html.Node, page []byte) *html.Node {
	page = page[:len(page)-unfinishedTagLen(page)]
	baseDoc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil
	}
	grownDoc, err := html.Parse(bytes.NewReader(append(slices.Clip(page), '!')))
	if err != nil {
		return nil
	}
	base, grown := scripts(baseDoc), scripts(grownDoc)
	for i, n := range scripts(doc) {
		if i < len(base) && i < len(grown) && !bytes.Equal(childText(base[i]), childText(grown[i])) {
			return n
		}
	}
	return nil
}

// unfinishedTagLen is the length of the tag page ends inside, which the
// tokenizer reports as its end-of-input token's raw bytes, or 0.
func unfinishedTagLen(page []byte) int {
	z := html.NewTokenizer(bytes.NewReader(page))
	for {
		if z.Next() == html.ErrorToken {
			return len(z.Raw())
		}
	}
}

func scripts(doc *html.Node) []*html.Node {
	var out []*html.Node
	for n := range shellElements(doc) {
		if n.Data == "script" && htmlOrSVG(n) {
			out = append(out, n)
		}
	}
	return out
}

// isInlineScript follows HTML "prepare the script element" and SVG 2's script
// element: a script with a source never runs its text, and an HTML script
// whose type is no script type is a data block the policy never sees.
func isInlineScript(n *html.Node) bool {
	switch {
	case n.Data != "script":
		return false
	case n.Namespace == "":
		_, src := attr(n, "", "src")
		return !src && scriptType(n)
	case n.Namespace == svgNamespace:
		_, href := attr(n, "", "href")
		_, xlinkHref := attr(n, "xlink", "href")
		return !href && !xlinkHref
	}
	return false
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

// scriptType reports whether an HTML script's type and language attributes
// make it a classic, module, import map or speculation rules script, by the
// "prepare the script element" steps that read them: a type with parameters,
// such as "text/javascript; charset=utf-8", matches no essence.
func scriptType(n *html.Node) bool {
	typ, hasType := attr(n, "", "type")
	lang, hasLang := attr(n, "", "language")
	switch {
	case hasType && typ != "":
	case !hasType && hasLang && lang != "":
		typ = "text/" + lang
	default:
		return true
	}
	switch typ = lowerASCIIString(strings.Trim(typ, " \t\n\f\r")); typ {
	case "module", "importmap", "speculationrules":
		return true
	}
	return slices.Contains(javaScriptTypes, typ)
}

func isInlineStyle(n *html.Node) bool {
	return n.Data == "style" && htmlOrSVG(n)
}

// svgNamespace is html.Node's Namespace for an element inside <svg>.
const svgNamespace = "svg"

func htmlOrSVG(n *html.Node) bool { return n.Namespace == "" || n.Namespace == svgNamespace }

func attr(n *html.Node, namespace, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == namespace && a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// childText is n's child text content, the string CSP hashes
// (https://w3c.github.io/webappsec-csp/#match-element-to-source-list).
func childText(n *html.Node) []byte {
	var b bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.Bytes()
}

func cspHash(content []byte) string {
	sum := sha256.Sum256(content)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}
