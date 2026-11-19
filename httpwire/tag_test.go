package httpwire_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/cplieger/go/httpwire"
)

var sampleOpen = [8]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}

func TestTagString(t *testing.T) {
	for name, tc := range map[string]struct {
		want string
		tag  httpwire.Tag
	}{
		"a revision":     {"0123456789abcdef.7", httpwire.Tag{Open: sampleOpen, Rev: 7}},
		"revision zero":  {"0000000000000000.0", httpwire.Tag{}},
		"the largest":    {"ffffffffffffffff.18446744073709551615", httpwire.Tag{Open: [8]byte{255, 255, 255, 255, 255, 255, 255, 255}, Rev: 1<<64 - 1}},
		"a ten revision": {"0123456789abcdef.10", httpwire.Tag{Open: sampleOpen, Rev: 10}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.tag.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
			b, err := tc.tag.MarshalText()
			if err != nil || string(b) != tc.want {
				t.Errorf("MarshalText() = %q, %v; want %q", b, err, tc.want)
			}
		})
	}
}

func TestTagUnmarshalTextAccepts(t *testing.T) {
	for text, want := range map[string]httpwire.Tag{
		"0123456789abcdef.7":                    {Open: sampleOpen, Rev: 7},
		"0123456789abcdef.0":                    {Open: sampleOpen},
		"0123456789abcdef.10":                   {Open: sampleOpen, Rev: 10},
		"ffffffffffffffff.18446744073709551615": {Open: [8]byte{255, 255, 255, 255, 255, 255, 255, 255}, Rev: 1<<64 - 1},
	} {
		t.Run(text, func(t *testing.T) {
			var got httpwire.Tag
			if err := got.UnmarshalText([]byte(text)); err != nil || got != want {
				t.Errorf("UnmarshalText = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestTagUnmarshalTextRefuses(t *testing.T) {
	for name, text := range map[string]string{
		"a bare revision":     "7",
		"uppercase hex":       "0123456789ABCDEF.7",
		"15 hex characters":   "0123456789abcde.7",
		"17 hex characters":   "0123456789abcdef0.7",
		"a leading zero":      "0123456789abcdef.07",
		"two zeros":           "0123456789abcdef.00",
		"no revision":         "0123456789abcdef.",
		"no token":            ".7",
		"no dot":              "0123456789abcdef",
		"a plus sign":         "0123456789abcdef.+7",
		"a minus sign":        "0123456789abcdef.-1",
		"2^64":                "0123456789abcdef.18446744073709551616",
		"21 digits":           "0123456789abcdef.100000000000000000000",
		"two dots":            "0123456789abcdef.7.8",
		"a space":             "0123456789abcdef. 7",
		"a trailing space":    "0123456789abcdef.7 ",
		"a non-hex character": "0123456789abcdeg.7",
		"an underscore":       "0123456789abcdef.1_000",
		"the quotes kept":     `"0123456789abcdef.7"`,
		"a weak tag":          `W/"0123456789abcdef.7"`,
		"empty":               "",
		"a hex revision":      "0123456789abcdef.0x7",
		"a full-width digit":  "0123456789abcdef.\uff17",
		"an exponent":         "0123456789abcdef.7e1",
		"a 0x prefix":         "0x23456789abcdef.7",
	} {
		t.Run(name, func(t *testing.T) {
			got := httpwire.Tag{Open: sampleOpen, Rev: 3}
			if err := got.UnmarshalText([]byte(text)); err == nil {
				t.Errorf("UnmarshalText(%q) accepted %+v", text, got)
			}
			if got != (httpwire.Tag{Open: sampleOpen, Rev: 3}) {
				t.Errorf("UnmarshalText(%q) changed the tag to %+v", text, got)
			}
		})
	}
}

func TestTagIsAJSONString(t *testing.T) {
	type holder struct {
		Tag httpwire.Tag `json:"tag"`
	}
	b, err := json.Marshal(holder{httpwire.Tag{Open: sampleOpen, Rev: 7}})
	if err != nil || string(b) != `{"tag":"0123456789abcdef.7"}` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var h holder
	if err := json.Unmarshal(b, &h); err != nil || h.Tag != (httpwire.Tag{Open: sampleOpen, Rev: 7}) {
		t.Errorf("Unmarshal = %+v, %v", h, err)
	}
	for _, body := range []string{`{"tag":7}`, `{"tag":"7"}`, `{"tag":{"Open":[1],"Rev":7}}`} {
		if err := json.Unmarshal([]byte(body), &h); err == nil {
			t.Errorf("Unmarshal(%s) accepted %+v", body, h)
		}
	}
}

func TestOpenToken(t *testing.T) {
	var counting, ones, zero [16]byte
	for i := range counting {
		counting[i] = byte(i)
		ones[i] = 0xff
	}
	// The first 8 bytes of SHA-256 over "httpwire tag", a NUL and the epoch,
	// computed outside Go.
	for name, tc := range map[string]struct {
		want  string
		epoch [16]byte
	}{
		"counting bytes": {"65942214c7deb99b", counting},
		"all ones":       {"d7e37dfdabcc6850", ones},
		"all zero":       {"de707aeb27c908a3", zero},
	} {
		t.Run(name, func(t *testing.T) {
			got := httpwire.OpenToken(tc.epoch)
			if hex.EncodeToString(got[:]) != tc.want {
				t.Errorf("OpenToken = %x, want %s", got, tc.want)
			}
			for _, b := range got {
				if bytes.IndexByte(tc.epoch[:], b) >= 0 {
					t.Errorf("OpenToken %x holds the epoch's byte %#x", got, b)
				}
			}
		})
	}
	t.Run("two epochs differ", func(t *testing.T) {
		if httpwire.OpenToken(counting) == httpwire.OpenToken(ones) {
			t.Error("two epochs gave one token")
		}
	})
}
