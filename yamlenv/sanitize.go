package yamlenv

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Value-independent parts of go-yaml v4's constructor-stage messages
// (LoadError.Message, no position prefix):
//
//	cannot construct !!str `<excerpt>` into <Go type>
//	mapping key "<key>" already defined at line <N>
//	field <key> not found in type <Go type>  (or "already set in type")
//
// Excerpt and key are document text and may repeat a marker, so cut at the LAST.
const (
	wrongTypePrefix  = "cannot construct "
	wrongTypeInto    = " into "
	dupKeyPrefix     = "mapping key "
	dupKeyDefinedAt  = " already defined at line "
	fieldPrefix      = "field "
	unknownKeyInType = " not found in type "
	fieldSetInType   = " already set in type "
	v3HookMarker     = "cannot invoke v3 Node hooks"
)

// coreTags are the tags go-yaml names on its own. A wrong-type entry repeats
// the node's tag as the document wrote it, so any other tag is document text.
var coreTags = map[string]bool{
	"!!binary": true, "!!bool": true, "!!float": true, "!!int": true, "!!map": true,
	"!!null": true, "!!seq": true, "!!str": true, "!!timestamp": true,
}

// withheldMessage is the fixed fallback for any error the sanitizer cannot
// rebuild from value-independent parts. It embeds nothing from the original.
const withheldMessage = "configuration could not be decoded (details withheld: they may embed an expanded secret)"

// entryWithheldMessage is the fixed per-entry fallback for a LoadErrors entry
// that matches none of the rebuilt shapes.
const entryWithheldMessage = "value rejected (details withheld: they may embed an expanded secret)"

const redacted = "<redacted>"

// SanitizeOption configures SanitizeDecodeError. The zero configuration is
// the safe default: everything value-derived is redacted.
type SanitizeOption func(*sanitizeOptions)

// sanitizeOptions carries the sanitizer's policy switches.
type sanitizeOptions struct {
	echoUnknownKey bool
}

// WithUnknownKeyEcho sets whether the key name of an unknown-key entry (from
// a go-yaml load with WithKnownFields) is kept in the sanitized error:
// "line N, column C: unknown configuration key "X"". The key name is what the
// operator needs to fix a misspelled or misplaced key, and when the strict
// load runs on the PRE-expansion document a key cannot carry an expanded
// secret, but that safety argument is the caller's to make, so the default
// redacts and the echo is an explicit opt-in. When the option repeats, the
// last one wins.
func WithUnknownKeyEcho(echo bool) SanitizeOption {
	return func(o *sanitizeOptions) { o.echoUnknownKey = echo }
}

// SanitizeDecodeError rewrites a go-yaml v4 load error so no fragment of a
// document value survives, because Expand can put a secret where go-yaml's
// errors quote one. A *yaml.LoadErrors keeps each entry's position and, for a
// wrong-type, duplicate-key or unknown-key entry, its value-free structure
// (the key only under WithUnknownKeyEcho(true)); any other entry becomes a
// fixed message. Another *yaml.LoadError keeps only its position, anything
// else becomes a fixed message, and nil returns nil. The result never wraps
// err, so no Unwrap or errors.As reaches the withheld text.
func SanitizeDecodeError(err error, opts ...SanitizeOption) error {
	if err == nil {
		return nil
	}
	var o sanitizeOptions
	for _, opt := range opts {
		opt(&o)
	}
	if errs, ok := errors.AsType[*yaml.LoadErrors](err); ok && len(errs.Errors) > 0 {
		entries := make([]string, 0, len(errs.Errors))
		for _, e := range errs.Errors {
			entries = append(entries, sanitizeEntry(e, o))
		}
		return errors.New("decode errors: " + strings.Join(entries, "; "))
	}
	if e, ok := errors.AsType[*yaml.LoadError](err); ok {
		return errors.New(locator(e.Mark) + withheldMessage)
	}
	return errors.New(withheldMessage)
}

// sanitizeEntry rebuilds one LoadErrors entry from its position and, for a
// constructor-stage entry, the value-independent parts of its message.
func sanitizeEntry(e *yaml.LoadError, o sanitizeOptions) string {
	if e == nil {
		return entryWithheldMessage
	}
	return locator(e.Mark) + rebuildMessage(e, o)
}

// rebuildMessage returns the value-independent form of an entry's message,
// or the fixed per-entry fallback.
func rebuildMessage(e *yaml.LoadError, o sanitizeOptions) string {
	if e.Stage != yaml.ConstructorStage {
		return entryWithheldMessage
	}
	if rest, ok := strings.CutPrefix(e.Message, dupKeyPrefix); ok {
		if _, line, ok := strings.CutLast(rest, dupKeyDefinedAt); ok && allDigits(line) {
			return dupKeyPrefix + redacted + dupKeyDefinedAt + line
		}
		return entryWithheldMessage
	}
	if key, ok := unknownKey(e.Message); ok {
		if o.echoUnknownKey {
			return fmt.Sprintf("unknown configuration key %q", key)
		}
		return "unknown configuration key " + redacted
	}
	if rest, ok := strings.CutPrefix(e.Message, wrongTypePrefix); ok {
		if source, dest, ok := strings.CutLast(rest, wrongTypeInto); ok {
			return wrongTypePrefix + keptSource(source) + wrongTypeInto + dest
		}
	}
	return entryWithheldMessage
}

// keptSource rebuilds a wrong-type entry's "<tag>" or "<tag> `<excerpt>`"
// source: a core tag survives, the excerpt never does.
func keptSource(source string) string {
	tag, _, hasExcerpt := strings.Cut(source, " ")
	if !coreTags[tag] {
		return redacted
	}
	if hasExcerpt {
		return tag + " " + redacted
	}
	return tag
}

// unknownKey returns the key of a go-yaml unknown-key message. The Go type
// after the marker is checked for the "already set" shape so a key that
// embeds the marker cannot turn that message into this one.
func unknownKey(msg string) (key string, ok bool) {
	rest, ok := strings.CutPrefix(msg, fieldPrefix)
	if !ok {
		return "", false
	}
	key, typ, ok := strings.CutLast(rest, unknownKeyInType)
	if !ok || strings.Contains(typ, fieldSetInType) {
		return "", false
	}
	return key, true
}

// isGoYAMLMessage reports whether msg has the shape of a message go-yaml's
// constructor builds itself, as opposed to one a config type's UnmarshalYAML
// returned.
func isGoYAMLMessage(msg string) bool {
	switch {
	case strings.HasPrefix(msg, wrongTypePrefix) && strings.Contains(msg, wrongTypeInto),
		strings.HasPrefix(msg, dupKeyPrefix) && strings.Contains(msg, dupKeyDefinedAt),
		strings.HasPrefix(msg, fieldPrefix) && (strings.Contains(msg, unknownKeyInType) || strings.Contains(msg, fieldSetInType)),
		strings.Contains(msg, v3HookMarker):
		return true
	}
	return false
}

// locator renders a Mark's integer fields as "line N, column C: ", or
// "line N: " without a column, or "" when the line is unknown.
func locator(m yaml.Mark) string {
	switch {
	case m.Line <= 0:
		return ""
	case m.Column <= 0:
		return "line " + strconv.Itoa(m.Line) + ": "
	default:
		return "line " + strconv.Itoa(m.Line) + ", column " + strconv.Itoa(m.Column) + ": "
	}
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
