package masked

import (
	"bytes"
	"crypto/aes"
	"reflect"
	"slices"
	"testing"
)

func TestValue(t *testing.T) {
	t.Parallel()
	const text = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$a2V5"
	t.Run("iv_depends_on_text", func(t *testing.T) {
		t.Parallel()
		a, b := text, "X"+text[1:len(text)-1]+"X"
		ma, mb := mask(a), mask(b)
		if bytes.Equal(ma.b[:aes.BlockSize], mb.b[:aes.BlockSize]) {
			t.Error("two texts masked with one IV")
		}
		bodies := make([]byte, len(a))
		texts := make([]byte, len(a))
		for i := range len(a) {
			bodies[i] = ma.b[tagSize+i] ^ mb.b[tagSize+i]
			texts[i] = a[i] ^ b[i]
		}
		if bytes.Equal(bodies, texts) {
			t.Error("two texts share a keystream: the XOR of their masked bodies is the XOR of the texts")
		}
	})
	t.Run("key_is_tag", func(t *testing.T) {
		t.Parallel()
		// The tag is keyed per process; an unkeyed digest of the text would
		// fingerprint the secret across processes.
		s := NewSecret(text)
		if got, want := s.Key(), [32]byte(s.p.b[:tagSize]); got != want {
			t.Errorf("Key() = %x, want the masked value's tag %x", got, want)
		}
	})
	t.Run("types", func(t *testing.T) {
		t.Parallel()
		st := reflect.TypeFor[Secret]()
		if st.Comparable() {
			t.Error("Secret is comparable, want == not to compile")
		}
		if got, want := fieldTypes(reflect.TypeFor[value]()), []reflect.Type{reflect.TypeFor[[]byte]()}; !slices.Equal(got, want) {
			t.Errorf("value field types = %v, want %v", got, want)
		}
		want := []reflect.Type{reflect.TypeFor[[0]func()](), reflect.TypeFor[*value]()}
		if got := fieldTypes(st); !slices.Equal(got, want) {
			t.Errorf("Secret field types = %v, want %v", got, want)
		}
	})
}

func fieldTypes(typ reflect.Type) []reflect.Type {
	out := make([]reflect.Type, typ.NumField())
	for i := range out {
		out[i] = typ.Field(i).Type
	}
	return out
}
