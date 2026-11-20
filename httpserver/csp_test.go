package httpserver

import (
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

// hashToken computes the expected CSP source token for an inline script body,
// independently of the production cspHash (same math, separate expression).
func hashToken(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestInlineScriptHashes(t *testing.T) {
	cases := []struct {
		name string
		html string
		want []string
	}{
		{"no scripts", `<html><body>hi</body></html>`, nil},
		{"single inline", `<head><script>let a=1;</script></head>`, []string{hashToken("let a=1;")}},
		{"external skipped", `<script src="/vendor/x.js"></script>`, nil},
		{"external with type skipped", `<script type="module" src="/x.js"></script>`, nil},
		{"mixed inline and external", `<script src="/x.js"></script><script>b=2</script>`, []string{hashToken("b=2")}},
		{
			"two inline preserve order",
			`<script type="importmap">{"i":1}</script><script type="module">go()</script>`,
			[]string{hashToken(`{"i":1}`), hashToken("go()")},
		},
		{"case-insensitive tag", `<SCRIPT>x=3</SCRIPT>`, []string{hashToken("x=3")}},
		{"data-src is not a src attribute", `<script data-src="x">y=4</script>`, []string{hashToken("y=4")}},
		{"newlines hashed verbatim", "<script>\n  z=5\n</script>", []string{hashToken("\n  z=5\n")}},
		{"scriptfoo is not a script tag", `<scriptfoo>nope</scriptfoo>`, nil},
		{"gt inside quoted attribute does not end the tag", `<script data-x="a>b">q=6</script>`, []string{hashToken("q=6")}},
		{"srcset is not src", `<script srcset="x">r=7</script>`, []string{hashToken("r=7")}},
		// HTML "prepare the script element": a script with a src attribute, even
		// an empty one, never runs its inline text.
		{"src with no value is external", `<script src>x=8</script>`, nil},
		{"src right after a quoted value is external", `<script title="x"src=/x.js></script>`, nil},
		{"a quote in an unquoted value opens no quoted value", `<script data=a"b>alert(1)</script>"`, []string{hashToken("alert(1)")}},
		{"src with spaces around its = is still external", `<script src = "/x.js"></script>`, nil},
		// HTML "text" insertion mode: a script closed by end of input never runs.
		{"unclosed script yields nothing", `<script>never closed`, nil},
		{"open tag never terminated yields nothing", `<script type=module`, nil},
		{"input ending inside the tag name yields nothing", `<p><script`, nil},
		// The close side needs the same tag-name boundary the open side has:
		// "</scriptfoo>" is content, not this element's end tag, so the span runs
		// on to the real one. Ending it early would hash bytes no browser hashes
		// and ship a policy that blocks the page.
		{
			"a longer close tag name does not end the element",
			`<script>a="</scriptfoo>";b=9</script>`,
			[]string{hashToken(`a="</scriptfoo>";b=9`)},
		},
		// An end tag truncated by end-of-input never leaves script data either, so
		// there is no span to hash.
		{"close tag truncated at end of input yields nothing", `<script>x=10</script`, nil},
		{"a script tag in a quoted value is no element", `<div title="<script src=x>"></div><script>alert(1)</script>`, []string{hashToken("alert(1)")}},
		{"script tags in a quoted value hash nothing", `<div title="</script><script>alert(1)</script>">x</div>`, nil},
		// WHATWG 13.2.5.28 to 13.2.5.31: after "<!--<script>" inside script
		// data, "</script>" is text until the escape ends.
		{
			"a double-escaped end tag does not end the script",
			`<script><!--<script>alert(1)</script>more</script>`,
			[]string{hashToken(`<!--<script>alert(1)</script>more`)},
		},
		// The tokenizer's preprocessing turns a NUL in script data into U+FFFD.
		{"a NUL is hashed as U+FFFD", "<script>a\x00b</script>", []string{hashToken("a\ufffdb")}},
		{"an svg script", `<svg><script>s=1</script></svg>`, []string{hashToken("s=1")}},
		{"an svg script with href is external", `<svg><script href="/a.js"></script></svg>`, nil},
		{"an svg script with xlink:href is external", `<svg><script xlink:href="/a.js"></script></svg>`, nil},
		{"a json data block is skipped", `<script type="application/json">{"a":1}</script>`, nil},
		{"a template data block is skipped", `<script type="text/template"><p>x</p></script>`, nil},
		{"a type with parameters is a data block", `<script type="text/javascript; charset=utf-8">p=1</script>`, nil},
		{"a javascript type runs", `<script type=" Text/JavaScript ">t=1</script>`, []string{hashToken("t=1")}},
		{"an empty type runs", `<script type="">e=1</script>`, []string{hashToken("e=1")}},
		{"a module type runs", `<script type=MODULE>m=1</script>`, []string{hashToken("m=1")}},
		{"a speculation rules script runs", `<script type="speculationrules">{}</script>`, []string{hashToken("{}")}},
		{"a language attribute names the type", `<script language="javascript">l=1</script>`, []string{hashToken("l=1")}},
		{"an unknown language is a data block", `<script language="vbscript">l=2</script>`, nil},
		{"a language is trimmed after text/ is prepended", `<script language="javascript ">l=3</script>`, []string{hashToken("l=3")}},
		{"a type attribute outranks language", `<script type="text/template" language="javascript">l=4</script>`, nil},
		{"an end tag cut at end of input leaves the script open", `<script>x=11</script `, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InlineScriptHashes([]byte(tc.html))
			if !slices.Equal(got, tc.want) {
				t.Errorf("InlineScriptHashes(%q) = %v, want %v", tc.html, got, tc.want)
			}
		})
	}
}

func TestInlineStyleHashes(t *testing.T) {
	cases := []struct {
		name string
		html string
		want []string
	}{
		{"no styles", `<html><body>hi</body></html>`, nil},
		{"single inline", `<head><style>body{margin:0}</style></head>`, []string{hashToken("body{margin:0}")}},
		{
			"two inline preserve order",
			`<style>a{color:red}</style><style>b{color:blue}</style>`,
			[]string{hashToken("a{color:red}"), hashToken("b{color:blue}")},
		},
		{"case-insensitive tag", `<STYLE>c{top:0}</STYLE>`, []string{hashToken("c{top:0}")}},
		{"newlines hashed verbatim", "<style>\n  d{left:0}\n</style>", []string{hashToken("\n  d{left:0}\n")}},
		{"styles is not a style tag", `<styles>nope</styles>`, nil},
		{"a quote in an unquoted value opens no quoted value", `<style data=a'b>k{z:0}</style>'`, []string{hashToken("k{z:0}")}},
		// HTML "text" insertion mode pops a style at end of input, and popping
		// it updates the style block, so a browser applies it.
		{"unclosed style is hashed", `<style>never closed`, []string{hashToken("never closed")}},
		// A quote that never closes swallows the rest of the input into the
		// opening tag, so the later "</style>" is attribute bytes, not an end tag.
		{"unterminated attribute quote yields nothing", `<style data-x="a>b{c:0}</style>`, nil},
		// "</styles>" is content, not this element's end tag.
		{
			"a longer close tag name does not end the element",
			`<style>a{content:"</styles>"}</style>`,
			[]string{hashToken(`a{content:"</styles>"}`)},
		},
		{"close tag truncated at end of input is style text", `<style>j{z:0}</style`, []string{hashToken("j{z:0}</style")}},
		{"an svg style", `<svg><style>c{x:0}</style></svg>`, []string{hashToken("c{x:0}")}},
		{
			"gt inside quoted attribute does not end the tag",
			`<style media="all and (min-width:1px)" data-x="a>b">e{right:0}</style>`,
			[]string{hashToken("e{right:0}")},
		},
		// A style has no external-reference form to skip: a stylesheet reference
		// is <link rel="stylesheet">, which style-src covers through 'self'.
		{"media attribute still hashed", `<style media="print">f{bottom:0}</style>`, []string{hashToken("f{bottom:0}")}},
		{"src attribute is meaningless on style and does not skip it", `<style src="x">g{gap:0}</style>`, []string{hashToken("g{gap:0}")}},
		// A <link rel=stylesheet> is not a <style> element and must not be hashed.
		{"link stylesheet ignored", `<link rel="stylesheet" href="/style.css">`, nil},
		{"style attribute is not a style element", `<div style="color:red">x</div>`, nil},
		{"attributes excluded from the hash", `<style type="text/css" media="all">h{x:0}</style>`, []string{hashToken("h{x:0}")}},
		{"spaced close tag", `<style>i{y:0}</style >`, []string{hashToken("i{y:0}")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InlineStyleHashes([]byte(tc.html))
			if !slices.Equal(got, tc.want) {
				t.Errorf("InlineStyleHashes(%q) = %v, want %v", tc.html, got, tc.want)
			}
		})
	}
}

// TestInlineHashesSurviveLengthChangingFold hashes content holding U+0130,
// whose Unicode fold is the 1-byte 'i', byte for byte: a reader that folded
// the page before slicing it would hash other bytes than a browser does.
func TestInlineHashesSurviveLengthChangingFold(t *testing.T) {
	const content = "\n  body { content: '\u0130' }\n"
	want := hashToken(content)

	if got := InlineStyleHashes([]byte("<html><style>" + content + "</style></html>")); !slices.Equal(got, []string{want}) {
		t.Errorf("InlineStyleHashes = %v, want %v ", got, []string{want})
	}
	if got := InlineScriptHashes([]byte("<html><script>" + content + "</script></html>")); !slices.Equal(got, []string{want}) {
		t.Errorf("InlineScriptHashes = %v, want %v ", got, []string{want})
	}
}

// A browser builds the script after the template, which html.Parse drops (see
// TestCheckShellRefusesTemplateInForeignContent), so no hash list is complete.
func TestInlineHashesOfAPageHTMLParseStopsIn(t *testing.T) {
	page := []byte(`<script>a()</script><style>a{}</style><svg><foreignObject><template></template><script>b()</script>`)
	if got := InlineScriptHashes(page); got != nil {
		t.Errorf("InlineScriptHashes = %v, want nil", got)
	}
	if got := InlineStyleHashes(page); got != nil {
		t.Errorf("InlineStyleHashes = %v, want nil", got)
	}
	spelled := []byte("<p>" + templateMark + "</p>" + string(page))
	if got := InlineScriptHashes(spelled); got != nil {
		t.Errorf("InlineScriptHashes(a page spelling templateMark) = %v, want nil", got)
	}
	cloned := []byte(`<script>a()</script><svg></svg><p><b title="<template>">a<p>b<svg><foreignObject><template></template><script>b()</script>`)
	if got := InlineScriptHashes(cloned); got != nil {
		t.Errorf("InlineScriptHashes(a template behind a cloned formatting element) = %v, want nil", got)
	}
}

// Scripts must not pick up style content and styles script content, on a page
// carrying both.
func TestInlineHashesDoNotCrossContaminate(t *testing.T) {
	html := []byte(`<head><style>body{margin:0}</style><script type="module">boot()</script></head>`)
	styles := InlineStyleHashes(html)
	scripts := InlineScriptHashes(html)
	if !slices.Equal(styles, []string{hashToken("body{margin:0}")}) {
		t.Errorf("InlineStyleHashes = %v, want the style body only", styles)
	}
	if !slices.Equal(scripts, []string{hashToken("boot()")}) {
		t.Errorf("InlineScriptHashes = %v, want the script body only", scripts)
	}
}

// TestCSPHashTokenFormat pins that cspHash emits a CSP-grammar source token: a
// standard-base64 encoding of a 32-byte sha256 digest wrapped as 'sha256-...'.
// It validates the encoding/format without hardcoding any expected hash value.
func TestCSPHashTokenFormat(t *testing.T) {
	tok := cspHash([]byte("console.log(1)"))
	if !strings.HasPrefix(tok, "'sha256-") || !strings.HasSuffix(tok, "'") {
		t.Fatalf("token = %q, want the 'sha256-<base64>' form", tok)
	}
	b64 := strings.TrimSuffix(strings.TrimPrefix(tok, "'sha256-"), "'")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("hash %q is not valid standard base64: %v", b64, err)
	}
	if len(raw) != sha256.Size {
		t.Errorf("decoded hash = %d bytes, want %d (sha256)", len(raw), sha256.Size)
	}
}
