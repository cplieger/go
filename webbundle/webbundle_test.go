package webbundle_test

import (
	"bytes"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
	"github.com/cplieger/go/webbundle"
)

func TestBuildWritesHashedTreeAndManifest(t *testing.T) {
	t.Parallel()
	c := newFixture(t, nil)
	r := mustBuild(t, c)
	tree := readTree(t, c.Out)
	b := readBundle(t, c.Out)

	app := entryPath(t, b, "app", webbundle.Module)
	css := entryPath(t, b, "app", webbundle.Style)
	prepaint := entryPath(t, b, "prepaint", webbundle.Classic)
	for path, re := range map[string]*regexp.Regexp{app: hashedName("app.js"), css: hashedName("app.css"), prepaint: hashedName("prepaint.js")} {
		if !re.MatchString(path) {
			t.Errorf("entry path %q does not match %s", path, re)
		}
	}

	if want := slices.Sorted(maps.Keys(tree)); !slices.Equal(r.Files, want) {
		t.Errorf("Result.Files = %q, want every file under Out %q", r.Files, want)
	}
	listedWant := slices.DeleteFunc(slices.Sorted(maps.Keys(tree)), func(p string) bool { return p == httpserver.BundlePath })
	if got := slices.Sorted(maps.Keys(b.Files)); !slices.Equal(got, listedWant) {
		t.Errorf("%s lists %q, want every file but itself %q", httpserver.BundlePath, got, listedWant)
	}
	for name, hash := range b.Files {
		if got := sha256Hex(tree[name]); got != hash {
			t.Errorf("%s gives %s the hash %s, its bytes hash to %s", httpserver.BundlePath, name, hash, got)
		}
	}

	shell := string(tree["index.html"])
	for _, want := range []string{`src="/` + prepaint + `"`, `href="/` + css + `"`, `src="/` + app + `"`} {
		if !strings.Contains(shell, want) {
			t.Errorf("index.html does not hold %s:\n%s", want, shell)
		}
	}
	for _, logical := range []string{`"/app.js"`, `"/app.css"`, `"/prepaint.js"`} {
		if strings.Contains(shell, logical) {
			t.Errorf("index.html still names the logical path %s", logical)
		}
	}

	types := httpserver.ContentTypes()
	for name, body := range tree {
		if strings.HasPrefix(name, "assets/") && types[path.Ext(name)] == "" {
			t.Errorf("%s has an extension outside httpserver's content-type table", name)
		}
		if strings.HasSuffix(name, ".map") || bytes.Contains(body, []byte("sourceMappingURL")) {
			t.Errorf("%s is or names a sourcemap", name)
		}
	}
}

func TestBuildTwiceWritesEqualBytes(t *testing.T) {
	t.Parallel()
	c := newFixture(t, nil)
	mustBuild(t, c)
	first := readTree(t, c.Out)

	mustBuild(t, c)
	if again := readTree(t, c.Out); !maps.EqualFunc(first, again, bytes.Equal) {
		t.Errorf("a rebuild into the same Out wrote other bytes: %q then %q", slices.Sorted(maps.Keys(first)), slices.Sorted(maps.Keys(again)))
	}

	other := c
	other.Out = filepath.Join(t.TempDir(), "static")
	mustBuild(t, other)
	if elsewhere := readTree(t, other.Out); !maps.EqualFunc(first, elsewhere, bytes.Equal) {
		t.Errorf("a build into another Out wrote other bytes: %q then %q", slices.Sorted(maps.Keys(first)), slices.Sorted(maps.Keys(elsewhere)))
	}
}

func TestBuiltTreePassesCheckSPA(t *testing.T) {
	t.Parallel()
	c := newFixture(t, nil)
	mustBuild(t, c)
	cfg := httpserver.SPAConfig{Assets: os.DirFS(c.Out), Pages: c.Pages, APIPrefix: c.APIPrefix}
	h, err := httpserver.SPA(cfg)
	if err != nil {
		t.Fatalf("httpserver.SPA refuses the built tree: %v", err)
	}
	httpservertest.CheckSPA(t, h, cfg)
}

func TestBuildModuleChunksAndClassicURLs(t *testing.T) {
	t.Parallel()
	shell := strings.Replace(fixtureShell, `<script type="module" src="/app.js"></script>`,
		`<script type="module" src="/app.js"></script>
<script type="module" src="/other.js"></script>`, 1)
	c := newFixture(t, files{
		"index.html": shell,
		"src/app.ts": `import { shared } from "./shared";
declare const BUNDLE_URLS: { worker: string };
shared(new SharedWorker(BUNDLE_URLS.worker));
`,
		"src/other.ts":      "import { shared } from \"./shared\";\nshared(document.title);\n",
		"src/shared.ts":     "export function shared(v: unknown) { console.log(\"shared module body\", v); }\n",
		"src/worker.js":     "import { count } from \"./worker-lib.js\";\nself.addEventListener(\"connect\", count);\n",
		"src/worker-lib.js": "let n = 0;\nexport function count() { n++; }\n",
	})
	c.Entries = append(c.Entries,
		webbundle.Entry{Name: "other", Path: "src/other.ts", Kind: webbundle.Module},
		webbundle.Entry{Name: "worker", Path: "src/worker.js", Kind: webbundle.Classic},
	)
	mustBuild(t, c)
	b := readBundle(t, c.Out)

	chunks := listed(b, regexp.MustCompile(`^assets/chunks/[^/]+\.js$`))
	if len(chunks) == 0 {
		t.Fatalf("no shared chunk under assets/chunks/; listed %q", slices.Sorted(maps.Keys(b.Files)))
	}
	var holder string
	for _, ch := range chunks {
		if bytes.Contains(readFile(t, c.Out, ch), []byte("shared module body")) {
			holder = ch
		}
	}
	if holder == "" {
		t.Errorf("no chunk under assets/chunks/ holds the module both entries import")
	}

	worker := entryPath(t, b, "worker", webbundle.Classic)
	workerBody := readFile(t, c.Out, worker)
	if regexp.MustCompile(`\bimport\b|\bexport\b`).Match(workerBody) {
		t.Errorf("the classic entry %s imports or exports:\n%s", worker, workerBody)
	}
	if !bytes.Contains(workerBody, []byte("connect")) {
		t.Errorf("the classic entry %s does not hold its own closure:\n%s", worker, workerBody)
	}
	app := loadedJS(t, c.Out, entryPath(t, b, "app", webbundle.Module))
	if !bytes.Contains(app, []byte(strconv.Quote("/"+worker))) {
		t.Errorf("the module entry and the chunks it imports do not read BUNDLE_URLS.worker as %q:\n%s", "/"+worker, app)
	}
}

// loadedJS is a module entry's bytes followed by those of every chunk it
// imports, directly or through another chunk.
func loadedJS(t testing.TB, out, entry string) []byte {
	t.Helper()
	importRe := regexp.MustCompile(`"(\./[^"]+\.js)"`)
	var all []byte
	seen := map[string]bool{entry: true}
	for queue := []string{entry}; len(queue) > 0; queue = queue[1:] {
		body := readFile(t, out, queue[0])
		all = append(all, body...)
		for _, m := range importRe.FindAllSubmatch(body, -1) {
			if p := path.Join(path.Dir(queue[0]), string(m[1])); !seen[p] {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	return all
}

func TestBuildLowersSyntaxThePinnedEnginesLack(t *testing.T) {
	t.Parallel()
	// esbuild's compat table lists no Safari version with explicit resource
	// management, so a pinned Safari floor must lower or refuse `using`.
	c := newFixture(t, files{"src/app.ts": `class Held { [Symbol.dispose]() { console.log("released"); } }
export function hold() { using held = new Held(); return held; }
hold();
`})
	if _, err := webbundle.Build(c); err != nil {
		for _, w := range []string{"using", "safari"} {
			if !strings.Contains(strings.ToLower(err.Error()), w) {
				t.Errorf("Build error %q does not name %q", err, w)
			}
		}
		return
	}
	app := readFile(t, c.Out, entryPath(t, readBundle(t, c.Out), "app", webbundle.Module))
	if regexp.MustCompile(`\busing\s+[A-Za-z_$][\w$]*\s*=`).Match(app) {
		t.Errorf("the module entry ships a using declaration unchanged:\n%s", app)
	}
}

func TestBuildDefines(t *testing.T) {
	t.Parallel()
	t.Run("JSONValueApplied", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"src/app.ts": "declare const FLAVOUR: string;\nconsole.log(FLAVOUR);\n"})
		c.Define = map[string]string{"FLAVOUR": `"vanilla-fixture"`}
		mustBuild(t, c)
		app := readFile(t, c.Out, entryPath(t, readBundle(t, c.Out), "app", webbundle.Module))
		if !bytes.Contains(app, []byte("vanilla-fixture")) {
			t.Errorf("the define's value is not in the module entry:\n%s", app)
		}
	})
	t.Run("NotJSON", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, nil)
		c.Define = map[string]string{"FLAVOUR": "vanilla"}
		buildRefused(t, c, "FLAVOUR")
	})
	t.Run("BundleURLsReserved", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, nil)
		c.Define = map[string]string{"BUNDLE_URLS": "{}"}
		buildRefused(t, c, "BUNDLE_URLS")
	})
}

func TestBuildRefusesConfig(t *testing.T) {
	t.Parallel()
	many := make([]webbundle.Entry, 33)
	for i := range many {
		many[i] = webbundle.Entry{Name: "e" + strconv.Itoa(i), Path: "src/app.ts", Kind: webbundle.Module}
	}
	cases := []struct {
		edit func(c *webbundle.Config)
		name string
		want []string
	}{
		{name: "NoSrc", edit: func(c *webbundle.Config) { c.Src = "" }, want: []string{"Src"}},
		{name: "NoOut", edit: func(c *webbundle.Config) { c.Out = "" }, want: []string{"Out"}},
		{name: "OutIsSrc", edit: func(c *webbundle.Config) { c.Out = c.Src }, want: []string{"Out"}},
		{name: "OutHoldsSrc", edit: func(c *webbundle.Config) { c.Out = filepath.Dir(c.Src) }, want: []string{"Out"}},
		{name: "OutInsideSrc", edit: func(c *webbundle.Config) { c.Out = filepath.Join(c.Src, "static") }, want: []string{"Out"}},
		{name: "OutIsFile", edit: func(c *webbundle.Config) { c.Out = filepath.Join(c.Src, "index.html") }, want: []string{"Out"}},
		{name: "TooManyEntries", edit: func(c *webbundle.Config) { c.Entries = many }, want: []string{"32"}},
		{name: "EntryOutsideSrc", edit: func(c *webbundle.Config) { c.Entries[0].Path = "../app.ts" }, want: []string{"../app.ts"}},
		{name: "EntryMissing", edit: func(c *webbundle.Config) { c.Entries[0].Path = "src/gone.ts" }, want: []string{"src/gone.ts"}},
		{name: "UnknownKind", edit: func(c *webbundle.Config) { c.Entries[0].Kind = "wasm" }, want: []string{"wasm"}},
		{name: "EmptyName", edit: func(c *webbundle.Config) { c.Entries[0].Name = "" }, want: []string{"src/app.ts"}},
		{name: "NameWithSlash", edit: func(c *webbundle.Config) { c.Entries[0].Name = "a/b" }, want: []string{"a/b"}},
		{name: "NameDotFile", edit: func(c *webbundle.Config) { c.Entries[0].Name = ".app" }, want: []string{".app"}},
		{name: "NameWithSpace", edit: func(c *webbundle.Config) { c.Entries[0].Name = "my app" }, want: []string{"my app"}},
		{name: "StyleNotCSS", edit: func(c *webbundle.Config) { c.Entries[1].Path = "src/app.ts" }, want: []string{"src/app.ts"}},
		{name: "ScriptIsCSS", edit: func(c *webbundle.Config) { c.Entries[0].Path = "src/app.css" }, want: []string{"src/app.css"}},
		{name: "ShellNotHTML", edit: func(c *webbundle.Config) { c.Shells = []string{"index.html", "package.json"} }, want: []string{"package.json"}},
		{name: "ShellTwice", edit: func(c *webbundle.Config) { c.Shells = []string{"index.html", "index.html"} }, want: []string{"index.html"}},
		{name: "NoDefaultShell", edit: func(c *webbundle.Config) { c.Shells = []string{"login.html"} }, want: []string{"index.html"}},
		{name: "PackagePatternBad", edit: func(c *webbundle.Config) { c.Packages = []string{"[x"} }, want: []string{"[x"}},
		{name: "PagesFileOutsideSrc", edit: func(c *webbundle.Config) { c.PagesFile = "../pages.gen.ts" }, want: []string{"../pages.gen.ts"}},
		{name: "BundleURLsMemberDefined", edit: func(c *webbundle.Config) { c.Define = map[string]string{"BUNDLE_URLS.worker": `"/x.js"`} }, want: []string{"BUNDLE_URLS.worker"}},
		{
			name: "SameLogicalPathTwice",
			edit: func(c *webbundle.Config) {
				c.Entries = append(c.Entries, webbundle.Entry{Name: "app", Path: "src/prepaint.js", Kind: webbundle.Classic})
			},
			want: []string{"/app.js"},
		},
		{name: "NoAPIPrefix", edit: func(c *webbundle.Config) { c.APIPrefix = "" }, want: []string{"API"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, nil)
			root := filepath.Dir(c.Src)
			before := readTree(t, c.Src)
			tc.edit(&c)
			buildRefused(t, c, tc.want...)
			if after := readTree(t, root); !maps.EqualFunc(prefixed("static-src/", before), after, bytes.Equal) {
				t.Errorf("a refused build changed the tree around Src: %q", slices.Sorted(maps.Keys(after)))
			}
		})
	}
}

func TestBuildRefusesOutputOverLimit(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("\x00", 64<<20+1)
	c := newFixture(t, files{
		"src/app.css": ".big { background: url(./big.png); }\n",
		"src/big.png": big,
	})
	buildRefused(t, c, "64 MiB")
}

func TestBuildFailureKeepsLastTree(t *testing.T) {
	t.Parallel()
	c := newFixture(t, nil)
	mustBuild(t, c)
	good := readTree(t, c.Out)

	writeFiles(t, c.Src, files{"index.html": strings.Replace(fixtureShell, "</body>", "<script>alert(1)</script></body>", 1)})
	buildRefused(t, c, "index.html")
	if after := readTree(t, c.Out); !maps.EqualFunc(good, after, bytes.Equal) {
		t.Errorf("a failed build changed Out: %q, want %q", slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(good)))
	}
	entries, err := os.ReadDir(filepath.Dir(c.Out))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"static", "static-src"}; !slices.Equal(names, want) {
		t.Errorf("Out's directory holds %q after a build and a failed build, want %q", names, want)
	}
}

func TestBuildKeepsOutputGitignore(t *testing.T) {
	t.Parallel()
	t.Run("Kept", func(t *testing.T) {
		t.Parallel()
		const ignore = "*\n!.gitignore"
		c := newFixture(t, nil)
		writeFiles(t, c.Out, files{".gitignore": ignore})

		mustBuild(t, c)
		if got := string(readFile(t, c.Out, ".gitignore")); got != ignore {
			t.Errorf("after a build Out/.gitignore = %q, want %q", got, ignore)
		}
		readBundle(t, c.Out)

		writeFiles(t, c.Src, files{"index.html": strings.Replace(fixtureShell, "<body>", `<body style="color: red">`, 1)})
		buildRefused(t, c, "index.html")
		if got := string(readFile(t, c.Out, ".gitignore")); got != ignore {
			t.Errorf("after a failed build Out/.gitignore = %q, want %q", got, ignore)
		}
	})
	t.Run("NotRegular", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, nil)
		writeFiles(t, c.Out, files{"elsewhere": "*\n"})
		if err := os.Symlink("elsewhere", filepath.Join(c.Out, ".gitignore")); err != nil {
			t.Fatal(err)
		}
		buildRefused(t, c, ".gitignore")
	})
	t.Run("NoneWritten", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, nil)
		mustBuild(t, c)
		if _, err := os.Lstat(filepath.Join(c.Out, ".gitignore")); !os.IsNotExist(err) {
			t.Errorf("a build with no Out/.gitignore left one: %v", err)
		}
	})
}

func TestBuildRemovesInterruptedTemporaryTree(t *testing.T) {
	t.Parallel()
	c := newFixture(t, nil)
	left := filepath.Join(filepath.Dir(c.Out), ".static.webbundle-1234")
	writeFiles(t, left, files{"assets/app-OLD.js": "stale"})
	mustBuild(t, c)
	if _, err := os.Lstat(left); !os.IsNotExist(err) {
		t.Errorf("an interrupted build's temporary tree is still beside Out: %v", err)
	}
}

func prefixed(prefix string, tree map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(tree))
	for name, body := range tree {
		out[prefix+name] = body
	}
	return out
}
