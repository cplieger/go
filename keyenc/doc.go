// Package keyenc joins several untrusted strings into one key that no
// component's content can forge.
//
// A composite key built by concatenating fields with a separator is correct
// only while no field can contain the separator. When one can, two distinct
// field tuples produce one key, and a cache, a dedupe token or a map silently
// merges two things it was built to keep apart.
//
// [Join] escapes each component before joining, so the separator between
// components is always distinguishable from one inside a component:
//
//	Join("a:b", "c") == `a\:b:c`
//	Join("a", "b:c") == `a:b\:c`
//	Join("a", "b", "c") == "a:b:c"
//
// Two characters are reserved: ':' separates and '\' escapes. A component
// containing neither is emitted verbatim, so a key whose fields are already
// separator-free keeps its naive bytes. [Split] is the exact inverse. A key
// with an inner list nests by passing an inner Join's result as one outer
// component.
//
// When the components' total raw size exceeds [MaxComponentBytes], Join returns
// a fixed-size "sha256:" identity instead, so unbounded upstream data cannot
// make a key allocate without limit. Element boundaries survive the hash.
// Split refuses a hashed key, so call [IsHashed] first where keys are parsed
// back.
//
// Standard library only, zero dependencies. The TypeScript twin,
// @cplieger/keyenc, produces byte-identical keys. docs/how-it-works.md covers
// the grammar, nesting and the size bound in full.
package keyenc
