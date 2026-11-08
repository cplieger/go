package pathinside_test

import (
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/pathinside"
)

// Every fixture is built at package scope, outside the measured closures: a
// strings.Repeat or `+` inside an AllocsPerRun closure measures the fixture,
// not the function under test.

// Each rung is 16x the previous, so a walk that became quadratic reports ~256x
// instead of ~16x between rungs.
const (
	shallowDepth = 2
	mediumDepth  = 32
	deepDepth    = 512
)

// longComponentBytes separates a per-BYTE cost from a per-COMPONENT cost.
const longComponentBytes = 4096

// boolSink absorbs every predicate result so the compiler cannot discard the
// call being timed.
var boolSink bool

// The containment corpus. Fixtures go through fs (filepath.FromSlash, defined
// in pathinside_test.go) so the literals read as paths while the benchmark
// exercises the platform's real separator.
var (
	// benchRoot is written cleanly on purpose: TestRootContainsRootSpellingCosts
	// measures an uncleanly-written root's per-call cost.
	benchRoot = pathinside.Root(fs("/srv/data"))

	// benchDeepRoot is deep enough that the LAST-component refusal below has to
	// walk something before it can differ.
	benchDeepRoot = pathinside.Root(fs("/srv/data/" + strings.Repeat("c/", deepDepth) + "x"))

	// The accept path at each ladder rung: a real descendant, cleanly written.
	containedShallow = containedPath(shallowDepth)
	containedMedium  = containedPath(mediumDepth)
	containedDeep    = containedPath(deepDepth)

	// escapeFirstComponent diverges from benchRoot at the very first component,
	// so Rel's comparison loop breaks immediately.
	escapeFirstComponent = fs("/etc/" + strings.Repeat("c/", deepDepth) + "leaf.txt")

	// escapeLastComponent is a sibling of benchDeepRoot differing only in its
	// final component, so Rel walks every component before it can decide. Paired
	// with escapeFirstComponent at a deliberately similar total length.
	escapeLastComponent = fs("/srv/data/" + strings.Repeat("c/", deepDepth) + "y")

	// prefixSibling is the case the library exists for: a directory whose name
	// merely extends the root's, which strings.HasPrefix accepts and this
	// package must refuse.
	prefixSibling = fs("/srv/data-evil/leaf.txt")

	// uncomparableTarget is relative where benchRoot is absolute, so Rel cannot
	// compare the pair. Kept deep on purpose: Rel builds its refusal message by
	// concatenating BOTH paths, so an attacker sizes this refusal's byte cost.
	uncomparableTarget = relPath(deepDepth)
)

// The hygiene corpus, judged without a root.
var (
	// No ".." at all, which is HasDotDot's worst case: it must walk every
	// component to answer false.
	hygieneShallow = relPath(shallowDepth)
	hygieneMedium  = relPath(mediumDepth)
	hygieneDeep    = relPath(deepDepth)

	// dotDotFirst, dotDotMiddle and dotDotLast put the traversal at each end and
	// in the middle of an otherwise identical path. HasDotDot returns on the
	// first match, so first is O(1) and last is O(depth).
	dotDotFirst  = fs("../" + strings.Repeat("c/", deepDepth) + "leaf.txt")
	dotDotMiddle = fs(strings.Repeat("c/", deepDepth/2) + "../" + strings.Repeat("c/", deepDepth/2) + "leaf.txt")
	dotDotLast   = fs(strings.Repeat("c/", deepDepth) + "..")

	// longComponent is the byte-cost fixture: one component, no separator.
	longComponent = strings.Repeat("x", longComponentBytes)

	// nonASCII has multi-byte components. These predicates compare bytes and
	// split on a separator byte, so a regression that starts decoding runes
	// shows up here and nowhere else.
	nonASCII = fs("Ünïcødé/日本語/файл.txt/ελληνικά")

	// escapeFirstRel holds the zero-allocation claim for an input that both
	// escapes and is clean.
	escapeFirstRel = fs("../" + strings.Repeat("c/", deepDepth) + "leaf.txt")

	// buriedTraversal is the unclean input the two axes were built to disagree
	// on, and the one RelEscapes/IsCanonical input class that allocates: Clean
	// must build a new string because the result is not a prefix of the input.
	buriedTraversal = fs(strings.Repeat("c/", deepDepth) + "../../etc/shadow")

	// redundantSeparators is the other way Clean is forced to rewrite.
	redundantSeparators = fs("/srv/data//sub///" + strings.Repeat("c//", deepDepth) + "file.txt")
)

// degenerateTargets is the adversarial corpus, held as one set because what
// matters is that the whole class stays cheap. Every member is also gated
// individually for allocations by the tests below.
var degenerateTargets = []string{
	fs("/srv/data"),      // target equals root: contained, and Rel's early return
	fs("/srv/data/"),     // trailing separator on the root itself
	fs("/srv/data/sub/"), // trailing separator on a descendant
	fs("///"),            // nothing but separators
	fs("/"),              // the filesystem root
	"",                   // the empty string
	fs(".."),             // a bare traversal
	fs("../.."),          // a doubled traversal
	fs("/srv/data/.."),   // a traversal that cancels the root
	fs("..extras/x"),     // a NAME that merely begins with two dots
	fs("key..v2"),        // two dots inside a component
	fs("..."),            // three dots is a directory name
	longComponent,        // one very long component
	nonASCII,             // multi-byte components
}

// containedPath returns an absolute path depth components below benchRoot.
func containedPath(depth int) string {
	return fs("/srv/data/" + strings.Repeat("c/", depth) + "leaf.txt")
}

// relPath returns a clean relative name depth components deep.
func relPath(depth int) string {
	return fs(strings.Repeat("c/", depth) + "leaf.txt")
}

// skipAllocContractOnWindows skips an allocation-contract test on Windows, where
// filepath.ToSlash rewrites every backslash and filepath.Clean rewrites
// separators, so both return a fresh string for input that needs no other
// change. The contract asserted below is therefore a Unix one.
func skipAllocContractOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skipf("allocation contract is Unix-only: on %s filepath.ToSlash and "+
			"filepath.Clean rewrite separators and so allocate", runtime.GOOS)
	}
}

// TestHasDotDotAllocations pins the strongest of the four contracts: HasDotDot
// is allocation-free, unconditionally. It is the only one of the four that does
// not call filepath.Clean, so the assertion is a flat `== 0` for every row.
//
// What it catches: strings.Split in place of strings.SplitSeq allocates a slice
// per call, and any rewrite that materializes the components (or reaches for a
// regexp) does the same. Both would pass every behavioural test in this repo.
//
// No t.Parallel here or in the siblings below: testing.AllocsPerRun pins
// GOMAXPROCS to 1 and measures process-wide allocation, so a parallel sibling's
// allocations land in this measurement.
func TestHasDotDotAllocations(t *testing.T) {
	skipAllocContractOnWindows(t)

	tests := map[string]string{
		"clean shallow path":         hygieneShallow,
		"clean deep path":            hygieneDeep,
		"traversal first component":  dotDotFirst,
		"traversal middle component": dotDotMiddle,
		"traversal last component":   dotDotLast,
		"bare traversal":             fs(".."),
		"buried traversal":           buriedTraversal,
		"redundant separators":       redundantSeparators,
		"trailing separator":         fs("/srv/data/sub/"),
		"only separators":            fs("///"),
		"empty string":               "",
		"long single component":      longComponent,
		"non-ascii components":       nonASCII,
		"names that merely dot":      fs("key..v2/.../..extras/a..b"),
	}

	for desc, p := range tests {
		t.Run(sub(desc), func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				boolSink = pathinside.HasDotDot(p)
			}); got != 0 {
				t.Errorf("HasDotDot(%s) allocated %v times per run, want 0: the "+
					"hygiene axis judges a path as written and must not build one",
					short(p), got)
			}
		})
	}
}

// TestRelEscapesAllocations pins RelEscapes' contract on canonical input:
// filepath.Clean returns a substring of its argument whenever it has nothing to
// rewrite, so a canonical name is judged for free.
//
// The unclean classes are NOT asserted at zero, because they measured 2 and
// asserting zero there would be asserting a bug. Their property is the one
// TestRefusalCostDoesNotGrowWithTheInput holds.
func TestRelEscapesAllocations(t *testing.T) {
	skipAllocContractOnWindows(t)

	// Every entry is already in filepath.Clean form, verified by the guard in the
	// loop: a quietly unclean fixture would make this test pass for the wrong
	// reason if the contract were ever loosened.
	tests := map[string]string{
		"clean shallow name":        hygieneShallow,
		"clean medium name":         hygieneMedium,
		"clean deep name":           hygieneDeep,
		"canonical escape at front": escapeFirstRel,
		"bare traversal":            fs(".."),
		"doubled traversal":         fs("../.."),
		"dot":                       ".",
		"absolute path":             fs("/etc/passwd"),
		"name that merely dots":     fs("..extras/movie.mkv"),
		"long single component":     longComponent,
		"non-ascii components":      nonASCII,
	}

	for desc, rel := range tests {
		t.Run(sub(desc), func(t *testing.T) {
			if !pathinside.IsCanonical(rel) {
				t.Fatalf("fixture %s is not canonical, so it cannot witness the "+
					"canonical-input contract", short(rel))
			}
			if got := testing.AllocsPerRun(200, func() {
				boolSink = pathinside.RelEscapes(rel)
			}); got != 0 {
				t.Errorf("RelEscapes(%s) allocated %v times per run, want 0: a "+
					"canonical name must be judged without building a second string",
					short(rel), got)
			}
		})
	}
}

// TestIsCanonicalAllocations pins the same contract for the other half of the
// hygiene axis: a path that IS canonical is judged for free, because Clean
// returned the argument unchanged. An input that answers false may or may not
// allocate, so the true side is the gateable claim.
func TestIsCanonicalAllocations(t *testing.T) {
	skipAllocContractOnWindows(t)

	tests := map[string]string{
		"canonical shallow path": containedShallow,
		"canonical deep path":    containedDeep,
		"canonical relative":     hygieneDeep,
		"canonical traversal":    fs(".."),
		"canonical up and down":  fs("../dumps"),
		"dot":                    ".",
		"filesystem root":        fs("/"),
		"long single component":  longComponent,
		"non-ascii components":   nonASCII,
	}

	for desc, p := range tests {
		t.Run(sub(desc), func(t *testing.T) {
			if !pathinside.IsCanonical(p) {
				t.Fatalf("fixture %s is not canonical, so it cannot witness the "+
					"canonical-input contract", short(p))
			}
			if got := testing.AllocsPerRun(200, func() {
				boolSink = pathinside.IsCanonical(p)
			}); got != 0 {
				t.Errorf("IsCanonical(%s) allocated %v times per run, want 0: "+
					"confirming a path is already clean must not produce a cleaned "+
					"copy of it", short(p), got)
			}
		})
	}
}

// TestRootContainsAllocations pins the containment axis's accept path at zero:
// filepath.Rel returns the constant "." when the target IS the root and a
// substring of the target when it is beneath it, at depth 2 and at depth 512
// alike.
//
// The empty root is included because it is a different mechanism: Root("")
// short-circuits before Rel, and a refactor that dropped the short-circuit would
// both change the answer and start allocating.
func TestRootContainsAllocations(t *testing.T) {
	skipAllocContractOnWindows(t)

	tests := map[string]struct {
		root   pathinside.Root
		target string
	}{
		"target equals root":       {benchRoot, fs("/srv/data")},
		"target is root plus sep":  {benchRoot, fs("/srv/data/")},
		"shallow descendant":       {benchRoot, containedShallow},
		"medium descendant":        {benchRoot, containedMedium},
		"deep descendant":          {benchRoot, containedDeep},
		"descendant trailing sep":  {benchRoot, fs("/srv/data/sub/")},
		"long single component":    {benchRoot, fs("/srv/data/" + longComponent)},
		"non-ascii descendant":     {benchRoot, fs("/srv/data/" + nonASCII)},
		"relative root and target": {pathinside.Root(fs("data")), fs("data/x/y")},
		"empty root refuses":       {pathinside.Root(""), containedDeep},
	}

	for desc, tt := range tests {
		t.Run(sub(desc), func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				boolSink = tt.root.Contains(tt.target)
			}); got != 0 {
				t.Errorf("Root(%s).Contains(%s) allocated %v times per run, want 0: "+
					"admitting a contained path must not build a string",
					short(string(tt.root)), short(tt.target), got)
			}
		})
	}
}

// TestRootContainsRootSpellingCosts records the one place where this package's
// documented equivalence is an equivalence of ANSWERS and not of COST: the root
// is re-cleaned inside filepath.Rel on every call, so a trailing separator is
// truncated out of a substring and costs nothing while a "." component forces
// Clean to build a new string, on every target, forever.
//
// It asserts only the direction it can defend — clean and trailing-separator are
// free, "." is not — rather than an exact count for the unclean case, which is a
// filepath.Clean implementation detail.
func TestRootContainsRootSpellingCosts(t *testing.T) {
	skipAllocContractOnWindows(t)

	target := containedDeep

	free := map[string]pathinside.Root{
		"clean root":                 pathinside.Root(fs("/srv/data")),
		"trailing separator":         pathinside.Root(fs("/srv/data/")),
		"doubled trailing separator": pathinside.Root(fs("/srv/data//")),
	}
	for desc, root := range free {
		t.Run(sub(desc), func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				boolSink = root.Contains(target)
			}); got != 0 {
				t.Errorf("Root(%s).Contains(%s) allocated %v times per run, want 0",
					short(string(root)), short(target), got)
			}
		})
	}

	t.Run("dot_component_root_is_not_free", func(t *testing.T) {
		root := pathinside.Root(fs("/srv/./data"))
		if !root.Contains(target) {
			t.Fatalf("Root(%s).Contains(%s) = false, want true: the fixture must "+
				"answer the same as the clean spelling", short(string(root)), short(target))
		}
		if got := testing.AllocsPerRun(200, func() {
			boolSink = root.Contains(target)
		}); got == 0 {
			t.Errorf("Root(%s).Contains(%s) allocated %v times per run, want more "+
				"than 0: if a root written with a %q component is now free, the "+
				"root is being cleaned once instead of per call and this test "+
				"should be replaced by a zero assertion",
				short(string(root)), short(target), got, ".")
		}
	})
}

// TestRefusalCostDoesNotGrowWithTheInput holds the property that matters for a
// security gate on the paths where allocation-freedom is not available.
//
// Every refusal allocates, because filepath.Rel builds either a "../.." ladder
// or an error message, and the caller that triggers one is by definition the
// untrusted one. An attacker who can make refusal a hundred times more expensive
// by sending a hundred times more path has an amplification vector inside the
// containment check, so the assertion is that refusal's allocation count is
// BOUNDED and does not track input length. Byte volume does grow with the input
// and is deliberately not gated: the count staying flat is what distinguishes
// bounded work from amplification.
func TestRefusalCostDoesNotGrowWithTheInput(t *testing.T) {
	skipAllocContractOnWindows(t)

	// A generous ceiling on purpose: the point is that the number does not track
	// depth, not that it is any particular small value.
	const maxRefusalAllocs = 8

	depths := []int{1, 4, 16, 64, 256}

	tests := map[string]func(depth int) (pathinside.Root, string){
		"escape at first component": func(depth int) (pathinside.Root, string) {
			return benchRoot, fs("/etc/" + strings.Repeat("c/", depth) + "leaf.txt")
		},
		"escape at last component": func(depth int) (pathinside.Root, string) {
			root := pathinside.Root(fs("/srv/data/" + strings.Repeat("c/", depth) + "x"))
			return root, fs("/srv/data/" + strings.Repeat("c/", depth) + "y")
		},
		"uncomparable pair": func(depth int) (pathinside.Root, string) {
			return benchRoot, relPath(depth)
		},
		"prefix sibling": func(depth int) (pathinside.Root, string) {
			return benchRoot, fs("/srv/data-evil/" + strings.Repeat("c/", depth) + "leaf.txt")
		},
	}

	for desc, build := range tests {
		t.Run(sub(desc), func(t *testing.T) {
			for _, depth := range depths {
				root, target := build(depth)
				if root.Contains(target) {
					t.Errorf("Root(%s).Contains(%s) = true, want false: the fixture "+
						"must exercise the refusal path", short(string(root)), short(target))
					continue
				}
				got := testing.AllocsPerRun(200, func() {
					boolSink = root.Contains(target)
				})
				if got > maxRefusalAllocs {
					t.Errorf("Root(%s).Contains(%s) allocated %v times per run at "+
						"depth %d, want at most %d: refusal cost must not grow with "+
						"the path an attacker supplies",
						short(string(root)), short(target), got, depth, maxRefusalAllocs)
				}
			}
		})
	}
}

// TestHygieneRefusalCostDoesNotGrowWithTheInput is the same property for the
// hygiene axis's one allocating class: RelEscapes and IsCanonical allocate when
// filepath.Clean has to rewrite, and unclean input is what an attacker supplies.
// Assert the flatness directly rather than the measured constant, so the test
// survives a Clean that grows a second buffer without becoming
// input-proportional.
func TestHygieneRefusalCostDoesNotGrowWithTheInput(t *testing.T) {
	skipAllocContractOnWindows(t)

	const maxUncleanAllocs = 8

	for _, depth := range []int{1, 4, 16, 64, 256} {
		p := fs(strings.Repeat("c/../", depth) + "leaf.txt")
		if pathinside.IsCanonical(p) {
			t.Errorf("IsCanonical(%s) = true, want false: the fixture must be "+
				"unclean to exercise the rewriting path", short(p))
			continue
		}

		relAllocs := testing.AllocsPerRun(200, func() {
			boolSink = pathinside.RelEscapes(p)
		})
		if relAllocs > maxUncleanAllocs {
			t.Errorf("RelEscapes(%s) allocated %v times per run at depth %d, want "+
				"at most %d", short(p), relAllocs, depth, maxUncleanAllocs)
		}

		canonAllocs := testing.AllocsPerRun(200, func() {
			boolSink = pathinside.IsCanonical(p)
		})
		if canonAllocs > maxUncleanAllocs {
			t.Errorf("IsCanonical(%s) allocated %v times per run at depth %d, want "+
				"at most %d", short(p), canonAllocs, depth, maxUncleanAllocs)
		}

		// HasDotDot sees the same hostile input and never cleans it, so it is held
		// to the strict contract even here.
		if got := testing.AllocsPerRun(200, func() {
			boolSink = pathinside.HasDotDot(p)
		}); got != 0 {
			t.Errorf("HasDotDot(%s) allocated %v times per run at depth %d, want 0",
				short(p), got, depth)
		}
	}
}

// BenchmarkRootContains measures the containment accept path across depths.
//
// Size-parameterised because the cost model is the claim: filepath.Rel compares
// components pairwise in one forward pass, so 16x the components should cost
// roughly 16x, not 256x.
func BenchmarkRootContains(b *testing.B) {
	cases := []struct {
		name   string
		target string
	}{
		{"contained_depth_" + strconv.Itoa(shallowDepth), containedShallow},
		{"contained_depth_" + strconv.Itoa(mediumDepth), containedMedium},
		{"contained_depth_" + strconv.Itoa(deepDepth), containedDeep},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				boolSink = benchRoot.Contains(tc.target)
			}
		})
	}
}

// BenchmarkRootContainsRefusal measures the four ways containment says no — the
// number that matters under attack, since none of these is a shape a caller
// chooses.
//
// The first two rows are a deliberate pair at a similar total length: diverging
// at the FIRST component does not cost less than diverging at the LAST, because
// Rel then copies the whole remaining target into a "../.." ladder, so there is
// no cheap refusal to route hostile input through. Keep both series — a future Go
// whose Rel gains a real early-out shows up as these two diverging.
//
// uncomparable_pair is the row to watch: Rel refuses an absolute-against-relative
// pair by building an error message concatenating BOTH paths, which this package
// then discards, so it is the most expensive refusal available and the caller
// sizes it.
func BenchmarkRootContainsRefusal(b *testing.B) {
	cases := []struct {
		name   string
		root   pathinside.Root
		target string
	}{
		{"escape_at_first_component", benchRoot, escapeFirstComponent},
		{"escape_at_last_component", benchDeepRoot, escapeLastComponent},
		{"prefix_sibling", benchRoot, prefixSibling},
		{"uncomparable_pair", benchRoot, uncomparableTarget},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if tc.root.Contains(tc.target) {
				b.Fatalf("Root(%s).Contains(%s) = true, want false: this benchmark "+
					"must measure the refusal path", short(string(tc.root)), short(tc.target))
			}
			b.ReportAllocs()
			for b.Loop() {
				boolSink = tc.root.Contains(tc.target)
			}
		})
	}
}

// BenchmarkHasDotDot measures the hygiene axis's worst case across depths: a
// path with no traversal anywhere, so every component is examined.
//
// Size-parameterised because a rewrite to strings.Split would allocate a slice
// proportional to the component count, which shows up as a step change in B/op
// far larger at 512 components than at 32. The ladder starts at 32 because a
// two-component path measures fixed call overhead rather than walking.
func BenchmarkHasDotDot(b *testing.B) {
	cases := []struct {
		name string
		path string
	}{
		{"clean_depth_" + strconv.Itoa(mediumDepth), hygieneMedium},
		{"clean_depth_" + strconv.Itoa(deepDepth), hygieneDeep},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if pathinside.HasDotDot(tc.path) {
				b.Fatalf("HasDotDot(%s) = true, want false: this benchmark must "+
					"measure the full walk", short(tc.path))
			}
			b.ReportAllocs()
			for b.Loop() {
				boolSink = pathinside.HasDotDot(tc.path)
			}
		})
	}
}

// BenchmarkHasDotDotPosition measures the early-out by moving the traversal from
// the front of a path to its end, holding everything else identical.
//
// HasDotDot returns on the first ".." component, so first is O(1) and last is
// O(depth). Two series rather than one, because the RELATIONSHIP between them is
// the regression signal: if they ever converge, the loop stopped returning early
// and now examines every component of every path it is handed — invisible to
// every behavioural test in this repo, because the answer never changes.
func BenchmarkHasDotDotPosition(b *testing.B) {
	cases := []struct {
		name string
		path string
	}{
		{"dotdot_at_first_component", dotDotFirst},
		{"dotdot_at_last_component", dotDotLast},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if !pathinside.HasDotDot(tc.path) {
				b.Fatalf("HasDotDot(%s) = false, want true: this benchmark must "+
					"measure a path that does hold a traversal", short(tc.path))
			}
			b.ReportAllocs()
			for b.Loop() {
				boolSink = pathinside.HasDotDot(tc.path)
			}
		})
	}
}

// BenchmarkHasDotDotShape separates the cost variable the depth ladder cannot:
// bytes, as distinct from components. The path is multi-byte throughout, so a
// rewrite that decoded runes would move this series sharply and leave the ASCII
// ladder flat.
//
// A series of its own rather than a row in the degenerate corpus below, which
// would dilute a 4x regression on one of fourteen inputs to a 1.2x move on the
// corpus total, under the tracker's 150% threshold.
func BenchmarkHasDotDotShape(b *testing.B) {
	cases := []struct {
		name string
		path string
	}{
		{"non_ascii_components", nonASCII},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				boolSink = pathinside.HasDotDot(tc.path)
			}
		})
	}
}

// BenchmarkRelEscapes measures the containment rule's rootless half.
//
// canonical_depth_512 is the whole cost model: filepath.Clean walks the entire
// path before the separator-precise prefix test looks at the first two bytes, so
// RelEscapes has NO early-out — which is why the cheap way to ask "does this name
// traverse" is the hygiene predicate rather than this one. An escaping row would
// track this one exactly, so it is not carried separately.
//
// unclean_buried_traversal is the allocating row, and the input class the two
// axes were built to disagree on, so it is where an allocation regression would
// be easiest to mistake for correct behaviour.
func BenchmarkRelEscapes(b *testing.B) {
	cases := []struct {
		name string
		rel  string
	}{
		{"canonical_depth_" + strconv.Itoa(deepDepth), hygieneDeep},
		{"unclean_buried_traversal", buriedTraversal},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				boolSink = pathinside.RelEscapes(tc.rel)
			}
		})
	}
}

// BenchmarkIsCanonical measures the hygiene axis's other half, which is one
// filepath.Clean and one string comparison.
//
// Only the canonical case is carried: the path is handed back as a substring, so
// the comparison is a pointer-and-length equality and nothing allocates, which is
// the property a naive rewrite stops special-casing. The unclean side is the same
// Clean rewrite BenchmarkRelEscapes/unclean_buried_traversal already tracks.
func BenchmarkIsCanonical(b *testing.B) {
	cases := []struct {
		name string
		path string
	}{
		{"canonical_depth_" + strconv.Itoa(deepDepth), containedDeep},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				boolSink = pathinside.IsCanonical(tc.path)
			}
		})
	}
}

// BenchmarkDegenerateInputs measures the adversarial corpus: `..` at every
// position, nothing but separators, a trailing separator, the filesystem root,
// the empty string, names that merely begin with two dots, a 4 KiB component,
// multi-byte components, and a target equal to its root.
//
// One series per axis rather than one per input: what matters is that the whole
// hostile class stays cheap, and each member is already gated individually for
// allocations by the tests above. One iteration judges the entire corpus, so
// ns/op is the corpus total — divide by the corpus length before comparing it to
// any single-input series.
func BenchmarkDegenerateInputs(b *testing.B) {
	b.Run("containment_corpus", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, target := range degenerateTargets {
				boolSink = benchRoot.Contains(target)
			}
		}
	})

	b.Run("hygiene_corpus", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			// Each result folds into acc (!= is XOR on bools) rather than
			// overwriting one shared sink three times: a dead store is exactly the
			// shape a compiler may remove along with the pure call that fed it.
			acc := false
			for _, p := range degenerateTargets {
				acc = acc != pathinside.HasDotDot(p)
				acc = acc != pathinside.IsCanonical(p)
				acc = acc != pathinside.RelEscapes(p)
			}
			boolSink = acc
		}
	})
}

// sub turns a case description into a subtest name -run can address: no spaces
// and, critically, no "/", which -run reads as its path separator. Path fixtures
// are full of slashes, so they stay in the case DATA and never in its name.
func sub(desc string) string {
	return strings.ReplaceAll(desc, " ", "_")
}

// short renders a path for a failure message: quoted in full when it is short
// enough to read, and as a quoted head plus a byte count when it is not, since a
// whole 512-component path buries the rest of the line. The head is trimmed to a
// rune boundary: these fixtures are deliberately non-ASCII in places, and a cut
// through a multi-byte rune reads like corrupted input rather than a truncated
// one.
func short(p string) string {
	const maxQuoted = 48
	if len(p) <= maxQuoted {
		return strconv.Quote(p)
	}
	head := strings.ToValidUTF8(p[:maxQuoted], "")
	return strconv.Quote(head) + "\u2026 (" + strconv.Itoa(len(p)) + " bytes)"
}
