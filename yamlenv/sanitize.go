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
	textUnmarshaler  = " (TextUnmarshaler)"
)

// goYAMLMarkers are fragments of go-yaml's own error text: its Error()
// wrappers and the fixed words of every message that embeds document text. A
// config type's error that copies go-yaml's with %v or errors.New loses the
// *LoadError chain, so these words are all that is left to recognise it by.
var goYAMLMarkers = []string{
	"go-yaml load error", ": yaml: ", wrongTypePrefix, dupKeyDefinedAt, unknownKeyInType,
	fieldSetInType, " as a map key", "unknown anchor '", "alias-data", v3HookMarker,
}

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
	// src is set by Load only. A config type's own error can be worded like
	// go-yaml's, so with src a rebuilt entry keeps a type name, an echoed key
	// or a line number only when the config type or the document has it.
	src            *provenance
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
// (the key only under WithUnknownKeyEcho(true)); anything else keeps at most
// a position. nil returns nil, and the result never wraps err. Called directly
// it trusts go-yaml's wording, so a caller's own error worded like go-yaml's
// keeps its text after " into ", and under the echo its key; Load checks both.
func SanitizeDecodeError(err error, opts ...SanitizeOption) error {
	var o sanitizeOptions
	for _, opt := range opts {
		opt(&o)
	}
	return sanitize(err, o)
}

// sanitize is SanitizeDecodeError with its options applied.
func sanitize(err error, o sanitizeOptions) error {
	if err == nil {
		return nil
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
		return rebuildDuplicateKey(e.Mark, rest, o.src)
	}
	if key, ok := unknownKey(e.Message); ok {
		if o.echoUnknownKey && o.src.vouchesKey(e.Mark, key) {
			return fmt.Sprintf("unknown configuration key %q", key)
		}
		return "unknown configuration key " + redacted
	}
	if rest, ok := strings.CutPrefix(e.Message, wrongTypePrefix); ok {
		if source, dest, ok := strings.CutLast(rest, wrongTypeInto); ok {
			if !o.src.vouchesType(dest) {
				dest = redacted
			}
			return wrongTypePrefix + keptSource(source) + wrongTypeInto + dest
		}
	}
	return entryWithheldMessage
}

// rebuildDuplicateKey rebuilds a duplicate-key entry from the message text
// after its prefix, keeping only the first definition's line.
func rebuildDuplicateKey(at yaml.Mark, rest string, src *provenance) string {
	if _, line, ok := strings.CutLast(rest, dupKeyDefinedAt); ok {
		if first, ok := firstDefinition(at, line, src); ok {
			return dupKeyPrefix + redacted + dupKeyDefinedAt + strconv.Itoa(first)
		}
	}
	return entryWithheldMessage
}

// firstDefinition parses a duplicate-key entry's first-definition line.
// go-yaml names an earlier key of the same mapping, so the line is never
// after the entry's own; with src, keys must sit at both positions.
func firstDefinition(at yaml.Mark, line string, src *provenance) (int, bool) {
	if !allDigits(line) {
		return 0, false
	}
	first, err := strconv.Atoi(line)
	if err != nil || first < 1 || first > at.Line {
		return 0, false
	}
	return first, src.vouchesDuplicate(at, first)
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

// isGoYAMLMessage reports whether msg carries go-yaml's own error text,
// whole or copied into a config type's error, as opposed to text the config
// type's UnmarshalYAML wrote itself.
func isGoYAMLMessage(msg string) bool {
	if strings.HasPrefix(msg, "yaml: ") {
		return true
	}
	for _, m := range goYAMLMarkers {
		if strings.Contains(msg, m) {
			return true
		}
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
