package chartdata

import (
	"slices"
	"time"

	"github.com/cplieger/go/measure/v2"
)

// Times is one row given to NewEventsFrame: its key and the instants of its
// events, in order.
type Times struct {
	Key string
	At  []time.Time
}

// State is one observation given to NewStatesFrame: its instant and its
// state's key, empty for a gap.
type State struct {
	At  time.Time
	Key string
}

// NewEventsFrame builds an EventsFrame of d's events in rows, at most
// MaxRows, each with a key no other row has and its instants never
// decreasing. It takes WithRefs and WithNotes.
func NewEventsFrame(d measure.Declared, rows []Times, opts ...Option) (EventsFrame, error) {
	if err := checkMost("rows", len(rows), MaxRows, "rows"); err != nil {
		return EventsFrame{}, err
	}
	charge := 0
	for _, r := range rows {
		charge += max(1, len(r.At))
	}
	decl, o, err := begin(d, opts, optMarks, charge,
		func() error { return checkKeys("rows", func(i int) string { return rows[i].Key }, len(rows)) })
	if err != nil {
		return EventsFrame{}, err
	}
	out := make([]FrameTimes, len(rows))
	for i, r := range rows {
		for k := 1; k < len(r.At); k++ {
			if r.At[k].Before(r.At[k-1]) {
				return EventsFrame{}, refuse(RuleOrder, indexed(indexed("rows", i)+".at", k), "must not decrease")
			}
		}
		at := millis(r.At)
		if at == nil {
			at = []int64{}
		}
		out[i] = FrameTimes{Key: r.Key, At: at}
	}
	return EventsFrame{Type: TypeEvents, Name: decl.Name, Rows: out, Refs: o.refs, Notes: o.notes}, nil
}

// NewStatesFrame builds a StatesFrame of d's observations, oldest first, with
// instants that strictly increase. When d is a state set, every key names one
// of its states. It takes WithRefs and WithNotes.
func NewStatesFrame(d measure.Declared, states []State, opts ...Option) (StatesFrame, error) {
	decl, o, err := begin(d, opts, optMarks, len(states))
	if err != nil {
		return StatesFrame{}, err
	}
	keys := make([]*string, len(states))
	at := make([]int64, len(states))
	for i, s := range states {
		field := indexed("states", i)
		if i > 0 && !s.At.After(states[i-1].At) {
			return StatesFrame{}, refuse(RuleOrder, field+".at", "must strictly increase")
		}
		at[i] = s.At.UnixMilli()
		if s.Key == "" {
			continue
		}
		if decl.Kind == measure.KindStateSet && !slices.Contains(decl.States, s.Key) {
			return StatesFrame{}, refusef(RuleShape, field+".key", "names %q, which %s does not declare", s.Key, decl.Name)
		}
		key := s.Key
		keys[i] = &key
	}
	return StatesFrame{Type: TypeStates, Name: decl.Name, Keys: keys, At: at, Refs: o.refs, Notes: o.notes}, nil
}

// NewValueFrame builds a ValueFrame of d's meters, each with a key no other
// meter has, Min below Max, and its value and any of Low, High and Optimum
// finite and between them, Low at or below High. It takes WithRefs and
// WithNotes.
func NewValueFrame(d measure.Declared, meters []FrameMeter, opts ...Option) (ValueFrame, error) {
	decl, o, err := begin(d, opts, optMarks, len(meters),
		func() error { return checkKeys("meters", func(i int) string { return meters[i].Key }, len(meters)) })
	if err != nil {
		return ValueFrame{}, err
	}
	out := make([]FrameMeter, len(meters))
	for i, m := range meters {
		if out[i], err = buildMeter(indexed("meters", i), m); err != nil {
			return ValueFrame{}, err
		}
	}
	return ValueFrame{Type: TypeValue, Name: decl.Name, Unit: string(decl.Unit), Meters: out, Refs: o.refs, Notes: o.notes}, nil
}

func buildMeter(field string, m FrameMeter) (FrameMeter, error) {
	if err := checkFinite(field+".min", m.Min); err != nil {
		return FrameMeter{}, err
	}
	if err := checkFinite(field+".max", m.Max); err != nil {
		return FrameMeter{}, err
	}
	if m.Min >= m.Max {
		return FrameMeter{}, refuse(RuleOrder, field, "must have min below max")
	}
	for _, v := range []struct {
		at   *float64
		name string
	}{{&m.Value, "value"}, {m.Low, "low"}, {m.High, "high"}, {m.Optimum, "optimum"}} {
		if v.at == nil {
			continue
		}
		if err := checkFinite(field+"."+v.name, *v.at); err != nil {
			return FrameMeter{}, err
		}
		if *v.at < m.Min || *v.at > m.Max {
			return FrameMeter{}, refuse(RuleShape, field+"."+v.name, "must lie from min to max")
		}
	}
	if m.Low != nil && m.High != nil && *m.Low > *m.High {
		return FrameMeter{}, refuse(RuleOrder, field, "must have low at or below high")
	}
	out := m
	out.Low, out.High, out.Optimum = copyPtr(m.Low), copyPtr(m.High), copyPtr(m.Optimum)
	return out, nil
}

func copyPtr(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
