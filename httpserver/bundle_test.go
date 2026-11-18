package httpserver_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/go/httpserver/v2"
)

const stableAsset = "sw.js"

var stableEntry = httpserver.BundleEntry{Name: "sw", Kind: httpserver.BundleClassic, Path: stableAsset, Stable: true}

// stableTree is the fixture bundle plus a stable entry at the root holding sw.
func stableTree(t *testing.T, sw string) fstest.MapFS {
	t.Helper()
	files := spaFiles()
	files[stableAsset] = sw
	return bundleTree(t, files, append(slices.Clone(spaListed), stableAsset), append(slices.Clone(spaEntries), stableEntry))
}

func TestReadBundleStableEntry(t *testing.T) {
	t.Run("a stable entry at the root is read", func(t *testing.T) {
		b, found, err := httpserver.ReadBundle(stableTree(t, "self.onfetch = null;\n"))
		if !found || err != nil {
			t.Fatalf("found %v err %v, want the bundle", found, err)
		}
		if want := append(slices.Clone(spaEntries), stableEntry); !slices.Equal(b.Entries, want) {
			t.Errorf("entries = %v, want %v", b.Entries, want)
		}
	})
	t.Run("the manifest key is stable, left out when false", func(t *testing.T) {
		raw, err := json.Marshal([]httpserver.BundleEntry{stableEntry, spaEntries[0]})
		if err != nil {
			t.Fatal(err)
		}
		want := `[{"name":"sw","kind":"classic","path":"sw.js","stable":true},{"name":"app","kind":"module","path":"assets/app-BBBB.js"}]`
		if string(raw) != want {
			t.Errorf("entries encode as %s, want %s", raw, want)
		}
	})

	files := spaFiles()
	files[stableAsset] = "self.onfetch = null;\n"
	files["sw.mjs"] = "export {};\n"
	files["app.css"] = "a{}\n"
	files["licenses/sw.js"] = "self.onfetch = null;\n"
	files["assets/sw-1.js"] = "self.onfetch = null;\n"
	tree := func(listed []string, e ...httpserver.BundleEntry) fstest.MapFS {
		return bundleTree(t, files, append(slices.Clone(spaListed), listed...), append(slices.Clone(spaEntries), e...))
	}
	stable := func(path string, kind httpserver.BundleKind) httpserver.BundleEntry {
		return httpserver.BundleEntry{Name: "sw", Kind: kind, Path: path, Stable: true}
	}
	refused := map[string]fstest.MapFS{
		"a stable shell":                  tree(nil, stable("index.html", httpserver.BundleClassic)),
		"a stable manifest":               tree(nil, stable(httpserver.BundlePath, httpserver.BundleClassic)),
		"a stable entry under assets":     tree([]string{"assets/sw-1.js"}, stable("assets/sw-1.js", httpserver.BundleClassic)),
		"a stable entry in a directory":   tree([]string{"licenses/sw.js"}, stable("licenses/sw.js", httpserver.BundleClassic)),
		"a stable entry not a .js file":   tree([]string{"sw.mjs"}, stable("sw.mjs", httpserver.BundleClassic)),
		"a stable style entry":            tree([]string{"app.css"}, stable("app.css", httpserver.BundleStyle)),
		"a non-stable entry at the root":  tree([]string{stableAsset}, httpserver.BundleEntry{Name: "sw", Kind: httpserver.BundleClassic, Path: stableAsset}),
		"a root .js file no entry builds": tree([]string{stableAsset}),
	}
	for name, fsys := range refused {
		t.Run(name, func(t *testing.T) {
			_, _, err := httpserver.ReadBundle(fsys)
			if err == nil || !strings.Contains(err.Error(), "stable") {
				t.Errorf("ReadBundle error = %v, want a refusal naming the stable-entry rule", err)
			}
		})
	}
}
