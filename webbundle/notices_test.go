package webbundle_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

// The families cplieger/ci configs/collect-licenses.sh copies for a Go module
// (copy_files): a regular file at the package root whose upper-cased name
// starts with LICENSE, LICENCE, COPYING or NOTICE, source extensions left out.
func TestNoticesFollowCollectLicensesFamilies(t *testing.T) {
	t.Parallel()
	t.Run("Families", func(t *testing.T) {
		t.Parallel()
		const pkg = "node_modules/@cplieger/fixture-a/"
		c := newFixture(t, files{
			pkg + "NOTICE":         "fixture-a notice\n",
			pkg + "license.js":     "export const license = \"MIT\";\n",
			pkg + "README.md":      "# fixture-a\n",
			pkg + "copying":        "fixture-a copying\n",
			pkg + "Licence.txt":    "fixture-a licence text\n",
			pkg + "LICENSE.ts":     "export {};\n",
			pkg + "NOTICE.map":     "{}\n",
			pkg + "LICENSES/extra": "nested\n",
			pkg + "lib/LICENSE":    "nested\n",
			"node_modules/@cplieger/unused/package.json": `{"name": "@cplieger/unused", "version": "9.9.9"}`,
			"node_modules/@cplieger/unused/LICENSE":      "unused licence\n",
		})
		r := mustBuild(t, c)
		tree := readTree(t, c.Out)

		var copied []string
		for name := range tree {
			if rest, ok := strings.CutPrefix(name, "licenses/"); ok && rest != "index.txt" {
				copied = append(copied, rest)
			}
		}
		slices.Sort(copied)
		notices := []string{"LICENSE.md", "Licence.txt", "NOTICE", "copying"}
		var want []string
		for _, n := range notices {
			want = append(want, "@cplieger/fixture-a@1.2.3/"+n)
		}
		if !slices.Equal(copied, want) {
			t.Errorf("licenses/ holds %q, want %q", copied, want)
		}
		for _, n := range notices {
			if src := readFile(t, c.Src, pkg+n); string(tree["licenses/@cplieger/fixture-a@1.2.3/"+n]) != string(src) {
				t.Errorf("licenses/@cplieger/fixture-a@1.2.3/%s does not hold the package file's bytes", n)
			}
		}
		if got, want := string(tree["licenses/index.txt"]), "@cplieger/fixture-a 1.2.3\n"; got != want {
			t.Errorf("licenses/index.txt = %q, want %q", got, want)
		}
		if want := []string{"@cplieger/fixture-a"}; !slices.Equal(r.Notices, want) {
			t.Errorf("Result.Notices = %q, want %q", r.Notices, want)
		}
		b := readBundle(t, c.Out)
		for _, name := range append(slices.Clone(want), "index.txt") {
			if _, ok := b.Files["licenses/"+name]; !ok {
				t.Errorf("%s does not list licenses/%s; it lists %q", httpserver.BundlePath, name, slices.Sorted(maps.Keys(b.Files)))
			}
		}
	})
	t.Run("PackageWithNone", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{
			"src/app.ts": "import { greet } from \"@cplieger/fixture-a\";\nimport { quiet } from \"@cplieger/fixture-b\";\ngreet(quiet());\n",
			"node_modules/@cplieger/fixture-b/package.json": `{"name": "@cplieger/fixture-b", "version": "0.1.0", "type": "module", "main": "index.js"}`,
			"node_modules/@cplieger/fixture-b/index.js":     "export function quiet() { return \"shh\"; }\n",
			"node_modules/@cplieger/fixture-b/README.md":    "# fixture-b\n",
			"node_modules/@cplieger/fixture-b/license.js":   "export const license = \"MIT\";\n",
		})
		buildRefused(t, c, "@cplieger/fixture-b")
	})
}

// fixtureNested is a second package bundling its own copy of fixture-a at
// 2.0.0, nested under it as npm installs a version conflict.
func fixtureNested(nestedLicense string) files {
	const c, nested = "node_modules/@cplieger/fixture-c/", "node_modules/@cplieger/fixture-c/node_modules/@cplieger/fixture-a/"
	return files{
		"src/app.ts":            "import { greet } from \"@cplieger/fixture-a\";\nimport { hail } from \"@cplieger/fixture-c\";\ngreet(hail());\n",
		c + "package.json":      `{"name": "@cplieger/fixture-c", "version": "0.3.0", "type": "module", "main": "index.js"}`,
		c + "index.js":          "import { greet } from \"@cplieger/fixture-a\";\nexport function hail() { greet(\"c\"); return \"c\"; }\n",
		c + "COPYING":           "fixture-c copying\n",
		nested + "package.json": `{"name": "@cplieger/fixture-a", "version": "2.0.0", "type": "module", "main": "index.js"}`,
		nested + "index.js":     "export function greet(to) { console.info(\"hello v2\", to); }\n",
		nested + "LICENSE.md":   nestedLicense,
	}
}

func TestNoticesOfEveryBundledVersion(t *testing.T) {
	t.Parallel()
	c := newFixture(t, fixtureNested("fixture-a v2 licence\n"))
	r := mustBuild(t, c)
	tree := readTree(t, c.Out)
	want := "@cplieger/fixture-a 1.2.3\n@cplieger/fixture-a 2.0.0\n@cplieger/fixture-c 0.3.0\n"
	if got := string(tree["licenses/index.txt"]); got != want {
		t.Errorf("licenses/index.txt = %q, want %q", got, want)
	}
	for name, body := range map[string]string{
		"@cplieger/fixture-a@1.2.3/LICENSE.md": "fixture-a licence\n",
		"@cplieger/fixture-a@2.0.0/LICENSE.md": "fixture-a v2 licence\n",
		"@cplieger/fixture-c@0.3.0/COPYING":    "fixture-c copying\n",
	} {
		if got := string(tree["licenses/"+name]); got != body {
			t.Errorf("licenses/%s = %q, want %q", name, got, body)
		}
	}
	if want := []string{"@cplieger/fixture-a", "@cplieger/fixture-c"}; !slices.Equal(r.Notices, want) {
		t.Errorf("Result.Notices = %q, want %q", r.Notices, want)
	}
}

// A pnpm install links each package from node_modules/.pnpm, once per set of
// peers; the notices are filed under the package's own name, not the store
// path, and one version is one index line.
func TestNoticesOfAStorePackage(t *testing.T) {
	t.Parallel()
	const store = "node_modules/.pnpm/@cplieger+fixture-a@1.2.3/node_modules/@cplieger/fixture-a/"
	const peers = "node_modules/.pnpm/@cplieger+fixture-a@1.2.3_peer@1.0.0/node_modules/@cplieger/fixture-a/"
	c := newFixture(t, files{
		"src/app.ts":           "import { greet } from \"./store\";\nimport { greet as hail } from \"./peers\";\ngreet(\"x\");\nhail(\"y\");\n",
		"src/store.ts":         "export { greet } from \"../" + store + "index.js\";\n",
		"src/peers.ts":         "export { greet } from \"../" + peers + "index.js\";\n",
		store + "package.json": `{"name": "@cplieger/fixture-a", "version": "1.2.3", "type": "module", "main": "index.js"}`,
		store + "index.js":     "export function greet(to) { console.log(\"store\", to); }\n",
		store + "NOTICE.txt":   "store notice\n",
		peers + "package.json": `{"name": "@cplieger/fixture-a", "version": "1.2.3", "type": "module", "main": "index.js"}`,
		peers + "index.js":     "export function greet(to) { console.info(\"peers\", to); }\n",
		peers + "NOTICE.txt":   "store notice\n",
	})
	mustBuild(t, c)
	tree := readTree(t, c.Out)
	if got := string(tree["licenses/@cplieger/fixture-a@1.2.3/NOTICE.txt"]); got != "store notice\n" {
		t.Errorf("licenses/@cplieger/fixture-a@1.2.3/NOTICE.txt = %q, want the store package's notice", got)
	}
	if got, want := string(tree["licenses/index.txt"]), "@cplieger/fixture-a 1.2.3\n"; got != want {
		t.Errorf("licenses/index.txt = %q, want %q", got, want)
	}
}

func TestNoticesRefuseAVersionNamingNoDirectory(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ field, want string }{
		"Missing":  {``, "has no version"},
		"Empty":    {`"version": "", `, "has no version"},
		"Slash":    {`"version": "1.0.0/../x", `, "not a semver version"},
		"Space":    {`"version": "1.0.0 2", `, "not a semver version"},
		"NotDigit": {`"version": "latest", `, "not a semver version"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newFixture(t, files{
				"node_modules/@cplieger/fixture-a/package.json": `{"name": "@cplieger/fixture-a", ` + tc.field + `"type": "module", "main": "index.js"}`,
			})
			buildRefused(t, c, "@cplieger/fixture-a", tc.want)
		})
	}
}

// pnpm keeps one version once per set of peers; a patched copy whose notice
// differs from its twin cannot share the version's directory.
func TestNoticesRefuseTwoCopiesOfOneVersionThatDiffer(t *testing.T) {
	t.Parallel()
	const store = "node_modules/.pnpm/@cplieger+fixture-a@1.2.3/node_modules/@cplieger/fixture-a/"
	const peers = "node_modules/.pnpm/@cplieger+fixture-a@1.2.3_peer@1.0.0/node_modules/@cplieger/fixture-a/"
	c := newFixture(t, files{
		"src/app.ts":           "import { greet } from \"../" + store + "index.js\";\nimport { greet as hail } from \"../" + peers + "index.js\";\ngreet(\"x\");\nhail(\"y\");\n",
		store + "package.json": `{"name": "@cplieger/fixture-a", "version": "1.2.3", "type": "module", "main": "index.js"}`,
		store + "index.js":     "export function greet(to) { console.log(\"store\", to); }\n",
		store + "LICENSE":      "store licence\n",
		peers + "package.json": `{"name": "@cplieger/fixture-a", "version": "1.2.3", "type": "module", "main": "index.js"}`,
		peers + "index.js":     "export function greet(to) { console.info(\"peers\", to); }\n",
		peers + "LICENSE":      "patched licence\n",
	})
	buildRefused(t, c, "@cplieger/fixture-a@1.2.3", "LICENSE")
}

// A build with no npm package writes no licenses/ tree.
func TestNoticesNoneBundled(t *testing.T) {
	t.Parallel()
	c := newFixture(t, files{
		"src/app.ts": "import { PAGES } from \"../pages.gen\";\nconsole.log(PAGES.library);\n",
	})
	r := mustBuild(t, c)
	for name := range readTree(t, c.Out) {
		if strings.HasPrefix(name, "licenses/") {
			t.Errorf("the build wrote %s with no package bundled", name)
		}
	}
	if len(r.Notices) != 0 {
		t.Errorf("Result.Notices = %q, want none", r.Notices)
	}
}
