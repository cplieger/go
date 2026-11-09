// Package unions provides tagged-union fixtures for wiregen's Union tests.
package unions

import (
	"encoding/json"
	"errors"
)

// Kind names a Timing's shape.
type Kind uint8

// The Timing kinds; the zero is no kind.
const (
	KindIdentity Kind = iota + 1
	KindShift
	KindLinear
)

// MarshalText names a kind on the wire.
func (k Kind) MarshalText() ([]byte, error) {
	switch k {
	case KindIdentity:
		return []byte("identity"), nil
	case KindShift:
		return []byte("shift"), nil
	case KindLinear:
		return []byte("linear"), nil
	}
	return nil, errors.New("unknown kind")
}

// Timing is a union tagged by an enum.
type Timing struct {
	Kind     Kind     `json:"kind"`
	Ms       *int64   `json:"ms,omitempty"`
	Scale    *float64 `json:"scale,omitempty"`
	OffsetMs *int64   `json:"offset_ms,omitempty"`
}

// Shape is a union tagged by a string.
type Shape struct {
	Type   string   `json:"type"`
	Radius float64  `json:"radius,omitempty"`
	Side   float64  `json:"side,omitempty"`
	Tags   []string `json:"tags"`
}

// Holder holds unions as fields.
type Holder struct {
	Timing Timing  `json:"timing"`
	Shapes []Shape `json:"shapes"`
}

// Tree is a union that reaches itself.
type Tree struct {
	Type     string `json:"type"`
	Value    int    `json:"value,omitempty"`
	Children []Tree `json:"children,omitempty"`
}

// Untagged has no field a union could be tagged by.
type Untagged struct {
	Kind *string `json:"kind,omitempty"`
	Name string  `json:"name"`
}

// Command is a union whose tag selects the type its payload decodes as.
type Command struct {
	Type    string          `json:"type"`
	ChatID  string          `json:"chat_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// CreateChat is the payload of a create command.
type CreateChat struct {
	Title string `json:"title"`
	Pin   bool   `json:"pin,omitempty"`
}

// Rename is the payload of a rename command.
type Rename struct {
	Name string `json:"name"`
}

// Nested is a payload type whose request schema differs from its response
// one: a decoder leaves a missing Note nil, so a request may omit it.
type Nested struct {
	Note  *string `json:"note"`
	Inner Rename  `json:"inner"`
}

// RequiredPayload has a payload encoding/json writes in every case.
type RequiredPayload struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// OmitZeroPayload has a payload omitzero drops when nil.
type OmitZeroPayload struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitzero"`
}

// PtrPayload has a payload behind a pointer, which omitempty drops only
// when the pointer is nil.
type PtrPayload struct {
	Type    string           `json:"type"`
	Payload *json.RawMessage `json:"payload,omitempty"`
}

// Custom is a struct encoding/json writes through its own MarshalJSON.
type Custom struct {
	Name string `json:"name"`
}

// MarshalJSON writes the name alone.
func (c Custom) MarshalJSON() ([]byte, error) { return json.Marshal(c.Name) }

// NotRaw has a payload field that is no json.RawMessage.
type NotRaw struct {
	Type    string `json:"type"`
	Payload string `json:"payload,omitempty"`
}

// Batch is a payload type that reaches its own union back.
type Batch struct {
	Commands []Command `json:"commands"`
}
