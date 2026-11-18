package webbundle_test

import (
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/webbundle"
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
			want:  []string{"index.html", "inline script"},
		},
		{
			name:  "InlineStyle",
			shell: edit("</head>", "<style>body { color: red; }</style></head>"),
			want:  []string{"index.html", "inline style"},
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
