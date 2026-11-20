package webbundle_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/webbundle"
	"golang.org/x/net/html"
)

// FuzzBuildRewritesTheElementsHTMLParseBuilds holds a built shell to the
// shell html.Parse reads: its HTML script and link elements are the
// source's, in order and with the same attributes, each logical path
// replaced by its entry's built one. A shell CheckShell passes whose only
// elements naming a .js or .css file are the fixture's own three must build.
func FuzzBuildRewritesTheElementsHTMLParseBuilds(f *testing.F) {
	for _, s := range []string{
		"", "<svg></p>", "<svg></br>", "<math></p>", "<svg><p>", "<math><mi>", "<svg><title>", "<svg><desc></svg>",
		"<!-->", "<!--->", "<!-- <script>go()</script> -->", "<noscript><p>x</p></noscript>", "<textarea><b></textarea>",
		"<table><tr><td>", "<table>", "<select><option>", "<template>", "</template>", "<frameset>", "<iframe></iframe>",
		"<math><annotation-xml encoding=\"text/html\">", "<svg><foreignObject><p>", "<![CDATA[ ]]>", "<font color=\"x\">",
		`<svg></p><link rel="stylesheet" href="/app.css">`, `<table><link rel="stylesheet" href="/theme.css"></table>`,
		`<math></p><script type="module" src="/app.js"></script>`, `<a><table><a>`, "<plaintext>", "<table><plaintext>",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		shell := strings.Replace(fixtureShell, "<body>", "<body>"+body, 1)
		src := htmlRefs(t, shell)
		c := newFixture(t, files{"index.html": shell})
		_, err := webbundle.Build(c)
		if err != nil {
			if httpserver.CheckShell([]byte(shell)) == nil && onlyFixtureRefs(t, src) && readsBack(t, shell) {
				t.Fatalf("Build refused a shell whose only references are the fixture's: %v\n%s", err, shell)
			}
			return
		}
		b := readBundle(t, c.Out)
		built := map[string]string{}
		for _, e := range b.Entries {
			ext := ".js"
			if e.Kind == httpserver.BundleKind(webbundle.Style) {
				ext = ".css"
			}
			built["/"+e.Name+ext] = "/" + e.Path
		}
		want := make([]string, len(src))
		for i, ref := range src {
			want[i] = ref.renamed(built)
		}
		got := make([]string, 0, len(want))
		for _, ref := range htmlRefs(t, string(readFile(t, c.Out, "index.html"))) {
			got = append(got, ref.String())
		}
		if !slices.Equal(got, want) {
			t.Fatalf("built shell elements\n%q\nwant\n%q\nfrom\n%s", got, want, shell)
		}
	})
}

// htmlRef is an HTML script or link element html.Parse builds.
type htmlRef html.Token

func (r htmlRef) String() string { return html.Token(r).String() }

// renamed returns r's tag with each src or href value built names replaced.
func (r htmlRef) renamed(built map[string]string) string {
	r.Attr = slices.Clone(r.Attr)
	for i, a := range r.Attr {
		if p, ok := built[a.Val]; ok && (a.Key == "src" && r.Data == "script" || a.Key == "href" && r.Data == "link") {
			r.Attr[i].Val = p
		}
	}
	return r.String()
}

func htmlRefs(t *testing.T, shell string) []htmlRef {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(shell))
	if err != nil {
		t.Fatal(err)
	}
	var refs []htmlRef
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Namespace == "" && (n.Data == "script" || n.Data == "link") {
			refs = append(refs, htmlRef{Type: html.StartTagToken, Data: n.Data, Attr: n.Attr})
		}
	}
	return refs
}

// readsBack reports whether html.Render writes shell so that it parses to
// the same script and link elements and passes CheckShell.
func readsBack(t *testing.T, shell string) bool {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(shell))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := html.Render(&out, doc); err != nil {
		return false
	}
	return httpserver.CheckShell([]byte(out.String())) == nil &&
		slices.EqualFunc(htmlRefs(t, shell), htmlRefs(t, out.String()), func(a, b htmlRef) bool { return a.String() == b.String() })
}

// onlyFixtureRefs reports whether the elements naming a .js or .css file
// are fixtureShell's three, once each.
func onlyFixtureRefs(t *testing.T, refs []htmlRef) bool {
	t.Helper()
	var named []string
	for _, r := range refs {
		if slices.ContainsFunc(r.Attr, func(a html.Attribute) bool {
			return strings.Contains(a.Val, ".js") || strings.Contains(a.Val, ".css")
		}) {
			named = append(named, r.String())
		}
	}
	var fixture []string
	for _, r := range htmlRefs(t, fixtureShell) {
		fixture = append(fixture, r.String())
	}
	slices.Sort(named)
	slices.Sort(fixture)
	return slices.Equal(named, fixture)
}
