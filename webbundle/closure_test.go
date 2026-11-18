package webbundle_test

import (
	"bytes"
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
