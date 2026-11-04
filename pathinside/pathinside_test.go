package pathinside_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cplieger/go/pathinside"
)

// fs turns a slash-written path into this platform's spelling, so the tables
// below read as paths while still exercising the real separator.
func fs(p string) string {
	return filepath.FromSlash(p)
}

// TestInside pins the containment contract documented on [pathinside.Root.Contains].
func TestInside(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		target string
		want   bool
	}{
		{"root itself", fs("/a/b"), fs("/a/b"), true},
		{"root itself, unclean target", fs("/a/b"), fs("/a/b/"), true},
		{"root itself, unclean root", fs("/a/b/"), fs("/a/b"), true},
		{"root itself via a no-op traversal", fs("/a/b"), fs("/a/x/../b"), true},
		{"direct child", fs("/a/b"), fs("/a/b/c"), true},
		{"deep descendant", fs("/a/b"), fs("/a/b/c/d/e.txt"), true},
		{"descendant through a cleaned traversal", fs("/a/b"), fs("/a/b/c/../d"), true},
		{"prefix sibling", fs("/a/b"), fs("/a/b-evil"), false},
		{"prefix sibling, deeper", fs("/a/b"), fs("/a/b-evil/c"), false},
		{"prefix sibling with no separator at all", fs("/a/b"), fs("/a/bevil"), false},
		{"parent", fs("/a/b"), fs("/a"), false},
		{"sibling", fs("/a/b"), fs("/a/c"), false},
		{"traversal out of the root", fs("/a/b"), fs("/a/b/../../etc/passwd"), false},
		{"traversal to the filesystem root", fs("/a/b"), "/", false},
		{"leading double-dot name is not a traversal", fs("/a/b"), fs("/a/b/..extras/movie.mkv"), true},
		{"double-dot name as the whole segment", fs("/a/b"), fs("/a/b/..extras"), true},
		{"three dots is a name", fs("/a/b"), fs("/a/b/.../x"), true},
		{"dotfile child", fs("/a/b"), fs("/a/b/.hidden"), true},
		{"absolute target, relative root", "b", fs("/a/b/c"), false},
		{"relative target, absolute root", fs("/a/b"), fs("c/d"), false},
		{"both relative, descendant", fs("a/b"), fs("a/b/c"), true},
		{"both relative, root itself", fs("a/b"), fs("a/b"), true},
		{"both relative, escape", fs("a/b"), fs("a/c"), false},
		{"both relative, up and back down", fs("a/b"), fs("a/b/../b/c"), true},
		{"dot root, plain child", ".", "c", true},
		{"dot root, parent", ".", "..", false},
		{"dot root, dotfile", ".", fs(".hidden"), true},
		{"filesystem root contains everything absolute", "/", fs("/etc/passwd"), true},
		{"filesystem root contains itself", "/", "/", true},
		{"empty root contains nothing (fail closed)", "", "c", false},
		{"empty target is the root itself", ".", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathinside.Root(tt.root).Contains(tt.target); got != tt.want {
				t.Errorf("Root(%q).Contains(%q) = %v, want %v", tt.root, tt.target, got, tt.want)
			}
		})
	}
}

// TestRootZeroValue pins the zero-value contract stated on the [pathinside.Root] type.
func TestRootZeroValue(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"c", fs("a/b"), ".", "", fs("../x"), "/abs"} {
		if got := pathinside.Root("").Contains(target); got {
			t.Errorf(`Root("").Contains(%q) = true, want false: the empty root must contain nothing`, target)
		}
	}
	if !pathinside.Root(".").Contains("c") {
		t.Error(`Root(".").Contains("c") = false, want true: the explicit cwd spelling must keep working`)
	}
}

// TestRelEscapes pins the contract documented on [pathinside.RelEscapes].
func TestRelEscapes(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		want bool
	}{
		{"exact parent", "..", true},
		{"parent with a child", fs("../x"), true},
		{"two levels up", fs("../../etc/passwd"), true},
		{"uncleaned traversal that ends up above the root", fs("a/../.."), true},
		{"uncleaned traversal that lands beside the root", fs("a/../../b"), true},
		{"trailing traversal that stays inside", fs("a/b/../c"), false},
		{"plain name", "x", false},
		{"plain nested name", fs("a/b/c.txt"), false},
		{"name beginning with two dots", fs("..extras/movie.mkv"), false},
		{"two dots as a whole segment name", "..extras", false},
		{"three dots", "...", false},
		{"three dots with a child", fs(".../x"), false},
		{"dotfile", ".hidden", false},
		{"current directory", ".", false},
		{"empty name cleans to the current directory", "", false},
		{"traversal buried below a segment stays inside", fs("a/../b"), false},
		{"absolute path does not escape by this test", fs("/etc/passwd"), false},
		{"absolute root does not escape by this test", "/", false},
		{"absolute traversal is clamped by cleaning, so it does not escape either", fs("/.."), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathinside.RelEscapes(tt.rel); got != tt.want {
				t.Errorf("RelEscapes(%q) = %v, want %v", tt.rel, got, tt.want)
			}
		})
	}
}

// TestSeparatorPrecision states the two decisions the separator buys, in the
// terms an adopting caller reasons about: the sibling a naive prefix test
// accepts is refused, and the legitimate two-dot NAME a naive ".." prefix test
// refuses is accepted. Written against the real separator rather than a
// hardcoded slash so the rule is pinned on whatever platform runs it.
func TestSeparatorPrecision(t *testing.T) {
	sep := string(filepath.Separator)
	root := pathinside.Root(sep + "srv" + sep + "data")

	if root.Contains(string(root) + "-evil") {
		t.Errorf("Root(%q).Contains(%q) = true, want false: a sibling whose name extends the root's is outside it", root, string(root)+"-evil")
	}
	if !root.Contains(string(root) + sep + "..extras") {
		t.Errorf("Root(%q).Contains(%q) = false, want true: a name beginning with two dots is a name, not a traversal", root, string(root)+sep+"..extras")
	}
	if !pathinside.RelEscapes(".." + sep + "escape") {
		t.Errorf("RelEscapes(%q) = false, want true", ".."+sep+"escape")
	}
	if pathinside.RelEscapes("..extras") {
		t.Errorf("RelEscapes(%q) = true, want false", "..extras")
	}
}

// TestNonSeparatorByteIsANameOnUnix pins the platform half of the separator
// rule: a byte that is a separator on another OS is an ordinary filename
// character here, so "..\\evil" is a name on Unix (no escape) and a traversal on
// Windows (escape). Splitting this out of the tables keeps them platform-neutral
// while still asserting the platform-dependent half.
func TestNonSeparatorByteIsANameOnUnix(t *testing.T) {
	got := pathinside.RelEscapes(`..\evil`)
	want := runtime.GOOS == "windows"
	if got != want {
		t.Errorf("RelEscapes(%q) = %v, want %v on %s", `..\evil`, got, want, runtime.GOOS)
	}
}

// TestNameValidationIsStricterThanContainment pins the asymmetry documented on
// [pathinside.RelEscapes] ("deliberately stricter than Root.Contains").
func TestNameValidationIsStricterThanContainment(t *testing.T) {
	root, rel := "a", fs("../a")
	if !pathinside.RelEscapes(rel) {
		t.Errorf("RelEscapes(%q) = false, want true: the name walks out of its root", rel)
	}
	if joined := filepath.Join(root, rel); !pathinside.Root(root).Contains(joined) {
		t.Errorf("Root(%q).Contains(%q) = false, want true: the joined result is the root itself", root, joined)
	}
}

// TestInsideAgreesWithRelEscapes pins the relationship the two containment
// predicates share: for any pair filepath.Rel can compare, Root.Contains is
// exactly "the relative path does not escape". A future change that tightens
// one half without the other breaks here.
func TestInsideAgreesWithRelEscapes(t *testing.T) {
	pairs := [][2]string{
		{fs("/a/b"), fs("/a/b")},
		{fs("/a/b"), fs("/a/b/c")},
		{fs("/a/b"), fs("/a/b-evil")},
		{fs("/a/b"), fs("/a")},
		{fs("/a/b"), fs("/a/b/..extras")},
		{fs("a/b"), fs("a/b/c")},
		{".", ".."},
	}
	for _, p := range pairs {
		root, target := p[0], p[1]
		rel, err := filepath.Rel(root, target)
		if err != nil {
			t.Fatalf("filepath.Rel(%q, %q) failed: %v", root, target, err)
		}
		if got, want := pathinside.Root(root).Contains(target), !pathinside.RelEscapes(rel); got != want {
			t.Errorf("Root(%q).Contains(%q) = %v, but RelEscapes(%q) = %v", root, target, got, rel, !want)
		}
	}
}

// TestHasDotDot pins the contract documented on [pathinside.HasDotDot].
func TestHasDotDot(t *testing.T) {
	tests := []struct {
		name string
		p    string
		want bool
	}{
		{"empty string has no components", "", false},
		{"exact parent", "..", true},
		{"parent with a child", fs("../dumps"), true},
		{"traversal buried mid-path", fs("a/../b"), true},
		{"traversal buried mid-path, absolute", fs("/dumps/../etc"), true},
		{"traversal that normalizes away entirely", fs("/run/secrets/../../etc/shadow"), true},
		{"traversal as the last component", fs("/dumps/nightly/.."), true},
		{"traversal as the last component, relative", fs("a/.."), true},
		{"traversal as the first component of an absolute path", fs("/../etc"), true},
		{"three dots is a name", "...", false},
		{"three dots with a child", fs(".../x"), false},
		{"two dots inside a name", "key..v2", false},
		{"two dots inside a name, absolute", fs("/dumps/a..b"), false},
		{"name beginning with two dots", fs("..extras/movie.mkv"), false},
		{"two dots as a whole segment name", "..extras", false},
		{"plain absolute path", fs("/run/secrets/pgpass"), false},
		{"plain relative path", fs("a/b/c.txt"), false},
		{"dotfile", ".hidden", false},
		{"current directory", ".", false},
		{"filesystem root", fs("/"), false},
		{"trailing separator after the traversal", fs("/dumps/../"), true},
		{"trailing separator, no traversal", fs("/dumps/nightly/"), false},
		{"doubled separators around the traversal", fs("a//..//b"), true},
		{"doubled separators, no traversal", fs("/dumps//nightly"), false},
		{"dot component beside a traversal", fs("./../dumps"), true},
		{"dot component mid-path, no traversal", fs("a/./b"), false},
		{"dot component with a trailing separator", fs("./"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathinside.HasDotDot(tt.p); got != tt.want {
				t.Errorf("HasDotDot(%q) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
}

// TestHasDotDotBackslashIsANameOnUnix pins the filepath.ToSlash contract, which
// is the platform half of the component rule and the reason the rule is worth
// centralizing. ToSlash rewrites "\" to "/" ONLY on Windows, so `a\..\b` is ONE
// component here — a legal Unix filename whose name merely contains dots, which
// must NOT read as traversal — while on Windows the same string is three
// components and the answer flips to true. Splitting this out of the table keeps
// the table platform-neutral while still asserting the platform-dependent half.
func TestHasDotDotBackslashIsANameOnUnix(t *testing.T) {
	got := pathinside.HasDotDot(`a\..\b`)
	want := runtime.GOOS == "windows"
	if got != want {
		t.Errorf("HasDotDot(%q) = %v, want %v on %s", `a\..\b`, got, want, runtime.GOOS)
	}
}

// TestCaseIsThePlatformsRule pins the case-fold contract documented on
// [pathinside.Root.Contains]. The three Unicode pairs are the ones Go 1.27
// changed (Unicode 17 folds U+FB05/U+FB06, U+0390/U+1FD3, U+03B0/U+1FE3); the
// ASCII row is the control, folding on Windows in every release.
func TestCaseIsThePlatformsRule(t *testing.T) {
	folded := runtime.GOOS == "windows"
	tests := []struct {
		name   string
		root   string
		target string
	}{
		{"ASCII case (folded on Windows in every release)", fs("/srv/Data"), fs("/srv/data/x")},
		{"U+FB05 against U+FB06 (newly folded in Unicode 17)", fs("/srv/\uFB05"), fs("/srv/\uFB06/x")},
		{"U+0390 against U+1FD3 (newly folded in Unicode 17)", fs("/srv/\u0390"), fs("/srv/\u1FD3/x")},
		{"U+03B0 against U+1FE3 (newly folded in Unicode 17)", fs("/srv/\u03B0"), fs("/srv/\u1FE3/x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathinside.Root(tt.root).Contains(tt.target); got != folded {
				t.Errorf("Root(%q).Contains(%q) = %v, want %v on %s", tt.root, tt.target, got, folded, runtime.GOOS)
			}
		})
	}
}

// TestHygieneIsFoldIndependent pins the other half of the case audit, and it
// holds on every platform: [pathinside.RelEscapes], [pathinside.HasDotDot] and
// [pathinside.IsCanonical] compare against the literal "..", whose runes have no
// case-fold partners at all, so no fold table can turn a name into a traversal
// or a traversal into a name. A rune that Unicode 17 newly folds is an ordinary
// filename byte sequence to all three, and a real traversal beside it is still
// found.
func TestHygieneIsFoldIndependent(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"\uFB05", "\uFB06", "\u0390", "\u1FD3", fs("/srv/\uFB05/movie.mkv")} {
		if pathinside.RelEscapes(p) {
			t.Errorf("RelEscapes(%q) = true, want false: a newly-folding rune is a name, not a traversal", p)
		}
		if pathinside.HasDotDot(p) {
			t.Errorf("HasDotDot(%q) = true, want false: a newly-folding rune is a name, not a traversal", p)
		}
		if !pathinside.IsCanonical(p) {
			t.Errorf("IsCanonical(%q) = false, want true: a newly-folding rune needs no cleaning", p)
		}
	}
	for _, p := range []string{fs("../\uFB05"), fs("/srv/\uFB06/../etc")} {
		if !pathinside.HasDotDot(p) {
			t.Errorf("HasDotDot(%q) = false, want true: a traversal beside a folding rune is still a traversal", p)
		}
	}
	if !pathinside.RelEscapes(fs("../\uFB05")) {
		t.Errorf("RelEscapes(%q) = false, want true", fs("../\uFB05"))
	}
}

// TestIsCanonical pins the contract documented on [pathinside.IsCanonical].
// The load-bearing rows are ".." and "../dumps": canonical yet traversing.
func TestIsCanonical(t *testing.T) {
	tests := []struct {
		name string
		p    string
		want bool
	}{
		{"cleaned absolute path", fs("/a/b"), true},
		{"doubled separator", fs("/a//b"), false},
		{"trailing separator", fs("a/b/"), false},
		{"dot component", fs("a/./b"), false},
		{"empty string cleans to the current directory", "", false},
		{"a leading traversal is canonical yet traverses", "..", true},
		{"a leading traversal with a child is canonical yet traverses", fs("../dumps"), true},
		{"two leading traversals are canonical", fs("../.."), true},
		{"a buried traversal is not canonical", fs("a/../b"), false},
		{"a buried traversal is not canonical, absolute", fs("/dumps/../etc"), false},
		{"a traversal clamped at the filesystem root is not canonical", fs("/.."), false},
		{"current directory", ".", true},
		{"filesystem root", fs("/"), true},
		{"plain name", "x", true},
		{"cleaned relative path", fs("a/b/c.txt"), true},
		{"name beginning with two dots", "..extras", true},
		{"three dots", "...", true},
		{"two dots inside a name", fs("/dumps/a..b"), true},
		{"dotfile", ".hidden", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathinside.IsCanonical(tt.p); got != tt.want {
				t.Errorf("IsCanonical(%q) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
}

// TestAxesDisagreeOnUncleanInput is the regression guard the two axes exist for.
// On UNCLEAN input they give OPPOSITE answers: RelEscapes cleans first, so a
// traversal that normalizes away reads false, while HasDotDot reads the path as
// written and reads true. So cleaning can hide a dangerous traversal:
// "/run/secrets/../../etc/shadow" cleans to "/etc/shadow", which escapes no
// root, and "/dumps/../etc" passes as a backup target. Cleaning also BOUNDS the disagreement, because Clean leaves ".." only
// at the front of a relative path, so on CANONICAL input the two axes AGREE.
func TestAxesDisagreeOnUncleanInput(t *testing.T) {
	unclean := []struct {
		name           string
		p              string
		wantRelEscapes bool
		wantHasDotDot  bool
	}{
		{"credential path whose traversal normalizes away", fs("/run/secrets/../../etc/shadow"), false, true},
		{"backup destination written with a traversal", fs("/dumps/../etc"), false, true},
		{"relative path whose traversal cancels out", fs("a/../b"), false, true},
	}
	for _, tt := range unclean {
		t.Run(tt.name, func(t *testing.T) {
			if pathinside.IsCanonical(tt.p) {
				t.Fatalf("IsCanonical(%q) = true, want false: the disagreement is about UNCLEAN input", tt.p)
			}
			if got := pathinside.RelEscapes(tt.p); got != tt.wantRelEscapes {
				t.Errorf("RelEscapes(%q) = %v, want %v", tt.p, got, tt.wantRelEscapes)
			}
			if got := pathinside.HasDotDot(tt.p); got != tt.wantHasDotDot {
				t.Errorf("HasDotDot(%q) = %v, want %v", tt.p, got, tt.wantHasDotDot)
			}
		})
	}

	for _, p := range []string{"..", fs("../dumps")} {
		t.Run("canonical input agrees: "+p, func(t *testing.T) {
			if !pathinside.IsCanonical(p) {
				t.Fatalf("IsCanonical(%q) = false, want true: this case is about CANONICAL input", p)
			}
			if !pathinside.RelEscapes(p) {
				t.Errorf("RelEscapes(%q) = false, want true", p)
			}
			if !pathinside.HasDotDot(p) {
				t.Errorf("HasDotDot(%q) = false, want true", p)
			}
		})
	}
}
