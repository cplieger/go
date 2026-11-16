package feedwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

var (
	errFeedMember     = errors.New("feedwire: listening frame has no integer feed member")
	errWireMember     = errors.New("feedwire: listening frame wire is not 16 lowercase hex")
	errListeningShape = errors.New("feedwire: listening frame members are not feed, wire and sources")
	errSourceShape    = errors.New("feedwire: listening source members are not source, epoch and floors")
	errSourceName     = errors.New("feedwire: listening source is not a source name")
	errSourceRepeated = errors.New("feedwire: listening frame names a source twice")
	errSourceEpoch    = errors.New("feedwire: listening source epoch is not 32 lowercase hex")
	errFloor          = errors.New("feedwire: listening floor is not a kind name with an integer from 1 to MaxRev")
	errFloorCount     = errors.New("feedwire: listening frame holds more than 64 kinds")
)

var (
	listeningRequired = []string{"feed", "sources"}
	listeningOptional = []string{"wire"}
	sourceMembers     = []string{"source", "epoch", "floors"}
)

func (l Listening) check() error {
	if l.Wire != "" && !validWire(l.Wire) {
		return errWireMember
	}
	kinds := 0
	for i, s := range l.Sources {
		switch {
		case !ValidSource(s.Source):
			return errSourceName
		case !ValidEpoch(s.Epoch):
			return errSourceEpoch
		case slices.ContainsFunc(l.Sources[:i], func(p ListeningSource) bool { return p.Source == s.Source }):
			return errSourceRepeated
		}
		for k, f := range s.Floors {
			if !ValidKind(k) || !validRev(f) {
				return errFloor
			}
		}
		kinds += len(s.Floors)
	}
	if kinds > maxKinds {
		return errFloorCount
	}
	return nil
}

// EncodeListening returns the frame data for l, "feed": Version first, then
// wire when it is not empty, then the sources in order, each source's floors
// sorted by kind. It refuses any value outside the bounds on Listening and
// ListeningSource, and data above 64 KiB.
func EncodeListening(l Listening) ([]byte, error) {
	if err := l.check(); err != nil {
		return nil, err
	}
	b := append(make([]byte, 0, 256), `{"feed":`...)
	b = strconv.AppendInt(b, Version, 10)
	if l.Wire != "" {
		b = append(b, `,"wire":"`...)
		b = append(b, l.Wire...)
		b = append(b, '"')
	}
	b = append(b, `,"sources":[`...)
	for i, s := range l.Sources {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"source":"`...)
		b = append(b, s.Source...)
		b = append(b, `","epoch":"`...)
		b = append(b, s.Epoch...)
		b = append(b, `","floors":{`...)
		for j, k := range slices.Sorted(maps.Keys(s.Floors)) {
			if j > 0 {
				b = append(b, ',')
			}
			b = append(b, '"')
			b = append(b, k...)
			b = append(b, `":`...)
			b = strconv.AppendUint(b, s.Floors[k], 10)
		}
		b = append(b, "}}"...)
	}
	b = append(b, "]}"...)
	if len(b) > maxFrameBytes {
		return nil, errTooLarge
	}
	return b, nil
}

// PeekListening reads only the frozen members of a listening frame's data, in
// any feed format: feed, a JSON integer within 2^53 - 1, and wire, 16 lowercase
// hex when present. Data above 64 KiB, or not a JSON object, is refused; no
// other member is looked at.
func PeekListening(data []byte) (Frozen, error) {
	obj, err := frameObject(data)
	if err != nil {
		return Frozen{}, err
	}
	return peek(obj)
}

func peek(obj map[string]json.RawMessage) (Frozen, error) {
	raw, ok := obj["feed"]
	if !ok {
		return Frozen{}, errFeedMember
	}
	feed, ok := integer(raw)
	if !ok || feed < -maxSafeInteger || feed > maxSafeInteger {
		return Frozen{}, errFeedMember
	}
	fr := Frozen{Feed: int(feed)}
	if raw, ok := obj["wire"]; ok {
		if fr.Wire, ok = str(raw); !ok || !validWire(fr.Wire) {
			return Frozen{}, errWireMember
		}
	}
	return fr, nil
}

// DecodeListening parses a listening frame's data strictly. It peeks first:
// a frame whose feed is not Version returns an error wrapping ErrOtherFormat;
// one of this format holds feed, sources and an optional wire and no other
// member, each within the bounds on Listening and ListeningSource.
func DecodeListening(data []byte) (Listening, error) {
	obj, err := frameObject(data)
	if err != nil {
		return Listening{}, err
	}
	fr, err := peek(obj)
	if err != nil {
		return Listening{}, err
	}
	if fr.Feed != Version {
		return Listening{}, fmt.Errorf("%w: feed %d, this package reads %d", ErrOtherFormat, fr.Feed, Version)
	}
	if !onlyMembers(obj, listeningRequired, listeningOptional) {
		return Listening{}, errListeningShape
	}
	raws, ok := array(obj["sources"])
	if !ok {
		return Listening{}, errListeningShape
	}
	l := Listening{Wire: fr.Wire, Sources: make([]ListeningSource, len(raws))}
	for i, raw := range raws {
		if l.Sources[i], err = decodeSource(raw); err != nil {
			return Listening{}, err
		}
	}
	if err := l.check(); err != nil {
		return Listening{}, err
	}
	return l, nil
}

func decodeSource(raw []byte) (ListeningSource, error) {
	obj, ok := object(raw)
	if !ok || !onlyMembers(obj, sourceMembers, nil) {
		return ListeningSource{}, errSourceShape
	}
	var s ListeningSource
	if s.Source, ok = str(obj["source"]); !ok {
		return ListeningSource{}, errSourceName
	}
	if s.Epoch, ok = str(obj["epoch"]); !ok {
		return ListeningSource{}, errSourceEpoch
	}
	floors, ok := object(obj["floors"])
	if !ok {
		return ListeningSource{}, errSourceShape
	}
	if len(floors) > maxKinds {
		return ListeningSource{}, errFloorCount
	}
	s.Floors = make(map[string]uint64, len(floors))
	for k, raw := range floors {
		if s.Floors[k], ok = rev(raw, 1); !ok {
			return ListeningSource{}, errFloor
		}
	}
	return s, nil
}
