package feedwire

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// feedCases is testdata/feed/cases.json, which the TypeScript feed suite
// reads too. A case with pad_to_bytes has its data followed by spaces up to
// that many bytes; a valid case with canonical false is accepted but is not
// what the encoder writes.
type feedCases struct {
	FeedVersion int `json:"feed_version"`
	Declared    struct {
		Kinds  map[string]string `json:"kinds"`
		Logged []string          `json:"logged"`
	} `json:"declared"`
	Changed   []frameCase    `json:"changed"`
	Listening []frameCase    `json:"listening"`
	Position  []positionCase `json:"position"`
}

type frameCase struct {
	Name       string          `json:"name"`
	Data       string          `json:"data"`
	PadToBytes int             `json:"pad_to_bytes"`
	Valid      bool            `json:"valid"`
	Canonical  *bool           `json:"canonical"`
	Decoded    json.RawMessage `json:"decoded"`
	Frozen     *caseFrozen     `json:"frozen"`
	Error      string          `json:"error"`
}

type positionCase struct {
	Name      string         `json:"name"`
	Value     string         `json:"value"`
	Valid     bool           `json:"valid"`
	Canonical *bool          `json:"canonical"`
	Decoded   []casePosition `json:"decoded"`
}

type caseChanged struct {
	Source string   `json:"source"`
	Epoch  string   `json:"epoch"`
	Kinds  []string `json:"kinds"`
	Rev    uint64   `json:"rev"`
}

type caseListening struct {
	Wire    string `json:"wire"`
	Sources []struct {
		Source string            `json:"source"`
		Epoch  string            `json:"epoch"`
		Floors map[string]uint64 `json:"floors"`
	} `json:"sources"`
}

type caseFrozen struct {
	Feed int    `json:"feed"`
	Wire string `json:"wire"`
}

type casePosition struct {
	Source string `json:"source"`
	Epoch  string `json:"epoch"`
	Rev    uint64 `json:"rev"`
}

func loadFeedCases(t testing.TB) feedCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "feed", "cases.json"))
	if err != nil {
		t.Fatalf("read cases.json: %v", err)
	}
	var fc feedCases
	if err := json.Unmarshal(raw, &fc); err != nil {
		t.Fatalf("parse cases.json: %v", err)
	}
	return fc
}

func (c frameCase) bytes() []byte {
	b := []byte(c.Data)
	if pad := c.PadToBytes - len(b); pad > 0 {
		b = append(b, strings.Repeat(" ", pad)...)
	}
	return b
}

func (c frameCase) canonical() bool { return c.Canonical == nil || *c.Canonical }

func (c positionCase) canonical() bool { return c.Canonical == nil || *c.Canonical }

func (c caseChanged) value() Changed {
	return Changed(c)
}

func (c caseListening) value() Listening {
	l := Listening{Wire: c.Wire, Sources: []ListeningSource{}}
	for _, s := range c.Sources {
		floors := s.Floors
		if floors == nil {
			floors = map[string]uint64{}
		}
		l.Sources = append(l.Sources, ListeningSource{Source: s.Source, Epoch: s.Epoch, Floors: floors})
	}
	return l
}

func TestFeedFixturesRoundTrip(t *testing.T) {
	fc := loadFeedCases(t)
	if fc.FeedVersion != Version {
		t.Fatalf("cases.json feed_version = %d, want Version %d", fc.FeedVersion, Version)
	}
	for kind, source := range fc.Declared.Kinds {
		if !ValidKind(kind) || !ValidSource(source) {
			t.Errorf("declared kind %q of source %q is not a valid name pair", kind, source)
		}
	}
	for _, c := range fc.Changed {
		t.Run(c.Name, func(t *testing.T) { checkChangedCase(t, c) })
	}
	for _, c := range fc.Listening {
		t.Run(c.Name, func(t *testing.T) { checkListeningCase(t, c) })
	}
	for _, c := range fc.Position {
		t.Run(c.Name, func(t *testing.T) { checkPositionCase(t, c) })
	}
}

func checkChangedCase(t *testing.T, c frameCase) {
	got, err := DecodeChanged(c.bytes())
	if !c.Valid {
		if err == nil {
			t.Fatalf("DecodeChanged accepted a refused case as %+v", got)
		}
		return
	}
	if err != nil {
		t.Fatalf("DecodeChanged error = %v", err)
	}
	var want caseChanged
	if err := json.Unmarshal(c.Decoded, &want); err != nil {
		t.Fatalf("parse decoded: %v", err)
	}
	if !reflect.DeepEqual(got, want.value()) {
		t.Fatalf("DecodeChanged = %+v, want %+v", got, want.value())
	}
	enc, err := EncodeChanged(want.value())
	if err != nil {
		t.Fatalf("EncodeChanged(decoded) error = %v", err)
	}
	if c.canonical() && string(enc) != c.Data {
		t.Fatalf("EncodeChanged(decoded) = %s, want the case's bytes %s", enc, c.Data)
	}
}

func checkListeningCase(t *testing.T, c frameCase) {
	data := c.bytes()
	frozen, perr := PeekListening(data)
	switch {
	case c.Frozen == nil && perr == nil:
		t.Fatalf("PeekListening = %+v, want an error", frozen)
	case c.Frozen != nil && perr != nil:
		t.Fatalf("PeekListening error = %v, want %+v", perr, *c.Frozen)
	case c.Frozen != nil && (frozen != Frozen{Feed: c.Frozen.Feed, Wire: c.Frozen.Wire}):
		t.Fatalf("PeekListening = %+v, want %+v", frozen, *c.Frozen)
	}
	got, err := DecodeListening(data)
	if c.Error == "other_format" && !errors.Is(err, ErrOtherFormat) {
		t.Fatalf("DecodeListening error = %v, want ErrOtherFormat", err)
	}
	if c.Error != "other_format" && errors.Is(err, ErrOtherFormat) {
		t.Fatalf("DecodeListening error = %v, which is not the other-format case", err)
	}
	if !c.Valid {
		if err == nil {
			t.Fatalf("DecodeListening accepted a refused case as %+v", got)
		}
		return
	}
	if err != nil {
		t.Fatalf("DecodeListening error = %v", err)
	}
	var want caseListening
	if err := json.Unmarshal(c.Decoded, &want); err != nil {
		t.Fatalf("parse decoded: %v", err)
	}
	if !reflect.DeepEqual(got, want.value()) {
		t.Fatalf("DecodeListening = %+v, want %+v", got, want.value())
	}
	enc, err := EncodeListening(want.value())
	if err != nil {
		t.Fatalf("EncodeListening(decoded) error = %v", err)
	}
	if c.canonical() && string(enc) != c.Data {
		t.Fatalf("EncodeListening(decoded) = %s, want the case's bytes %s", enc, c.Data)
	}
}

func checkPositionCase(t *testing.T, c positionCase) {
	got, err := ParsePosition(c.Value)
	if !c.Valid {
		if err == nil {
			t.Fatalf("ParsePosition accepted a refused case as %+v", got)
		}
		return
	}
	if err != nil {
		t.Fatalf("ParsePosition error = %v", err)
	}
	want := make([]Position, 0, len(c.Decoded))
	for _, p := range c.Decoded {
		want = append(want, Position(p))
	}
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("ParsePosition = %+v, want %+v", got, want)
	}
	if !c.canonical() {
		return
	}
	enc, err := FormatPosition(want)
	if err != nil {
		t.Fatalf("FormatPosition(decoded) error = %v", err)
	}
	if enc != c.Value {
		t.Fatalf("FormatPosition(decoded) = %q, want the case's value %q", enc, c.Value)
	}
}
