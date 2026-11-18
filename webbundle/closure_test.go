package webbundle_test

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/webbundle"
)

// outsideFiles makes fixture-a import a package outside the default
// Packages.
func outsideFiles() files {
	return files{
		"node_modules/@cplieger/fixture-a/index.js":  "import pad from \"left-pad-fixture\";\nexport function greet(to) { console.log(pad(\"hello\"), to); }\n",
		"node_modules/left-pad-fixture/package.json": `{"name": "left-pad-fixture", "version": "0.0.1", "type": "module", "main": "index.js"}`,
		"node_modules/left-pad-fixture/index.js":     "export default function pad(s) { return \" \" + s; }\n",
		"node_modules/left-pad-fixture/LICENSE":      "left-pad-fixture licence\n",
	}
}

func TestBuildRefusesPackageOutsidePackages(t *testing.T) {
	t.Parallel()
	t.Run("Default", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, outsideFiles())
		buildRefused(t, c, "left-pad-fixture", "@cplieger/fixture-a", "src/app.ts")
	})
	t.Run("Listed", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, outsideFiles())
		c.Packages = []string{"@cplieger/*", "left-pad-fixture"}
		r := mustBuild(t, c)
		if want := []string{"@cplieger/fixture-a", "left-pad-fixture"}; !slices.Equal(r.Notices, want) {
			t.Errorf("Result.Notices = %q, want %q", r.Notices, want)
		}
	})
}

func TestBuildRefusesForbiddenClosure(t *testing.T) {
	t.Parallel()
	worker := webbundle.Entry{
		Name:   "worker",
		Path:   "src/worker.js",
		Kind:   webbundle.Classic,
		Forbid: []string{"@cplieger/fixture-a", "src/wire"},
	}
	shell := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Fixture</title>
<script src="/prepaint.js"></script>
<link rel="stylesheet" href="/app.css">
<script src="/worker.js"></script>
</head>
<body>
<script type="module" src="/app.js"></script>
</body>
</html>
`
	cases := []struct {
		name   string
		worker string
		want   []string
	}{
		{name: "SrcPath", worker: "import { decode } from \"./wire/decode.js\";\nself.addEventListener(\"connect\", decode);\n", want: []string{"worker", "src/wire"}},
		{name: "Package", worker: "import { greet } from \"@cplieger/fixture-a\";\nself.addEventListener(\"connect\", greet);\n", want: []string{"worker", "@cplieger/fixture-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{
				"index.html":         shell,
				"src/worker.js":      tc.worker,
				"src/wire/decode.js": "export function decode(e) { return e; }\n",
			})
			c.Entries = append(c.Entries, worker)
			buildRefused(t, c, tc.want...)
		})
	}
	t.Run("NotReached", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{
			"index.html":         shell,
			"src/worker.js":      "import { count } from \"./wirelike.js\";\nself.addEventListener(\"connect\", count);\n",
			"src/wirelike.js":    "export function count() {}\n",
			"src/wire/decode.js": "export function decode(e) { return e; }\n",
		})
		c.Entries = append(c.Entries, worker)
		mustBuild(t, c)
	})
}

// A prepaint script blocks first paint, so an app keeps every other file of
// its own tree out of it by forbidding the directory the entry sits in.
func TestBuildForbidNamingTheEntrysOwnDirectory(t *testing.T) {
	t.Parallel()
	prepaint := func(c *webbundle.Config) {
		c.Entries[2].Forbid = []string{"src"}
	}
	t.Run("EntryAlone", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, nil)
		prepaint(&c)
		mustBuild(t, c)
	})
	t.Run("LocalImport", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{
			"src/prepaint.js":    "import { stored } from \"./theme-store.js\";\ndocument.documentElement.dataset.theme = stored();\n",
			"src/theme-store.js": "export function stored() { return localStorage.getItem(\"theme\") ?? \"light\"; }\n",
		})
		prepaint(&c)
		buildRefused(t, c, "prepaint", "src/theme-store.js")
	})
}

// Each case reaches a file beside Src, outside it, whose marker must not
// ship: a script import, a url(), an @import, and a symlink inside an
// allowed package whose target leaves Src.
func TestBuildRefusesInputOutsideSrc(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		extra   files
		outside files
		link    [2]string // target and link, under Src
		want    []string
	}{
		{
			name:    "Import",
			extra:   files{"src/app.ts": "import { mark } from \"../../outside.js\";\nconsole.log(mark);\n"},
			outside: files{"outside.js": "export const mark = \"OUTSIDE_MARKER\";\n"},
			want:    []string{"../outside.js", "src/app.ts", "outside Src"},
		},
		{
			name:    "URL",
			extra:   files{"src/app.css": ".x { background: url(../../outside.svg); }\n"},
			outside: files{"outside.svg": "<svg xmlns=\"http://www.w3.org/2000/svg\"><!-- OUTSIDE_MARKER --></svg>\n"},
			want:    []string{"../outside.svg", "src/app.css", "outside Src"},
		},
		{
			name:    "AtImport",
			extra:   files{"src/app.css": "@import \"../../outside.css\";\n.x { color: red; }\n"},
			outside: files{"outside.css": ".outside-marker { color: blue; }\n"},
			want:    []string{"../outside.css", "src/app.css", "outside Src"},
		},
		{
			name: "SymlinkInAPackage",
			extra: files{
				"node_modules/@cplieger/fixture-a/index.js": "import { mark } from \"./linked.js\";\nexport function greet(to) { console.log(mark, to); }\n",
			},
			outside: files{"linked.js": "export const mark = \"OUTSIDE_MARKER\";\n"},
			link:    [2]string{"../../../../linked.js", "node_modules/@cplieger/fixture-a/linked.js"},
			want:    []string{"../linked.js", "@cplieger/fixture-a", "outside Src"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, tc.extra)
			writeFiles(t, filepath.Dir(c.Src), tc.outside)
			if tc.link[1] != "" {
				symlink(t, c.Src, tc.link[0], tc.link[1])
			}
			buildRefused(t, c, tc.want...)
			outMissing(t, c.Out)
		})
	}
}

// pnpmFiles lays @cplieger/fixture-ui out as pnpm does: the package lives in
// node_modules/.pnpm and node_modules/@cplieger/fixture-ui is a symlink to it.
func pnpmFiles(t testing.TB, src string) {
	t.Helper()
	const store = "node_modules/.pnpm/@cplieger+fixture-ui@2.0.0/node_modules/@cplieger/fixture-ui/"
	writeFiles(t, src, files{
		store + "package.json":   `{"name": "@cplieger/fixture-ui", "version": "2.0.0"}`,
		store + "LICENSE":        "fixture-ui licence\n",
		store + "css/system.css": ".cp-system { color: rgb(1 2 3); }\n",
		store + "theme.js":       "document.documentElement.dataset.theme = \"dark\";\n",
	})
	symlink(t, src, "../.pnpm/@cplieger+fixture-ui@2.0.0/node_modules/@cplieger/fixture-ui", "node_modules/@cplieger/fixture-ui")
}

// An entry may sit in a package pnpm installed: its path names the symlink,
// esbuild names the file by its real path in the store.
func TestBuildEntriesThroughASymlinkedPackage(t *testing.T) {
	t.Parallel()
	const (
		system = "node_modules/@cplieger/fixture-ui/css/system.css"
		theme  = "node_modules/@cplieger/fixture-ui/theme.js"
	)
	shell := strings.Replace(fixtureShell, "</head>", "<link rel=\"stylesheet\" href=\"/system.css\">\n<script src=\"/theme.js\"></script>\n</head>", 1)
	t.Run("BuildStyle", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, nil)
		pnpmFiles(t, c.Src)
		got, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: c.Src, Path: system})
		if err != nil {
			t.Fatalf("BuildStyle: %v", err)
		}
		if !strings.Contains(string(got), ".cp-system") {
			t.Errorf("BuildStyle gave %q, want the system rules", got)
		}
	})
	t.Run("StyleAndClassicEntries", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"index.html": shell})
		pnpmFiles(t, c.Src)
		c.Entries = append(c.Entries,
			webbundle.Entry{Name: "system", Path: system, Kind: webbundle.Style},
			webbundle.Entry{Name: "theme", Path: theme, Kind: webbundle.Classic},
		)
		r := mustBuild(t, c)
		b := readBundle(t, c.Out)
		css := readFile(t, c.Out, entryPath(t, b, "system", webbundle.Style))
		want, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: c.Src, Path: system})
		if err != nil {
			t.Fatalf("BuildStyle: %v", err)
		}
		if !bytes.Equal(css, want) {
			t.Errorf("the system entry holds %q, BuildStyle gives %q", css, want)
		}
		if js := readFile(t, c.Out, entryPath(t, b, "theme", webbundle.Classic)); !strings.Contains(string(js), "dark") {
			t.Errorf("the theme entry holds %q, want the store file's script", js)
		}
		if !slices.Contains(r.Notices, "@cplieger/fixture-ui") {
			t.Errorf("Result.Notices = %q, want @cplieger/fixture-ui", r.Notices)
		}
		if got := string(readFile(t, c.Out, "licenses/@cplieger/fixture-ui@2.0.0/LICENSE")); got != "fixture-ui licence\n" {
			t.Errorf("licenses/@cplieger/fixture-ui@2.0.0/LICENSE = %q", got)
		}
	})
	t.Run("SrcThroughASymlink", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, nil)
		link := filepath.Join(filepath.Dir(c.Src), "linked-src")
		if err := os.Symlink(c.Src, link); err != nil {
			t.Fatal(err)
		}
		c.Src = link
		mustBuild(t, c)
		if _, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: link, Path: "src/app.css"}); err != nil {
			t.Errorf("BuildStyle: %v", err)
		}
	})
}

// A path naming a symlink whose target leaves Src is refused before
// anything is built.
func TestBuildRefusesEntryLinkedOutsideSrc(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) webbundle.Config {
		c := newFixture(t, nil)
		writeFiles(t, filepath.Dir(c.Src), files{"outside.css": ".outside-marker { color: blue; }\n"})
		symlink(t, c.Src, "../../outside.css", "src/linked.css")
		return c
	}
	t.Run("Entry", func(t *testing.T) {
		t.Parallel()
		c := setup(t)
		c.Entries[1].Path = "src/linked.css"
		buildRefused(t, c, "src/linked.css", "outside Src")
		outMissing(t, c.Out)
	})
	t.Run("BuildStyle", func(t *testing.T) {
		t.Parallel()
		c := setup(t)
		got, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: c.Src, Path: "src/linked.css"})
		if err == nil || !strings.Contains(err.Error(), "src/linked.css") || !strings.Contains(err.Error(), "outside Src") {
			t.Errorf("BuildStyle = %q, %v; want an error naming src/linked.css outside Src", got, err)
		}
	})
}

// uiFiles is a package whose exports map names its files by subpaths that
// are not their paths inside it, as @cplieger/ui's does; theme is the
// source of src/theme.js.
func uiFiles(theme string) files {
	const root = "node_modules/@cplieger/fixture-ui/"
	return files{
		root + "package.json":  `{"name": "@cplieger/fixture-ui", "version": "2.0.0", "type": "module", "exports": {"./theme": "./src/theme.js", "./tokens": "./src/tokens.js"}}`,
		root + "LICENSE":       "fixture-ui licence\n",
		root + "src/theme.js":  theme,
		root + "src/tokens.js": "export const dark = \"TOKENS_MARKER\";\n",
	}
}

const (
	themeAlone  = "export function apply() { document.documentElement.dataset.theme = \"THEME_MARKER\"; }\n"
	themeTokens = "import { dark } from \"./tokens.js\";\nexport function apply() { document.documentElement.dataset.theme = dark; }\n"
	prepaintUI  = "import { apply } from \"@cplieger/fixture-ui/theme\";\napply();\n"
)

// Allow holds a prepaint script to the files it names: a file a package
// exports is the file an import of it resolves to, whatever its path inside
// the package.
func TestBuildAllowHoldsTheClosure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		extra files
		allow []string
		want  []string // the refusal's words; nil builds
	}{
		{
			name:  "OnePackageFile",
			extra: uiFiles(themeAlone),
			allow: []string{"@cplieger/fixture-ui/theme"},
		},
		{
			name:  "PackageFileImportingASecondModule",
			extra: uiFiles(themeTokens),
			allow: []string{"@cplieger/fixture-ui/theme"},
			want:  []string{"prepaint", "Allow", "node_modules/@cplieger/fixture-ui/src/theme.js -> node_modules/@cplieger/fixture-ui/src/tokens.js"},
		},
		{
			name:  "BothPackageFiles",
			extra: uiFiles(themeTokens),
			allow: []string{"@cplieger/fixture-ui/theme", "@cplieger/fixture-ui/tokens"},
		},
		{
			name:  "WholePackage",
			extra: uiFiles(themeTokens),
			allow: []string{"@cplieger/fixture-ui"},
		},
		{
			name: "OwnFileNotNamed",
			extra: func() files {
				f := uiFiles(themeAlone)
				f["src/prepaint.js"] = "import { apply } from \"@cplieger/fixture-ui/theme\";\nimport { stored } from \"./theme-store.js\";\napply(stored());\n"
				f["src/theme-store.js"] = "export function stored() { return localStorage.getItem(\"theme\"); }\n"
				return f
			}(),
			allow: []string{"@cplieger/fixture-ui/theme"},
			want:  []string{"prepaint", "Allow", "src/prepaint.js -> src/theme-store.js"},
		},
		{
			name: "SrcPath",
			extra: files{
				"src/prepaint.js":         "import { stored } from \"./theme/store.js\";\ndocument.documentElement.dataset.theme = stored();\n",
				"src/theme/store.js":      "import { fallback } from \"./fallback.js\";\nexport function stored() { return localStorage.getItem(\"theme\") ?? fallback; }\n",
				"src/theme/fallback.js":   "export const fallback = \"light\";\n",
				"src/themelike/unused.js": "export const unused = 1;\n",
			},
			allow: []string{"src/theme"},
		},
		{
			name: "SrcPathByWholeSegments",
			extra: files{
				"src/prepaint.js":    "import { stored } from \"./themelike.js\";\ndocument.documentElement.dataset.theme = stored();\n",
				"src/themelike.js":   "export function stored() { return \"light\"; }\n",
				"src/theme/store.js": "export const unused = 1;\n",
			},
			allow: []string{"src/theme"},
			want:  []string{"prepaint", "Allow", "src/themelike.js"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			extra := files{"src/prepaint.js": prepaintUI}
			maps.Copy(extra, tc.extra)
			c := newFixture(t, extra)
			c.Entries[2].Allow = tc.allow
			if tc.want != nil {
				buildRefused(t, c, tc.want...)
				outMissing(t, c.Out)
				return
			}
			mustBuild(t, c)
		})
	}
	// A script's import reads a package's "import" export condition and a
	// stylesheet's @import does not, so a style entry's item resolves as a
	// CSS @import does.
	t.Run("StyleEntryPackageFile", func(t *testing.T) {
		t.Parallel()
		const root = "node_modules/@cplieger/fixture-ui/"
		c := newFixture(t, files{
			"src/app.css":                "@import \"@cplieger/fixture-ui/css/system\";\n.fixture-app { color: rgb(10 20 30); }\n",
			root + "package.json":        `{"name": "@cplieger/fixture-ui", "version": "2.0.0", "exports": {"./css/system": {"import": "./css/script-side.css", "default": "./css/system.css"}}}`,
			root + "LICENSE":             "fixture-ui licence\n",
			root + "css/system.css":      ".cp-system { color: rgb(1 2 3); }\n",
			root + "css/script-side.css": ".cp-script-side { color: rgb(4 5 6); }\n",
		})
		c.Entries[1].Allow = []string{"@cplieger/fixture-ui/css/system"}
		mustBuild(t, c)
	})
	t.Run("ThroughASymlinkedPackage", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"src/prepaint.js": "import \"@cplieger/fixture-ui/theme.js\";\n"})
		pnpmFiles(t, c.Src)
		c.Entries[2].Allow = []string{"@cplieger/fixture-ui/theme.js"}
		mustBuild(t, c)
		js := readFile(t, c.Out, entryPath(t, readBundle(t, c.Out), "prepaint", webbundle.Classic))
		if !strings.Contains(string(js), "dark") {
			t.Errorf("the prepaint entry holds %q, want the store file's script", js)
		}
	})
}

// Forbid and Allow share one matcher, so a Forbid item of each shape fires.
func TestBuildForbidMatchesEveryShape(t *testing.T) {
	t.Parallel()
	nested := "node_modules/@cplieger/fixture-a/node_modules/@cplieger/fixture-ui/"
	cases := []struct {
		name   string
		extra  files
		forbid []string
		want   []string // the refusal's words; nil builds
	}{
		{
			name:   "PackageFile",
			extra:  uiFiles(themeTokens),
			forbid: []string{"@cplieger/fixture-ui/tokens"},
			want:   []string{"prepaint", "@cplieger/fixture-ui/tokens", "node_modules/@cplieger/fixture-ui/src/theme.js -> node_modules/@cplieger/fixture-ui/src/tokens.js"},
		},
		{
			name:   "PackageFileNotReached",
			extra:  uiFiles(themeAlone),
			forbid: []string{"@cplieger/fixture-ui/tokens"},
		},
		{
			// A package another one depends on at its own version is
			// installed inside it; the item names the file in every copy.
			name: "PackageFileInANestedCopy",
			extra: func() files {
				f := uiFiles(themeAlone)
				f["src/prepaint.js"] = "import { greet } from \"@cplieger/fixture-a\";\ngreet(\"prepaint\");\n"
				f["node_modules/@cplieger/fixture-a/index.js"] = "import { apply } from \"@cplieger/fixture-ui/theme\";\nexport function greet(to) { apply(); console.log(to); }\n"
				f[nested+"package.json"] = `{"name": "@cplieger/fixture-ui", "version": "1.0.0", "type": "module", "exports": {"./theme": "./src/theme.js"}}`
				f[nested+"LICENSE"] = "fixture-ui 1 licence\n"
				f[nested+"src/theme.js"] = themeAlone
				return f
			}(),
			forbid: []string{"@cplieger/fixture-ui/theme"},
			want:   []string{"prepaint", "@cplieger/fixture-ui/theme", nested + "src/theme.js"},
		},
		{
			name: "SrcPathHoldingAPackage",
			extra: files{
				"src/prepaint.js": "import { greet } from \"../vendor/node_modules/@cplieger/fixture-v/index.js\";\ngreet(\"prepaint\");\n",
				"vendor/node_modules/@cplieger/fixture-v/package.json": `{"name": "@cplieger/fixture-v", "version": "0.1.0", "type": "module", "main": "index.js"}`,
				"vendor/node_modules/@cplieger/fixture-v/index.js":     "export function greet(to) { console.log(to); }\n",
				"vendor/node_modules/@cplieger/fixture-v/LICENSE":      "fixture-v licence\n",
			},
			forbid: []string{"vendor"},
			want:   []string{"prepaint", "vendor", "src/prepaint.js -> vendor/node_modules/@cplieger/fixture-v/index.js"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			extra := files{"src/prepaint.js": prepaintUI}
			maps.Copy(extra, tc.extra)
			c := newFixture(t, extra)
			c.Entries[2].Forbid = tc.forbid
			if tc.want != nil {
				buildRefused(t, c, tc.want...)
				return
			}
			mustBuild(t, c)
		})
	}
}

// An item that can match no input would guard nothing, or hold the entry to
// nothing, so it is refused before anything is built.
func TestBuildRefusesClosureItemMatchingNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		item string
		want string
	}{
		{name: "Empty", item: "", want: "is no package, package file or path under Src"},
		{name: "SrcRoot", item: ".", want: "is no package, package file or path under Src"},
		{name: "OutsideSrc", item: "../outside", want: "is no package, package file or path under Src"},
		{name: "Absolute", item: "/src/wire", want: "is no package, package file or path under Src"},
		{name: "UnderNodeModules", item: "node_modules/@cplieger/fixture-a", want: "name the package"},
		{name: "UnderNodeModulesMissing", item: "node_modules/@cplieger/fixture-z/index.js", want: "name the package"},
		{name: "ScopeAlone", item: "@cplieger", want: "no package name"},
		{name: "DotPackage", item: ".pnpm/x", want: "Config.Packages"},
		{name: "PackageNotListed", item: "left-pad-fixture", want: "Config.Packages"},
		{name: "SrcPathMissing", item: "src/nothere", want: "Config.Packages"},
		{name: "SrcPathUnderAFile", item: "src/prepaint.js/x", want: "Config.Packages"},
		{name: "ExplicitSrcPathMissing", item: "./lit/nothere", want: "none exists there"},
		{name: "PackageFileMissing", item: "@cplieger/fixture-a/missing.js", want: "resolves to no file"},
		{name: "PackageFileNotExported", item: "@cplieger/fixture-ui/src/theme.js", want: "resolves to no file"},
		{name: "SrcPathAndPackage", item: "lit/core", want: `"./lit/core"`},
	}
	for _, field := range []string{"Forbid", "Allow"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					extra := uiFiles(themeAlone)
					extra["lit/core/x.js"] = "export const x = 1;\n"
					extra["node_modules/lit/package.json"] = `{"name": "lit", "version": "3.0.0"}`
					extra["node_modules/lit/LICENSE"] = "lit licence\n"
					c := newFixture(t, extra)
					c.Packages = []string{"@cplieger/*", "lit"}
					if field == "Forbid" {
						c.Entries[2].Forbid = []string{tc.item}
					} else {
						c.Entries[2].Allow = []string{tc.item}
					}
					buildRefused(t, c, "prepaint", field, tc.item, tc.want)
					outMissing(t, c.Out)
				})
			}
		})
	}
	// A file a package holds through a link into another package is
	// bundled as the other package's, so the item would name nothing.
	t.Run("PackageFileLinkedIntoAnotherPackage", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, nil)
		symlink(t, c.Src, "../fixture-a/index.js", "node_modules/@cplieger/fixture-l/index.js")
		writeFiles(t, c.Src, files{"node_modules/@cplieger/fixture-l/package.json": `{"name": "@cplieger/fixture-l", "version": "0.1.0"}`})
		c.Entries[2].Forbid = []string{"@cplieger/fixture-l/index.js"}
		buildRefused(t, c, "prepaint", "Forbid", "@cplieger/fixture-l/index.js", "node_modules/@cplieger/fixture-a/index.js", "not a file of @cplieger/fixture-l")
	})
	t.Run("ExplicitSrcPath", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{"lit/core/x.js": "export const x = 1;\n", "node_modules/lit/package.json": `{"name": "lit", "version": "3.0.0"}`})
		c.Entries[2].Forbid = []string{"./lit/core"}
		mustBuild(t, c)
	})
}
