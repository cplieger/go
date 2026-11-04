package pathinside

import (
	"path/filepath"
	"strings"
)

// Root is the tree a containment question is asked against — the sticky side
// of the comparison. Construct it ONCE, where the confinement boundary is
// decided, and let targets flow past it: a Root held in a named field or
// variable has no pair left to transpose, unlike an inline
// pathinside.Root(x).Contains(y), where x and y can be swapped and still
// compile. (A site whose root varies per item while the target is fixed is the
// inverted case — keep the inline form there.)
//
// A Root is constructed by plain conversion: pathinside.Root("/srv/data"). The
// conversion IS the construction — no constructor, no validation, no hidden
// state. Nothing is normalized at conversion time either: cleaning happens
// inside filepath.Rel at judgment time, so Root("/a/b/"), Root("/a/./b") and
// Root("/a/b") judge every target identically — though not at identical cost: a
// root whose spelling forces filepath.Clean to rewrite it (a "." component)
// pays for that rewrite on every target it judges, for as long as the value
// lives, where a clean or trailing-separator root judges for zero allocations.
// Prefer storing a filepath.Clean'd root when the value comes from
// configuration and will judge many paths.
//
// The zero value Root("") CONTAINS NOTHING: every Contains answer is false. An
// empty root is almost always an unset field or a missed configuration value,
// and the fail-open alternative — filepath.Rel cleans "" to ".", silently
// confining to the current working directory, a boundary nobody chose — is the
// direction a containment bug must not take. A caller that genuinely wants
// cwd-relative containment writes Root(".") and says so.
type Root string

// Contains reports whether target names the root itself or a path beneath it.
// It is the containment predicate a caller reaches for before letting an
// externally-influenced path reach the filesystem: an archive entry name, a
// filesystem-event path, a request-supplied file path, a path read back out of
// a log the program itself wrote.
//
// The root and the target are both cleaned by filepath.Rel, so "/a/b/",
// "/a/./b" and "/a/x/../b" are all the same root, and nothing else is
// normalized — no symlink resolution, no Unicode normalization, no conversion
// between absolute and relative.
//
// CASE IS THE PLATFORM'S RULE, and it is the one place this method is not
// byte-exact: filepath.Rel compares path components case-INSENSITIVELY on
// Windows and byte-exactly on Unix and Plan 9, so Root("/srv/Data").Contains(
// "/srv/data/x") is false on Linux and true on Windows. This package neither
// adds that folding nor removes it. The folding only ever grows containment
// MORE permissive on Windows, never less — Go 1.27's Unicode 17 tables fold
// U+FB05/U+FB06 and the Greek U+0390/U+1FD3, U+03B0/U+1FE3, which Go 1.26 held
// distinct, so Windows containment loosened by exactly those names. The
// hygiene predicates ([RelEscapes], [HasDotDot], [IsCanonical]) are immune:
// they compare against the literal "..", which has no case-fold partners.
//
// THE ROOT ITSELF IS CONTAINED. Root(p).Contains(p) is true for every
// non-empty p: the tree rooted at root includes root itself (a scan that
// starts at root, a watch registered on root, an archive's "./" entry). A
// caller that must EXCLUDE the root (an operation that would rewrite or delete
// the tree's own directory when handed an empty relative name) needs a second,
// explicit test of its own — a false from this method never means "not equal
// to root".
//
// The comparison is LEXICAL: it says nothing about what the two paths resolve
// to. A symlink at root/link pointing at /etc makes root/link/passwd lexically
// inside root, and this method reports it as such — the right answer for a
// NAME-level decision (is this name mine to handle), the wrong one for an
// ACCESS-level decision (may this open succeed). A caller that opens, reads,
// writes, renames or removes through the path needs kernel-enforced
// confinement — os.Root (os.OpenRoot, os.OpenInRoot) — which closes the
// TOCTOU window this predicate cannot see. Use both when both questions apply.
//
// Two paths that cannot be compared lexically are refused rather than guessed:
// an absolute target against a relative root (or the reverse), and on Windows
// two different volumes. filepath.Rel reports those as an error, and this
// method answers false, so an unanswerable comparison never reads as
// containment.
func (r Root) Contains(target string) bool {
	if r == "" {
		return false
	}
	rel, err := filepath.Rel(string(r), target)
	if err != nil {
		return false
	}
	return !RelEscapes(rel)
}

// RelEscapes reports whether rel, read as a path relative to some root, leaves
// that root: it IS ".." or it begins with ".." followed by a separator.
//
// It is a separate function from [Root.Contains], not folded into it: fusing
// them would force every caller to buy both. Root.Contains asks whether one
// path lies within another; RelEscapes asks whether a relative NAME is
// well-formed for use under a root — asked BEFORE joining the name onto
// anything (an archive entry name, a configured sub-path), or by a caller
// already holding a filepath.Rel result (an os.Root-relative Stat or Remove)
// that should not pay for a second Rel. It takes the relative name alone, so
// there is no pair to swap and no [Root] to construct.
//
// rel is cleaned first, so an uncleaned traversal buried mid-string
// ("a/../../etc") is still caught. This is also the boundary between this
// package's two axes: a traversal that NORMALIZES AWAY is not an escape —
// "/run/secrets/../../etc/shadow" cleans to "/etc/shadow" and reports false
// here. A caller asking instead whether a path was WRITTEN with traversal in
// it — a config-supplied credential path, a backup destination — wants
// [HasDotDot]; reaching for RelEscapes there turns a deliberate refusal into
// an acceptance.
//
// RelEscapes is deliberately STRICTER than [Root.Contains]'s locational
// answer: a name that walks out of the root and back into a directory sharing
// the root's name ("../a" under root "a") is refused here while the joined
// result — the root itself — is inside. A caller validating an untrusted name
// wants this strictness; a caller classifying a path it was already handed
// wants Root.Contains. Fusing them would silently pick one answer for both.
//
// The test is separator-precise: a leading-".." STRING prefix test would
// refuse the legitimate name "..extras/movie.mkv". Requiring the separator
// (or an exact "..") splits the cases: "../x" escapes, "..extras/x" does not.
//
// RelEscapes is absoluteness-agnostic: "/etc/passwd" cleans to itself, is not
// "..", and does not begin with "../", so a caller validating an untrusted
// name must reject absolute paths separately (filepath.IsAbs). That refusal
// matters because filepath.Clean CLAMPS a traversal at the filesystem root
// ("/.." cleans to "/"), while filepath.Join re-attaches the unclamped
// traversal to a relative base (filepath.Join("data", "/..") is "."). An
// absolute name is the caller's to refuse.
func RelEscapes(rel string) bool {
	rel = filepath.Clean(rel)
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// HasDotDot reports whether p contains a ".." path COMPONENT, examined AS
// WRITTEN. It is this package's second axis — syntactic hygiene — and it takes
// no root, because it asks nothing about where p points: only whether the path a
// caller was handed spells a traversal at all.
//
// p is NOT cleaned, and that is the whole of the difference from [RelEscapes].
// The two give OPPOSITE answers on exactly the inputs that matter:
// "/run/secrets/../../etc/shadow" cleans to "/etc/shadow", which leaves no root,
// so RelEscapes reports false — while this function reports true, because the
// traversal is written. Reach for [RelEscapes] when the question is "after
// normalization, does this name leave its root": an archive entry about to be
// joined, a configured sub-path resolved against a base. Reach for this function
// when the question is "was I handed a path with traversal in it": a
// config-supplied credential path, a backup destination, a value a human was
// supposed to spell plainly. Such a path is not suspicious because of where it
// resolves — it is suspicious because a legitimate one would not have been
// written that way — and answering that question with RelEscapes converts a
// deliberate refusal into an acceptance.
//
// The components are taken from filepath.ToSlash(p) split on "/". ToSlash
// rewrites "\" to "/" ONLY on Windows: on Unix a backslash is a legal filename
// byte, so `a\..\b` is ONE component and must NOT read as traversal; on
// Windows it IS a separator, so the same string is three components and must.
// A split on both characters refuses a legal Unix filename, a split on
// filepath.Separator alone misses the Windows traversal, and
// strings.Contains(p, "..") refuses ordinary names like "key..v2" everywhere.
//
// Only an exact ".." component counts — "...", "..extras" and "a..b" are not
// traversing. The empty string has no components and is false. A ".." anywhere
// in the path is true — first, last, or buried in the middle, which is the
// case cleaning would have hidden.
//
// HasDotDot judges nothing else: nothing about absoluteness (the caller's
// refusal, as with RelEscapes), nothing about whether p is otherwise cleanly
// written (pair with [IsCanonical] for that), and nothing about what p
// resolves to — a symlink component is invisible to it.
func HasDotDot(p string) bool {
	for component := range strings.SplitSeq(filepath.ToSlash(p), "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

// IsCanonical reports whether p is already in filepath.Clean form — cleaning
// it would change nothing. It is the other half of the hygiene axis, and it
// needs no root either.
//
// Refusing a non-canonical path refuses in one test the whole class of
// spellings a later normalization would silently rewrite: a trailing
// separator, a doubled separator, a "." component, a traversal buried
// mid-path. Canonicality is NOT hygiene, which is why this is a separate
// predicate rather than a stricter [HasDotDot]: ".." and "../dumps" are
// perfectly canonical, so a canonicality test alone accepts a leading
// traversal. The composed rule a validating caller wants is the OR of both,
// !IsCanonical(p) || HasDotDot(p).
//
// Canonicality also bounds the disagreement between the two axes: Clean
// leaves ".." components only at the FRONT of a relative path, so a canonical
// path containing one escapes by [RelEscapes] too — the axes diverge only on
// unclean input, which is precisely what an attacker supplies.
//
// The test is a string identity against filepath.Clean, so it inherits
// Clean's platform rules: the empty string is not canonical (cleans to "."),
// and on Windows a slash-written path is not canonical either (Clean rewrites
// "a/b" to `a\b`). A caller accepting slash-written input on Windows converts
// with filepath.FromSlash before testing, not after.
func IsCanonical(p string) bool {
	return p == filepath.Clean(p)
}
