package webbundle_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/webbundle"
	"golang.org/x/net/html"
)

func TestBuildRefusesShell(t *testing.T) {
	t.Parallel()
	edit := func(old, repl string) string { return strings.Replace(fixtureShell, old, repl, 1) }
	cases := []struct {
		name    string
		shell   string
		entries []webbundle.Entry
		want    []string
	}{
		{
			name:  "LogicalPathTwice",
			shell: edit("</body>", `<script type="module" src="/app.js"></script></body>`),
			want:  []string{"index.html", "/app.js"},
		},
		{
			name:    "EntryNoShellNames",
			entries: append(fixtureEntries(), webbundle.Entry{Name: "extra", Path: "src/prepaint.js", Kind: webbundle.Module}),
			want:    []string{"extra"},
		},
		{
			name:  "UndeclaredModule",
			shell: edit("</body>", `<script type="module" src="/nope.js"></script></body>`),
			want:  []string{"index.html", "/nope.js"},
		},
		{
			name:  "InlineScript",
			shell: edit("</body>", "<script>document.title = 1;</script></body>"),
			want:  []string{"index.html", "inline <script>"},
		},
		{
			name:  "InlineStyle",
			shell: edit("</head>", "<style>body { color: red; }</style></head>"),
			want:  []string{"index.html", "inline <style>"},
		},
		{
			name:  "StyleAttribute",
			shell: edit("<body>", `<body style="color: red">`),
			want:  []string{"index.html", "style attribute"},
		},
		{
			name:  "UndeclaredStylesheet",
			shell: edit("</head>", `<link rel="stylesheet" href="/theme.css"></head>`),
			want:  []string{"index.html", "/theme.css"},
		},
		{
			name:  "EventHandlerAttribute",
			shell: edit("<body>", `<body onload="start()">`),
			want:  []string{"index.html", "onload"},
		},
		{
			name:  "EventHandlerAttributeUpperCase",
			shell: edit("<body>", `<body><button type="button" ONCLICK="go()">Go</button>`),
			want:  []string{"index.html", "onclick"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			extra := files{}
			if tc.shell != "" {
				extra["index.html"] = tc.shell
			}
			c := newFixture(t, extra)
			if tc.entries != nil {
				c.Entries = tc.entries
			}
			buildRefused(t, c, tc.want...)
		})
	}
}

// TestBuildReadsShellAsHTML pins that a shell is read as the HTML tokenizer
// reads it: a comment (an abrupt "<!-->" or "<!--->" included) or a raw-text
// element body holds text, never tags.
func TestBuildReadsShellAsHTML(t *testing.T) {
	t.Parallel()
	edit := func(old, repl string) string { return strings.Replace(fixtureShell, old, repl, 1) }
	moduleTag := `<script type="module" src="/app.js"></script>`
	t.Run("HandlerAfterTitleQuotingOne", func(t *testing.T) {
		t.Parallel()
		shell := strings.Replace(edit("<title>Fixture</title>", `<title><x onclick="a()"></title>`), "<body>", `<body onload="start()">`, 1)
		_, err := webbundle.Build(newFixture(t, files{"index.html": shell}))
		if err == nil || !strings.Contains(err.Error(), "onload") || strings.Contains(err.Error(), "onclick") {
			t.Fatalf("Build = %v, want a refusal naming onload and not the title's text", err)
		}
	})
	abrupt := []struct{ name, comment string }{{"AbruptComment", "<!-->"}, {"AbruptDashComment", "<!--->"}}
	for _, tc := range abrupt {
		t.Run("HandlerAfter"+tc.name, func(t *testing.T) {
			t.Parallel()
			shell := edit("<body>", "<body>"+tc.comment+`<p onclick="go()">Go</p><!-- end -->`)
			buildRefused(t, newFixture(t, files{"index.html": shell}), "index.html", "onclick")
		})
	}
	for _, tc := range []struct{ name, body string }{
		{"HandlerInSVGTitle", `<svg><title><b onclick="go()">Go</b></title></svg>`},
		{"HandlerAfterSVGBreakout", `<svg><circle r="1"></circle><p>out</p><b onclick="go()">Go</b>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			buildRefused(t, newFixture(t, files{"index.html": edit("<body>", "<body>"+tc.body)}), "index.html", "onclick")
		})
	}
	for _, tc := range []struct{ name, body string }{
		{"HandlerInComment", `<!-- <p onclick="go()"> -->`},
		{"HandlerInTextarea", `<textarea><p onclick="go()"></textarea>`},
		{"HandlerInNoscript", `<noscript><p onclick="go()">Turn on JavaScript.</p></noscript>`},
		{"HandlerInSVGCDATA", `<svg><![CDATA[ a > <b onclick="go()"> ]]></svg>`},
		{"HandlerInTextareaInForeignObject", `<svg><foreignObject><textarea><b onclick="go()"></textarea></foreignObject></svg>`},
		{"HandlerInTextareaAfterSVGBreakout", `<svg><circle r="1"></circle><p>out</p><textarea><b onclick="go()"></textarea>`},
		{"HandlerInTextareaInMathText", `<math><mi><textarea><b onclick="go()"></textarea></mi></math>`},
		{"HandlerInTextareaInHTMLAnnotation", `<math><annotation-xml encoding="Text/HTML"><textarea><b onclick="go()"></textarea></annotation-xml></math>`},
		{"HandlerInTextareaInSVGDescInAnnotation", `<math><annotation-xml><svg><desc><textarea><b onclick="go()"></textarea></desc></svg></annotation-xml></math>`},
		{"HandlerInTemplateScript", `<script type="text/template"><p onclick="go()"></script>`},
		{"ScriptInComment", `<!-- <script>go()</script> -->`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mustBuild(t, newFixture(t, files{"index.html": edit("<body>", "<body>"+tc.body)}))
		})
	}
	for _, tc := range abrupt {
		t.Run("LogicalPathAfter"+tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{"index.html": edit(moduleTag, tc.comment+moduleTag)})
			mustBuild(t, c)
			shell := string(readFile(t, c.Out, "index.html"))
			if want := tc.comment + `<script type="module" src="/` + entryPath(t, readBundle(t, c.Out), "app", webbundle.Module) + `">`; !strings.Contains(shell, want) {
				t.Errorf("index.html does not hold %s:\n%s", want, shell)
			}
		})
	}
	t.Run("LogicalPathInCommentUntouched", func(t *testing.T) {
		t.Parallel()
		comment := "<!-- " + moduleTag + " -->"
		c := newFixture(t, files{"index.html": edit(moduleTag, comment+"\n"+moduleTag)})
		mustBuild(t, c)
		shell := string(readFile(t, c.Out, "index.html"))
		if !strings.Contains(shell, comment+"\n") || !strings.Contains(shell, `src="/`+entryPath(t, readBundle(t, c.Out), "app", webbundle.Module)+`"`) {
			t.Errorf("index.html rewrote the comment or missed the real tag:\n%s", shell)
		}
	})
	t.Run("HeadEndInCommentUntouched", func(t *testing.T) {
		t.Parallel()
		comment := "<!-- the icon links go before </head> -->"
		c := newFixture(t, files{"index.html": edit("<head>", "<head>\n"+comment), "icon.svg": seedIcon(t, "subflux.svg")})
		c.Icon = "icon.svg"
		mustBuild(t, c)
		shell := string(readFile(t, c.Out, "index.html"))
		head, _, _ := strings.Cut(strings.Replace(shell, comment, "", 1), "</head>")
		if !strings.Contains(shell, comment) || !strings.Contains(head, `rel="stylesheet"`) || !strings.Contains(head, "apple-touch-icon") ||
			strings.Index(head, "apple-touch-icon") < strings.Index(head, `rel="stylesheet"`) {
			t.Errorf("the icon links are not at the end of the real <head>, or the comment changed:\n%s", shell)
		}
	})
}

// TestBuildRewritesTheTagsHTMLParseFinds pins that a logical path is
// rewritten where html.Parse builds an HTML element naming it, and nowhere
// else: want is that count, from the HTML parsing rules, and the test also
// asks html.Parse.
func TestBuildRewritesTheTagsHTMLParseFinds(t *testing.T) {
	t.Parallel()
	const tag = `<link rel="stylesheet" href="/theme.css">`
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"Body", tag, 1},
		{"Comment", "<!-- " + tag + " -->", 0},
		{"Noscript", "<noscript>" + tag + "</noscript>", 0},
		{"Textarea", "<textarea>" + tag + "</textarea>", 0},
		{"Title", "<title>" + tag + "</title>", 0},
		{"TemplateScript", `<script type="text/template">` + tag + "</script>", 0},
		{"AttributeValue", `<p title='` + tag + `'>x</p>`, 0},
		{"SVGTitle", "<svg><title>" + tag + "</title></svg>", 1},
		{"SVGDesc", "<svg><desc>" + tag + "</desc></svg>", 1},
		{"SVG", "<svg>" + tag + "</svg>", 0},
		{"SVGTextarea", "<svg><textarea>" + tag + "</textarea></svg>", 0},
		{"SVGTextareaHoldingMarkup", "<svg><textarea><p>" + tag + "</p></textarea></svg>", 1},
		{"SVGTitleTextarea", "<svg><title><textarea>" + tag + "</textarea></title></svg>", 0},
		{"SVGBreakout", "<svg><p>" + tag + "</p></svg>", 1},
		{"MathStyle", "<math><style>" + tag + "</style></math>", 0},
		{"MathStyleHoldingMarkup", "<math><style><p>" + tag + "</p></style></math>", 1},
		{"MathText", "<math><mi>" + tag + "</mi></math>", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shell := strings.Replace(fixtureShell, "<body>", "<body>"+tc.body, 1)
			if got := parsedLinks(t, shell, "/theme.css"); got != tc.want {
				t.Fatalf("html.Parse finds %d elements naming /theme.css, the case says %d", got, tc.want)
			}
			rewritten := tc.want
			if tc.want == 0 {
				shell = strings.Replace(shell, "</head>", tag+"</head>", 1)
				rewritten = 1
			}
			c := newFixture(t, files{"index.html": shell, "src/theme.css": ".fixture-theme { color: rgb(1 2 3); }\n"})
			c.Entries = append(c.Entries, webbundle.Entry{Name: "theme", Path: "src/theme.css", Kind: webbundle.Style})
			mustBuild(t, c)
			out := string(readFile(t, c.Out, "index.html"))
			hashed := `href="/` + entryPath(t, readBundle(t, c.Out), "theme", webbundle.Style) + `"`
			if got := strings.Count(out, hashed); got != rewritten {
				t.Errorf("the shell names the built stylesheet %d times, want %d:\n%s", got, rewritten, out)
			}
			if got, want := strings.Count(out, "/theme.css"), strings.Count(shell, "/theme.css")-rewritten; got != want {
				t.Errorf("the shell keeps %d mentions of /theme.css, want %d left as text:\n%s", got, want, out)
			}
		})
	}
}

// parsedLinks counts the HTML link elements html.Parse builds from shell
// whose href is href.
func parsedLinks(t *testing.T, shell, href string) int {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(shell))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for node := range doc.Descendants() {
		if node.Type == html.ElementNode && node.Namespace == "" && node.Data == "link" &&
			slices.ContainsFunc(node.Attr, func(a html.Attribute) bool { return a.Key == "href" && a.Val == href }) {
			n++
		}
	}
	return n
}

// TestBuildChecksShellTagKind pins that a shell loads each entry as its kind
// runs: a module by <script type=module>, a classic by a <script> with no
// type or a JavaScript one, a style by <link rel=stylesheet> that is not an
// alternate, keywords matched ASCII case-insensitively.
func TestBuildChecksShellTagKind(t *testing.T) {
	t.Parallel()
	edit := func(old, repl string) string { return strings.Replace(fixtureShell, old, repl, 1) }
	for _, tc := range []struct{ name, shell, path, kind string }{
		{"ModuleByClassicScript", edit(`<script type="module" src="/app.js">`, `<script src="/app.js">`), "/app.js", "module"},
		{"ClassicByModuleScript", edit(`<script src="/prepaint.js">`, `<script type="module" src="/prepaint.js">`), "/prepaint.js", "classic"},
		{"StyleByPreload", edit(`<link rel="stylesheet" href="/app.css">`, `<link rel="preload" as="style" href="/app.css">`), "/app.css", "style"},
		{"StyleByScript", edit(`<link rel="stylesheet" href="/app.css">`, `<script src="/app.css"></script>`), "/app.css", "style"},
		{"ModuleByLink", edit(`<script type="module" src="/app.js"></script>`, `<link rel="modulepreload" href="/app.js">`), "/app.js", "module"},
		{"StyleByAlternateStylesheet", edit(`rel="stylesheet"`, `rel="alternate stylesheet" title="Alt"`), "/app.css", "style"},
		{"ClassicByTemplateType", edit(`<script src="/prepaint.js">`, `<script type="text/template" src="/prepaint.js">`), "/prepaint.js", "classic"},
		{"ClassicByOtherLanguage", edit(`<script src="/prepaint.js">`, `<script language="vbscript" src="/prepaint.js">`), "/prepaint.js", "classic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			buildRefused(t, newFixture(t, files{"index.html": tc.shell}), "index.html", tc.path, tc.kind)
		})
	}
	t.Run("KindWordsInAnyCase", func(t *testing.T) {
		t.Parallel()
		shell := edit(`<script type="module" src="/app.js">`, `<script type=" MODULE " src="/app.js">`)
		shell = strings.Replace(shell, `rel="stylesheet"`, `rel="Stylesheet"`, 1)
		shell = strings.Replace(shell, `<script src="/prepaint.js">`, `<script type="text/JavaScript" src="/prepaint.js">`, 1)
		mustBuild(t, newFixture(t, files{"index.html": shell}))
	})
}

func TestBuildRefusesClassicEntryNothingReads(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{"src/worker.js": "self.addEventListener(\"connect\", () => {});\n"})
	c.Entries = append(c.Entries, webbundle.Entry{Name: "worker", Path: "src/worker.js", Kind: webbundle.Classic})
	buildRefused(t, c, "worker", "BUNDLE_URLS")
}

func TestBuildRefusesClassicEntryUnreadBesideAReadOne(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{
		"src/app.ts":    "declare const BUNDLE_URLS: { worker: string };\nnew SharedWorker(BUNDLE_URLS.worker);\n",
		"src/worker.js": "self.addEventListener(\"connect\", () => {});\n",
		"src/unread.js": "self.addEventListener(\"message\", () => {});\n",
	})
	c.Entries = append(c.Entries,
		webbundle.Entry{Name: "worker", Path: "src/worker.js", Kind: webbundle.Classic},
		webbundle.Entry{Name: "unread", Path: "src/unread.js", Kind: webbundle.Classic},
	)
	_, err := webbundle.Build(c)
	if err == nil || !strings.Contains(err.Error(), `"unread"`) || strings.Contains(err.Error(), `"worker"`) {
		t.Fatalf("Build = %v, want a refusal naming the unread classic entry only", err)
	}
}

func TestBuildRefusesScriptImportingCSS(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{"src/app.ts": "import \"./app.css\";\nconsole.log(1);\n"})
	buildRefused(t, c, "CSS", "Style entry")
}

func TestBuildPassesAttributesNamedLikeHandlers(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{"index.html": strings.Replace(fixtureShell, "<body>",
		`<body data-onload="x"><p title="onclick=go()">Plain text naming onclick="go()" is no attribute.</p>`, 1)})
	mustBuild(t, c)
}

func TestBuildPageShells(t *testing.T) {
	t.Parallel()
	const login = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Sign in</title>
<link rel="stylesheet" href="/app.css">
</head>
<body>
<script type="module" src="/login.js"></script>
</body>
</html>
`
	t.Run("SecondShell", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"login.html": login, "src/login.ts": "console.log(\"sign in\");\n"})
		c.Shells = []string{"index.html", "login.html"}
		c.Entries = append(c.Entries, webbundle.Entry{Name: "login", Path: "src/login.ts", Kind: webbundle.Module})
		c.Pages = append(c.Pages, httpserver.Page{Name: "login", Pattern: "/login", Shell: "login.html"})
		writePageFiles(t, c)
		mustBuild(t, c)
		b := readBundle(t, c.Out)
		shell := string(readFile(t, c.Out, "login.html"))
		if want := `src="/` + entryPath(t, b, "login", webbundle.Module) + `"`; !strings.Contains(shell, want) {
			t.Errorf("login.html does not hold %s:\n%s", want, shell)
		}
		if _, ok := b.Files["login.html"]; !ok {
			t.Errorf("%s does not list login.html", httpserver.BundlePath)
		}
	})
	t.Run("PageShellNotDeclared", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"login.html": login, "src/login.ts": "console.log(\"sign in\");\n"})
		c.Entries = append(c.Entries, webbundle.Entry{Name: "login", Path: "src/login.ts", Kind: webbundle.Module})
		c.Pages = append(c.Pages, httpserver.Page{Name: "login", Pattern: "/login", Shell: "login.html"})
		writePageFiles(t, c)
		buildRefused(t, c, "login.html")
	})
}
