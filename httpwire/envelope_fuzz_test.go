package httpwire_test

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// FuzzDecodeEnvelope_roundtrip: an accepted envelope, encoded again, is
// accepted as the same value, so no accepted value sits outside the rules.
func FuzzDecodeEnvelope_roundtrip(f *testing.F) {
	for _, fx := range handCases() {
		f.Add([]byte(fx.Body))
	}
	f.Add([]byte(`{"code":"a","message":"\ud800","remediation":"","request_id":"","fields":[],"retry_after_ms":-0}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		env, err := httpwire.DecodeEnvelope(body)
		if err != nil {
			return
		}
		again, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("Marshal(%+v) = %v", env, err)
		}
		back, err := httpwire.DecodeEnvelope(again)
		if err != nil {
			t.Fatalf("accepted %q, refused its encoding %s: %v", body, again, err)
		}
		if !envelopeEqual(env, back) {
			t.Errorf("round trip changed %+v into %+v", env, back)
		}
	})
}
