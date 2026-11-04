// Package pathinside judges path names along two separate axes.
//
// Containment needs a root and asks where a path points: is this cleaned
// target the same as this root, or beneath it? [Root.Contains] and
// [RelEscapes] answer it.
//
// Hygiene needs no root and asks how a path is written: does it spell a
// traversal, and is it in cleaned form? [HasDotDot] and [IsCanonical] answer
// it.
//
// The axes disagree on unclean input, which is what an attacker supplies.
// Containment cleans first, so "/run/secrets/../../etc/shadow" cleans to
// "/etc/shadow" and passes. Hygiene never cleans, so the same path fails.
// Answering a hygiene question with a containment function turns a refusal
// into an acceptance. On canonical input the two always agree, because
// filepath.Clean leaves ".." only at the front of a relative path.
//
// All four predicates are lexical. They compare names and resolve nothing, so
// a symlink inside the root is still inside it. Code that opens, writes or
// removes through the path also wants the kernel-enforced confinement of
// os.Root. [Root.Contains] inherits filepath.Rel's case folding on Windows.
// docs/contract.md has the full contract.
//
// Standard library only, zero dependencies.
package pathinside
