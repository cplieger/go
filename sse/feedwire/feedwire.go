package feedwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
)

// Version is the feed frame format. Every change to a frame member or to the
// Feed-Position header raises it, together with the fixtures in
// testdata/feed/cases.json and FEED_VERSION in @cplieger/sse/feed.
const Version = 1

// MaxRev is the largest revision a frame or a Feed-Position value carries: the
// largest RFC 9651 Integer (15 digits), below 2^53 - 1 so a browser number
// holds it exactly.
const MaxRev = 999_999_999_999_999

// maxFrameBytes bounds a frame's data before it is parsed; the browser half
// applies the same bound to the data's UTF-8 length.
const maxFrameBytes = 64 << 10

// maxKinds bounds the kinds of one changed frame and of one listening frame.
const maxKinds = 64

// maxSafeInteger is 2^53 - 1, the largest integer a browser number holds exactly.
const maxSafeInteger = 1<<53 - 1

// Changed is the data of a changed frame: one commit of Source at Rev, naming
// the kinds it touched.
type Changed struct {
	Source string   // ValidSource
	Epoch  string   // ValidEpoch
	Kinds  []string // 1 to 64 ValidKind names, sorted, no repeat
	Rev    uint64   // 1 to MaxRev
}

// ListeningSource is one source of a listening frame: its epoch and each
// kind's floor.
type ListeningSource struct {
	Floors map[string]uint64 // ValidKind to 1 to MaxRev; a kind at 0 is omitted
	Source string            // ValidSource
	Epoch  string            // ValidEpoch
}

// Listening is the data of a listening frame. The encoder also writes the
// frozen member "feed": Version on every frame.
type Listening struct {
	Wire    string            // 16 lowercase hex, or "" when the member is absent
	Sources []ListeningSource // no repeated source, at most 64 kinds in all
}

// Frozen holds the two members of a listening frame that keep their name, type
// and place in every feed format.
type Frozen struct {
	Wire string // "" when absent
	Feed int    // the format the frame is written in
}

// ErrOtherFormat is wrapped by DecodeListening when the frame's feed member is
// not Version.
var ErrOtherFormat = errors.New("feedwire: listening frame written in another feed format")

// Position is one member of a Feed-Position value: the source's view position
// taken before a read.
type Position struct {
	Source string // ValidSource
	Epoch  string // ValidEpoch
	Rev    uint64 // 0 to MaxRev
}

// ValidSource reports whether name is a source name: a lowercase letter, then
// at most 31 lowercase letters, digits or underscores. It is the code grammar
// narrowed to what RFC 9651 admits as a dictionary key.
func ValidSource(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	return ValidKind(name)
}

// ValidKind reports whether name is a kind name: 1 to 32 bytes of lowercase
// letters, digits and underscores.
func ValidKind(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i := range len(name) {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

// ValidEpoch reports whether epoch is 32 lowercase hex.
func ValidEpoch(epoch string) bool { return lowerHex(epoch, 32) }

func validWire(wire string) bool { return lowerHex(wire, 16) }

func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := range n {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validRev(rev uint64) bool { return rev >= 1 && rev <= MaxRev }

var (
	errTooLarge  = errors.New("feedwire: frame data above 64 KiB")
	errNotObject = errors.New("feedwire: frame data is not a JSON object")
)

// frameObject parses data as one JSON object. Its members' names are matched
// exactly, unlike encoding/json's struct fields, and a repeated name keeps its
// last value, as JSON.parse does.
func frameObject(data []byte) (map[string]json.RawMessage, error) {
	if len(data) > maxFrameBytes {
		return nil, errTooLarge
	}
	if t := bytes.TrimLeft(data, " \t\r\n"); len(t) == 0 || t[0] != '{' {
		return nil, errNotObject
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, errNotObject
	}
	return obj, nil
}

func object(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var obj map[string]json.RawMessage
	return obj, json.Unmarshal(raw, &obj) == nil
}

func array(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var arr []json.RawMessage
	return arr, json.Unmarshal(raw, &arr) == nil
}

func str(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	return s, json.Unmarshal(raw, &s) == nil
}

// integer reads a JSON number as an IEEE double, as the browser half does, so
// 1.0 and 1e3 are integers and both halves give one verdict on every input.
func integer(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.Trunc(f) != f {
		return 0, false
	}
	return f, true
}

func rev(raw json.RawMessage, low uint64) (uint64, bool) {
	f, ok := integer(raw)
	if !ok || f < float64(low) || f > MaxRev {
		return 0, false
	}
	return uint64(f), true
}

// onlyMembers reports whether obj holds every required member and no member
// outside required and optional.
func onlyMembers(obj map[string]json.RawMessage, required, optional []string) bool {
	n := 0
	for _, name := range required {
		if _, ok := obj[name]; !ok {
			return false
		}
		n++
	}
	for _, name := range optional {
		if _, ok := obj[name]; ok {
			n++
		}
	}
	return n == len(obj)
}
