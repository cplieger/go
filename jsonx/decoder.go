package jsonx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sentinel errors, matched with errors.Is through the wrapped errors the
// budget and cap checks return.
var (
	// ErrElementBudget reports that the Decoder's aggregate element budget
	// was exhausted: the total number of array elements and map entries
	// decoded across every Array and Map call on this Decoder exceeded the
	// budget given to NewDecoder.
	ErrElementBudget = errors.New("jsonx: element budget exceeded")
	// ErrArrayCap reports that one array exceeded its per-Array cardinality
	// cap. The wrapping error names the array via Array's what argument.
	ErrArrayCap = errors.New("jsonx: array cardinality cap exceeded")
	// ErrMapCap reports that one map exceeded its per-Map cardinality cap.
	// The wrapping error names the map via Map's what argument, never a key:
	// at this boundary the keys are attacker-shaped text.
	ErrMapCap = errors.New("jsonx: map cardinality cap exceeded")
	// ErrDuplicateKey reports that Preflight found an object repeating a key
	// (matched case-insensitively). The wrapping error carries a bounded,
	// quoted snippet of the offending key.
	ErrDuplicateKey = errors.New("jsonx: duplicate object key")
)

// maxDepth is the nesting ceiling every walk in this package observes: at most
// maxDepth containers open at once. encoding/json enforces this since Go 1.27
// (its jsontext Decoder refuses to read the token that would open container
// maxDepth+1) and surfaces it as *json.SyntaxError; this package does not
// translate it, because jsontext's own depth error is unexported and its
// documented contents are subject to change. It is unexported because the
// value is the standard library's, which a later Go may move; the package
// promises only that its walks accept exactly the depth json.Unmarshal does.
// Do not lower it to compensate for a local check:
// TestPreflightDepthMatchesUnmarshal pins that Preflight's depth acceptance
// set is exactly json.Unmarshal's, and a lower value would make Preflight
// reject a body Unmarshal accepts.
const maxDepth = 10000

// maxKeySnippet bounds the untrusted key text a duplicate-key error renders,
// so an oversized upstream key cannot balloon an error string or a log line.
// The key is rendered with %q, which escapes controls and newlines, so the
// message stays single-line and safe to log as-is; a caller with a stricter
// text policy applies it to its own wrapping message.
const maxKeySnippet = 64

// Decoder walks one JSON value as a token stream, charging every decoded
// array element and map entry against an aggregate budget. It is not safe for
// concurrent use.
type Decoder struct {
	dec      *json.Decoder
	limit    int
	elements int
}

// NewDecoder returns a Decoder reading from r with the given aggregate
// element budget. Construction through NewDecoder is mandatory: the zero
// Decoder has no underlying json.Decoder, so every decoding method
// nil-dereferences. Every array element decoded through Array and every map
// entry decoded through Map is charged against the budget, whichever
// container it belongs to, so deeply nested or repeated containers cannot
// multiply a per-container cap. elementBudget <= 0 means no aggregate budget
// (per-container caps still apply). A caller carrying one budget across
// several bodies uses Next rather than passing a remainder here, since a
// remainder of 0 would read as no budget at all.
func NewDecoder(r io.Reader, elementBudget int) *Decoder {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	return &Decoder{dec: dec, limit: elementBudget}
}

// Next returns a Decoder reading from r under d's budget, with everything d
// has charged already spent, so a caller paginating across several bodies
// bounds the whole walk by one budget. Once the budget is spent, the first
// element the new Decoder reaches fails with ErrElementBudget. d itself is
// not changed, and the two Decoders share nothing but the starting count.
func (d *Decoder) Next(r io.Reader) *Decoder {
	next := NewDecoder(r, d.limit)
	next.elements = d.elements
	return next
}

// Elements reports how many array elements and map entries have been charged
// against the budget so far, counting what a Decoder built by Next inherited.
func (d *Decoder) Elements() int { return d.elements }

// More reports whether the current array or object has another element,
// delegating to json.Decoder.More.
func (d *Decoder) More() bool { return d.dec.More() }

// Decode decodes the next value via json.Decoder.Decode, giving
// stdlib-identical handling for scalar and leaf values, except that a number
// decoded into an untyped any is a json.Number rather than a float64: the
// Decoder calls UseNumber, so a large id is never rounded.
func (d *Decoder) Decode(v any) error { return d.dec.Decode(v) }

// count charges one decoded array element or map entry against the aggregate
// budget.
func (d *Decoder) count() error {
	d.elements++
	if d.limit > 0 && d.elements > d.limit {
		return fmt.Errorf("%w: %d", ErrElementBudget, d.limit)
	}
	return nil
}

// Open consumes the opening delimiter of a container. It reports ok=false
// (without error) for a JSON null, so the caller can implement Unmarshal's
// null-into-value semantics, and errors on any other token.
func (d *Decoder) Open(delim json.Delim) (ok bool, err error) {
	t, err := d.dec.Token()
	if err != nil {
		return false, err
	}
	if t == nil {
		return false, nil
	}
	if got, isDelim := t.(json.Delim); !isDelim || got != delim {
		return false, fmt.Errorf("jsonx: expected %q, got %s", rune(delim), tokenKind(t))
	}
	return true, nil
}

// tokenKind names a token by its kind and never by its content, so a shape
// mismatch error quotes no document bytes: a wire string can be any length
// and carry any control byte.
func tokenKind(t json.Token) string {
	switch v := t.(type) {
	case json.Delim:
		return fmt.Sprintf("%q", rune(v))
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("a %T", t)
	}
}

// Close consumes a container's closing delimiter (the token json.Decoder
// guarantees once More reports false).
func (d *Decoder) Close() error {
	_, err := d.dec.Token()
	return err
}

// Key reads the next object key.
func (d *Decoder) Key() (string, error) {
	t, err := d.dec.Token()
	if err != nil {
		return "", err
	}
	s, isString := t.(string)
	if !isString {
		return "", fmt.Errorf("jsonx: expected an object key, got %s", tokenKind(t))
	}
	return s, nil
}

// Skip consumes and discards one whole value (scalar or container) without
// materializing it — json.Unmarshal's unknown-field handling without the
// allocation.
func (d *Decoder) Skip() error {
	depth := 0
	for {
		t, err := d.dec.Token()
		if err != nil {
			return err
		}
		if delim, isDelim := t.(json.Delim); isDelim {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

// Object decodes one JSON object by walking its keys: field is invoked once
// per key and must consume exactly that key's value (Decode a known field,
// Skip an unknown one, or recurse into Object/Array for a container). A
// JSON null in place of the object is a no-op — field is never invoked and
// the caller's target stays untouched — matching json.Unmarshal's
// null-into-struct semantics, which is what makes a duplicate key like
// `"expand": null` unable to wipe an already-decoded value. Dispatch keys
// with strings.EqualFold for Unmarshal's case-insensitive field matching.
func (d *Decoder) Object(field func(key string) error) error {
	ok, err := d.Open('{')
	if err != nil || !ok {
		return err
	}
	for d.dec.More() {
		k, err := d.Key()
		if err != nil {
			return err
		}
		if err := field(k); err != nil {
			return err
		}
	}
	return d.Close()
}

// End verifies the input is exhausted after the top-level value: anything
// but io.EOF — trailing data or a syntax error — is rejected, matching
// json.Unmarshal's whole-input strictness (json.Decoder alone would leave
// trailing garbage unread and unreported).
func (d *Decoder) End() error {
	if _, err := d.dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("jsonx: trailing data after top-level value: %w", err)
		}
		return errors.New("jsonx: trailing data after top-level value")
	}
	return nil
}

// Array decodes one JSON array under the shared bounded lifecycle: per-array
// cap check BEFORE the element is counted (ErrArrayCap, named by what),
// aggregate budget charge BEFORE the element is allocated (ErrElementBudget),
// decode INTO the regrown element, then truncation at the decoded length.
//
// prior is the already-decoded value of a previous occurrence of the same
// key, giving json.Unmarshal's duplicate-key slice semantics: a
// within-capacity regrow re-exposes the retained backing element (so
// field-wise merge matches stdlib), the result truncates to the new array's
// length, and an empty re-occurrence replaces the slice with a fresh one so no
// later occurrence can re-expose its backing. A JSON null yields nil without
// error (Unmarshal's null-into-slice); `[]` yields an empty non-nil slice.
// Pass maxElems <= 0 for no per-array cap (the aggregate budget still
// applies).
//
// On error the returned slice holds the elements decoded so far, as Map's
// map does: prior unchanged when the value is not an array at all, otherwise
// the first elements up to the one that failed. Elements decode in place into
// prior's backing array while it has capacity, as json.Unmarshal regrows a
// slice, so prior's own elements show what was decoded into them. When
// decodeElem fails at an index within prior's length, that element holds
// whatever decodeElem wrote before failing: the previous value if it wrote
// nothing, a partial one if it wrote some. Array's own cap and budget checks
// run before decodeElem, so they leave that element as it was. The error,
// not the slice, is the result.
func (d *Decoder) Array[T any](prior []T, maxElems int, what string, decodeElem func(*T) error) ([]T, error) {
	ok, err := d.Open('[')
	if err != nil {
		// Nothing was consumed into the slice, so the caller's own value
		// stands, as Map leaves its map.
		return prior, err
	}
	if !ok {
		return nil, nil
	}
	s := prior
	n := 0
	for d.dec.More() {
		if maxElems > 0 && n >= maxElems {
			return s[:n], fmt.Errorf("%s: %w: %d", what, ErrArrayCap, maxElems)
		}
		if err := d.count(); err != nil {
			return s[:n], err
		}
		s = growForIndex(s, n)
		if err := decodeElem(&s[n]); err != nil {
			return s[:n], err
		}
		n++
	}
	return truncateArray(s, n), d.Close()
}

// growForIndex ensures the slice covers index n, matching json.Unmarshal's
// slice-regrow semantics for duplicate keys: within retained capacity the
// existing backing element is re-exposed (stdlib SetLen), beyond capacity a
// zero element is appended (stdlib Grow reallocates; the new tail is zero).
func growForIndex[T any](s []T, n int) []T {
	if n < len(s) {
		return s
	}
	if n < cap(s) {
		return s[:n+1]
	}
	var zero T
	return append(s, zero)
}

// truncateArray finalizes a decoded array at n elements, matching
// json.Unmarshal's end-of-array semantics: an empty array REPLACES the
// slice with a fresh empty non-nil slice (stdlib MakeSlice(0,0) — allocated,
// so `[]` is distinguishable from null's nil, and no retained backing a
// later duplicate occurrence could re-expose), a non-empty one truncates in
// place (stdlib SetLen).
func truncateArray[T any](s []T, n int) []T {
	if n == 0 {
		return []T{}
	}
	return s[:n]
}

// Map decodes one JSON object into a Go map under the same bounded lifecycle
// Array gives a slice: per-map cap check BEFORE the entry costs anything
// (ErrMapCap, named by what), aggregate budget charge BEFORE the entry is
// read (ErrElementBudget), then key, value, store. decodeValue is invoked
// once per entry with that entry's key and a pointer to the zero value it
// must decode into; the key is passed so a caller with a per-key policy can
// apply it without a second walk. Pass maxEntries <= 0 for no per-map cap
// (the aggregate budget still applies). The charge lands before the key is
// read because the key is itself an unbounded allocation from the wire.
//
// Map is a twin of Array rather than an exported entry-charge primitive so
// every caller gets the check order and the Unmarshal semantics applied
// consistently by the walk. A caller whose map policy does not fit this
// shape keeps the token primitives (Open, Key, Decode, Skip, More, Close)
// and its own accounting instead.
//
// The map semantics reproduce json.Unmarshal's, which differ from Object and
// Array in three ways:
//
//   - A JSON null yields nil, wiping an already-decoded earlier occurrence
//     of the same key (Unmarshal's null-into-slice handling, not its
//     null-into-struct no-op).
//   - Each entry decodes into a fresh zero value, so a duplicate key
//     REPLACES rather than merges field-wise, and a null value stores the
//     zero value.
//   - An empty object `{}` allocates an empty non-nil map when prior is
//     nil, but does not replace a non-nil prior (unlike Array's empty
//     re-occurrence).
//
// Keys compare by the map's own exact equality, so "a" and "A" are two
// entries here even though Unmarshal matches struct fields case-insensitively.
//
// On error the returned map holds the entries decoded so far, exactly as a
// failed json.Unmarshal leaves them in the caller's map; the error, not the
// map, is the result.
func (d *Decoder) Map[V any](prior map[string]V, maxEntries int, what string, decodeValue func(key string, v *V) error) (map[string]V, error) {
	ok, err := d.Open('{')
	if err != nil {
		// Nothing was consumed into the map, so the caller's own value stands
		// - Unmarshal leaves a target untouched when the value is the wrong
		// shape for it.
		return prior, err
	}
	if !ok {
		return nil, nil
	}
	m := prior
	if m == nil {
		m = make(map[string]V)
	}
	n := 0
	for d.dec.More() {
		if maxEntries > 0 && n >= maxEntries {
			return m, fmt.Errorf("%s: %w: %d", what, ErrMapCap, maxEntries)
		}
		if err := d.count(); err != nil {
			return m, err
		}
		key, err := d.Key()
		if err != nil {
			return m, err
		}
		var v V
		if err := decodeValue(key, &v); err != nil {
			return m, err
		}
		m[key] = v
		n++
	}
	return m, d.Close()
}

// Preflight bounds no size or count; a Decoder does that. It walks one
// complete JSON value from r, decoding nothing, and rejects the first
// repeated object key, matched case-insensitively as encoding/json matches
// fields, with ErrDuplicateKey, and trailing data after the value. It checks
// structure only, so an accepted body can still fail the decode, and it judges
// neither content nor UTF-8 validity. It accepts exactly the nesting depth
// json.Unmarshal accepts. Its cost is linear in the body, so the caller's body
// cap bounds it. It takes an io.Reader because it runs on encoding/json's
// tokenizer.
func Preflight(r io.Reader) error {
	d := NewDecoder(r, 0)
	if err := d.preflightValue(); err != nil {
		return err
	}
	return d.End()
}

// preflightValue consumes exactly one JSON value, recursing into objects and
// arrays. A scalar is consumed by the leading Token call; a container is
// closed by the trailing one. The recursion is bounded by maxDepth without a
// check here: encoding/json refuses to read the token that opens container
// maxDepth+1, so the Token call below fails before the frame that would
// exceed it is entered.
func (d *Decoder) preflightValue() error {
	t, err := d.dec.Token()
	if err != nil {
		return err
	}
	delim, isDelim := t.(json.Delim)
	if !isDelim {
		return nil
	}
	if err := d.preflightContainer(delim); err != nil {
		return err
	}
	return d.Close()
}

// preflightContainer traverses the members of a container whose opening
// delimiter preflightValue has read. The closing delimiter stays
// preflightValue's, so container framing has one owner.
func (d *Decoder) preflightContainer(delim json.Delim) error {
	switch delim {
	case '{':
		return d.preflightObject()
	case '[':
		for d.dec.More() {
			if err := d.preflightValue(); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("jsonx: unexpected JSON delimiter %q", rune(delim))
	}
}

// preflightObject consumes an object's members after its opening delimiter,
// failing on the first repeated key. Keys are held in a fold-canonicalized
// set, so a key-dense object costs O(keys) rather than the O(keys^2) an
// EqualFold scan over the accumulated keys would.
func (d *Decoder) preflightObject() error {
	seen := make(map[string]struct{})
	for d.dec.More() {
		key, err := d.Key()
		if err != nil {
			return err
		}
		folded := foldKey(key)
		if _, dup := seen[folded]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateKey, keySnippet(key))
		}
		seen[folded] = struct{}{}
		if err := d.preflightValue(); err != nil {
			return err
		}
	}
	return nil
}

// foldKey canonicalizes each rune to the smallest member of its simple
// case-folding orbit, so map equality on the result is exactly
// strings.EqualFold equality without the per-key linear scan.
func foldKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		c := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < c {
				c = f
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}

// keySnippet truncates an untrusted key to maxKeySnippet bytes on a rune
// boundary, so the error text stays bounded and valid UTF-8.
func keySnippet(key string) string {
	if len(key) <= maxKeySnippet {
		return key
	}
	cut := maxKeySnippet
	for cut > 0 && !utf8.RuneStart(key[cut]) {
		cut--
	}
	return key[:cut]
}
