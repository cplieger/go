package httpserver

import "strings"

// tokenLen is the length of the RFC 9110 section 5.6.2 token at the front of s.
func tokenLen(s string) int {
	for i := range len(s) {
		if !tokenByte(s[i]) {
			return i
		}
	}
	return len(s)
}

func tokenByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func qdtextByte(c byte) bool {
	return c == '\t' || c == ' ' || c == 0x21 || c >= 0x23 && c <= 0x5b || c >= 0x5d && c <= 0x7e || c >= 0x80
}

func quotedPairByte(c byte) bool {
	return c == '\t' || c >= 0x20 && c != 0x7f
}
