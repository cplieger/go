package measure

import "time"

// ReadState says how the read behind a count went.
type ReadState string

const (
	ReadComplete    ReadState = "complete"
	ReadPartial     ReadState = "partial"
	ReadBlind       ReadState = "blind"
	ReadUnread      ReadState = "unread"
	ReadUnsupported ReadState = "unsupported"
	ReadExcluded    ReadState = "excluded"
)

// ReadStates returns the six states.
func ReadStates() []string { return nil }

// Read is one scope's read.
type Read struct {
	AsOf   time.Time
	State  ReadState
	Reason string
}

// Whole returns a complete read at at.
func Whole(at time.Time) Read { return Read{} }

// Cut returns r with a new state and reason.
func (r Read) Cut(state ReadState, reason string) Read { return Read{} }

// Under returns r as read through parent.
func (r Read) Under(parent Read) Read { return Read{} }

// FoldRead folds parts into one read.
func FoldRead(parts ...Read) Read { return Read{} }

// ReadDescs is the pair of families one read scope exports.
type ReadDescs struct{ State, Complete *Desc }

// NewReadDescs declares the pair for stem.
func NewReadDescs(stem, what string, labels []string, opts ...Option) ReadDescs {
	return ReadDescs{
		State:    NewGaugeDesc(stem, what, UnitNone, labels, opts...),
		Complete: NewGaugeDesc(stem+"_time", what, UnitNone, labels, opts...),
	}
}

// EmitRead emits r for one scope.
func (e *Emitter) EmitRead(d ReadDescs, r Read, labelValues ...string) {}

// Keep serves the last successful run's samples.
func (e *Emitter) Keep(err error) {}
