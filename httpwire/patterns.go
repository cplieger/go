package httpwire

import "slices"

// The only pattern keywords a document may carry: wiregen writes them from
// these constants, and the TS half matches each under ECMA-262's u flag, so
// both engines give one verdict (the shared corpus at
// web/testdata/wirepatterns.json holds them to it).
const (
	// PatternJSONString is the JSON text of a ,string string or enum.
	PatternJSONString = `^"(?:[^"\\\x00-\x1F]|\\(?:["\\/bfnrt]|u[0-9a-fA-F]{4}))*"$`
	// PatternJSONNumber is the JSON text of a ,string float.
	PatternJSONNumber = `^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`
	// PatternJSONInt is a ,string signed integer, and a signed integer map key.
	PatternJSONInt = `^-?(?:0|[1-9][0-9]*)$`
	// PatternJSONUint is a ,string unsigned integer, and an unsigned integer
	// map key.
	PatternJSONUint = `^(?:0|[1-9][0-9]*)$`
	// PatternJSONBool is the JSON text of a ,string boolean.
	PatternJSONBool = `^(?:true|false)$`
	// PatternHeader is a declared header's value: printable ASCII.
	PatternHeader = `^[\x20-\x7E]*$`
)

// wirePatterns is an array, not a slice or map, so no importer can extend
// the allow-list behind IsWirePattern.
var wirePatterns = [...]string{
	PatternJSONString, PatternJSONNumber, PatternJSONInt,
	PatternJSONUint, PatternJSONBool, PatternHeader,
}

// IsWirePattern reports whether p is one of the six Pattern constants.
func IsWirePattern(p string) bool {
	return slices.Contains(wirePatterns[:], p)
}
