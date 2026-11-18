// Package httpfield parses RFC 9110 list-valued header fields for the
// httpserver module's packages.
package httpfield

import (
	"iter"
	"net/http"
	"strings"
)

// Syntax is how a list-valued field quotes its elements, which decides
// whether a comma separates them (RFC 9110 section 5.6.1).
type Syntax uint8

const (
	// Tokens has no quoting: a '"' is an ordinary byte, so a client's
	// stray quote cannot swallow the elements after it.
	Tokens Syntax = iota
	// QuotedStrings holds RFC 9110 section 5.6.4 quoted-strings, with
	// quoted-pair escapes.
	QuotedStrings
	// EntityTags holds RFC 9110 section 8.8.3 opaque-tags: quoted, with
	// no escape, so a '\' before the closing quote is part of the tag.
	EntityTags
)

// List yields h's name field as one RFC 9110 section 5.6.1 list: field
// lines joined with "," (section 5.2), so a proxy adding a line cannot leave
// a client's line read alone; split at each comma outside quotes; elements
// trimmed of OWS; empty elements dropped. A caller bounding its elements
// stops the loop; the scan up to there is linear in the field's length.
func List(h http.Header, name string, syntax Syntax) iter.Seq[string] {
	return func(yield func(string) bool) {
		field := strings.Join(h.Values(name), ",")
		start, inQuote := 0, false
		for i := 0; i < len(field); i++ {
			switch c := field[i]; {
			case syntax == QuotedStrings && inQuote && c == '\\':
				i++
			case syntax != Tokens && c == '"':
				inQuote = !inQuote
			case c == ',' && !inQuote:
				if elem := strings.Trim(field[start:i], " \t"); elem != "" && !yield(elem) {
					return
				}
				start = i + 1
			}
		}
		if elem := strings.Trim(field[start:], " \t"); elem != "" {
			yield(elem)
		}
	}
}

// IfNoneMatch reports whether h's If-None-Match, over every field line, is
// "*" as the whole field or lists an entity-tag weakly equal to etag, a
// strong entity-tag (RFC 9110 section 13.1.2).
func IfNoneMatch(h http.Header, etag string) bool {
	members, wildcard := 0, false
	for member := range List(h, "If-None-Match", EntityTags) {
		members++
		wildcard = member == "*"
		if strings.TrimPrefix(member, "W/") == etag {
			return true
		}
	}
	return wildcard && members == 1
}
