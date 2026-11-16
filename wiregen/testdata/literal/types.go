// Package literal provides fixtures for fields a wiregen const tag pins to
// one value.
package literal

// Kind is a string enum a const field narrows.
type Kind string

// Kind values.
const (
	KindA Kind = "a"
	KindB Kind = "b"
)

// A always sends kind "a" and tag "x".
type A struct {
	Kind Kind   `json:"kind" wiregen:"const=a"`
	Tag  string `json:"tag" wiregen:"const=x"`
	N    int    `json:"n"`
}

// NotString pins a number.
type NotString struct {
	N int `json:"n" wiregen:"const=1"`
}

// NotMember pins a value its enum lacks.
type NotMember struct {
	Kind Kind `json:"kind" wiregen:"const=c"`
}

// Omitted pins a field omitempty can drop.
type Omitted struct {
	Tag string `json:"tag,omitempty" wiregen:"const=x"`
}

// Pointer pins a field a nil pointer sends as null.
type Pointer struct {
	Tag *string `json:"tag" wiregen:"const=x"`
}

// Unknown has a wiregen tag that is no const.
type Unknown struct {
	Tag string `json:"tag" wiregen:"x"`
}

// Empty pins the empty string.
type Empty struct {
	Tag string `json:"tag" wiregen:"const="`
}

// Inline is flattened into Flat, so its tag pins no field.
type Inline struct {
	V int `json:"v"`
}

// Flat embeds Inline under a wiregen tag.
type Flat struct {
	Inline `wiregen:"const=x"`
}

// Tagged is a union whose tag is pinned.
type Tagged struct {
	Kind Kind `json:"kind" wiregen:"const=a"`
	N    int  `json:"n,omitempty"`
}

// B always sends kind "b".
type B struct {
	Kind Kind   `json:"kind" wiregen:"const=b"`
	M    string `json:"m"`
}

// Short is a string bounded to 8 bytes.
type Short string

// MaxBytes bounds Short.
func (Short) MaxBytes() int { return 8 }

// Bounded pins a bounded field.
type Bounded struct {
	Tag Short `json:"tag" wiregen:"const=x"`
}

// Pinned holds a pinned field another type promotes.
type Pinned struct {
	Tag string `json:"tag" wiregen:"const=x"`
}

// ViaPtr promotes Pinned's field through a pointer, which a nil one drops.
type ViaPtr struct {
	*Pinned
}
