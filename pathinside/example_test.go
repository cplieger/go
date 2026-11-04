package pathinside_test

import (
	"fmt"
	"path/filepath"

	"github.com/cplieger/go/pathinside"
)

// ExampleRoot_Contains shows the three answers that matter: the root itself is
// inside, a real descendant is inside, and a sibling whose name merely extends
// the root's is not — the case a strings.HasPrefix test accepts. The Root is a
// plain conversion, made once; every target is judged against it.
func ExampleRoot_Contains() {
	root := pathinside.Root("/srv/data")
	fmt.Println(root.Contains("/srv/data"))
	fmt.Println(root.Contains("/srv/data/2026/report.csv"))
	fmt.Println(root.Contains("/srv/data-evil/report.csv"))
	fmt.Println(root.Contains("/srv/data/../../etc/passwd"))
	// Output:
	// true
	// true
	// false
	// false
}

// ExampleRelEscapes validates an archive entry name before it is joined onto an
// extraction directory. A traversal is refused; a name that merely begins with
// two dots is a name, not a traversal. RelEscapes says nothing about
// absoluteness, so the caller rejects an absolute entry itself.
func ExampleRelEscapes() {
	for _, name := range []string{"docs/readme.md", "../../etc/passwd", "..extras/movie.mkv", "/etc/passwd"} {
		switch {
		case filepath.IsAbs(name):
			fmt.Printf("%-20s refused: absolute\n", name)
		case pathinside.RelEscapes(name):
			fmt.Printf("%-20s refused: escapes the extraction directory\n", name)
		default:
			fmt.Printf("%-20s accepted\n", name)
		}
	}
	// Output:
	// docs/readme.md       accepted
	// ../../etc/passwd     refused: escapes the extraction directory
	// ..extras/movie.mkv   accepted
	// /etc/passwd          refused: absolute
}

// ExampleHasDotDot judges config-supplied values on their own, with no root to
// compare them against: a path a human was supposed to spell plainly is refused
// when it spells a traversal, whatever it resolves to. The last two are ordinary
// names — two adjacent dots inside a component are not a traversal, which is
// what a strings.Contains test gets wrong.
func ExampleHasDotDot() {
	for _, p := range []string{"/run/secrets/pgpass", "/run/secrets/../../etc/shadow", "/dumps/a..b", "key..v2"} {
		fmt.Printf("%-32s %v\n", p, pathinside.HasDotDot(p))
	}
	// Output:
	// /run/secrets/pgpass              false
	// /run/secrets/../../etc/shadow    true
	// /dumps/a..b                      false
	// key..v2                          false
}

// ExampleIsCanonical shows the composed rule a validating caller wants,
// !IsCanonical(p) || HasDotDot(p), and why it takes both halves: an unclean
// spelling is refused by canonicality alone, while a leading traversal is
// perfectly canonical and is refused only by HasDotDot.
func ExampleIsCanonical() {
	for _, p := range []string{"/dumps", "/dumps/", "/dumps/./nightly", "..", "../dumps"} {
		accepted := pathinside.IsCanonical(p) && !pathinside.HasDotDot(p)
		fmt.Printf("%-18s canonical=%-5v traversing=%-5v accepted=%v\n", p, pathinside.IsCanonical(p), pathinside.HasDotDot(p), accepted)
	}
	// Output:
	// /dumps             canonical=true  traversing=false accepted=true
	// /dumps/            canonical=false traversing=false accepted=false
	// /dumps/./nightly   canonical=false traversing=false accepted=false
	// ..                 canonical=true  traversing=true  accepted=false
	// ../dumps           canonical=true  traversing=true  accepted=false
}
