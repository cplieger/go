// Package other declares a type spelled as an instantiation's derived name
// in another package than the generic's.
package other

// List_Peer is spelled as generics.List[generics.Peer]'s derived name.
type List_Peer struct {
	Y int `json:"y"`
}
