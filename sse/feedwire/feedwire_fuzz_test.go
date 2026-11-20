package feedwire

import (
	"errors"
	"reflect"
	"testing"
)

// frameSeeds adds every frame of one type in cases.json as a seed.
func frameSeeds(f *testing.F, pick func(feedCases) []frameCase) {
	for _, c := range pick(loadFeedCases(f)) {
		f.Add(string(c.bytes()))
	}
}

func FuzzDecodeChanged_roundTripOrRefuse(f *testing.F) {
	frameSeeds(f, func(fc feedCases) []frameCase { return fc.Changed })
	f.Fuzz(func(t *testing.T, data string) {
		c, err := DecodeChanged([]byte(data))
		if err != nil {
			if !reflect.DeepEqual(c, Changed{}) {
				t.Fatalf("DecodeChanged(%q) = %+v with an error, want the zero Changed", data, c)
			}
			return
		}
		if len(data) > maxFrameBytes {
			t.Fatalf("DecodeChanged accepted %d bytes", len(data))
		}
		enc, err := EncodeChanged(c)
		if err != nil {
			t.Fatalf("EncodeChanged(DecodeChanged(%q)) error = %v", data, err)
		}
		again, err := DecodeChanged(enc)
		if err != nil || !reflect.DeepEqual(again, c) {
			t.Fatalf("DecodeChanged(%s) = %+v, %v, want %+v", enc, again, err, c)
		}
	})
}

func FuzzDecodeListening_roundTripOrRefuse(f *testing.F) {
	frameSeeds(f, func(fc feedCases) []frameCase { return fc.Listening })
	f.Fuzz(func(t *testing.T, data string) {
		fr, perr := PeekListening([]byte(data))
		l, err := DecodeListening([]byte(data))
		switch {
		case errors.Is(err, ErrOtherFormat):
			if perr != nil || fr.Feed == Version {
				t.Fatalf("DecodeListening(%q) = other format, but PeekListening = %+v, %v", data, fr, perr)
			}
			return
		case err != nil:
			return
		case perr != nil || fr != (Frozen{Feed: Version, Wire: l.Wire}):
			t.Fatalf("DecodeListening(%q) accepted, but PeekListening = %+v, %v", data, fr, perr)
		}
		enc, err := EncodeListening(l)
		if err != nil {
			t.Fatalf("EncodeListening(DecodeListening(%q)) error = %v", data, err)
		}
		again, err := DecodeListening(enc)
		if err != nil || !reflect.DeepEqual(again, l) {
			t.Fatalf("DecodeListening(%s) = %+v, %v, want %+v", enc, again, err, l)
		}
	})
}

func FuzzParsePosition_roundTripOrRefuse(f *testing.F) {
	for _, c := range loadFeedCases(f).Position {
		f.Add(c.Value)
	}
	f.Fuzz(func(t *testing.T, v string) {
		ps, err := ParsePosition(v)
		if err != nil {
			if ps != nil {
				t.Fatalf("ParsePosition(%q) = %+v with an error, want nil", v, ps)
			}
			return
		}
		if len(ps) == 0 {
			return
		}
		enc, err := FormatPosition(ps)
		if err != nil {
			t.Fatalf("FormatPosition(ParsePosition(%q)) error = %v", v, err)
		}
		again, err := ParsePosition(enc)
		if err != nil || !reflect.DeepEqual(again, ps) {
			t.Fatalf("ParsePosition(%q) = %+v, %v, want %+v", enc, again, err, ps)
		}
	})
}
