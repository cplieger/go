package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"slices"
)

// InlineScriptHashes returns a CSP source token 'sha256-<base64>' for each
// inline <script> element in html, hashing the exact bytes between its '>'
// and its '</script>' as a browser does. A script with a src attribute is
// skipped. It reads pages the app controls and is not a sanitizer: malformed
// or script-less input yields an empty slice, which a caller whose page holds
// an inline script treats as a broken build rather than loosening its policy.
func InlineScriptHashes(html []byte) []string {
	return inlineElementHashes(html, "script", func(attrs []string) bool { return slices.Contains(attrs, "src") })
}

// InlineStyleHashes returns a CSP source token for each inline <style>
// element in html, as InlineScriptHashes does for scripts. It covers style
// elements only: a style attribute needs style-src-attr, which no element
// hash satisfies.
func InlineStyleHashes(html []byte) []string {
	return inlineElementHashes(html, "style", nil)
}

// inlineElementHashes is the one scanner behind both hash functions, because
// a second copy whose element bounds drifted from the browser's would emit a
// policy that blocks the page it was computed from. skip, when non-nil, drops
// an element by its start tag's attribute names.
func inlineElementHashes(html []byte, tag string, skip func(attrs []string) bool) []string {
	openTag := "<" + tag
	closeTag := "</" + tag
	var out []string
	for i := 0; i < len(html); {
		open := findElementOpen(html, i, openTag)
		if open < 0 {
			break
		}
		_, attrs, content := scanStartTag(html, open+1)
		if content < 0 {
			break
		}
		closeIdx := findElementClose(html, content, closeTag)
		if closeIdx < 0 {
			break
		}
		if skip == nil || !skip(attrs) {
			out = append(out, cspHash(html[content:closeIdx]))
		}
		i = closeIdx + len(closeTag)
	}
	return out
}

func cspHash(content []byte) string {
	sum := sha256.Sum256(content)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// findElementOpen returns the index at or after from of the next
// case-insensitive openTag ("<script") followed by a tag-name boundary, or -1.
func findElementOpen(html []byte, from int, openTag string) int {
	for i := from; ; {
		s := indexFoldASCII(html, i, openTag)
		if s < 0 {
			return -1
		}
		after := s + len(openTag)
		if after >= len(html) || isTagNameBoundary(html[after]) {
			return s
		}
		i = after
	}
}

// findElementClose returns the index at or after from of the end tag closing
// the element, or -1. The tokenizer leaves script or style data only on
// "</name" followed by a tag-name boundary, never at end of input, so a
// "</scriptfoo>" or a truncated end tag is content.
func findElementClose(html []byte, from int, closeTag string) int {
	for i := from; ; {
		s := indexFoldASCII(html, i, closeTag)
		if s < 0 {
			return -1
		}
		after := s + len(closeTag)
		if after < len(html) && isTagNameBoundary(html[after]) {
			return s
		}
		i = after
	}
}

// scanStartTag reads the start tag whose name begins at html[from] through
// the tokenizer states of https://html.spec.whatwg.org/multipage/parsing.html#tag-name-state,
// returning the lowercase tag name, each lowercase attribute name, and the
// index after the closing '>', or -1 when input ends first.
func scanStartTag(html []byte, from int) (tag string, attrs []string, end int) {
	i := from
	for i < len(html) && !isTagNameBoundary(html[i]) {
		i++
	}
	tag = lowerASCIIString(string(html[from:i]))
	for i < len(html) {
		switch c := html[i]; {
		case c == '>':
			return tag, attrs, i + 1
		case isASCIISpace(c) || c == '/':
			i++
		default:
			var name string
			name, i = scanAttr(html, i)
			attrs = append(attrs, name)
		}
	}
	return tag, attrs, -1
}

// scanAttr reads the attribute whose name begins at html[from] and returns
// its lowercase name and the index after its value. A leading '=' belongs to
// the name, and a quote ends nothing but a quoted value, as the tokenizer has it.
func scanAttr(html []byte, from int) (name string, end int) {
	i := from + 1
	for i < len(html) && !isTagNameBoundary(html[i]) && html[i] != '=' {
		i++
	}
	name = lowerASCIIString(string(html[from:i]))
	i = skipASCIISpace(html, i)
	if i == len(html) || html[i] != '=' {
		return name, i
	}
	i = skipASCIISpace(html, i+1)
	if i == len(html) {
		return name, i
	}
	switch q := html[i]; q {
	case '"', '\'':
		if n := bytes.IndexByte(html[i+1:], q); n >= 0 {
			return name, i + 1 + n + 1
		}
		return name, len(html)
	case '>':
		return name, i
	}
	for i < len(html) && !isASCIISpace(html[i]) && html[i] != '>' {
		i++
	}
	return name, i
}

func skipASCIISpace(b []byte, i int) int {
	for i < len(b) && isASCIISpace(b[i]) {
		i++
	}
	return i
}

// indexFoldASCII returns the index at or after from of the first
// ASCII-case-insensitive match of the lowercase literal needle in b, or -1.
// It scans b itself, so the index addresses the bytes a browser hashes.
func indexFoldASCII(b []byte, from int, needle string) int {
	for i := from; i <= len(b)-len(needle); i++ {
		if hasFoldPrefix(b[i:], needle) {
			return i
		}
	}
	return -1
}

// hasFoldPrefix reports whether b begins with the lowercase ASCII literal
// lowerNeedle, comparing ASCII letters case-insensitively.
func hasFoldPrefix(b []byte, lowerNeedle string) bool {
	if len(b) < len(lowerNeedle) {
		return false
	}
	for i := range len(lowerNeedle) {
		if lowerASCII(b[i]) != lowerNeedle[i] {
			return false
		}
	}
	return true
}

// isTagNameBoundary reports whether c ends an HTML tag name ('>', '/', or ASCII
// whitespace).
func isTagNameBoundary(c byte) bool {
	return c == '>' || c == '/' || isASCIISpace(c)
}

// isASCIISpace reports whether c is an HTML ASCII whitespace byte.
func isASCIISpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}
