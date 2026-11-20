package envx

import "strconv"

// Key is the name of an environment variable, such as "APP_LISTEN", never its
// value. A plain string variable in a key position does not compile, though an
// untyped literal converts.
//
// A Key that is not a variable name ([A-Za-z_][A-Za-z0-9_]*, never empty)
// panics on first use, naming the string, because it would otherwise read as
// unset and return a default forever.
type Key string

// keyEchoMax bounds the key an invalid-name panic echoes, since a built name
// can be long or carry a value fragment.
const keyEchoMax = 64

// validate panics when k is not a variable name. Every getter calls it before
// reading anything, so a malformed key fails at its first use.
func (k Key) validate() {
	if !k.isName() {
		panic("envx: " + quoteCapped(string(k), keyEchoMax) + " is not an environment variable name")
	}
}

// quoteCapped returns strconv.Quote(s), or when that exceeds limit bytes (at
// least 5) the quote of the longest prefix of s that fits with "..." before the
// closing quote. It cuts s and quotes again, never the quoted form, because a
// byte cut there can split an escape or a rune and leave invalid UTF-8 in a
// panic message that reaches a log store.
func quoteCapped(s string, limit int) string {
	if quoted := strconv.Quote(s); len(quoted) <= limit {
		return quoted
	}
	// The truncated form is len(ellipsis) bytes longer than Quote(prefix).
	const ellipsis = "..."
	end := 0
	// Ranging over a string stops only on boundaries Quote renders whole.
	for i := range s {
		if len(strconv.Quote(s[:i]))+len(ellipsis) > limit {
			break
		}
		end = i
	}
	quoted := strconv.Quote(s[:end])
	return quoted[:len(quoted)-1] + ellipsis + `"`
}

// isName reports whether k matches [A-Za-z_][A-Za-z0-9_]*. The grammar is
// narrower than POSIX tolerates on purpose, so a typo such as "app.listen" is
// caught instead of read as unset.
func (k Key) isName() bool {
	if k == "" {
		return false
	}
	for i := range len(k) {
		switch c := k[i]; {
		case c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
