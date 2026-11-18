// Package config shares its package name with fixture/one/config, so a test
// can check that the checks key a type by its import path.
package config

// Box holds a credential-named text field.
type Box struct {
	Token string `json:"token"`
}
