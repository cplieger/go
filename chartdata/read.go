package chartdata

import (
	"cmp"
	"slices"
	"time"

	"github.com/cplieger/go/measure/v2"
)

// MaxReasonBytes is the longest reason a read carries.
const MaxReasonBytes = 64

// Count is an API row's figure for a count read elsewhere: the last whole
// read's value and time, and how the latest read went. Value and AsOf are
// both null before the first whole read, and both set after it.
type Count struct {
	// Value is the last whole read's value, never a later cut read's.
	Value *float64          `json:"value"`
	State measure.ReadState `json:"state"`
	// AsOf is the last whole read's instant, an RFC 3339 string.
	AsOf *time.Time `json:"as_of"`
	// Reason is the app's code for why the read is not whole, absent when
	// it is.
	Reason string `json:"reason,omitempty"`
}

// CountOf builds the Count of last, the value of r's last whole read, nil
// before the first. It refuses by [RuleRead] a state outside the six, a
// complete read with a reason or with no AsOf, a reason past
// [MaxReasonBytes] or outside [a-z0-9_], an AsOf time.Time's JSON cannot
// write, and a last given without an AsOf or an AsOf without a last; and
// a last that is not finite or past [MaxMagnitude] as a frame's value is
// refused. The zero r reads as unread.
func CountOf(last *float64, r measure.Read) (Count, error) {
	state, asOf, err := checkRead("", r)
	if err != nil {
		return Count{}, err
	}
	switch {
	case state == measure.ReadComplete && asOf == nil:
		return Count{}, refuse(RuleRead, "as_of", "must be set on a complete count")
	case last != nil && asOf == nil:
		return Count{}, refuse(RuleRead, "value", "must be null before the first whole read, when as_of is")
	case last == nil && asOf != nil:
		return Count{}, refuse(RuleRead, "value", "must hold the last whole read's value when as_of is set")
	}
	c := Count{State: state, AsOf: asOf, Reason: r.Reason}
	if last != nil {
		if err := checkFinite("value", *last); err != nil {
			return Count{}, err
		}
		v := *last
		c.Value = &v
	}
	return c, nil
}

// Read is how a read went, on the wire: beside a figure that is not a
// count, such as a list's total, and as a frame's read when the read was not
// whole.
type Read struct {
	State measure.ReadState `json:"state"`
	// AsOf is the last whole read's instant, an RFC 3339 string; null
	// before the first.
	AsOf *time.Time `json:"as_of"`
	// Reason is the app's code for why the read is not whole.
	Reason string `json:"reason,omitempty"`
}

// ReadOf is r on the wire, refused by [RuleRead] as [CountOf] refuses a
// read, a complete r with no AsOf among them. It writes every state,
// complete included.
func ReadOf(r measure.Read) (Read, error) {
	state, asOf, err := checkRead("", r)
	if err != nil {
		return Read{}, err
	}
	if state == measure.ReadComplete && asOf == nil {
		return Read{}, refuse(RuleRead, "as_of", "must be set on a complete read")
	}
	return Read{State: state, AsOf: asOf, Reason: r.Reason}, nil
}

// WithRead marks the frame as built from r. It is refused by [RuleRead] for
// a state outside the six, a complete read with a reason, a reason past
// [MaxReasonBytes] or outside [a-z0-9_], and an AsOf time.Time's JSON cannot
// write. A complete r those lines do not refuse adds nothing, one with no
// AsOf included, so a frame built from a whole read encodes as one built
// with no option. Every frame takes it.
func WithRead(r measure.Read) Option {
	return func(o *options) { o.read, o.used = &r, o.used|optRead }
}

// frameRead is the read field WithRead's r gives a frame, nil when whole.
func frameRead(r *measure.Read) (*Read, error) {
	if r == nil {
		return nil, nil
	}
	state, asOf, err := checkRead("read.", *r)
	if err != nil || state == measure.ReadComplete {
		return nil, err
	}
	return &Read{State: state, AsOf: asOf, Reason: r.Reason}, nil
}

// checkRead refuses what rule read refuses on a Count, a Read and a frame's
// read, naming each field after prefix, and returns r's state, the zero one
// as unread, and a copy of its AsOf, nil when zero.
func checkRead(prefix string, r measure.Read) (measure.ReadState, *time.Time, error) {
	state := cmp.Or(r.State, measure.ReadUnread)
	if !slices.Contains(measure.ReadStates(), string(state)) {
		return "", nil, refusef(RuleRead, prefix+"state", "must be one of the six read states, not %q", r.State)
	}
	if state == measure.ReadComplete && r.Reason != "" {
		return "", nil, refuse(RuleRead, prefix+"reason", "must be empty on a complete read")
	}
	if len(r.Reason) > MaxReasonBytes {
		return "", nil, refusef(RuleRead, prefix+"reason", "holds %d bytes, past the most, %d", len(r.Reason), MaxReasonBytes)
	}
	if !isCode(r.Reason) {
		return "", nil, refuse(RuleRead, prefix+"reason", "must be a code of a-z, 0-9 and _")
	}
	if r.AsOf.IsZero() {
		return state, nil, nil
	}
	if _, err := r.AsOf.MarshalJSON(); err != nil {
		return "", nil, refuse(RuleRead, prefix+"as_of", "must be an instant RFC 3339 can write: a year from 0 to 9999, a zone offset under 24 hours")
	}
	at := r.AsOf
	return state, &at, nil
}

func isCode(s string) bool {
	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}
