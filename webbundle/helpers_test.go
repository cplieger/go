package webbundle_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/webbundle"
)

// files is a source tree by slash path.
type files map[string]string

const fixtureAPIPrefix = "/api"

const fixtureShell = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Fixture</title>
<script src="/prepaint.js"></script>
<link rel="stylesheet" href="/app.css">
</head>
<body>
<script type="module" src="/app.js"></script>
</body>
</html>
`

var fixturePages = []httpserver.Page{
	{Name: "library", Pattern: "/library"},
	{Name: "entry", Pattern: "/library/{id}"},
}

// fixtureFiles is a small app: a module entry importing the generated page
// table and one first-party package, a stylesheet, a prepaint script and the
// shell naming all three.
func fixtureFiles() files {
	return files{
		"package.json": `{"name": "fixture", "private": true}`,
		"index.html":   fixtureShell,
		"src/app.ts": `import { PAGES } from "../pages.gen";
import { greet } from "@cplieger/fixture-a";
greet(PAGES.library);
`,
		"src/app.css":     ".fixture-app { color: rgb(10 20 30); }\n",
		"src/prepaint.js": "document.documentElement.dataset.theme = localStorage.getItem(\"theme\") ?? \"light\";\n",
		"node_modules/@cplieger/fixture-a/package.json": `{"name": "@cplieger/fixture-a", "version": "1.2.3", "type": "module", "main": "index.js"}`,
		"node_modules/@cplieger/fixture-a/index.js":     "export function greet(to) { console.log(\"hello\", to); }\n",
		"node_modules/@cplieger/fixture-a/LICENSE.md":   "fixture-a licence\n",
	}
}

func fixtureEntries() []webbundle.Entry {
	return []webbundle.Entry{
		{Name: "app", Path: "src/app.ts", Kind: webbundle.Module},
		{Name: "app", Path: "src/app.css", Kind: webbundle.Style},
		{Name: "prepaint", Path: "src/prepaint.js", Kind: webbundle.Classic},
	}
}

// newFixture writes the fixture app with extra laid over it (an extra file
// replaces the fixture's file of the same path) into a fresh directory, with
// its page table and corpus generated, and returns the Config building it
// into an Out beside Src that does not exist yet.
func newFixture(t testing.TB, extra files) webbundle.Config {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "static-src")
	tree := fixtureFiles()
	maps.Copy(tree, extra)
	writeFiles(t, src, tree)
	c := webbundle.Config{
		Src:       src,
		Out:       filepath.Join(root, "static"),
		Entries:   fixtureEntries(),
		Pages:     slices.Clone(fixturePages),
		APIPrefix: fixtureAPIPrefix,
	}
	writePageFiles(t, c)
	return c
}

// writePageFiles writes c's page table and corpus as go generate would.
func writePageFiles(t testing.TB, c webbundle.Config) {
	t.Helper()
	table, err := webbundle.RenderPages(c.Pages, c.APIPrefix)
	if err != nil {
		t.Fatalf("RenderPages: %v", err)
	}
	corpus, err := webbundle.RenderPageCorpus(c.Pages, c.APIPrefix)
	if err != nil {
		t.Fatalf("RenderPageCorpus: %v", err)
	}
	pagesFile, corpusFile := c.PagesFile, c.PageCorpusFile
	if pagesFile == "" {
		pagesFile = "pages.gen.ts"
	}
	if corpusFile == "" {
		corpusFile = "pages.corpus.gen.json"
	}
	writeFiles(t, c.Src, files{pagesFile: string(table), corpusFile: string(corpus)})
}

func writeFiles(t testing.TB, root string, tree files) {
	t.Helper()
	for name, body := range tree {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree returns every regular file under root by slash path; a missing
// root is an empty tree.
func readTree(t testing.TB, root string) map[string][]byte {
	t.Helper()
	tree := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return filepath.SkipDir
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		tree[filepath.ToSlash(rel)] = body
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func mustBuild(t testing.TB, c webbundle.Config) webbundle.Result {
	t.Helper()
	r, err := webbundle.Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}

// buildRefused builds c, which must fail with an error naming each of want.
func buildRefused(t testing.TB, c webbundle.Config, want ...string) {
	t.Helper()
	_, err := webbundle.Build(c)
	if err == nil {
		t.Fatalf("Build succeeded; want an error naming %q", want)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("Build error %q does not name %q", err, w)
		}
	}
}

// readBundle reads out's manifest the way httpserver.SPA does.
func readBundle(t testing.TB, out string) httpserver.Bundle {
	t.Helper()
	b, found, err := httpserver.ReadBundle(os.DirFS(out))
	if err != nil {
		t.Fatalf("httpserver.ReadBundle: %v", err)
	}
	if !found {
		t.Fatalf("%s has no %s", out, httpserver.BundlePath)
	}
	return b
}

// entryPath is the hashed path b records for the entry name of kind.
func entryPath(t testing.TB, b httpserver.Bundle, name string, kind webbundle.EntryKind) string {
	t.Helper()
	for _, e := range b.Entries {
		if e.Name == name && string(e.Kind) == string(kind) {
			return e.Path
		}
	}
	t.Fatalf("%s has no %s entry %q: %+v", httpserver.BundlePath, kind, name, b.Entries)
	return ""
}

// listed returns b's listed files matching re, sorted.
func listed(b httpserver.Bundle, re *regexp.Regexp) []string {
	var got []string
	for name := range b.Files {
		if re.MatchString(name) {
			got = append(got, name)
		}
	}
	slices.Sort(got)
	return got
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func readFile(t testing.TB, root, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// hashedName matches the hashed file Build writes for a source base name
// ("app.css" gives assets/app-<hash>.css).
func hashedName(base string) *regexp.Regexp {
	ext := path.Ext(base)
	return regexp.MustCompile(`^assets/` + regexp.QuoteMeta(strings.TrimSuffix(base, ext)) + `-[A-Za-z0-9]+` + regexp.QuoteMeta(ext) + `$`)
}

// symlink makes link, a slash path under root, a symbolic link to target.
func symlink(t testing.TB, root, target, link string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(link))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.FromSlash(target), p); err != nil {
		t.Fatal(err)
	}
}

// outMissing fails t when out exists: a refused first build writes no tree.
func outMissing(t testing.TB, out string) {
	t.Helper()
	if _, err := os.Lstat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused build left %s (Lstat: %v)", out, err)
	}
}
