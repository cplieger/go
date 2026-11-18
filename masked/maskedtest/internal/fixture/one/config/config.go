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
