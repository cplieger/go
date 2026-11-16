// Package generics provides generic type fixtures for wiregen's
// instantiation tests.
package generics

import "encoding/json"

// Peer is a struct a test registers and passes as a type argument.
type Peer struct {
	Name string `json:"name"`
	ID   int    `json:"id"`
}

// List is a cursor page of T.
type List[T any] struct {
	Next  string `json:"next,omitempty"`
	Items []T    `json:"items"`
}

// Pair holds a key and a value of two type parameters.
type Pair[K, V any] struct {
	Key K `json:"key"`
	Val V `json:"val"`
}

// Tree reaches its own instance.
type Tree[T any] struct {
	Kids  []Tree[T] `json:"kids"`
	Value T         `json:"value"`
}

// Page embeds an instantiation beside a field of its own.
type Page struct {
	List[Peer]
	Total int `json:"total"`
}

// PeerList declares by hand the fields List[Peer] has.
type PeerList struct {
	Next  string `json:"next,omitempty"`
	Items []Peer `json:"items"`
}

// Holder carries instantiations as fields.
type Holder struct {
	Peers  List[Peer] `json:"peers"`
	Counts List[int]  `json:"counts"`
}

// HasUnregistered carries an instantiation a test does not register.
type HasUnregistered struct {
	Names List[string] `json:"names"`
}

// Aliased carries instantiations whose argument is a predeclared alias.
type Aliased struct {
	Bytes List[byte] `json:"bytes"`
	Runes List[rune] `json:"runes"`
}

// List_Peer is spelled as List[Peer]'s derived name.
type List_Peer struct {
	X int `json:"x"`
}

// Maß has a name outside OpenAPI's component-name set.
type Maß struct {
	N int `json:"n"`
}

// Unregistered is a struct no test registers.
type Unregistered struct {
	A int `json:"a"`
}

// Command is a union whose payload a test types with an instantiation.
type Command struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// A, A_B, B_C and C make Pair[A_B, C] and Pair[A, B_C] derive one name.
type (
	A   struct{}
	A_B struct{}
	B_C struct{}
	C   struct{}
)

// Status is a string enum a test passes as a type argument.
type Status string

// Status values.
const (
	StatusUp   Status = "up"
	StatusDown Status = "down"
)
