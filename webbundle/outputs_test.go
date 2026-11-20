package webbundle_test

import (
	"bytes"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/webbundle"
)

// An entry whose built file is empty or only whitespace ships nothing a
// page can use, whatever its kind: a type-only module, a stylesheet of
// comments, a script emptied by minification.
func TestBuildRefusesEmptyOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, body string
		kind             webbundle.EntryKind
		entry            string
	}{
		{name: "StyleEmpty", path: "src/app.css", body: "", kind: webbundle.Style, entry: "app"},
		{name: "StyleWhitespace", path: "src/app.css", body: "  \n\t\n", kind: webbundle.Style, entry: "app"},
		{name: "StyleCommentsOnly", path: "src/app.css", body: "/* nothing yet */\n", kind: webbundle.Style, entry: "app"},
		{name: "StyleEmptyRule", path: "src/app.css", body: ".unused {}\n", kind: webbundle.Style, entry: "app"},
		{name: "ModuleEmpty", path: "src/app.ts", body: "", kind: webbundle.Module, entry: "app"},
		{name: "ModuleTypesOnly", path: "src/app.ts", body: "export type Theme = \"light\" | \"dark\";\n", kind: webbundle.Module, entry: "app"},
		{name: "ClassicEmpty", path: "src/prepaint.js", body: "", kind: webbundle.Classic, entry: "prepaint"},
		{name: "ClassicCommentsOnly", path: "src/prepaint.js", body: "// set the theme here\n\n", kind: webbundle.Classic, entry: "prepaint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{tc.path: tc.body})
			buildRefused(t, c, string(tc.kind)+` entry "`+tc.entry+`"`, tc.path, "empty")
			outMissing(t, c.Out)
		})
	}
}

// A module entry whose own file adds no code but loads a chunk it shares
// with another entry still runs that code, so it is not empty; a classic
// entry that only imports a package for its effects holds that package's
// code.
func TestBuildKeepsAnEntryWhoseCodeIsImported(t *testing.T) {
	t.Parallel()
	shell := strings.Replace(fixtureShell, `<script type="module" src="/app.js"></script>`,
		`<script type="module" src="/app.js"></script>
<script type="module" src="/other.js"></script>`, 1)
	c := newFixture(t, files{
		"index.html":      shell,
		"src/app.ts":      "import \"./shared\";\n",
		"src/other.ts":    "import \"./shared\";\nconsole.log(\"other\");\n",
		"src/shared.ts":   "console.log(\"shared-marker\");\n",
		"src/prepaint.js": "import \"@cplieger/fixture-a/effect.js\";\n",
		"node_modules/@cplieger/fixture-a/effect.js": "document.documentElement.dataset.effect = \"on\";\n",
	})
	c.Entries = append(c.Entries, webbundle.Entry{Name: "other", Path: "src/other.ts", Kind: webbundle.Module})
	mustBuild(t, c)
	b := readBundle(t, c.Out)
	app := readFile(t, c.Out, entryPath(t, b, "app", webbundle.Module))
	if bytes.Contains(app, []byte("shared-marker")) || !bytes.Contains(loadedJS(t, c.Out, entryPath(t, b, "app", webbundle.Module)), []byte("shared-marker")) {
		t.Errorf("the app entry should load the shared module from a chunk, not hold it:\n%s", app)
	}
}

func TestBuildStyleRefusesEmptyOutput(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"Empty":        "",
		"Whitespace":   " \r\n\t ",
		"CommentsOnly": "/* tokens come later */\n",
		"EmptyRule":    ":root {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{"css/system.css": body})
			got, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: c.Src, Path: "css/system.css"})
			if err == nil {
				t.Fatalf("BuildStyle = %q with no error; want an error naming css/system.css as empty", got)
			}
			for _, want := range []string{"css/system.css", "empty"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("BuildStyle error %q does not name %q", err, want)
				}
			}
		})
	}
}

// Result.Outputs tells, for every script the tree holds, which npm packages
// it holds and whether it holds the app's own code, so an app can tell a
// chunk built only from a package from one that mixes in its own files,
// though both list the same packages.
func TestResultOutputsNamePackagesAndOwnCode(t *testing.T) {
	t.Parallel()
	shell := strings.Replace(fixtureShell, `<script type="module" src="/app.js"></script>`,
		`<script type="module" src="/app.js"></script>
<script type="module" src="/one.js"></script>
<script type="module" src="/two.js"></script>
<script type="module" src="/three.js"></script>`, 1)
	c := newFixture(t, files{
		"index.html":   shell,
		"src/one.ts":   "import { x } from \"@cplieger/fixture-a/x.js\";\nx(\"one\");\n",
		"src/two.ts":   "import { x } from \"@cplieger/fixture-a/x.js\";\nimport { own } from \"./own\";\nx(\"two\");\nown();\n",
		"src/three.ts": "import { own } from \"./own\";\nown();\n",
		"src/own.ts":   "import { y } from \"@cplieger/fixture-a/y.js\";\nimport { z } from \"@cplieger/fixture-b\";\nexport function own() { y(\"own-marker\"); z(); }\n",
		"node_modules/@cplieger/fixture-b/package.json": `{"name": "@cplieger/fixture-b", "version": "0.1.0", "main": "index.js"}`,
		"node_modules/@cplieger/fixture-b/index.js":     "export function z() { console.log(\"z-marker\"); }\n",
		"node_modules/@cplieger/fixture-b/LICENSE":      "fixture-b licence\n",
		// An entry file that only imports a package for its effects puts no
		// code of its own in the script.
		"src/prepaint.js": "import \"@cplieger/fixture-a/theme.js\";\n",
		"node_modules/@cplieger/fixture-a/theme.js": "document.documentElement.dataset.theme = \"light\";\n",
		"node_modules/@cplieger/fixture-a/x.js":     "export function x(v) { console.log(\"x-marker\", v); }\n",
		"node_modules/@cplieger/fixture-a/y.js":     "export function y(v) { console.log(\"y-marker\", v); }\n",
	})
	c.Entries = append(c.Entries,
		webbundle.Entry{Name: "one", Path: "src/one.ts", Kind: webbundle.Module},
		webbundle.Entry{Name: "two", Path: "src/two.ts", Kind: webbundle.Module},
		webbundle.Entry{Name: "three", Path: "src/three.ts", Kind: webbundle.Module},
	)
	r := mustBuild(t, c)
	b := readBundle(t, c.Out)

	scripts := listed(b, regexp.MustCompile(`\.js$`))
	if got := slices.Sorted(maps.Keys(r.Outputs)); !slices.Equal(got, scripts) {
		t.Fatalf("Result.Outputs names %q, want every script in the tree %q", got, scripts)
	}
	holder := func(marker string) string {
		t.Helper()
		var found []string
		for _, p := range listed(b, regexp.MustCompile(`^assets/chunks/[^/]+\.js$`)) {
			if bytes.Contains(readFile(t, c.Out, p), []byte(marker)) {
				found = append(found, p)
			}
		}
		if len(found) != 1 {
			t.Fatalf("chunks holding %s: %q, want one", marker, found)
		}
		return found[0]
	}
	for _, tc := range []struct {
		name, path string
		packages   []string
		own        bool
	}{
		{name: "PackageOnlyChunk", path: holder("x-marker"), packages: []string{"@cplieger/fixture-a"}},
		{name: "MixedChunk", path: holder("own-marker"), packages: []string{"@cplieger/fixture-a", "@cplieger/fixture-b"}, own: true},
		{name: "ModuleEntryWithAPackage", path: entryPath(t, b, "app", webbundle.Module), packages: []string{"@cplieger/fixture-a"}, own: true},
		{name: "ModuleEntryOwnOnly", path: entryPath(t, b, "three", webbundle.Module), own: true},
		{name: "ClassicEntryOfAPackageOnly", path: entryPath(t, b, "prepaint", webbundle.Classic), packages: []string{"@cplieger/fixture-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Outputs[tc.path]
			if !slices.Equal(got.Packages, tc.packages) {
				t.Errorf("Outputs[%s].Packages = %q, want %q", tc.path, got.Packages, tc.packages)
			}
			if got.Own != tc.own {
				t.Errorf("Outputs[%s].Own = %v, want %v", tc.path, got.Own, tc.own)
			}
		})
	}
}
