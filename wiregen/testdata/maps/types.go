// Package maps provides named map fixtures for wiregen's map type tests.
package maps

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Settings is an open settings patch: any key, each value raw JSON.
type Settings map[string]json.RawMessage

// Labels names each item by its key.
type Labels map[string]Item

// Item is one labelled item.
type Item struct {
	Name string `json:"name"`
}

// Holder carries a registered map type as a field, written always and
// omitted when empty.
type Holder struct {
	Labels Labels `json:"labels"`
	Extra  Labels `json:"extra,omitempty"`
}

// Ranked is keyed by an integer, which encoding/json writes as its decimal
// text.
type Ranked map[int]string

// Custom writes itself through its own MarshalJSON.
type Custom map[string]string

// MarshalJSON writes the map sorted, as encoding/json would.
func (c Custom) MarshalJSON() ([]byte, error) { return json.Marshal(map[string]string(c)) }

// Union is a map a union cannot be tagged by.
type Union map[string]string

// Tree reaches itself: each value is another tree.
type Tree map[string]Tree

// Left and Right reach each other.
type (
	Left  map[string]Right
	Right map[string]Left
)

// Node reaches itself through the registered map Kids.
type Node struct {
	Kids Kids `json:"kids"`
}

// Kids maps a name to a child node.
type Kids map[string]Node

// Lists maps a name to a list of strings.
type Lists map[string][]string

// Loop reaches itself and is not registered, so a field of it is planned
// inline.
type Loop map[string]Loop

// HasLoop carries the unregistered Loop.
type HasLoop struct {
	Loop Loop `json:"loop"`
}

// Alias is an alias of a map type, not a defined one.
type Alias = map[string]string

// Counted declares a Count bound, which bounds slices only.
type Counted map[string]string

// Count bounds Counted's entries, which wiregen refuses for a map.
func (Counted) Count() (lo, hi int) { return 1, 3 }

// ItemAlias is an alias of a struct, not a defined type.
type ItemAlias = Item

// Word is a string type, neither a struct nor a map.
type Word string

// Key writes non-decimal text, which encoding/json cannot read back without
// UnmarshalText.
type Key int

// MarshalText writes the key as k and its digits.
func (k Key) MarshalText() ([]byte, error) { return []byte("k" + strconv.Itoa(int(k))), nil }

// Keyed is keyed by Key.
type Keyed map[Key]string

// Opt has a field a request may omit, so its request form differs.
type Opt struct {
	N *int `json:"n"`
}

// Opts maps a name to an Opt.
type Opts map[string]Opt

// G is a generic map; G[G[int]] nests two instances, not a cycle.
type G[T any] map[string]T

// GL is a generic slice; GL[GL[string]] nests two instances.
type GL[T any] []T

// Nested carries the nested instances.
type Nested struct {
	Maps   G[G[int]]      `json:"maps"`
	Slices GL[GL[string]] `json:"slices"`
}

// GLoop is a generic map that reaches its own instance.
type GLoop[T any] map[string]GLoop[T]

// HasGLoop carries a GLoop.
type HasGLoop struct {
	Loop GLoop[int] `json:"loop"`
}

// SL is a slice that reaches itself.
type SL []SL

// HasSL carries an SL.
type HasSL struct {
	SL SL `json:"sl"`
}

// Slug is a lowercase word.
type Slug string

// Pattern bounds a slug to lowercase letters.
func (Slug) Pattern() string { return "^[a-z]+$" }

// Slugs maps a name to a slug.
type Slugs map[string]Slug

// PtrID has its text methods on the pointer, so encoding/json writes a map
// key of it as a decimal and reads it back through UnmarshalText.
type PtrID int

// MarshalText writes the ID with a prefix; a map key never calls it.
func (p *PtrID) MarshalText() ([]byte, error) { return []byte("id" + strconv.Itoa(int(*p))), nil }

// UnmarshalText reads the prefixed form only.
func (p *PtrID) UnmarshalText(b []byte) error {
	s, ok := strings.CutPrefix(string(b), "id")
	if !ok {
		return errors.New("want an id prefix")
	}
	n, err := strconv.Atoi(s)
	*p = PtrID(n)
	return err
}

// ByPtrID is keyed by PtrID.
type ByPtrID map[PtrID]string

// Prefixed is a string key with a text form: encoding/json writes the raw
// string, not MarshalText's form, and reads it back through UnmarshalText.
type Prefixed string

// MarshalText writes the key with a p: prefix.
func (p Prefixed) MarshalText() ([]byte, error) { return []byte("p:" + string(p)), nil }

// UnmarshalText reads a key that starts with p:.
func (p *Prefixed) UnmarshalText(b []byte) error {
	s, ok := strings.CutPrefix(string(b), "p:")
	if !ok {
		return errors.New("no p: prefix")
	}
	*p = Prefixed(s)
	return nil
}

// ByPrefixed is keyed by Prefixed.
type ByPrefixed map[Prefixed]string

// TextMap is written as an object but read only from a JSON string, through
// its pointer UnmarshalText.
type TextMap map[string]string

// UnmarshalText reads a comma-separated k=v list.
func (m *TextMap) UnmarshalText(b []byte) error {
	*m = TextMap{}
	for pair := range strings.SplitSeq(string(b), ",") {
		k, v, _ := strings.Cut(pair, "=")
		(*m)[k] = v
	}
	return nil
}

// Level is written as a number but read only from a JSON string, through
// its pointer UnmarshalText.
type Level int

// UnmarshalText reads a level's decimal text.
func (l *Level) UnmarshalText(b []byte) error {
	n, err := strconv.Atoi(string(b))
	*l = Level(n)
	return err
}

// HasLevel carries a Level.
type HasLevel struct {
	Level Level `json:"level"`
}

// Tag is a string read through its pointer UnmarshalText.
type Tag string

// UnmarshalText reads the tag as written.
func (t *Tag) UnmarshalText(b []byte) error { *t = Tag(b); return nil }

// Coded is an int read through its pointer UnmarshalJSON, which wins over
// UnmarshalText.
type Coded int

// UnmarshalText is never called: UnmarshalJSON wins.
func (c *Coded) UnmarshalText([]byte) error { return errors.New("not text") }

// UnmarshalJSON reads the decimal encoding/json writes.
func (c *Coded) UnmarshalJSON(b []byte) error {
	n, err := strconv.Atoi(string(b))
	*c = Coded(n)
	return err
}

// Accepted carries the text decoders a request accepts.
type Accepted struct {
	Tag   Tag            `json:"tag"`
	Coded Coded          `json:"coded"`
	ByTag map[Tag]string `json:"byTag"`
}

// Blob is written as base64 text and read through its pointer UnmarshalText,
// which need not read base64.
type Blob []byte

// UnmarshalText keeps the text as written.
func (b *Blob) UnmarshalText(t []byte) error { *b = append((*b)[:0], t...); return nil }

// HasBlob carries a Blob.
type HasBlob struct {
	Blob Blob `json:"blob"`
}

// Coin is written through MarshalText and read through its pointer
// UnmarshalText, so it is refused for its marshaler alone.
type Coin struct {
	Cents int `json:"cents"`
}

// MarshalText writes the cents.
func (c Coin) MarshalText() ([]byte, error) { return []byte(strconv.Itoa(c.Cents)), nil }

// UnmarshalText reads the cents.
func (c *Coin) UnmarshalText(b []byte) error {
	n, err := strconv.Atoi(string(b))
	c.Cents = n
	return err
}

// HasTextMap carries a registered TextMap.
type HasTextMap struct {
	M TextMap `json:"m"`
}
