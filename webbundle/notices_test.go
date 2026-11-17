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
		want := []string{
			"@cplieger/fixture-a/LICENSE.md",
			"@cplieger/fixture-a/Licence.txt",
			"@cplieger/fixture-a/NOTICE",
			"@cplieger/fixture-a/copying",
		}
		if !slices.Equal(copied, want) {
			t.Errorf("licenses/ holds %q, want %q", copied, want)
		}
		for _, name := range want {
			if src := readFile(t, c.Src, "node_modules/"+name); string(tree["licenses/"+name]) != string(src) {
				t.Errorf("licenses/%s does not hold the package file's bytes", name)
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
