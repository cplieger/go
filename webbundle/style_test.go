package webbundle_test

import (
	"bytes"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/webbundle"
)

const fixtureMark = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#3372bf"/></svg>`

// styleFiles is a stylesheet importing another and naming a font and an
// image by url().
func styleFiles() files {
	return files{
		"src/app.css": `@import "./parts/base.css";
@font-face { font-family: Fixture; src: url(./fonts/fixture.woff2) format("woff2"); }
.fixture-mark { background: url(./img/mark.svg) no-repeat; }
`,
		"src/parts/base.css":      ".fixture-base { font-family: Fixture, sans-serif; }\n",
		"src/fonts/fixture.woff2": "wOF2 fixture font bytes",
		"src/img/mark.svg":        fixtureMark,
	}
}

func TestStyleURLAssetsHashed(t *testing.T) {
	t.Parallel()
	t.Run("FontAndImage", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, styleFiles())
		mustBuild(t, c)
		b := readBundle(t, c.Out)
		css := readFile(t, c.Out, entryPath(t, b, "app", webbundle.Style))
		for _, src := range []string{"src/fonts/fixture.woff2", "src/img/mark.svg"} {
			got := listed(b, hashedName(path.Base(src)))
			if len(got) != 1 {
				t.Errorf("%s: listed %q, want one hashed file; every listed file: %q", src, got, slices.Sorted(maps.Keys(b.Files)))
				continue
			}
			if !bytes.Equal(readFile(t, c.Out, got[0]), readFile(t, c.Src, src)) {
				t.Errorf("%s does not hold %s's bytes", got[0], src)
			}
			if !bytes.Contains(css, []byte(path.Base(got[0]))) {
				t.Errorf("the style entry does not name %s:\n%s", got[0], css)
			}
		}
		if bytes.Contains(css, []byte("data:")) {
			t.Errorf("the style entry inlines a file as a data URL:\n%s", css)
		}
		if !bytes.Contains(css, []byte("fixture-base")) {
			t.Errorf("the style entry does not hold its @import:\n%s", css)
		}
	})
	t.Run("ExtensionOutsideTable", func(t *testing.T) {
		t.Parallel()
		c := newFixture(t, files{
			"src/app.css":       "@font-face { font-family: Old; src: url(./fonts/old.ttf); }\n",
			"src/fonts/old.ttf": "ttf bytes",
		})
		buildRefused(t, c, "old.ttf")
	})
}

func TestBuildStyleEqualsStyleEntryFile(t *testing.T) {
	t.Parallel()
	c := newFixture(t, styleFiles())
	mustBuild(t, c)
	built := readFile(t, c.Out, entryPath(t, readBundle(t, c.Out), "app", webbundle.Style))
	src, out := readTree(t, c.Src), readTree(t, c.Out)

	got, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: c.Src, Path: "src/app.css"})
	if err != nil {
		t.Fatalf("BuildStyle: %v", err)
	}
	if !bytes.Equal(got, built) {
		t.Errorf("BuildStyle gave\n%s\nthe Style entry's file holds\n%s", got, built)
	}
	if after := readTree(t, c.Src); !maps.EqualFunc(src, after, bytes.Equal) {
		t.Errorf("BuildStyle changed Src: %q", slices.Sorted(maps.Keys(after)))
	}
	if after := readTree(t, c.Out); !maps.EqualFunc(out, after, bytes.Equal) {
		t.Errorf("BuildStyle changed Out: %q", slices.Sorted(maps.Keys(after)))
	}
}

func TestBuildStyleRefuses(t *testing.T) {
	t.Parallel()
	c := newFixture(t, styleFiles())
	cases := []struct{ name, path string }{
		{name: "OutsideSrc", path: "../app.css"},
		{name: "Absolute", path: "/src/app.css"},
		{name: "NotCSS", path: "src/app.ts"},
		{name: "Missing", path: "src/gone.css"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: c.Src, Path: tc.path})
			switch {
			case err == nil:
				t.Errorf("BuildStyle(%q) = %q, want an error", tc.path, got)
			case !strings.Contains(err.Error(), tc.path):
				t.Errorf("BuildStyle(%q) error %q does not name the path", tc.path, err)
			}
		})
	}
}
