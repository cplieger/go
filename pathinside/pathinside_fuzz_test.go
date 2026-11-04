package pathinside_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/pathinside"
)

// fuzzPaths is the adversarial seed corpus shared by the targets below: roots
// and targets that differ only in a name extension (the prefix-sibling case),
// traversals of every depth, names that merely begin with two dots, unclean
// spellings, absolute/relative mixes, the filesystem root, and the degenerate
// empty and dot forms. The committed seeds are the durable fuzz coverage — the
// weekly coverage-guided run starts from these every time.
var fuzzPaths = []string{
	"",
	".",
	"..",
	"/",
	"//",
	"/a/b",
	"/a/b/",
	"/a/b/c",
	"/a/b-evil",
	"/a/b-evil/c",
	"/a/bevil",
	"/a/b/..extras",
	"/a/b/..extras/movie.mkv",
	"/a/b/../../etc/passwd",
	"/a/x/../b",
	"/a/b/./c",
	"a/b",
	"a/b/c",
	"a/../b",
	"a/../..",
	"../a/b",
	"../../etc",
	"..extras",
	"...",
	".hidden",
	`..\evil`,
	"/a/b/\x00c",
	"/a/b/ c ",
	// The three rune pairs Unicode 17 newly case-folds (Go 1.27); Unicode 15
	// held each member distinct. They are ordinary names on Unix, where
	// filepath.Rel compares components byte-exactly, and fold-equal on Windows,
	// where it compares them with strings.EqualFold — so these are the inputs on
	// which the Rel-derived oracle below and a hand-rolled byte comparison would
	// disagree, which is exactly the regression the oracle exists to catch.
	// TestCaseIsThePlatformsRule states the contract they pin.
	"/a/\uFB05",
	"/a/\uFB06",
	"/a/\u0390",
	"/a/\u1FD3",
}

// FuzzInsideMatchesRelOracle drives Root.Contains with arbitrary path pairs and
// checks it against an independently-derived filepath.Rel oracle: the target is
// inside the root exactly when Rel succeeds and the FIRST SEGMENT of its result
// is not "..". The oracle splits the relative path into segments rather than
// testing a string prefix, so it is not a restatement of the implementation — a
// regression to the naive strings.HasPrefix(rel, "..") form disagrees with it on
// every name that merely begins with two dots ("..extras"), and a regression to
// a strings.HasPrefix(target, root) containment test disagrees on every
// prefix sibling ("/a/b-evil" under "/a/b").
//
// Every path reported inside additionally has to survive reconstruction: its
// relative form must be non-escaping, must not be absolute, and joining it back
// onto the root must reproduce the cleaned target. That is the property callers
// actually rely on — a path reported inside can be re-derived as root plus a
// downward path, so it cannot reach outside when it is joined.
//
// The target is purely lexical, so it touches no filesystem: nothing here builds
// a real path out of fuzz input.
func FuzzInsideMatchesRelOracle(f *testing.F) {
	for _, root := range fuzzPaths {
		for _, target := range fuzzPaths {
			f.Add(root, target)
		}
	}
	f.Fuzz(func(t *testing.T, root, target string) {
		if root == "" {
			// The empty root is the one deliberate divergence from the
			// filepath.Rel oracle: Root("") fails closed and contains nothing,
			// where Rel would clean "" to "." (see the Root doc). Pinned by
			// TestRootZeroValue; the oracle governs every non-empty root.
			if pathinside.Root(root).Contains(target) {
				t.Fatalf(`Root("").Contains(%q) = true, want false: the empty root must contain nothing`, target)
			}
			return
		}
		got := pathinside.Root(root).Contains(target)

		rel, err := filepath.Rel(root, target)
		if err != nil {
			if got {
				t.Fatalf("Root(%q).Contains(%q) = true, but the pair is not lexically comparable: %v", root, target, err)
			}
			return
		}

		first, _, _ := strings.Cut(rel, string(filepath.Separator))
		if want := first != ".."; got != want {
			t.Fatalf("Root(%q).Contains(%q) = %v, oracle (first segment of rel %q) = %v", root, target, got, rel, want)
		}
		if !got {
			return
		}
		if pathinside.RelEscapes(rel) {
			t.Fatalf("Root(%q).Contains(%q) = true but RelEscapes(%q) = true", root, target, rel)
		}
		if filepath.IsAbs(rel) {
			t.Fatalf("Root(%q).Contains(%q) = true but the relative form %q is absolute", root, target, rel)
		}
		if joined, want := filepath.Join(root, rel), filepath.Clean(target); joined != want {
			t.Fatalf("Root(%q).Contains(%q) = true but Join(root, %q) = %q, want %q", root, target, rel, joined, want)
		}
	})
}

// FuzzInsideRejectsPrefixSibling pins the security invariant the separator
// buys, over arbitrary inputs: EXTENDING a root's final name segment never
// produces a path inside that root. Whatever the root is and whatever is
// appended to it, as long as the appended text starts no new segment, the
// result is a sibling — "/srv/data" + "-evil" is not inside "/srv/data" — which
// is precisely the case a strings.HasPrefix containment test accepts.
//
// Roots with no final segment to extend are skipped, because appending to them
// creates a CHILD rather than a sibling: appending "evil" to "/" is "/evil" and
// to "." is ".evil", both genuinely inside.
func FuzzInsideRejectsPrefixSibling(f *testing.F) {
	for _, root := range fuzzPaths {
		for _, suffix := range []string{"-evil", "evil", ".", "..", "\x00", " ", "-", "..extras"} {
			f.Add(root, suffix)
		}
	}
	f.Fuzz(func(t *testing.T, root, suffix string) {
		root = filepath.Clean(root)
		if root == "." || isFilesystemRoot(root) {
			return // no final segment to extend
		}
		if suffix == "" || strings.ContainsAny(suffix, `/\`) {
			return // an appended separator starts a new segment: that is a child, not a sibling
		}
		if pathinside.Root(root).Contains(root + suffix) {
			t.Fatalf("Root(%q).Contains(%q) = true: extending the root's own name must not be inside it", root, root+suffix)
		}
	})
}

// FuzzRelEscapesGovernsJoin pins the safety direction between the two
// containment predicates: a RELATIVE name RelEscapes accepts always lands
// INSIDE the root it is joined onto. That is the contract a caller leans on
// when it validates a name first and joins it afterwards.
//
// The converse is deliberately NOT asserted, because it is false —
// TestNameValidationIsStricterThanContainment pins the counterexample ("../a"
// under root "a"): the two predicates answer different questions, one about
// the shape of a NAME, one about the location of a RESULT.
//
// The accepted direction is restricted to non-absolute names: an ABSOLUTE name
// is not judged by RelEscapes at all, and joining one onto a relative root can
// move ABOVE that root (see [pathinside.RelEscapes]'s doc for the Clean/Join
// asymmetry this exploits). A caller validating an untrusted name must reject
// absoluteness itself.
func FuzzRelEscapesGovernsJoin(f *testing.F) {
	for _, root := range fuzzPaths {
		for _, rel := range fuzzPaths {
			f.Add(root, rel)
		}
	}
	f.Add("data", "/..")   // absolute name whose traversal is clamped by Clean, then re-attached by Join
	f.Add("data", "/../x") // the same shape with a name after the traversal
	f.Add("a", "../a")     // leaves the root and returns to it: refused by name, inside by result
	f.Add("a", "../a/b")   // the same shape, landing on a descendant
	f.Fuzz(func(t *testing.T, root, rel string) {
		escapes := pathinside.RelEscapes(rel)
		if cleaned := pathinside.RelEscapes(filepath.Clean(rel)); cleaned != escapes {
			t.Fatalf("RelEscapes(%q) = %v but RelEscapes(Clean(%q)) = %v: cleaning must be idempotent", rel, escapes, rel, cleaned)
		}
		if escapes || filepath.IsAbs(rel) {
			return
		}

		if root == "" {
			// Root("") contains nothing by contract, so the join invariant is
			// asserted only for non-empty roots (TestRootZeroValue owns "").
			return
		}
		joined := filepath.Join(root, rel)
		if filepath.IsAbs(joined) != filepath.IsAbs(filepath.Clean(root)) {
			// filepath.Join drops an empty element, so joining an absolute name
			// onto the empty root yields the name itself. The result is no longer
			// a path under that root, so the invariant is not about it.
			return
		}
		if !pathinside.Root(root).Contains(joined) {
			t.Fatalf("RelEscapes(%q) = false but Join(%q, rel) = %q is not inside the root", rel, root, joined)
		}
	})
}

// isFilesystemRoot reports whether p is its own parent — "/" on Unix, a volume
// root on Windows. Such a path both has no final segment to extend and clamps
// every traversal, so the two fuzz targets above exclude it from the invariants
// those properties break.
func isFilesystemRoot(p string) bool {
	return filepath.Dir(p) == p
}

// FuzzHasDotDotSurvivesCleaning pins the invariant that makes HasDotDot safe to
// pair with normalization: if a path was NOT written with a ".." component,
// cleaning it cannot produce one, because filepath.Clean only drops and merges
// components, never invents them. The unit tables prove the converse — that a
// traversal cleaning would REMOVE is still reported.
//
// Each input is also checked against an INDEPENDENT component oracle: a manual
// byte scan on os.IsPathSeparator rather than strings.SplitSeq(filepath.ToSlash(p),
// "/"). The two agree on both platforms, so this is a genuine second opinion:
// it fails on strings.Contains(p, "..") (wrong on "key..v2"), a split on both
// separator characters (wrong on a Unix filename containing a backslash), and
// a version that cleaned p first (the fusion this package exists to prevent).
//
// Seeds include the axis-disagreement rows from TestAxesDisagreeOnUncleanInput,
// the inputs a fusion regression would break first.
//
// Purely lexical: nothing here builds a real path out of fuzz input.
func FuzzHasDotDotSurvivesCleaning(f *testing.F) {
	for _, p := range fuzzPaths {
		f.Add(p)
	}
	f.Add("/run/secrets/../../etc/shadow") // axis disagreement: RelEscapes false, HasDotDot true
	f.Add("/dumps/../etc")                 // axis disagreement, backup-destination shape
	f.Add("a/../b")                        // axis disagreement, relative shape
	f.Add("/dumps/nightly/..")             // traversal as the last component
	f.Add("key..v2")                       // two adjacent dots inside a name
	f.Add("/dumps/a..b")                   // the same, as a path component
	f.Add("a//..//b")                      // doubled separators around a traversal
	f.Add("./../dumps")                    // a dot component beside a traversal
	f.Add(`a\..\b`)                        // one component on Unix, three on Windows
	f.Fuzz(func(t *testing.T, p string) {
		got := pathinside.HasDotDot(p)

		if want := hasDotDotOracle(p); got != want {
			t.Fatalf("HasDotDot(%q) = %v, independent component oracle = %v", p, got, want)
		}

		cleaned := filepath.Clean(p)
		if !got {
			if hasDotDotOracle(cleaned) {
				t.Fatalf("HasDotDot(%q) = false but Clean(%q) = %q holds a %q component: cleaning must not introduce traversal that was not written", p, p, cleaned, "..")
			}
			if pathinside.HasDotDot(cleaned) {
				t.Fatalf("HasDotDot(%q) = false but HasDotDot(Clean(%q) = %q) = true", p, p, cleaned)
			}
		}
		if !pathinside.IsCanonical(cleaned) {
			t.Fatalf("IsCanonical(Clean(%q) = %q) = false: a cleaned path is canonical by definition", p, cleaned)
		}
	})
}

// hasDotDotOracle reports whether p holds a ".." component, derived
// independently of the implementation: a manual scan that ends a component at
// every byte os.IsPathSeparator accepts, rather than a split of
// filepath.ToSlash(p) on "/". os.IsPathSeparator accepts "\" only on Windows, so
// the oracle carries the same platform rule by a different route.
func hasDotDotOracle(p string) bool {
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || os.IsPathSeparator(p[i]) {
			if p[start:i] == ".." {
				return true
			}
			start = i + 1
		}
	}
	return false
}
