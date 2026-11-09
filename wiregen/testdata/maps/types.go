// Package maps provides named map fixtures for wiregen's map type tests.
package maps

import "encoding/json"

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
