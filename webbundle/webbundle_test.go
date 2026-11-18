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

func TestBuildReadsAnUnderscoredNameDotted(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{
		"src/app.ts": `declare const BUNDLE_URLS: { feed_worker: string };
new SharedWorker(BUNDLE_URLS.feed_worker);
`,
		"src/feed-worker.js": "self.addEventListener(\"connect\", () => {});\n",
	})
	c.Entries = append(c.Entries, webbundle.Entry{Name: "feed_worker", Path: "src/feed-worker.js", Kind: webbundle.Classic})
	mustBuild(t, c)
	b := readBundle(t, c.Out)
	worker := entryPath(t, b, "feed_worker", webbundle.Classic)
	if !strings.HasPrefix(worker, "assets/feed_worker-") {
		t.Errorf("the classic entry feed_worker is built as %s, not under assets/feed_worker-<hash>.js", worker)
	}
	app := loadedJS(t, c.Out, entryPath(t, b, "app", webbundle.Module))
	if !bytes.Contains(app, []byte(strconv.Quote("/"+worker))) {
		t.Errorf("the module entry does not read BUNDLE_URLS.feed_worker as %q:\n%s", "/"+worker, app)
	}
}

// A read of BUNDLE_URLS by a classic entry's name is the one read: any other
// would see every classic entry's URL, or none, at run time.
func TestBuildRefusesBundleURLsReadOtherThanByName(t *testing.T) {
	t.Parallel()
	worker := files{"src/worker.js": "self.addEventListener(\"connect\", () => {});\n"}
	for _, tc := range []struct {
		name  string
		files files
	}{
		{name: "Values", files: files{"src/app.ts": "declare const BUNDLE_URLS: Record<string, string>;\nfor (const u of Object.values(BUNDLE_URLS)) new SharedWorker(u);\n"}},
		{name: "Spread", files: files{"src/app.ts": "declare const BUNDLE_URLS: Record<string, string>;\nconsole.log({ ...BUNDLE_URLS });\n"}},
		{name: "Bracket", files: files{"src/app.ts": "declare const BUNDLE_URLS: Record<string, string>;\nconst k = String(Date.now());\nnew SharedWorker(BUNDLE_URLS[k]);\n"}},
		{name: "UnknownName", files: files{"src/app.ts": "declare const BUNDLE_URLS: { nope: string };\nnew SharedWorker(BUNDLE_URLS.nope);\n"}},
		{name: "InALoadedChunk", files: files{
			"src/app.ts":  "void import(\"./late\");\n",
			"src/late.ts": "declare const BUNDLE_URLS: Record<string, string>;\nconsole.log(Object.keys(BUNDLE_URLS));\n",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := files{}
			maps.Copy(fs, worker)
			maps.Copy(fs, tc.files)
			c := newFixture(t, fs)
			c.Entries = append(c.Entries, webbundle.Entry{Name: "worker", Path: "src/worker.js", Kind: webbundle.Classic})
			buildRefused(t, c, `"app"`, "BUNDLE_URLS.worker")
			outMissing(t, c.Out)
		})
	}
	for _, tc := range []struct{ name, read string }{
		{name: "QuotedBracket", read: `BUNDLE_URLS["worker"]`},
		{name: "OptionalChain", read: "BUNDLE_URLS?.worker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := files{"src/app.ts": "declare const BUNDLE_URLS: { worker: string };\nnew SharedWorker(" + tc.read + ");\n"}
			maps.Copy(fs, worker)
			c := newFixture(t, fs)
			c.Entries = append(c.Entries, webbundle.Entry{Name: "worker", Path: "src/worker.js", Kind: webbundle.Classic})
			mustBuild(t, c)
			b := readBundle(t, c.Out)
			url := "/" + entryPath(t, b, "worker", webbundle.Classic)
			app := loadedJS(t, c.Out, entryPath(t, b, "app", webbundle.Module))
			if !bytes.Contains(app, []byte(strconv.Quote(url))) {
				t.Errorf("the module entry does not read %s as %q:\n%s", tc.read, url, app)
			}
		})
	}
	t.Run("ClassicEntry", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{
			"src/app.ts":    "declare const BUNDLE_URLS: { worker: string };\nnew SharedWorker(BUNDLE_URLS.worker);\n",
			"src/worker.js": "self.addEventListener(\"connect\", () => console.log(BUNDLE_URLS.worker));\n",
		})
		c.Entries = append(c.Entries, webbundle.Entry{Name: "worker", Path: "src/worker.js", Kind: webbundle.Classic})
		buildRefused(t, c, `"worker"`, "BUNDLE_URLS")
		outMissing(t, c.Out)
	})
	t.Run("AStringOfTheName", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{
			"src/app.ts":    "declare const BUNDLE_URLS: { worker: string };\nconsole.log(\"BUNDLE_URLS\");\nnew SharedWorker(BUNDLE_URLS.worker);\n",
			"src/worker.js": "self.addEventListener(\"connect\", () => {});\n",
		})
		c.Entries = append(c.Entries, webbundle.Entry{Name: "worker", Path: "src/worker.js", Kind: webbundle.Classic})
		mustBuild(t, c)
	})
}

// A script's import of an asset gives the URL the asset is served at, which
// the page resolves from any path: an icon factory fed such URLs draws on a
// nested page as on the root.
func TestBuildScriptAssetImportIsItsServedURL(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{
		"src/app.ts":         "import pin from \"./icons/pin.svg\";\nconsole.log(\"pin-url\", pin);\n",
		"src/prepaint.js":    "import mark from \"./icons/mark.svg\";\ndocument.documentElement.dataset.mark = mark;\n",
		"src/icons/pin.svg":  fixtureMark,
		"src/icons/mark.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><path d="M1 4h6"/></svg>`,
	})
	mustBuild(t, c)
	b := readBundle(t, c.Out)
	for _, tc := range []struct {
		entry, asset string
		kind         webbundle.EntryKind
	}{
		{entry: "app", asset: "pin.svg", kind: webbundle.Module},
		{entry: "prepaint", asset: "mark.svg", kind: webbundle.Classic},
	} {
		got := listed(b, hashedName(tc.asset))
		if len(got) != 1 {
			t.Errorf("listed %q for %s, want one hashed file", got, tc.asset)
			continue
		}
		js := loadedJS(t, c.Out, entryPath(t, b, tc.entry, tc.kind))
		if want := strconv.Quote("/" + got[0]); !bytes.Contains(js, []byte(want)) {
			t.Errorf("the %s entry does not read %s as %s:\n%s", tc.entry, tc.asset, want, js)
		}
	}
}

// A module imported only by import() lands in a chunk of its own, fetched when
// the page asks for it, never inlined into the entry.
func TestBuildSplitsADynamicImport(t *testing.T) {
	t.Parallel()
	const marker = "lazy-chunk-marker"
	c := newFixture(t, files{
		"src/app.ts":  "export async function open() { const m = await import(\"./lazy\"); m.run(); }\nvoid open();\n",
		"src/lazy.ts": "export function run() { console.log(" + strconv.Quote(marker) + "); }\n",
	})
	mustBuild(t, c)
	b := readBundle(t, c.Out)
	app := readFile(t, c.Out, entryPath(t, b, "app", webbundle.Module))
	if bytes.Contains(app, []byte(marker)) {
		t.Errorf("the module entry inlines the dynamically imported module:\n%s", app)
	}
	var holders []string
	for _, ch := range listed(b, regexp.MustCompile(`^assets/chunks/[^/]+\.js$`)) {
		if bytes.Contains(readFile(t, c.Out, ch), []byte(marker)) {
			holders = append(holders, ch)
		}
	}
	if len(holders) != 1 {
		t.Fatalf("chunks holding the lazy module: %q, want one", holders)
	}
	if !bytes.Contains(app, []byte(`import("`)) || !slices.Contains(importedJS(entryPath(t, b, "app", webbundle.Module), app), holders[0]) {
		t.Errorf("the module entry does not import() %s:\n%s", holders[0], app)
	}
}

// A changed source gives its output another hashed name, and the rebuilt Out
// holds no file of the tree it replaced.
func TestBuildRebuildLeavesNoStaleFile(t *testing.T) {
	t.Parallel()
	c := newFixture(t, nil)
	mustBuild(t, c)
	before := entryPath(t, readBundle(t, c.Out), "prepaint", webbundle.Classic)

	writeFiles(t, c.Src, files{"src/prepaint.js": "document.documentElement.dataset.theme = \"dark\";\n"})
	mustBuild(t, c)
	b := readBundle(t, c.Out)
	if after := entryPath(t, b, "prepaint", webbundle.Classic); after == before {
		t.Errorf("the prepaint entry kept %s after its source changed", after)
	}
	for name := range readTree(t, c.Out) {
		if _, ok := b.Files[name]; !ok && name != httpserver.BundlePath {
			t.Errorf("Out holds %s, which the rebuilt tree does not list", name)
		}
	}
}

// An app's cmd/bundle passes paths relative to the repo root; the tree must
// be the one an absolute Src and Out give.
func TestBuildFromRelativePaths(t *testing.T) {
	c := newFixture(t, nil)
	mustBuild(t, c)
	want := readTree(t, c.Out)

	root := filepath.Dir(c.Src)
	t.Chdir(root)
	rel := c
	rel.Src, rel.Out = "static-src", filepath.Join("build", "static")
	mustBuild(t, rel)
	if got := readTree(t, filepath.Join(root, rel.Out)); !maps.EqualFunc(want, got, bytes.Equal) {
		t.Errorf("a build from relative paths wrote %q, the absolute build %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
}

// loadedJS is a module entry's bytes followed by those of every chunk it
// imports, directly or through another chunk.
func loadedJS(t testing.TB, out, entry string) []byte {
	t.Helper()
	var all []byte
	seen := map[string]bool{entry: true}
	for queue := []string{entry}; len(queue) > 0; queue = queue[1:] {
		body := readFile(t, out, queue[0])
		all = append(all, body...)
		for _, p := range importedJS(queue[0], body) {
			if !seen[p] {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	return all
}

var importRe = regexp.MustCompile(`"((?:\./|\.\./|/)[^"]+\.js)"`)

// importedJS is every script body's import specifier resolved, as the browser
// does, against from's URL, as a slash path in the tree.
func importedJS(from string, body []byte) []string {
	var got []string
	for _, m := range importRe.FindAllSubmatch(body, -1) {
		spec := string(m[1])
		if rest, ok := strings.CutPrefix(spec, "/"); ok {
			got = append(got, path.Clean(rest))
			continue
		}
		got = append(got, path.Join(path.Dir(from), spec))
	}
	return got
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

func TestEnginesAreTheSupportFloor(t *testing.T) {
	t.Parallel()
	got := webbundle.Engines()
	names := map[string]bool{}
	for _, e := range got {
		if _, err := strconv.Atoi(e.Version); err != nil {
			t.Errorf("engine %s has version %q, want a major version", e.Name, e.Version)
		}
		names[e.Name] = true
	}
	if want := []string{"chrome", "firefox", "safari"}; len(got) != len(want) || !slices.Equal(slices.Sorted(maps.Keys(names)), want) {
		t.Errorf("Engines() = %+v, want one each of %q", got, want)
	}
	got[0].Version = "1"
	if again := webbundle.Engines(); again[0].Version == "1" {
		t.Errorf("Engines() returns the package's own slice: a caller's write changed it")
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
		{name: "NameWithHyphen", edit: func(c *webbundle.Config) { c.Entries[0].Name = "feed-worker" }, want: []string{"feed-worker", "BUNDLE_URLS"}},
		{name: "NameLeadingDigit", edit: func(c *webbundle.Config) { c.Entries[0].Name = "2app" }, want: []string{"2app", "BUNDLE_URLS"}},
		{name: "NameLeadingUnderscore", edit: func(c *webbundle.Config) { c.Entries[0].Name = "_app" }, want: []string{"_app", "BUNDLE_URLS"}},
		{name: "NameUpperCase", edit: func(c *webbundle.Config) { c.Entries[0].Name = "App" }, want: []string{"App", "BUNDLE_URLS"}},
		{name: "NameWithDot", edit: func(c *webbundle.Config) { c.Entries[0].Name = "app.min" }, want: []string{"app.min", "BUNDLE_URLS"}},
		{name: "NameNotASCII", edit: func(c *webbundle.Config) { c.Entries[0].Name = "café" }, want: []string{"café", "BUNDLE_URLS"}},
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

// Build reads its tree as httpserver.SPA will before swapping it in: a
// package's notice under a dot-directory would be listed at a path SPA
// never serves.
func TestBuildRefusesTreeSPACannotRead(t *testing.T) {
	t.Parallel()
	const pkg = "node_modules/@cplieger/fixture-a/"
	c := newFixture(t, files{
		pkg + "package.json":   `{"name": "@cplieger/fixture-a", "version": "1.2.3", "type": "module", "main": ".dist/index.js"}`,
		pkg + ".dist/index.js": "export function greet(to) { console.log(\"hello\", to); }\n",
		pkg + ".dist/LICENSE":  "fixture-a dist licence\n",
	})
	buildRefused(t, c, ".dist/LICENSE", "httpserver")
	outMissing(t, c.Out)
}

// Out named through a symlink into Src would replace a directory of Src.
func TestBuildRefusesOutInsideSrcThroughALink(t *testing.T) {
	t.Parallel()
	c := newFixture(t, nil)
	link := filepath.Join(filepath.Dir(c.Src), "src-link")
	if err := os.Symlink(c.Src, link); err != nil {
		t.Fatal(err)
	}
	c.Out = filepath.Join(link, "static")
	buildRefused(t, c, "overlap")
	outMissing(t, filepath.Join(c.Src, "static"))
}
