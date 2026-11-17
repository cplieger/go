package webbundle_test

import (
	"slices"
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
