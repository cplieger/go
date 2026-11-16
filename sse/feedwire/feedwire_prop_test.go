package feedwire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"pgregory.net/rapid"
)

var (
	genSource = rapid.StringMatching(`[a-z][a-z0-9_]{0,31}`)
	genKind   = rapid.StringMatching(`[a-z0-9_]{1,32}`)
	genEpoch  = rapid.StringMatching(`[0-9a-f]{32}`)
	genWire   = rapid.OneOf(rapid.Just(""), rapid.StringMatching(`[0-9a-f]{16}`))
	genRev    = rapid.Uint64Range(1, MaxRev)
)

func genChanged() *rapid.Generator[Changed] {
	return rapid.Custom(func(t *rapid.T) Changed {
		kinds := rapid.SliceOfNDistinct(genKind, 1, maxKinds, rapid.ID).Draw(t, "kinds")
		slices.Sort(kinds)
		return Changed{
			Source: genSource.Draw(t, "source"),
			Epoch:  genEpoch.Draw(t, "epoch"),
			Rev:    genRev.Draw(t, "rev"),
			Kinds:  kinds,
		}
	})
}

func genListening() *rapid.Generator[Listening] {
	return rapid.Custom(func(t *rapid.T) Listening {
		names := rapid.SliceOfNDistinct(genSource, 0, 4, rapid.ID).Draw(t, "sources")
		l := Listening{Wire: genWire.Draw(t, "wire"), Sources: make([]ListeningSource, len(names))}
		for i, name := range names {
			l.Sources[i] = ListeningSource{
				Source: name,
				Epoch:  genEpoch.Draw(t, "epoch"),
				Floors: rapid.MapOfN(genKind, genRev, 0, maxKinds/4).Draw(t, "floors"),
			}
		}
		return l
	})
}

// reshaped writes the same JSON value with its members in another order and
// whitespace between every token: a strict decoder reads it as the original.
func reshaped(t *rapid.T, data []byte) []byte {
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("encoder output is not a JSON object: %v", err)
	}
	flat, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, flat, " ", "\t"); err != nil {
		t.Fatalf("indent: %v", err)
	}
	return out.Bytes()
}

func TestChangedRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := genChanged().Draw(t, "changed")
		data, err := EncodeChanged(c)
		if err != nil {
			t.Fatalf("EncodeChanged(%+v) error = %v", c, err)
		}
		got, err := DecodeChanged(data)
		if err != nil || !reflect.DeepEqual(got, c) {
			t.Fatalf("DecodeChanged(%s) = %+v, %v, want %+v", data, got, err, c)
		}
		got, err = DecodeChanged(reshaped(t, data))
		if err != nil || !reflect.DeepEqual(got, c) {
			t.Fatalf("DecodeChanged(reshaped) = %+v, %v, want %+v", got, err, c)
		}
	})
}

func TestListeningRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		l := genListening().Draw(t, "listening")
		data, err := EncodeListening(l)
		if err != nil {
			t.Fatalf("EncodeListening(%+v) error = %v", l, err)
		}
		got, err := DecodeListening(data)
		if err != nil || !reflect.DeepEqual(got, l) {
			t.Fatalf("DecodeListening(%s) = %+v, %v, want %+v", data, got, err, l)
		}
		fr, err := PeekListening(data)
		if err != nil || fr != (Frozen{Feed: Version, Wire: l.Wire}) {
			t.Fatalf("PeekListening(%s) = %+v, %v, want feed %d and wire %q", data, fr, err, Version, l.Wire)
		}
		got, err = DecodeListening(reshaped(t, data))
		if err != nil || !reflect.DeepEqual(got, l) {
			t.Fatalf("DecodeListening(reshaped) = %+v, %v, want %+v", got, err, l)
		}
	})
}

func TestPositionRoundTrip_prop(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		names := rapid.SliceOfNDistinct(genSource, 1, 6, rapid.ID).Draw(t, "sources")
		ps := make([]Position, len(names))
		for i, name := range names {
			ps[i] = Position{Source: name, Epoch: genEpoch.Draw(t, "epoch"), Rev: rapid.Uint64Range(0, MaxRev).Draw(t, "rev")}
		}
		v, err := FormatPosition(ps)
		if err != nil {
			t.Fatalf("FormatPosition(%+v) error = %v", ps, err)
		}
		got, err := ParsePosition(v)
		if err != nil || !reflect.DeepEqual(got, ps) {
			t.Fatalf("ParsePosition(%q) = %+v, %v, want %+v", v, got, err, ps)
		}
	})
}
