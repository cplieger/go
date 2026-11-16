package feedwire

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	testEpoch  = "9f2c4e1a7b3d5f60819a2b3c4d5e6f70"
	testEpoch2 = "41ab0c9d8e7f6a5b4c3d2e1f00112233"
	testWire   = "3f9a0c1d2e4b5a67"
)

func validChanged() Changed {
	return Changed{Source: "store", Epoch: testEpoch, Rev: 1234, Kinds: []string{"budgets", "runs"}}
}

func validListening() Listening {
	return Listening{Wire: testWire, Sources: []ListeningSource{
		{Source: "store", Epoch: testEpoch, Floors: map[string]uint64{"runs": 1230, "budgets": 1101}},
		{Source: "live", Epoch: testEpoch2, Floors: map[string]uint64{"queue": 7}},
	}}
}

func kindNames(n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = fmt.Sprintf("k%03d", i)
	}
	return out
}

func TestEncodeChangedRefuses(t *testing.T) {
	if _, err := EncodeChanged(validChanged()); err != nil {
		t.Fatalf("EncodeChanged(valid) error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Changed)
	}{
		{"empty source", func(c *Changed) { c.Source = "" }},
		{"source digit first", func(c *Changed) { c.Source = "2store" }},
		{"source 33 bytes", func(c *Changed) { c.Source = strings.Repeat("s", 33) }},
		{"epoch uppercase", func(c *Changed) { c.Epoch = strings.ToUpper(testEpoch) }},
		{"epoch 31 chars", func(c *Changed) { c.Epoch = testEpoch[:31] }},
		{"rev 0", func(c *Changed) { c.Rev = 0 }},
		{"rev above MaxRev", func(c *Changed) { c.Rev = MaxRev + 1 }},
		{"no kinds", func(c *Changed) { c.Kinds = nil }},
		{"65 kinds", func(c *Changed) { c.Kinds = kindNames(65) }},
		{"kind with a capital", func(c *Changed) { c.Kinds = []string{"Runs"} }},
		{"kind 33 bytes", func(c *Changed) { c.Kinds = []string{strings.Repeat("k", 33)} }},
		{"kinds unsorted", func(c *Changed) { c.Kinds = []string{"runs", "budgets"} }},
		{"kinds repeated", func(c *Changed) { c.Kinds = []string{"runs", "runs"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validChanged()
			tt.mutate(&c)
			if b, err := EncodeChanged(c); err == nil {
				t.Errorf("EncodeChanged(%+v) = %s, want an error", c, b)
			}
		})
	}
}

func TestEncodeChangedBounds(t *testing.T) {
	c := Changed{Source: strings.Repeat("s", 32), Epoch: testEpoch, Rev: MaxRev, Kinds: kindNames(64)}
	b, err := EncodeChanged(c)
	if err != nil {
		t.Fatalf("EncodeChanged(64 kinds, MaxRev, 32-byte source) error = %v", err)
	}
	got, err := DecodeChanged(b)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("DecodeChanged(EncodeChanged(c)) = %+v, %v, want %+v", got, err, c)
	}
}

func TestEncodeListeningRefuses(t *testing.T) {
	if _, err := EncodeListening(validListening()); err != nil {
		t.Fatalf("EncodeListening(valid) error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Listening)
	}{
		{"wire 15 chars", func(l *Listening) { l.Wire = testWire[:15] }},
		{"wire uppercase", func(l *Listening) { l.Wire = strings.ToUpper(testWire) }},
		{"source invalid", func(l *Listening) { l.Sources[0].Source = "_store" }},
		{"epoch invalid", func(l *Listening) { l.Sources[1].Epoch = "41AB" }},
		{"source repeated", func(l *Listening) { l.Sources[1].Source = "store" }},
		{"floor 0", func(l *Listening) { l.Sources[0].Floors["runs"] = 0 }},
		{"floor above MaxRev", func(l *Listening) { l.Sources[0].Floors["runs"] = MaxRev + 1 }},
		{"kind invalid", func(l *Listening) { l.Sources[0].Floors["Runs"] = 5 }},
		{"65 kinds over two sources", func(l *Listening) {
			for _, k := range kindNames(63) {
				l.Sources[1].Floors[k] = 1
			}
		}},
		{"data above 64 KiB", func(l *Listening) {
			for i := range 1000 {
				l.Sources = append(l.Sources, ListeningSource{Source: fmt.Sprintf("s%04d", i), Epoch: testEpoch})
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := validListening()
			tt.mutate(&l)
			if b, err := EncodeListening(l); err == nil {
				t.Errorf("EncodeListening = %d bytes, want an error", len(b))
			}
		})
	}
}

func TestEncodeListeningByteForm(t *testing.T) {
	tests := []struct {
		name string
		l    Listening
		want string
	}{
		{
			name: "wire first after feed, floors sorted",
			l:    validListening(),
			want: `{"feed":1,"wire":"` + testWire + `","sources":[{"source":"store","epoch":"` + testEpoch +
				`","floors":{"budgets":1101,"runs":1230}},{"source":"live","epoch":"` + testEpoch2 + `","floors":{"queue":7}}]}`,
		},
		{
			name: "no wire member when empty",
			l:    Listening{Sources: []ListeningSource{{Source: "store", Epoch: testEpoch}}},
			want: `{"feed":1,"sources":[{"source":"store","epoch":"` + testEpoch + `","floors":{}}]}`,
		},
		{
			name: "no source",
			l:    Listening{},
			want: `{"feed":1,"sources":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EncodeListening(tt.l)
			if err != nil || string(got) != tt.want {
				t.Errorf("EncodeListening = %s, %v, want %s", got, err, tt.want)
			}
		})
	}
}

func TestDecodeFrameSizeBound(t *testing.T) {
	c, _ := EncodeChanged(validChanged())
	l, _ := EncodeListening(validListening())
	pad := func(b []byte, n int) []byte { return append(append([]byte{}, b...), strings.Repeat(" ", n-len(b))...) }
	if _, err := DecodeChanged(pad(c, maxFrameBytes)); err != nil {
		t.Errorf("DecodeChanged(65,536 bytes) error = %v, want nil", err)
	}
	if _, err := DecodeChanged(pad(c, maxFrameBytes+1)); err == nil {
		t.Errorf("DecodeChanged(65,537 bytes) error = nil, want refused")
	}
	if _, err := DecodeListening(pad(l, maxFrameBytes)); err != nil {
		t.Errorf("DecodeListening(65,536 bytes) error = %v, want nil", err)
	}
	if _, err := PeekListening(pad(l, maxFrameBytes+1)); err == nil {
		t.Errorf("PeekListening(65,537 bytes) error = nil, want refused")
	}
}

func TestPeekListeningReadsOnlyFrozen(t *testing.T) {
	tests := []struct {
		name string
		data string
		want Frozen
	}{
		{"other members of any shape", `{"sources":"not a list","feed":7,"x":{"deep":[1,{"y":null}]},"wire":"` + testWire + `"}`, Frozen{Feed: 7, Wire: testWire}},
		{"no wire", `{"feed":1,"sources":7}`, Frozen{Feed: 1}},
		{"feed written as a double", `{"feed":2.0e0}`, Frozen{Feed: 2}},
		{"repeated feed takes the last", `{"feed":1,"feed":3}`, Frozen{Feed: 3}},
		{"largest safe integer", `{"feed":-9007199254740991}`, Frozen{Feed: -9007199254740991}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PeekListening([]byte(tt.data))
			if err != nil || got != tt.want {
				t.Errorf("PeekListening(%s) = %+v, %v, want %+v", tt.data, got, err, tt.want)
			}
		})
	}
	if _, err := DecodeListening([]byte(`{"feed":1,"sources":7}`)); err == nil || errors.Is(err, ErrOtherFormat) {
		t.Errorf("DecodeListening(own format, sources not a list) error = %v, want a parse error", err)
	}
}

func TestPositionRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ps   []Position
		want string
	}{
		{"one source", []Position{{Source: "store", Epoch: testEpoch, Rev: 1234}}, `store=1234;epoch="` + testEpoch + `"`},
		{
			"two sources in order",
			[]Position{{Source: "store", Epoch: testEpoch, Rev: 1234}, {Source: "live", Epoch: testEpoch2, Rev: 7}},
			`store=1234;epoch="` + testEpoch + `", live=7;epoch="` + testEpoch2 + `"`,
		},
		{
			"rev 0 and MaxRev",
			[]Position{{Source: "a", Epoch: testEpoch, Rev: 0}, {Source: "b", Epoch: testEpoch, Rev: MaxRev}},
			`a=0;epoch="` + testEpoch + `", b=999999999999999;epoch="` + testEpoch + `"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatPosition(tt.ps)
			if err != nil || got != tt.want {
				t.Fatalf("FormatPosition = %q, %v, want %q", got, err, tt.want)
			}
			back, err := ParsePosition(got)
			if err != nil || !reflect.DeepEqual(back, tt.ps) {
				t.Errorf("ParsePosition(%q) = %+v, %v, want %+v", got, back, err, tt.ps)
			}
		})
	}
}

func TestFormatPositionRefuses(t *testing.T) {
	ok := Position{Source: "store", Epoch: testEpoch, Rev: 1}
	tests := []struct {
		name string
		ps   []Position
	}{
		{"no member", nil},
		{"source invalid", []Position{{Source: "_x", Epoch: testEpoch}}},
		{"epoch invalid", []Position{{Source: "store", Epoch: testEpoch[:31]}}},
		{"rev above MaxRev", []Position{{Source: "store", Epoch: testEpoch, Rev: MaxRev + 1}}},
		{"source repeated", []Position{ok, ok}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := FormatPosition(tt.ps); err == nil {
				t.Errorf("FormatPosition(%+v) = %q, want an error", tt.ps, got)
			}
		})
	}
}

func TestParsePositionAcceptsRFC9651(t *testing.T) {
	member := `store=5;epoch="` + testEpoch + `"`
	five := []Position{{Source: "store", Epoch: testEpoch, Rev: 5}}
	tests := []struct {
		name  string
		value string
		want  []Position
	}{
		{"ignored date parameter", member + `;d=@1659578233`, five},
		{"ignored display string parameter", member + `;d=%"caf%c3%a9"`, five},
		{"ignored decimal and boolean parameters", member + `;n=-12.345;b;f=?0`, five},
		{"ignored token parameter with colon and slash", member + `;t=a:b/c`, five},
		{"escaped quote in an ignored string", member + `;s="a\"b\\c"`, five},
		{"an inner-list member overwritten by a valid one", `store=(1 "x";p=2);q, ` + member, five},
		{"negative zero", `store=-0;epoch="` + testEpoch + `"`, []Position{{Source: "store", Epoch: testEpoch, Rev: 0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePosition(tt.value)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParsePosition(%q) = %+v, %v, want %+v", tt.value, got, err, tt.want)
			}
		})
	}
}

func TestParsePositionRefuses(t *testing.T) {
	member := `store=5;epoch="` + testEpoch + `"`
	tests := []struct {
		name  string
		value string
	}{
		{"leading tab", "\t" + member},
		{"space before a parameter", `store=5 ;epoch="` + testEpoch + `"`},
		{"last epoch a token", member + `;epoch=e`},
		{"epoch a byte sequence", `store=5;epoch=:AQID:`},
		{"byte sequence outside base64", member + `;b=:a-b:`},
		{"display string with uppercase hex", member + `;d=%"caf%C3%A9"`},
		{"display string not UTF-8", member + `;d=%"%ff"`},
		{"integer parameter with 16 digits", member + `;n=1234567890123456`},
		{"decimal with four fraction digits", member + `;n=1.2345`},
		{"decimal with 13 integer digits", member + `;n=1234567890123.1`},
		{"decimal ending in a point", member + `;n=1.`},
		{"date as decimal", member + `;d=@1.5`},
		{"boolean other than 0 or 1", member + `;b=?2`},
		{"unterminated inner list", `store=(1 2;epoch="` + testEpoch + `"`},
		{"invalid escape", member + `;s="a\x"`},
		{"control character in a string", member + ";s=\"a\x01\""},
		{"comma with no member", member + ", "},
		{"two commas", member + ",, " + member},
		{"only a comma", ","},
		{"star key", `*=1;epoch="` + testEpoch + `"`},
		{"one bad member among good ones", member + `, live=7`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := ParsePosition(tt.value); err == nil {
				t.Errorf("ParsePosition(%q) = %+v, want an error", tt.value, got)
			}
		})
	}
}

func TestParsePositionEmpty(t *testing.T) {
	for _, v := range []string{"", "   "} {
		got, err := ParsePosition(v)
		if err != nil || len(got) != 0 {
			t.Errorf("ParsePosition(%q) = %+v, %v, want no member and no error", v, got, err)
		}
	}
}

func TestValidNames(t *testing.T) {
	tests := []struct {
		name               string
		in                 string
		source, kind, epoc bool
	}{
		{"lowercase word", "store", true, true, false},
		{"underscore inside", "second_factors", true, true, false},
		{"digit first", "2fa", false, true, false},
		{"underscore first", "_accounts", false, true, false},
		{"empty", "", false, false, false},
		{"32 bytes", strings.Repeat("s", 32), true, true, false},
		{"33 bytes", strings.Repeat("s", 33), false, false, false},
		{"capital", "Store", false, false, false},
		{"hyphen", "pull-requests", false, false, false},
		{"dot", "a.b", false, false, false},
		{"non-ASCII", "störe", false, false, false},
		{"32 lowercase hex", testEpoch, false, true, true},
		{"32 uppercase hex", strings.ToUpper(testEpoch), false, false, false},
		{"31 hex", testEpoch[:31], false, true, false},
		{"33 hex", testEpoch + "0", false, false, false},
		{"32 chars with a g", "g" + testEpoch[1:], true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidSource(tt.in); got != tt.source {
				t.Errorf("ValidSource(%q) = %v, want %v", tt.in, got, tt.source)
			}
			if got := ValidKind(tt.in); got != tt.kind {
				t.Errorf("ValidKind(%q) = %v, want %v", tt.in, got, tt.kind)
			}
			if got := ValidEpoch(tt.in); got != tt.epoc {
				t.Errorf("ValidEpoch(%q) = %v, want %v", tt.in, got, tt.epoc)
			}
		})
	}
}
