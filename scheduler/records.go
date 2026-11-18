package scheduler

import (
	"context"
	"encoding/json"
	"time"
)

// Records stores one record per job. bolttable/jobstore is the implementation
// an app wires; every method is safe for concurrent use.
type Records interface {
	// Insert writes a new record. With a key it first looks the key up and,
	// when a record holds it, answers that record and true without writing
	// (a keyed resend). It answers ErrCapacity when no record may make room.
	Insert(ctx context.Context, r Record, key *Key) (Record, bool, error)
	// Put writes r over the stored record when r.Rev is newer: queued to
	// running, a phase reached.
	Put(ctx context.Context, r Record) error
	// End writes e unless the record already has an end (one written in the
	// job's own commit) and answers the stored record either way.
	End(ctx context.Context, id ID, e End) (Record, error)
	// Get answers the record of id, false when there is none.
	Get(ctx context.Context, id ID) (Record, bool, error)
	// ByKey answers the record holding the idempotency key of credential on
	// row.
	ByKey(ctx context.Context, credential, row, key string) (Record, bool, error)
	// Dismiss drops line from id's review list: ErrUnknownJob, ErrUnknownLine.
	Dismiss(ctx context.Context, id ID, line int) error
	// Live answers the queued and running records and the live view's ended
	// ones at now.
	Live(ctx context.Context, now time.Time) ([]Record, error)
	// History answers up to n ended records of kind ("" for every kind)
	// newest first, after the cursor after, and the cursor of the next page
	// ("" on the last).
	History(ctx context.Context, kind Kind, after string, n int) ([]Record, string, error)
	// LastEnds answers each scheduled scope's last moving end: the end of a
	// job that started and did not end cancelled or interrupted.
	LastEnds(ctx context.Context) (map[Scheduled]LastEnd, error)
}

// The record states.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateEnded   = "ended"
)

// Record is one stored job: Job's fields with the payload as JSON, plus its
// life.
type Record struct {
	ID      ID
	Kind    Kind
	Scope   string
	Payload json.RawMessage
	Trigger Trigger
	Owner   string

	State  string // StateQueued, StateRunning or StateEnded
	Phase  Phase
	Step   struct{ N, Of int }
	Phases []PhaseAt // each distinct phase reached with its first time, at most 32
	End    *End

	QueuedAt, StartedAt, EndedAt time.Time

	Lines []Line // a batch's, one per item
	Key   *Key   // the start's idempotency slot, when it had one
	Rev   uint64
}

// PhaseAt is the first time a job reached a phase.
type PhaseAt struct {
	Phase Phase
	At    time.Time
}

// LastEnd is a scheduled scope's last moving end.
type LastEnd struct {
	At     time.Time
	Result Result
}

// Scheduled names one scheduled scope.
type Scheduled struct {
	Kind  Kind
	Scope string
}

// Key is a start's idempotency slot: the credential, the row's name and the
// key the client sent. Until is QueuedAt plus the row's window.
type Key struct {
	Credential, Row, Key string
	Fingerprint          [32]byte
	Until                time.Time
}
