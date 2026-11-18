// Package memrecords is an in-memory scheduler.Records for this module's
// tests. Every write commits at once, so Crash answers the state a restart
// would open. Fail, Capacity and EndIn drive the store's failure and
// own-commit paths.
package memrecords

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

// Op names one Records method, for Fail and Count.
type Op string

// The Records methods.
const (
	OpInsert   Op = "Insert"
	OpPut      Op = "Put"
	OpEnd      Op = "End"
	OpGet      Op = "Get"
	OpByKey    Op = "ByKey"
	OpDismiss  Op = "Dismiss"
	OpLive     Op = "Live"
	OpHistory  Op = "History"
	OpLastEnds Op = "LastEnds"
)

// ErrNotRunning is EndIn's answer for a record that is not running.
var ErrNotRunning = errors.New("memrecords: record not running")

// ErrCursor is History's answer for a cursor it did not hand out.
var ErrCursor = errors.New("memrecords: unknown cursor")

// The live view's bounds.
const (
	liveMaxAge  = 15 * time.Minute
	liveMaxDone = 50
)

// Store is the in-memory Records. The zero value is not usable; call New.
type Store struct {
	recs     map[scheduler.ID]scheduler.Record
	fail     map[Op]error
	calls    map[Op]int
	log      *slog.Logger
	order    []scheduler.ID
	ended    []scheduler.ID // in the order their ends were written
	capacity int
	mu       sync.Mutex
}

var _ scheduler.Records = (*Store)(nil)

// New answers an empty store with no bound.
func New() *Store {
	return &Store{
		recs:  map[scheduler.ID]scheduler.Record{},
		fail:  map[Op]error{},
		calls: map[Op]int{},
		log:   slog.Default(),
	}
}

// Logger sets where EndIn logs, slog.Default until set.
func (s *Store) Logger(l *slog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = l
}

// Fail makes every later call of op answer err without touching the store;
// a nil err clears it.
func (s *Store) Fail(op Op, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.fail, op)
		return
	}
	s.fail[op] = err
}

// Capacity bounds the store at n records: an Insert that would hold more
// answers scheduler.ErrCapacity and writes nothing. Zero lifts the bound.
func (s *Store) Capacity(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.capacity = n
}

// Count answers how many times op was called, failed calls included.
func (s *Store) Count(op Op) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[op]
}

// All answers every record in insert order.
func (s *Store) All() []scheduler.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]scheduler.Record, 0, len(s.order))
	for _, id := range s.order {
		r := s.recs[id]
		out = append(out, clone(&r))
	}
	return out
}

// EndIn writes e as id's end closed by scheduler.CloseEnd, as a kind's own
// commit does through its store, logging what it cut at ERROR:
// ErrNotRunning for a record that is not running, writing nothing.
func (s *Store) EndIn(id scheduler.ID, e scheduler.End) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	if !ok || r.State != scheduler.StateRunning {
		return ErrNotRunning
	}
	var cut scheduler.Cut
	r.Lines, e, cut = scheduler.CloseEnd(r.Lines, e)
	if cut != (scheduler.Cut{}) {
		s.log.Error("job end cut to its bounds", "kind", r.Kind, "scope", r.Scope, "job", r.ID,
			"reasons_dropped", cut.Reasons, "facts_bytes", cut.FactsBytes)
	}
	s.end(&r, e)
	return nil
}

// Crash answers a new store holding a copy of every committed record, with
// no failure or bound set: the file a restart opens.
func (s *Store) Crash() *Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := New()
	c.order = slices.Clone(s.order)
	c.ended = slices.Clone(s.ended)
	for id := range s.recs {
		r := s.recs[id]
		c.recs[id] = clone(&r)
	}
	return c
}

// Reopen does what a store does when it opens after a restart: every queued or
// running record ends interrupted, and each of its lines that had not ended
// goes to review with reason interrupted.
func (s *Store) Reopen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		r := s.recs[id]
		if r.State == scheduler.StateEnded {
			continue
		}
		for i, l := range r.Lines {
			if l.State == scheduler.LineQueued || l.State == scheduler.LineRunning {
				r.Lines[i] = scheduler.Line{State: scheduler.LineReview, Reason: scheduler.ReasonInterrupted}
			}
		}
		s.end(&r, scheduler.End{Result: scheduler.ResultInterrupted})
	}
}

// Insert writes r holding key, stamping QueuedAt and the key's Until at the
// wall time. A key some record already holds answers that record and true,
// writing nothing.
func (s *Store) Insert(_ context.Context, r scheduler.Record, key *scheduler.Key) (scheduler.Record, bool, error) { //nolint:gocritic // hugeParam: the scheduler.Records signature
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpInsert); err != nil {
		return scheduler.Record{}, false, err
	}
	if key != nil {
		if old, ok := s.byKey(key.Credential, key.Row, key.Key); ok {
			return clone(&old), true, nil
		}
	}
	if _, ok := s.recs[r.ID]; ok {
		return scheduler.Record{}, false, errors.New("memrecords: duplicate id " + string(r.ID))
	}
	if s.capacity > 0 && len(s.recs) >= s.capacity {
		return scheduler.Record{}, false, scheduler.ErrCapacity
	}
	r = clone(&r)
	r.QueuedAt, r.StartedAt, r.EndedAt = time.Now(), time.Time{}, time.Time{}
	r.Key = nil
	if key != nil {
		k := *key
		k.Until = r.QueuedAt.Add(k.Window)
		r.Key = &k
	}
	s.recs[r.ID] = r
	s.order = append(s.order, r.ID)
	return clone(&r), false, nil
}

// Put writes r over the stored record at the stored Rev plus one, whatever
// r.Rev says, unless the stored record has ended; then it writes nothing and
// answers nil. A stored skipped line r holds in review stays skipped. It
// stamps StartedAt and each newly reached phase at the wall time and keeps
// the stored stamps.
func (s *Store) Put(_ context.Context, r scheduler.Record) error { //nolint:gocritic // hugeParam: the scheduler.Records signature
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpPut); err != nil {
		return err
	}
	old, ok := s.recs[r.ID]
	if !ok {
		return scheduler.ErrUnknownJob
	}
	if old.State == scheduler.StateEnded {
		return nil
	}
	r = clone(&r)
	now := time.Now()
	r.Rev = old.Rev + 1
	r.QueuedAt, r.StartedAt, r.EndedAt = old.QueuedAt, old.StartedAt, old.EndedAt
	if r.StartedAt.IsZero() && r.State == scheduler.StateRunning {
		r.StartedAt = now
	}
	for i := range min(len(r.Lines), len(old.Lines)) {
		if old.Lines[i].State == scheduler.LineSkipped && r.Lines[i].State == scheduler.LineReview {
			r.Lines[i] = old.Lines[i]
		}
	}
	for i, p := range r.Phases {
		r.Phases[i].At = now
		if j := slices.IndexFunc(old.Phases, func(o scheduler.PhaseAt) bool { return o.Phase == p.Phase }); j >= 0 {
			r.Phases[i].At = old.Phases[j].At
		}
	}
	s.recs[r.ID] = r
	return nil
}

// End writes e unless the record already has an end and answers the stored
// record either way.
func (s *Store) End(_ context.Context, id scheduler.ID, e scheduler.End) (scheduler.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpEnd); err != nil {
		return scheduler.Record{}, err
	}
	r, ok := s.recs[id]
	if !ok {
		return scheduler.Record{}, scheduler.ErrUnknownJob
	}
	if r.End == nil {
		s.end(&r, e)
	}
	return clone(&r), nil
}

// Get answers the record of id.
func (s *Store) Get(_ context.Context, id scheduler.ID) (scheduler.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpGet); err != nil {
		return scheduler.Record{}, false, err
	}
	r, ok := s.recs[id]
	if !ok {
		return scheduler.Record{}, false, nil
	}
	return clone(&r), true, nil
}

// ByKey answers the record holding the idempotency key of credential on row.
func (s *Store) ByKey(_ context.Context, credential, row, key string) (scheduler.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpByKey); err != nil {
		return scheduler.Record{}, false, err
	}
	r, ok := s.byKey(credential, row, key)
	if !ok {
		return scheduler.Record{}, false, nil
	}
	return clone(&r), true, nil
}

// Dismiss moves line from review to skipped, keeping its reason and facts,
// and leaves the record's end as it was. A line in range that is not in
// review is left as it is and answers nil, so a repeat answers as the first
// did.
func (s *Store) Dismiss(_ context.Context, id scheduler.ID, line int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpDismiss); err != nil {
		return err
	}
	r, ok := s.recs[id]
	if !ok {
		return scheduler.ErrUnknownJob
	}
	if line < 0 || line >= len(r.Lines) {
		return scheduler.ErrUnknownLine
	}
	if r.Lines[line].State != scheduler.LineReview {
		return nil
	}
	r.Lines[line].State = scheduler.LineSkipped
	r.Rev++
	s.recs[id] = r
	return nil
}

// Live answers the queued and running records in insert order, then the
// newest 50 records that ended in the 15 minutes before now, newest first.
func (s *Store) Live(_ context.Context, now time.Time) ([]scheduler.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpLive); err != nil {
		return nil, err
	}
	var live, ended []scheduler.Record
	for _, id := range s.order {
		r := s.recs[id]
		switch {
		case r.State != scheduler.StateEnded:
			live = append(live, clone(&r))
		case !r.EndedAt.Before(now.Add(-liveMaxAge)):
			ended = append(ended, clone(&r))
		}
	}
	sortNewest(ended)
	if len(ended) > liveMaxDone {
		ended = ended[:liveMaxDone]
	}
	return append(live, ended...), nil
}

// History answers up to n ended records of kind ("" for every kind), newest
// first, after the cursor after; the cursor is the last answered record's ID
// and "" on the last page, and n below 1 answers an empty last page.
func (s *Store) History(_ context.Context, kind scheduler.Kind, after string, n int) ([]scheduler.Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpHistory); err != nil {
		return nil, "", err
	}
	var ended []scheduler.Record
	for _, id := range s.order {
		r := s.recs[id]
		if r.State == scheduler.StateEnded && (kind == "" || r.Kind == kind) {
			ended = append(ended, r)
		}
	}
	sortNewest(ended)
	start := 0
	if after != "" {
		i := slices.IndexFunc(ended, func(r scheduler.Record) bool { return string(r.ID) == after })
		if i < 0 {
			return nil, "", ErrCursor
		}
		start = i + 1
	}
	if n < 1 {
		return []scheduler.Record{}, "", nil
	}
	page := ended[start:]
	next := ""
	if len(page) > n {
		page = page[:n]
		next = string(page[n-1].ID)
	}
	out := make([]scheduler.Record, 0, len(page))
	for i := range page {
		out = append(out, clone(&page[i]))
	}
	return out, next, nil
}

// LastEnds answers each kind and scope's last moving end: the latest end of
// a record that started and did not end cancelled or interrupted, the one
// written last among ends at one instant. It answers every scope, scheduled
// or not, so the executor can apply covering.
func (s *Store) LastEnds(_ context.Context) (map[scheduler.ScheduledScope]scheduler.LastEnd, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.call(OpLastEnds); err != nil {
		return nil, err
	}
	out := map[scheduler.ScheduledScope]scheduler.LastEnd{}
	for _, id := range s.ended {
		r := s.recs[id]
		if r.StartedAt.IsZero() ||
			r.End.Result == scheduler.ResultCancelled || r.End.Result == scheduler.ResultInterrupted {
			continue
		}
		k := scheduler.ScheduledScope{Kind: r.Kind, Scope: r.Scope}
		if old, ok := out[k]; !ok || !r.EndedAt.Before(old.At) {
			out[k] = scheduler.LastEnd{At: r.EndedAt, Result: r.End.Result}
		}
	}
	return out, nil
}

// call counts op and answers its injected failure. s.mu is held.
func (s *Store) call(op Op) error {
	s.calls[op]++
	return s.fail[op]
}

// end ends r with e at the wall time and stores it. s.mu is held.
func (s *Store) end(r *scheduler.Record, e scheduler.End) {
	e.Reasons = slices.Clone(e.Reasons)
	e.Facts = slices.Clone(e.Facts)
	r.End = &e
	r.State = scheduler.StateEnded
	r.EndedAt = time.Now()
	r.Rev++
	s.recs[r.ID] = *r
	s.ended = append(s.ended, r.ID)
}

func (s *Store) byKey(credential, row, key string) (scheduler.Record, bool) {
	for _, id := range s.order {
		r := s.recs[id]
		if k := r.Key; k != nil && k.Credential == credential && k.Row == row && k.Key == key {
			return r, true
		}
	}
	return scheduler.Record{}, false
}

func sortNewest(rs []scheduler.Record) {
	slices.SortStableFunc(rs, func(a, b scheduler.Record) int {
		return cmp.Or(b.EndedAt.Compare(a.EndedAt), cmp.Compare(b.ID, a.ID))
	})
}

// clone deep-copies *p, so no caller shares the store's slices or pointers.
func clone(p *scheduler.Record) scheduler.Record {
	r := *p
	r.Payload = slices.Clone(r.Payload)
	r.Phases = slices.Clone(r.Phases)
	if r.Lines != nil {
		lines := make([]scheduler.Line, len(r.Lines))
		for i, l := range r.Lines {
			l.Facts = slices.Clone(l.Facts)
			lines[i] = l
		}
		r.Lines = lines
	}
	if r.End != nil {
		e := *r.End
		e.Reasons = slices.Clone(e.Reasons)
		e.Facts = slices.Clone(e.Facts)
		r.End = &e
	}
	if r.Key != nil {
		k := *r.Key
		r.Key = &k
	}
	return r
}
