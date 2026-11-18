package webbundle_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/go/webbundle"
)

// decodeJSON decodes one JSON value, keeping each number's text.
func decodeJSON(t testing.TB, body []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

// fixtureManifest uses the members a browser reads beside the ones Build
// checks, an app's own member, and src paths from the manifest's folder.
const fixtureManifest = `{
  "id": "/?app=fixture",
  "name": "Fixture App",
  "short_name": "Fixture",
  "description": "A fixture app",
  "start_url": "/library?source=pwa",
  "scope": "/",
  "display": "standalone",
  "display_override": ["window-controls-overlay", "minimal-ui"],
  "orientation": "any",
  "theme_color": "#112233",
  "background_color": "#f7f5f4",
  "launch_handler": {"client_mode": "focus-existing"},
  "shortcuts": [
    {"name": "Library", "url": "/library", "icons": [{"src": "icons/library.png", "sizes": "96x96", "type": "image/png"}]},
    {"name": "Search", "url": "/library?q=", "icons": [{"src": "./icons/library.png", "sizes": "96x96"}]}
  ],
  "screenshots": [{"src": "../shots/wide.webp", "sizes": "1280x720", "form_factor": "wide"}],
  "share_target": {"action": "/library", "method": "GET", "params": {"title": "title", "text": "text"}},
  "x_fixture_ratio": 1.50
}`

func manifestFixture(t testing.TB, manifest string) webbundle.Config {
	t.Helper()
	c := newFixture(t, files{
		"icon.svg":              seedIcon(t, "subflux.svg"),
		"app/manifest.json":     manifest,
		"app/icons/library.png": solidPNG(t, 96),
		"shots/wide.webp":       "webp bytes",
		"notes.txt":             "notes",
	})
	c.Icon, c.Manifest = "icon.svg", "app/manifest.json"
	return c
}

// jsonAt walks v by object keys and array indices.
func jsonAt(t testing.TB, v any, steps ...any) any {
	t.Helper()
	for i, step := range steps {
		switch k := step.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("at %v: %v is not an object", steps[:i], v)
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("at %v: %v is not an array holding index %d", steps[:i], v, k)
			}
			v = a[k]
		}
	}
	return v
}

func builtManifest(t testing.TB, out string) map[string]any {
	t.Helper()
	manifests := listed(readBundle(t, out), regexp.MustCompile(`^assets/[^/]+\.webmanifest$`))
	if len(manifests) != 1 {
		t.Fatalf("listed manifests %q, want one", manifests)
	}
	m, ok := decodeJSON(t, readFile(t, out, manifests[0])).(map[string]any)
	if !ok {
		t.Fatalf("%s is not a JSON object", manifests[0])
	}
	return m
}

func TestManifestPassesMembersThrough(t *testing.T) {
	t.Parallel()
	c := manifestFixture(t, fixtureManifest)
	mustBuild(t, c)
	b := readBundle(t, c.Out)
	got := builtManifest(t, c.Out)

	obj := func(steps ...any) map[string]any {
		t.Helper()
		m, ok := jsonAt(t, got, steps...).(map[string]any)
		if !ok {
			t.Fatalf("the built manifest at %v is not an object", steps)
		}
		return m
	}
	for _, s := range []struct {
		in   map[string]any
		from string
		file string
	}{
		{in: obj("shortcuts", 0, "icons", 0), from: "icons/library.png", file: "app/icons/library.png"},
		{in: obj("shortcuts", 1, "icons", 0), from: "./icons/library.png", file: "app/icons/library.png"},
		{in: obj("screenshots", 0), from: "../shots/wide.webp", file: "shots/wide.webp"},
	} {
		served, _ := s.in["src"].(string)
		name := strings.TrimPrefix(served, "/")
		if !strings.HasPrefix(served, "/") || !hashedName(s.file[strings.LastIndex(s.file, "/")+1:]).MatchString(name) {
			t.Errorf("src %q is %q, want the served URL of %s as a hashed asset", s.from, served, s.file)
		} else if _, ok := b.Files[name]; !ok {
			t.Errorf("src %q is %q, which bundle.json does not list", s.from, served)
		} else if !bytes.Equal(readFile(t, c.Out, name), readFile(t, c.Src, s.file)) {
			t.Errorf("src %q is %q, which does not hold %s's bytes", s.from, served, s.file)
		}
		s.in["src"] = s.from
	}
	if _, ok := got["icons"]; !ok {
		t.Error("the manifest has no icons")
	}
	delete(got, "icons")
	if want := decodeJSON(t, []byte(fixtureManifest)); !reflect.DeepEqual(any(got), want) {
		t.Errorf("the built manifest, src members and icons aside, is\n%v\nwant the source's\n%v", got, want)
	}
}

func TestManifestSharedSrcIsOneFile(t *testing.T) {
	t.Parallel()
	c := manifestFixture(t, fixtureManifest)
	mustBuild(t, c)
	if got := listed(readBundle(t, c.Out), hashedName("library.png")); len(got) != 1 {
		t.Errorf("listed %q for the two shortcuts' one src file, want one", got)
	}
}

func TestManifestScope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, manifest string
	}{
		{name: "IDOutsideScope", manifest: `{"name": "A", "id": "/other", "scope": "/app/", "start_url": "/app/"}`},
		{name: "DefaultScopeFromStartURL", manifest: `{"name": "A", "start_url": "/app/home?x=1", "shortcuts": [{"name": "s", "url": "/app/s"}]}`},
		{name: "ScopeQueryIgnored", manifest: `{"name": "A", "scope": "/app/?v=1", "start_url": "/app/", "share_target": {"action": "/app/share"}}`},
		{name: "NoScopeNorStartURL", manifest: `{"name": "A", "shortcuts": [{"name": "s", "url": "/s"}]}`},
		{name: "ScopePrefixNotSegment", manifest: `{"name": "A", "scope": "/app", "start_url": "/apps"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mustBuild(t, manifestFixture(t, tc.manifest))
		})
	}
}

func TestManifestRefuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, manifest string
		want           []string
	}{
		{name: "NotJSON", manifest: `{"name": `, want: []string{"app/manifest.json", "JSON"}},
		{name: "NotObject", manifest: `["name"]`, want: []string{"app/manifest.json", "object"}},
		{name: "TwoValues", manifest: `{"name": "A"} {}`, want: []string{"app/manifest.json", "more than one"}},
		{name: "NoName", manifest: `{"short_name": "A"}`, want: []string{"name"}},
		{name: "BlankName", manifest: `{"name": " "}`, want: []string{"name"}},
		{name: "NameNotString", manifest: `{"name": 1}`, want: []string{"name"}},
		{name: "ThemeColorNotHex", manifest: `{"name": "A", "theme_color": "red"}`, want: []string{"theme_color", "red"}},
		{name: "BackgroundShortHex", manifest: `{"name": "A", "background_color": "#fff"}`, want: []string{"background_color", "#fff"}},
		{name: "ColourNotString", manifest: `{"name": "A", "theme_color": 3}`, want: []string{"theme_color"}},
		{name: "Icons", manifest: `{"name": "A", "icons": []}`, want: []string{"icons", "Config.Icon"}},
		{name: "StartURLAbsolute", manifest: `{"name": "A", "start_url": "https://example.com/"}`, want: []string{"start_url", "https://example.com/"}},
		{name: "StartURLOtherHost", manifest: `{"name": "A", "start_url": "//example.com/"}`, want: []string{"start_url", "//example.com/"}},
		{name: "StartURLBackslash", manifest: `{"name": "A", "start_url": "/\\example.com/"}`, want: []string{"start_url"}},
		{name: "StartURLRelative", manifest: `{"name": "A", "start_url": "library"}`, want: []string{"start_url", "library"}},
		{name: "StartURLNotString", manifest: `{"name": "A", "start_url": ["/"]}`, want: []string{"start_url"}},
		{name: "ScopeRelative", manifest: `{"name": "A", "scope": "app/"}`, want: []string{"scope", "app/"}},
		{name: "IDAbsolute", manifest: `{"name": "A", "id": "https://example.com/app"}`, want: []string{"id", "https://example.com/app"}},
		{name: "StartURLOutsideScope", manifest: `{"name": "A", "scope": "/app/", "start_url": "/admin"}`, want: []string{"start_url", "/admin", "/app/"}},
		{name: "StartURLDotSegment", manifest: `{"name": "A", "scope": "/app/", "start_url": "/app/../admin"}`, want: []string{"start_url", "dot segment"}},
		{name: "StartURLEncodedDotSegment", manifest: `{"name": "A", "scope": "/app/", "start_url": "/app/%2E%2e/admin"}`, want: []string{"start_url", "dot segment"}},
		{name: "ShortcutOutsideScope", manifest: `{"name": "A", "scope": "/app/", "start_url": "/app/", "shortcuts": [{"name": "s", "url": "/admin"}]}`, want: []string{"shortcuts[0].url", "/admin", "/app/"}},
		{name: "ShortcutAbsolute", manifest: `{"name": "A", "shortcuts": [{"name": "s", "url": "https://example.com/"}]}`, want: []string{"shortcuts[0].url", "https://example.com/"}},
		{name: "ShortcutNoURL", manifest: `{"name": "A", "shortcuts": [{"name": "s", "url": "/s"}, {"name": "t"}]}`, want: []string{"shortcuts[1].url", "missing"}},
		{name: "ShortcutsNotArray", manifest: `{"name": "A", "shortcuts": {}}`, want: []string{"shortcuts", "array"}},
		{name: "ShortcutNotObject", manifest: `{"name": "A", "shortcuts": ["/s"]}`, want: []string{"shortcuts[0]", "object"}},
		{name: "ShareTargetOutsideScope", manifest: `{"name": "A", "scope": "/app/", "start_url": "/app/", "share_target": {"action": "/admin"}}`, want: []string{"share_target.action", "/admin"}},
		{name: "ShareTargetNoAction", manifest: `{"name": "A", "share_target": {"method": "GET"}}`, want: []string{"share_target.action", "missing"}},
		{name: "ShareTargetNotObject", manifest: `{"name": "A", "share_target": "/share"}`, want: []string{"share_target", "object"}},
		{name: "DefaultScopeFromStartURL", manifest: `{"name": "A", "start_url": "/app/home", "shortcuts": [{"name": "s", "url": "/other"}]}`, want: []string{"/other", "/app/"}},
		{name: "SrcRootRelative", manifest: `{"name": "A", "screenshots": [{"src": "/icons/library.png"}]}`, want: []string{"src", "/icons/library.png"}},
		{name: "SrcURL", manifest: `{"name": "A", "screenshots": [{"src": "https://example.com/a.png"}]}`, want: []string{"src", "https://example.com/a.png"}},
		{name: "SrcQuery", manifest: `{"name": "A", "screenshots": [{"src": "icons/library.png?v=1"}]}`, want: []string{"src", "icons/library.png?v=1"}},
		{name: "SrcBackslash", manifest: `{"name": "A", "screenshots": [{"src": "icons\\library.png"}]}`, want: []string{"src"}},
		{name: "SrcMissing", manifest: `{"name": "A", "screenshots": [{"src": "icons/none.png"}]}`, want: []string{"app/icons/none.png"}},
		{name: "SrcOutsideSrc", manifest: `{"name": "A", "screenshots": [{"src": "../../x.png"}]}`, want: []string{"../x.png"}},
		{name: "SrcNotImage", manifest: `{"name": "A", "screenshots": [{"src": "../notes.txt"}]}`, want: []string{"src", "../notes.txt", "image"}},
		{name: "SrcNotString", manifest: `{"name": "A", "screenshots": [{"src": 1}]}`, want: []string{"src", "1"}},
		{name: "SrcEmpty", manifest: `{"name": "A", "x_fixture": {"deep": [{"src": ""}]}}`, want: []string{"src", `""`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := manifestFixture(t, tc.manifest)
			buildRefused(t, c, tc.want...)
			outMissing(t, c.Out)
		})
	}
	t.Run("FileMissing", func(t *testing.T) {
		t.Parallel()
		c := manifestFixture(t, fixtureManifest)
		c.Manifest = "none.json"
		buildRefused(t, c, "none.json")
	})
	t.Run("WithoutIcon", func(t *testing.T) {
		t.Parallel()
		c := manifestFixture(t, fixtureManifest)
		c.Icon = ""
		buildRefused(t, c, "Manifest", "Icon")
	})
}
