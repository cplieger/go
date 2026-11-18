package measure

import (
	"slices"
	"time"
)

// ReadState says how the read behind a count went. The zero ReadState reads
// as [ReadUnread], so a scope no read reached never reads complete. A value
// outside the six reads as [ReadBlind] in [FoldRead] and [Read.Under], and
// [Emitter.EmitRead] drops it.
type ReadState string

const (
	// ReadComplete is a scope read whole.
	ReadComplete ReadState = "complete"
	// ReadPartial is a scope read in part: a cut list, some reads failed, a
	// stop mid-scope.
	ReadPartial ReadState = "partial"
	// ReadBlind is a scope nothing read can vouch for: every read failed, or
	// what it is read through failed.
	ReadBlind ReadState = "blind"
	// ReadUnread is a scope no read reached yet.
	ReadUnread ReadState = "unread"
	// ReadUnsupported is a read the credential or the upstream can never
	// make: a lasting limit, not a failure.
	ReadUnsupported ReadState = "unsupported"
	// ReadExcluded is a scope the person's configuration leaves out.
	ReadExcluded ReadState = "excluded"
)

// ReadStates returns the six states in declaration order, the values of the
// state label [NewReadDescs] declares.
func ReadStates() []string {
	return []string{
		string(ReadComplete), string(ReadPartial), string(ReadBlind),
		string(ReadUnread), string(ReadUnsupported), string(ReadExcluded),
	}
}

// norm returns s as the reads compare it: the zero value as unread, a value
// outside the six as blind.
func (s ReadState) norm() ReadState {
	switch s {
	case "":
		return ReadUnread
	case ReadComplete, ReadPartial, ReadBlind, ReadUnread, ReadUnsupported, ReadExcluded:
		return s
	}
	return ReadBlind
}

// worse ranks the states FoldRead takes the worst of.
var worse = map[ReadState]int{ReadComplete: 0, ReadPartial: 1, ReadUnread: 2, ReadBlind: 3}

// Read is one scope's read. Its zero value is unread with no time.
type Read struct {
	// AsOf is the last whole read's time, whatever State says; zero before
	// the first whole read.
	AsOf time.Time
	// State is how the latest read went.
	State ReadState
	// Reason is the app's code for why the read is not whole, empty when it
	// is.
	Reason string
}

// Whole returns a complete read at at.
func Whole(at time.Time) Read { return Read{State: ReadComplete, AsOf: at} }

// Cut returns r with state and reason and r's AsOf: a read that fails or
// stops after a whole one is prev.Cut(ReadPartial, reason), so only a whole
// read moves the time a page and a series show.
func (r Read) Cut(state ReadState, reason string) Read {
	return Read{State: state, AsOf: r.AsOf, Reason: reason}
}

// Under returns r as read through parent, since a read is never more
// complete than what it is read through. A read neither complete nor partial
// is kept, and so is any read under a complete or excluded parent, the
// latter with its own AsOf; under a blind parent it is blind, under any
// other it is partial. Otherwise AsOf is the older of the two, zero when
// either is. A state that changes takes the parent's reason.
func (r Read) Under(parent Read) Read {
	own, ps := r.State.norm(), parent.State.norm()
	r.State = own
	if (own != ReadComplete && own != ReadPartial) || ps == ReadExcluded {
		return r
	}
	r.AsOf = older(r.AsOf, parent.AsOf)
	switch ps {
	case ReadComplete:
		return r
	case ReadBlind:
		r.State = ReadBlind
	default:
		r.State = ReadPartial
	}
	if r.State != own {
		r.Reason = parent.Reason
	}
	return r
}

// older returns the earlier of a and b, zero when either is.
func older(a, b time.Time) time.Time {
	if a.IsZero() || b.IsZero() {
		return time.Time{}
	}
	if b.Before(a) {
		return b
	}
	return a
}

// FoldRead folds the reads of a scope's parts, or of the signals one count
// is computed from, into one read. No parts is unread; every part excluded
// is excluded; else, leaving the excluded out, every part unsupported is
// unsupported; else, leaving those out too, the worst of blind, unread,
// partial and complete. AsOf is the oldest among the parts counted, zero
// when any has none; Reason is the first worst part's, none when complete.
func FoldRead(parts ...Read) Read {
	if len(parts) == 0 {
		return Read{State: ReadUnread}
	}
	supported := func(s ReadState) bool { return s != ReadExcluded && s != ReadUnsupported }
	included := func(s ReadState) bool { return s != ReadExcluded }
	out, counted := Read{State: ReadExcluded}, func(ReadState) bool { return true }
	switch {
	case anyOf(parts, supported):
		out.State, counted = ReadComplete, supported
	case anyOf(parts, included):
		out.State, counted = ReadUnsupported, included
	}
	lasting, first := out.State != ReadComplete, true
	for _, p := range parts {
		s := p.State.norm()
		if !counted(s) {
			continue
		}
		if first {
			out.AsOf, first = p.AsOf, false
			if lasting {
				out.Reason = p.Reason
			}
		}
		out.AsOf = older(out.AsOf, p.AsOf)
		if !lasting && worse[s] > worse[out.State] {
			out.State, out.Reason = s, p.Reason
		}
	}
	return out
}

func anyOf(parts []Read, f func(ReadState) bool) bool {
	for _, p := range parts {
		if f(p.State.norm()) {
			return true
		}
	}
	return false
}

// ReadDescs is the pair of families one read scope exports: State, a state
// set over the scope's labels and the six states, and Complete, the last
// whole read's time.
type ReadDescs struct{ State, Complete *Desc }

// NewReadDescs declares the pair for stem over labels for one
// [NewCollector]: <stem>_state, a state set whose last label is state with
// the six [ReadStates], and <stem>_complete_timestamp_seconds, a gauge in
// seconds, 0 before the first whole read. what is the HELP text both
// start with. Its errors surface at [Registry.Register], as every Desc's do.
func NewReadDescs(stem, what string, labels []string, opts ...Option) ReadDescs {
	return ReadDescs{
		State: NewStateSetDesc(stem+"_state", what+": the state of its last read",
			slices.Concat(labels, []string{"state"}), ReadStates(), opts...),
		Complete: NewGaugeDesc(stem+"_complete_timestamp_seconds",
			what+": the time of its last whole read, 0 before the first", UnitSeconds, labels, opts...),
	}
}

// EmitRead emits r for one scope of d: its state, the zero state as unread,
// and its AsOf in Unix seconds, 0 when zero. labelValues are one per label
// d was declared over, in order. A state outside the six drops the state
// set's samples, as [Emitter.EmitState] drops an undeclared state.
func (e *Emitter) EmitRead(d ReadDescs, r Read, labelValues ...string) {
	state := r.State
	if state == "" {
		state = ReadUnread
	}
	e.EmitState(d.State, string(state), labelValues...)
	at := 0.0
	if !r.AsOf.IsZero() {
		at = float64(r.AsOf.Unix()) + float64(r.AsOf.Nanosecond())/1e9
	}
	e.Emit(d.Complete, at, labelValues...)
}
