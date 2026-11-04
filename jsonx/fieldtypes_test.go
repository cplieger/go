package jsonx_test

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/go/jsonx"
)

// TestFieldTypesStructDecode exercises the three field types the way
// consumers use them: inside a struct through encoding/json.
func TestFieldTypesStructDecode(t *testing.T) {
	t.Parallel()

	type record struct {
		Tolerant jsonx.TolerantInt         `json:"tolerant"`
		Strict   jsonx.StrictInt           `json:"strict"`
		Absent   jsonx.StrictAbsentZeroInt `json:"absent"`
	}

	var r record
	in := `{"tolerant":"1e3","strict":"42","absent":14}`
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatalf("Unmarshal(%s) error: %v", in, err)
	}
	if r.Tolerant != 1000 || r.Strict != 42 || r.Absent != 14 {
		t.Errorf("decoded = %+v, want tolerant 1000, strict 42, absent 14", r)
	}

	// Absent fields never invoke UnmarshalJSON: all stay zero.
	r = record{}
	if err := json.Unmarshal([]byte(`{}`), &r); err != nil {
		t.Fatalf("Unmarshal({}) error: %v", err)
	}
	if r.Tolerant != 0 || r.Strict != 0 || r.Absent != 0 {
		t.Errorf("absent fields = %+v, want all zero", r)
	}

	// A strict field poisons the record on an odd value; the tolerant
	// one absorbs it.
	if err := json.Unmarshal([]byte(`{"strict":"abc"}`), &r); err == nil {
		t.Error("Unmarshal strict abc = nil error, want error")
	}
	r = record{}
	if err := json.Unmarshal([]byte(`{"tolerant":"abc"}`), &r); err != nil {
		t.Errorf("Unmarshal tolerant abc error = %v, want nil", err)
	}
}

// TestFieldTypesResetOnReuse pins the duplicate-key reset invariant the
// seadex-scout decoders rely on: encoding/json processes duplicate object
// keys in order against the SAME field receiver, so a later tolerated-odd
// value must clear the earlier decode, not silently retain it.
func TestFieldTypesResetOnReuse(t *testing.T) {
	t.Parallel()

	var v jsonx.TolerantInt
	if err := v.UnmarshalJSON([]byte(`7`)); err != nil || v != 7 {
		t.Fatalf("first decode = %d, %v, want 7, nil", v, err)
	}
	if err := v.UnmarshalJSON([]byte(`false`)); err != nil || v != 0 {
		t.Errorf("reuse with odd value = %d, %v, want reset to 0, nil", v, err)
	}

	// End to end through duplicate JSON keys.
	var w struct {
		N jsonx.TolerantInt `json:"n"`
	}
	if err := json.Unmarshal([]byte(`{"n":7,"n":false}`), &w); err != nil {
		t.Fatalf("duplicate-key decode error: %v", err)
	}
	if w.N != 0 {
		t.Errorf("duplicate-key later odd value = %d, want 0 (later value wins)", w.N)
	}
}

// TestFieldTypesNoPartialValueOnError pins that an erroring decode leaves
// the receiver at 0 rather than a stale or partial value.
func TestFieldTypesNoPartialValueOnError(t *testing.T) {
	t.Parallel()

	s := jsonx.StrictInt(7)
	if err := s.UnmarshalJSON([]byte(`"abc"`)); err == nil {
		t.Fatal("UnmarshalJSON(abc) = nil error, want error")
	}
	if s != 0 {
		t.Errorf("receiver after error = %d, want 0 (no partial value retained)", s)
	}

	v := jsonx.TolerantInt(7)
	if err := v.UnmarshalJSON([]byte(`"broken`)); err == nil {
		t.Fatal("UnmarshalJSON(malformed string) = nil error, want error")
	}
	if v != 0 {
		t.Errorf("receiver after error = %d, want 0 (no partial value retained)", v)
	}
}

// TestStrictAbsentZeroIntEmptyInput pins the plex-language-sync direct
// call contract: zero-length input decodes to 0 without error, on a
// receiver holding a stale value.
func TestStrictAbsentZeroIntEmptyInput(t *testing.T) {
	t.Parallel()
	for _, data := range [][]byte{nil, {}} {
		v := jsonx.StrictAbsentZeroInt(7)
		if err := v.UnmarshalJSON(data); err != nil {
			t.Fatalf("UnmarshalJSON(%q) error = %v, want nil", data, err)
		}
		if v != 0 {
			t.Errorf("UnmarshalJSON(%q) = %d, want 0", data, v)
		}
	}
	var s jsonx.StrictInt
	if err := s.UnmarshalJSON(nil); err == nil {
		t.Error("StrictInt.UnmarshalJSON(nil) = nil error, want error (Strict rejects empty input)")
	}
	a := jsonx.StrictAbsentZeroInt(7)
	if err := a.UnmarshalJSON([]byte(`{}`)); err == nil {
		t.Error("StrictAbsentZeroInt.UnmarshalJSON({}) = nil error, want error")
	}
	if a != 0 {
		t.Errorf("receiver after error = %d, want 0 (no partial value retained)", a)
	}
}

// TestFieldTypesMarshalPlainNumber pins the documented marshal side: the
// field types emit plain numbers via their underlying int64.
func TestFieldTypesMarshalPlainNumber(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(struct {
		A jsonx.TolerantInt         `json:"a"`
		B jsonx.StrictInt           `json:"b"`
		C jsonx.StrictAbsentZeroInt `json:"c"`
	}{9, -3, 0})
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if got, want := string(out), `{"a":9,"b":-3,"c":0}`; got != want {
		t.Errorf("Marshal = %s, want %s", got, want)
	}
}
