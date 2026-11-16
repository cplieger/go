package feedwire

import (
	"errors"
	"strconv"
)

var (
	errChangedShape  = errors.New("feedwire: changed frame members are not source, epoch, rev and kinds")
	errChangedSource = errors.New("feedwire: changed frame source is not a source name")
	errChangedEpoch  = errors.New("feedwire: changed frame epoch is not 32 lowercase hex")
	errChangedRev    = errors.New("feedwire: changed frame rev is not an integer from 1 to MaxRev")
	errChangedKinds  = errors.New("feedwire: changed frame kinds are not 1 to 64 sorted, distinct kind names")
)

var changedMembers = []string{"source", "epoch", "rev", "kinds"}

func (c Changed) check() error {
	switch {
	case !ValidSource(c.Source):
		return errChangedSource
	case !ValidEpoch(c.Epoch):
		return errChangedEpoch
	case !validRev(c.Rev):
		return errChangedRev
	case len(c.Kinds) == 0 || len(c.Kinds) > maxKinds:
		return errChangedKinds
	}
	for i, k := range c.Kinds {
		if !ValidKind(k) || (i > 0 && c.Kinds[i-1] >= k) {
			return errChangedKinds
		}
	}
	return nil
}

// EncodeChanged returns the frame data for c, refusing any value outside the
// bounds on Changed.
func EncodeChanged(c Changed) ([]byte, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	b := make([]byte, 0, 96+len(c.Kinds)*36)
	b = append(b, `{"source":"`...)
	b = append(b, c.Source...)
	b = append(b, `","epoch":"`...)
	b = append(b, c.Epoch...)
	b = append(b, `","rev":`...)
	b = strconv.AppendUint(b, c.Rev, 10)
	b = append(b, `,"kinds":[`...)
	for i, k := range c.Kinds {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		b = append(b, k...)
		b = append(b, '"')
	}
	return append(b, "]}"...), nil
}

// DecodeChanged parses a changed frame's data strictly: at most 64 KiB, the
// four members and no other, each within the bounds on Changed.
func DecodeChanged(data []byte) (Changed, error) {
	obj, err := frameObject(data)
	if err != nil {
		return Changed{}, err
	}
	if !onlyMembers(obj, changedMembers, nil) {
		return Changed{}, errChangedShape
	}
	var c Changed
	var ok bool
	if c.Source, ok = str(obj["source"]); !ok {
		return Changed{}, errChangedSource
	}
	if c.Epoch, ok = str(obj["epoch"]); !ok {
		return Changed{}, errChangedEpoch
	}
	if c.Rev, ok = rev(obj["rev"], 1); !ok {
		return Changed{}, errChangedRev
	}
	kinds, ok := array(obj["kinds"])
	if !ok || len(kinds) > maxKinds {
		return Changed{}, errChangedKinds
	}
	c.Kinds = make([]string, len(kinds))
	for i, raw := range kinds {
		if c.Kinds[i], ok = str(raw); !ok {
			return Changed{}, errChangedKinds
		}
	}
	if err := c.check(); err != nil {
		return Changed{}, err
	}
	return c, nil
}
