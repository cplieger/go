// Package config shares its package name with fixture/two/config, so a test
// can check that the checks key a type by its import path.
package config

// Box holds a credential-named text field.
type Box struct {
	Token string `json:"token"`
}

// Settings has an unnamed struct type with an unexported field, which
// reflect.Type.String writes alike in both fixture packages.
var Settings = struct{ token string }{token: "one"}

// Tok is a named text type: reflect.Type.String writes struct{ T Tok } alike
// in both fixture packages.
type Tok string

// Embedded embeds a predeclared type: reflect.Type.String writes it alike in
// both fixture packages, yet the two types differ.
var Embedded = struct {
	string
	S string
}{string: "e", S: "s"}

// Iface holds an interface with an unexported method, whose method set
// differs between the two fixture packages.
var Iface = struct {
	I interface{ m() }
	S string
}{I: nil, S: "i"}
