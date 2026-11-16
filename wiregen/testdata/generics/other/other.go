// Package other declares namesakes, in another package than the generic's,
// of an instantiation's derived name and of generics' Peer and Status.
package other

// List_Peer is spelled as generics.List[generics.Peer]'s derived name.
type List_Peer struct {
	Y int `json:"y"`
}

// Peer shares generics.Peer's name.
type Peer struct {
	Z int `json:"z"`
}

// Status shares generics.Status's name.
type Status string

// StatusOn is a Status value.
const StatusOn Status = "on"
