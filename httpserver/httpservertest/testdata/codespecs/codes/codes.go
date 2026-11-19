// Package codes declares codes and a non-writer for the app fixture.
package codes

import hs "github.com/cplieger/go/httpserver/v2"

const (
	Shared       hs.ErrorCode = "shared_code"
	Unwritten    hs.ErrorCode = "unwritten_code"
	prefix                    = "concat"
	Concatenated              = prefix + "_code"
)

// Register takes a code without answering it.
func Register(hs.ErrorCode) {}

// Responder holds a func value that answers codes.
type Responder struct {
	Respond hs.ErrorResponder
}

// Spec is another package's struct with a code field.
type Spec struct {
	Code hs.ErrorCode
}
