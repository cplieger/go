// Package generics provides generic type fixtures for wiregen's
// instantiation tests.
package generics

import (
	"encoding/json"

	"github.com/cplieger/go/wiregen/v2/testdata/generics/other"
)

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

// Holder carries instantiations as fields.
type Holder struct {
	Peers  List[Peer] `json:"peers"`
	Counts List[int]  `json:"counts"`
}

// HasUnregistered carries an instantiation a test does not register.
type HasUnregistered struct {
	Names List[string] `json:"names"`
}

// HasPeerList carries List[Peer] alone, for a test that registers its
// derived name's namesake List_Peer instead.
type HasPeerList struct {
	Peers List[Peer] `json:"peers"`
}

// HasOtherPeers carries a List whose argument is another package's Peer.
type HasOtherPeers struct {
	Peers List[other.Peer] `json:"peers"`
}

// ListAlias is a generic alias of List.
type ListAlias[T any] = List[T]

// Dir has a request schema apart from its response one: a nil map is
// written omitted but may be read as null.
type Dir[T any] struct {
	M map[string]T `json:"m,omitempty"`
}

// Dir_PeerInput is spelled as Dir[Peer]'s request component.
type Dir_PeerInput struct {
	Z int `json:"z"`
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

// Num constrains its type parameter to a number.
type Num[T int | float64] struct {
	V T `json:"v"`
}

// Pair_int_int is an enum spelled as Pair[int, int]'s derived name.
type Pair_int_int string

// PairIntIntX is Pair_int_int's one value.
const PairIntIntX Pair_int_int = "x"

// Slot holds a T encoding/json always writes, as null when nil, beside
// one omitempty drops, under a kind its const tag pins.
type Slot[T any] struct {
	Val  *T     `json:"val"`
	Opt  *T     `json:"opt,omitempty"`
	Kind string `json:"kind" wiregen:"const=slot"`
}
