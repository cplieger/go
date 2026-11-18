package chartdata

import (
	"slices"
	"time"

	"github.com/cplieger/go/measure/v2"
)

// Option sets an optional part of a frame. An option the frame's type cannot
// take is refused with [RuleOption].
type Option func(*options)

// Range is a span from From to To, such as an axis's domain.
type Range struct {
	From float64
	To   float64
}

type optionKind uint8

const (
	optRefs optionKind = 1 << iota
	optNotes
	optDomain
	optOpen
	optResets
	optRead
)

// optMarks are the options every frame takes.
const optMarks = optRefs | optNotes | optRead

type options struct {
	domain *Range
	read   *measure.Read
	// frameRead is the frame's read field, which applyOptions builds from read.
	frameRead *FrameRead
	refs      []FrameRef
	notes     []FrameNote
	resets    []time.Time
	used      optionKind
	open      bool
}

// WithRefs sets the frame's reference values, replacing any set before.
// Each value must be finite.
func WithRefs(refs ...FrameRef) Option {
	return func(o *options) { o.refs, o.used = refs, o.used|optRefs }
}

// WithNotes sets the frame's notes, replacing any set before. Each note's
// At must be finite.
func WithNotes(notes ...FrameNote) Option {
	return func(o *options) { o.notes, o.used = notes, o.used|optNotes }
}

// WithDomain sets the span a SeriesFrame's x axis or a BinsFrame's axis
// covers. From must be below To, and both finite.
func WithDomain(r Range) Option {
	return func(o *options) { o.domain, o.used = &r, o.used|optDomain }
}

// WithOpen says whether a SeriesFrame's last column, a BinsFrame's last bin
// or a CalendarFrame's last day is still filling.
func WithOpen(open bool) Option {
	return func(o *options) { o.open, o.used = open, o.used|optOpen }
}

// WithResets sets the instants a restart broke a SeriesFrame's or a
// BinsFrame's series at, in order.
func WithResets(at ...time.Time) Option {
	return func(o *options) { o.resets, o.used = at, o.used|optResets }
}

var optionNames = []struct {
	name string
	kind optionKind
}{
	{"WithRefs", optRefs},
	{"WithNotes", optNotes},
	{"WithDomain", optDomain},
	{"WithOpen", optOpen},
	{"WithResets", optResets},
	{"WithRead", optRead},
}

// applyOptions applies opts and refuses an option outside allowed or with a
// bad value, and returns the values the frame's own fields hold.
func applyOptions(opts []Option, allowed optionKind, values *count) (options, error) {
	var o options
	for _, opt := range opts {
		if opt == nil {
			return o, refuse(RuleOption, "options", "holds a nil Option")
		}
		opt(&o)
	}
	o.refs, o.notes = slices.Clone(o.refs), slices.Clone(o.notes)
	for _, n := range optionNames {
		if o.used&n.kind != 0 && allowed&n.kind == 0 {
			return o, refuse(RuleOption, "options", n.name+" has no place on this frame")
		}
	}
	if err := checkMarks(o.refs, o.notes, values); err != nil {
		return o, err
	}
	if err := o.checkAxis(); err != nil {
		return o, err
	}
	var err error
	o.frameRead, err = frameRead(o.read)
	return o, err
}

// checkAxis refuses a domain that is not finite and from a low to a high,
// and resets that do not strictly increase.
func (o *options) checkAxis() error {
	if d := o.domain; d != nil {
		if err := checkFinite("domain", d.From); err != nil {
			return err
		}
		if err := checkFinite("domain", d.To); err != nil {
			return err
		}
		if d.From >= d.To {
			return refuse(RuleOrder, "domain", "must run from a low to a high")
		}
	}
	for i := 1; i < len(o.resets); i++ {
		if o.resets[i].UnixMilli() <= o.resets[i-1].UnixMilli() {
			return refusef(RuleOrder, "resets", "must strictly increase, at %d", i)
		}
	}
	return nil
}

func checkMarks(refs []FrameRef, notes []FrameNote, values *count) error {
	for i, r := range refs {
		if err := checkFinite(indexed("refs", i)+".value", r.Value); err != nil {
			return err
		}
	}
	for i, n := range notes {
		if err := checkFinite(indexed("notes", i)+".at", n.At); err != nil {
			return err
		}
	}
	return values.add(len(refs)+len(notes), "refs")
}

func (o *options) domainSlice() []float64 {
	if o.domain == nil {
		return nil
	}
	return []float64{o.domain.From, o.domain.To}
}

func (o *options) resetMillis() []int64 {
	return millis(o.resets)
}

func millis(ts []time.Time) []int64 {
	if len(ts) == 0 {
		return nil
	}
	out := make([]int64, len(ts))
	for i, t := range ts {
		out[i] = t.UnixMilli()
	}
	return out
}
