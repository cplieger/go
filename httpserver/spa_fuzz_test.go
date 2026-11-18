package httpserver_test

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

var shellAttrErrRE = regexp.MustCompile(`^holds the event-handler attribute ("(?:[^"\\]|\\.)*") on <`)

// FuzzCheckShell holds CheckShell to the same answer twice, to accepting no
// shell InlineScriptHashes or InlineStyleHashes hashes, and to naming only an
// event-handler attribute whose name the input spells, NUL read as U+FFFD and
// ASCII case folded as the tokenizer does.
func FuzzCheckShell(f *testing.F) {
	for _, s := range []string{
		`<html><head><script type="module" src="/assets/app.js"></script></head><body></body></html>`,
		`<body onload="init()">x</body>`,
		`<button type=button OnClick='go()'>x</button>`,
		`<button title="onclick=go()">x</button>`,
		`<div data-on="click">x</div>`,
		`<!-- <a title=' --><body onload="init()">x</body><!-- ' -->`,
		`<title><a title='</title><body onload=go()>'</title>`,
		`<textarea><a title='</textarea><button onclick=go()>x</button>'</textarea>`,
		`<script src=/a.js>var s = "<a title='";</script><body onload=go()><script src=/b.js>'</script>`,
		`<noscript><a title='</noscript><button onclick=go()>'</noscript>`,
		`<!-- <button onclick="old()">x</button> -->`,
		`<div title="<script src=x>"></div><script>alert(1)</script>`,
		`<div title="</script><script>alert(1)</script>">x</div>`,
		`<script><!--<script>alert(1)</script>more</script>`,
		`<svg><title><img src=x onerror=alert(1)></title></svg>`,
		"<p on\x00click=x>",
		`<script src=/a.js></script `,
		`<style>a{}`,
		"<body>" + strings.Repeat("<div>", 512) + "<script>x()</script>",
		`<svg><foreignObject><template></template><img src=x onerror=alert(1)>`,
		`<svg><foreignObject><div title="<template>"></div></foreignObject></svg>`,
		`<svg></svg><p><b title="<template>">a<p>b<svg><foreignObject><template></template><img src=x onerror=alert(1)>`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		err := httpserver.CheckShell(body)
		again := httpserver.CheckShell(bytes.Clone(body))
		if (err == nil) != (again == nil) || err != nil && err.Error() != again.Error() {
			t.Fatalf("CheckShell(%q) = %v, then %v", body, err, again)
		}
		scripts, styles := httpserver.InlineScriptHashes(body), httpserver.InlineStyleHashes(body)
		if err == nil {
			if len(scripts) > 0 {
				t.Fatalf("CheckShell accepted %q, which InlineScriptHashes hashes", body)
			}
			if len(styles) > 0 {
				t.Fatalf("CheckShell accepted %q, which InlineStyleHashes hashes", body)
			}
			return
		}
		m := shellAttrErrRE.FindStringSubmatch(err.Error())
		if m == nil {
			return
		}
		name, uerr := strconv.Unquote(m[1])
		if uerr != nil {
			t.Fatalf("CheckShell(%q) = %q, an unreadable attribute name", body, err)
		}
		spelled := oracleLowerASCII(strings.ReplaceAll(string(body), "\x00", "\ufffd"))
		if !strings.HasPrefix(name, "on") || !strings.Contains(spelled, name) {
			t.Fatalf("CheckShell(%q) = %q, naming an attribute the input does not spell", body, err)
		}
	})
}
