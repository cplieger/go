// Package maps provides named map fixtures for wiregen's map type tests.
package maps

import (
	"encoding/json"
	"strconv"
)

// Settings is an open settings patch: any key, each value raw JSON.
type Settings map[string]json.RawMessage

// Labels names each item by its key.
type Labels map[string]Item

// Item is one labelled item.
type Item struct {
	Name string `json:"name"`
}

// Holder carries a registered map type as a field.
type Holder struct {
	Labels Labels `json:"labels"`
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

// Key is an integer written through MarshalText, which encoding/json cannot
// read back without UnmarshalText.
type Key int

// MarshalText writes the key's decimal text.
func (k Key) MarshalText() ([]byte, error) { return []byte(strconv.Itoa(int(k))), nil }

// Keyed is keyed by Key.
type Keyed map[Key]string

// Opt has a field a request may omit, so its request form differs.
type Opt struct {
	N *int `json:"n"`
}

// Opts maps a name to an Opt.
type Opts map[string]Opt
