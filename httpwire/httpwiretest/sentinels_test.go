package httpwiretest_test

import (
	"errors"
	"go/build"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/httpwire/httpwiretest"
)

// sentinelsIn runs Sentinels in a package directory holding files.
func sentinelsIn(t *testing.T, files map[string]string, owner *httpwire.Owner) *recorder {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.go"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return record(t, func(tb testing.TB) { httpwiretest.Sentinels(tb, owner) })
}

var errA, errB, errC = errors.New("a"), errors.New("b"), errors.New("c")

// threeSentinels declares ErrA, ErrB and ErrC, beside names that are no
// exported Err* var of this build.
var threeSentinels = map[string]string{
	"pkg.go": `package pkg

import "errors"

var (
	ErrA      = errors.New("a")
	ErrB      = errors.New("b")
	errHidden = errors.New("hidden")
)

const ErrConst = "not a var"

func ErrFunc() error { return nil }
`,
	"pkg2.go":      "package pkg\n\nimport \"errors\"\n\nvar ErrC, Other = errors.New(\"c\"), 1\n",
	"pkg_test.go":  "package pkg\n\nvar ErrInTest = 1\n",
	"ignored.go":   "//go:build ignore\n\npackage main\n\nvar ErrIgnored = 1\n",
	"README.md":    "var ErrInText = 1\n",
	"pkg_other.go": "//go:build never\n\npackage pkg\n\nvar ErrOtherBuild = 1\n",
}

// cgoFiles add a cgo file declaring ErrCgo, a sentinel only of a cgo build.
func cgoFiles() map[string]string {
	files := maps.Clone(threeSentinels)
	files["cgo.go"] = "package pkg\n\nimport \"C\"\n\nvar ErrCgo = 1\n"
	return files
}

// cgoOwner places ErrCgo outside when this build has cgo, and leaves it out
// of the owner when not.
func cgoOwner(codes []httpwire.OwnerCode, all map[string]error) *httpwire.Owner {
	sentinels, out := maps.Clone(all), map[string]string{"ErrC": "x"}
	if build.Default.CgoEnabled {
		sentinels["ErrCgo"], out["ErrCgo"] = errors.New("cgo"), "x"
	}
	return owner(codes, sentinels, out)
}

func owner(codes []httpwire.OwnerCode, sentinels map[string]error, outside map[string]string) *httpwire.Owner {
	return &httpwire.Owner{Name: "pkg", Codes: codes, Sentinels: sentinels, Outside: outside}
}

func TestSentinels(t *testing.T) {
	all := map[string]error{"ErrA": errA, "ErrB": errB, "ErrC": errC}
	codes := []httpwire.OwnerCode{{Err: errA, Code: "a_code"}, {Err: errB, Code: "b_code"}}
	out := map[string]string{"ErrC": "handled inside the package"}
	cases := map[string]struct {
		owner *httpwire.Owner
		files map[string]string
		want  []string
		fatal bool
	}{
		"every sentinel placed":                {owner: owner(codes, all, out)},
		"two sentinels on one code":            {owner: owner([]httpwire.OwnerCode{{Err: errA, Code: "x"}, {Err: errB, Code: "x"}}, all, out)},
		"a declared sentinel it lacks":         {owner: owner(codes, map[string]error{"ErrA": errA, "ErrB": errB}, nil), want: []string{"ErrC", "declares"}},
		"a sentinel the package lacks":         {owner: owner(codes, map[string]error{"ErrA": errA, "ErrB": errB, "ErrC": errC, "ErrD": errors.New("d")}, map[string]string{"ErrC": "x", "ErrD": "x"}), want: []string{"ErrD", "does not declare"}},
		"a sentinel neither coded nor outside": {owner: owner(codes, all, nil), want: []string{"ErrC", "neither"}},
		"a sentinel coded and outside":         {owner: owner(codes, all, map[string]string{"ErrA": "x", "ErrC": "x"}), want: []string{"ErrA", "both"}},
		"one sentinel under two codes":         {owner: owner(append(codes, httpwire.OwnerCode{Err: errA, Code: "other"}), all, out), want: []string{"ErrA", "twice"}},
		"a code for no sentinel":               {owner: owner(append(codes, httpwire.OwnerCode{Err: errors.New("z"), Code: "z"}), all, out), want: []string{"not one of its Sentinels"}},
		"a code with a nil Err":                {owner: owner(append(codes, httpwire.OwnerCode{Code: "z"}), all, out), want: []string{"nil Err"}},
		"a code that is empty":                 {owner: owner([]httpwire.OwnerCode{{Err: errA}, {Err: errB, Code: "b"}}, all, out), want: []string{"ErrA", "empty code"}},
		"an Outside name of no sentinel":       {owner: owner(codes, all, map[string]string{"ErrC": "x", "ErrZ": "x"}), want: []string{"ErrZ"}},
		"an Outside with no reason":            {owner: owner(codes, all, map[string]string{"ErrC": " "}), want: []string{"ErrC", "reason"}},
		"a nil sentinel":                       {owner: owner(codes, map[string]error{"ErrA": errA, "ErrB": errB, "ErrC": nil}, out), want: []string{"ErrC", "nil"}},
		"a nil owner":                          {fatal: true, want: []string{"nil"}},
		"no sentinel declared":                 {owner: owner(nil, nil, nil), files: map[string]string{"pkg.go": "package pkg\n\nvar errHidden = 1\n"}, fatal: true, want: []string{"no exported Err"}},
		"a file that does not parse":           {owner: owner(codes, all, out), files: map[string]string{"pkg.go": threeSentinels["pkg.go"], "bad.go": "package pkg\n\nvar (\n"}, fatal: true, want: []string{"bad.go"}},
		"a cgo file of this build":             {owner: cgoOwner(codes, all), files: cgoFiles()},
		"no Go file":                           {owner: owner(codes, all, out), files: map[string]string{"README.md": "x"}, fatal: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			files := tc.files
			if files == nil {
				files = threeSentinels
			}
			r := sentinelsIn(t, files, tc.owner)
			if r.fatal != tc.fatal {
				t.Errorf("fatal %v; want %v (failures %q)", r.fatal, tc.fatal, r.failures)
			}
			switch {
			case tc.want == nil && !tc.fatal && len(r.failures) != 0:
				t.Errorf("failures %q; want none", r.failures)
			case tc.want != nil && !r.failed(tc.want...):
				t.Errorf("failures %q; want one with %q", r.failures, tc.want)
			case tc.fatal && len(r.failures) == 0:
				t.Error("no failure; want a fatal one")
			}
		})
	}
}
