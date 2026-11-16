// Package dotted lives under a path whose last element holds a dot, which
// reflect escapes as %2e in an instantiation's name.
package dotted

// Peer is a struct a test passes as a type argument.
type Peer struct {
	Z int `json:"z"`
}
