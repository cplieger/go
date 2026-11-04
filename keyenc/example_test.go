package keyenc_test

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cplieger/go/keyenc"
)

// A key whose fields carry no separator encodes exactly as its naive
// concatenation, which is what lets an existing key adopt this package without
// changing its bytes or invalidating persisted state.
func ExampleJoin() {
	fmt.Println(keyenc.Join("streams", "u-42", "1234", "3", "5"))
	// Output: streams:u-42:1234:3:5
}

// The escaping is what a naive concatenation lacks: moving the separator into a
// field changes the key, so two distinct field tuples can never collide.
func ExampleJoin_shiftedBoundary() {
	fmt.Println(keyenc.Join("a:b", "c"))
	fmt.Println(keyenc.Join("a", "b:c"))
	fmt.Println(keyenc.Join("a:b", "c") == keyenc.Join("a", "b:c"))
	// Output:
	// a\:b:c
	// a:b\:c
	// false
}

// A host carrying a port is the everyday case where content meets the
// separator. Split recovers it exactly, so a site that parses its keys back
// needs no second parser.
func ExampleSplit() {
	key := keyenc.Join("gitea", "git.example.com:3000")
	fmt.Println(key)

	parts, err := keyenc.Split(key)
	if err != nil {
		fmt.Println("split:", err)
		return
	}
	fmt.Printf("%q\n", parts)
	// Output:
	// gitea:git.example.com\:3000
	// ["gitea" "git.example.com:3000"]
}

// An inner list nests by composition: encode it, then pass the result as one
// outer component. The inner separators are escaped as the value becomes an
// outer component, so they cannot be read as outer boundaries at any depth.
func ExampleJoin_nesting() {
	languages := keyenc.Join("en", "fr")
	key := keyenc.Join("episode", "tvdb-1-s01e02", languages)
	fmt.Println(key)

	outer, _ := keyenc.Split(key)
	inner, _ := keyenc.Split(outer[2])
	fmt.Printf("%q\n", inner)
	// Output:
	// episode:tvdb-1-s01e02:en\:fr
	// ["en" "fr"]
}

// Above MaxComponentBytes the key becomes a fixed-size identity, so a caller
// folding unbounded upstream data into a key cannot be made to allocate without
// limit. Hashing is one-way, so a site that parses its keys back checks first.
func ExampleIsHashed() {
	oversized := keyenc.Join(strings.Repeat("x", keyenc.MaxComponentBytes+1))
	fmt.Println(keyenc.IsHashed(oversized), len(oversized))

	_, err := keyenc.Split(oversized)
	fmt.Println(errors.Is(err, keyenc.ErrHashed))
	// Output:
	// true 71
	// true
}

// Split accepts exactly what Join emits, so a key arriving from a persisted
// file, a URL segment or client storage is refused rather than normalized when
// it could not have been produced by this package.
func ExampleSplit_malformed() {
	_, err := keyenc.Split(`a\b`)
	fmt.Println(errors.Is(err, keyenc.ErrMalformed))
	// Output: true
}
