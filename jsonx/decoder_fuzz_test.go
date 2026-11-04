package jsonx_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cplieger/go/jsonx"
)

// FuzzParityWithUnmarshal drives arbitrary bytes through the bounded widget
// walk (uncapped, unbudgeted) and json.Unmarshal. Invariants: the walk never
// panics, and it is never LOOSER than json.Unmarshal — an input the bounded
// walk accepts must be stdlib-accepted with an EXACTLY DeepEqual result,
// nil-vs-empty included (null → nil for slices AND maps, `[]` / `{}` → empty
// non-nil, absent → untouched). Bounded-stricter is allowed by design (caps
// and budget exist to reject what Unmarshal would materialize), so an
// Unmarshal-accepted input the walk rejects is not a violation; here with no
// caps configured the accept sets should in practice coincide.
func FuzzParityWithUnmarshal(f *testing.F) {
	f.Add([]byte(`{"name":"a","count":2,"tags":["x","y"],"parts":[{"id":1,"kind":"k"},{"id":2}]}`))
	f.Add([]byte(`{"NAME":"a","Count":2,"TAGS":["x"],"PaRtS":[{"ID":7,"KIND":"k"}]}`))
	f.Add([]byte(`{"parts":[{"id":1,"kind":"k"}],"parts":[{"id":5}]}`))
	f.Add([]byte(`{"tags":["x"],"tags":null}`))
	f.Add([]byte(`{"big":1e1000,"name":"a"}`))
	f.Add([]byte(`{"parts":[null]}`))
	f.Add([]byte(`{"meta":{"a":"1","b":"2"},"specs":{"x":{"id":1,"kind":"k"}}}`))
	f.Add([]byte(`{"meta":{"a":"1"},"meta":null}`))
	f.Add([]byte(`{"meta":{"a":"1"},"meta":{}}`))
	f.Add([]byte(`{"meta":{"a":null},"specs":{"x":null}}`))
	f.Add([]byte(`{"specs":{"x":{"id":1,"kind":"k"},"x":{"id":2}}}`))
	f.Add([]byte(`{"nested":{"o":{"a":"1"},"p":{},"q":null}}`))
	f.Add([]byte(`{"meta":{"a":1}}`))
	f.Add([]byte(`{"meta":{"a":"1","A":"2"}}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{} {}`))
	f.Add([]byte(`{"count":"x"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		got, err := budgetWidget(body, 0, 0, 0)
		if err != nil {
			return // bounded-stricter or both-reject: nothing to compare
		}
		var want widget
		if uErr := json.Unmarshal(body, &want); uErr != nil {
			t.Fatalf("bounded accepted %q but json.Unmarshal rejects it: %v (the walk must never be looser than stdlib)", body, uErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("bounded(%q) = %+v, want json.Unmarshal parity %+v", body, got, want)
		}
	})
}

// FuzzPreflight drives arbitrary bytes through the structural preflight.
// Invariants: it never panics; it is never LOOSER than the stdlib's own
// syntactic check, so a body it accepts is valid whole-input JSON and any
// later decode failure is the caller's schema, not structure the preflight
// waved through; it is deterministic; and it is stable under array wrapping -
// enclosing an accepted body in a one-element array (a structure-preserving
// transform that only adds a level of nesting) keeps it accepted, which pins
// that acceptance is a property of the body's shape rather than of the
// depth it happened to start at.
func FuzzPreflight(f *testing.F) {
	f.Add([]byte(`{"name":"a","count":2,"tags":["x","y"]}`))
	f.Add([]byte(`{"name":"a","name":"b"}`))
	f.Add([]byte(`{"NAME":"a","name":"b"}`))
	f.Add([]byte(`{"a":{"b":{"c":[1,2,{"d":null}]}}}`))
	f.Add([]byte(`{"big":1e1000}`))
	f.Add([]byte(`[[[[[[[[[[`))
	f.Add([]byte(`{} {}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	// The three simple-fold orbits Unicode 17 changed where BOTH members were
	// already assigned in Unicode 15, so real upstream text can carry them.
	// foldKey walks SimpleFold, so these are the only inputs whose duplicate
	// verdict the toolchain's Unicode bump can move, and the committed corpus
	// does not otherwise reach them.
	f.Add([]byte("{\"\u0390\":1,\"\u1fd3\":2}"))
	f.Add([]byte("{\"\u03b0\":1,\"\u1fe3\":2}"))
	f.Add([]byte("{\"\ufb05\":1,\"\ufb06\":2}"))
	// The two runes that lower to ASCII, which is the other way a fold orbit
	// can reach a key an ASCII-only canonicalizer would miss.
	f.Add([]byte("{\"\u0130\":1,\"i\":2}"))
	f.Add([]byte("{\"\u212a\":1,\"k\":2}"))

	f.Fuzz(func(t *testing.T, data []byte) {
		err := jsonx.Preflight(bytes.NewReader(data))

		if again := jsonx.Preflight(bytes.NewReader(data)); (again == nil) != (err == nil) {
			t.Fatalf("Preflight is nondeterministic on %q: %v then %v", data, err, again)
		}
		if err != nil {
			return
		}
		if !json.Valid(data) {
			t.Fatalf("Preflight accepted %q but json.Valid rejects it", data)
		}
		wrapped := append(append([]byte{'['}, data...), ']')
		if wrapErr := jsonx.Preflight(bytes.NewReader(wrapped)); wrapErr != nil {
			t.Fatalf("Preflight accepted %q but rejected it wrapped in an array: %v", data, wrapErr)
		}
	})
}
