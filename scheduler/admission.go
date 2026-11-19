package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Start admits j, writing its record (a batch's: one queued line per item)
// before queueing it. j joins a stored record with its Key, its owner's held
// proposal of its scope, a queued job of its scope or covering it, or a
// running one of its scope inside Fresh, for a timer request or of a proposing
// kind, yielded too (Joined). A Hold(all) or shutdown during the write ends it
// interrupted. Errors: *RecentError; ErrQueueFull past a bound (record ended
// skipped, queue_full) or in a Hold(all) or after shutdown (no record);
// ErrStoreUnavailable, ctx's error, ErrBatchSize, ErrUnknownKind, ErrScope.
func (e *Executor[P]) Start(ctx context.Context, j Job[P]) (Started, error) {
	spec, ok := e.kinds[j.Kind]
	switch {
	case !ok:
		return Started{}, ErrUnknownKind
	case !validScope(j.Scope):
		return Started{}, ErrScope
	}
	en, err := e.newJob(spec, j)
	if err != nil {
		return Started{}, err
	}
	s, placed, err := e.place(ctx, spec, en)
	switch placed {
	case placeRefused:
		defer e.wrote()
		return e.refuse(ctx, en)
	case placeAdmitted:
		defer e.wrote()
		return e.admit(ctx, en)
	case placeAnswered:
	}
	return s, err
}

// placement is what place did with a request.
type placement uint8

const (
	placeAnswered placement = iota // place's Started and error are Start's answer
	placeRefused                   // past a bound, its write counted: refuse ends it queue_full
	placeAdmitted                  // live, in the bounds and its write counted: admit writes it
)

// place answers en's request from a held proposal, a waiting or running job,
// a hold or a Fresh window, or counts its record write and places it. mu is
// released by defer, since Fresh and Covers are called under it.
func (e *Executor[P]) place(ctx context.Context, spec *KindSpec[P], en *job[P]) (Started, placement, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok, err := e.matchLocked(ctx, spec, &en.j); ok || err != nil {
		return s, placeAnswered, err
	}
	if e.holds > 0 || e.closed {
		return Started{}, placeAnswered, ErrQueueFull
	}
	if s, ok, err := e.answerLocked(spec, &en.j); ok || err != nil {
		return s, placeAnswered, err
	}
	e.writeLocked()
	if e.fullLocked(&en.j) || e.proposalsFullLocked(spec, &en.j) {
		return Started{}, placeRefused, nil
	}
	e.live = append(e.live, en)
	e.waitLocked(en)
	return Started{}, placeAdmitted, nil
}

// matchLocked answers j from its owner's held proposal or a waiting job,
// waiting out a match whose record is still being written. It is called
// holding mu, releases it while it waits and holds it again on return.
func (e *Executor[P]) matchLocked(ctx context.Context, spec *KindSpec[P], j *Job[P]) (Started, bool, error) {
	for {
		if s, ok := e.heldLocked(spec, j); ok {
			return s, true, nil
		}
		s, admitting, ok := e.joinLocked(j)
		if ok || admitting == nil {
			return s, ok, nil
		}
		e.mu.Unlock()
		select {
		case <-admitting.admitted:
		case <-ctx.Done():
			e.mu.Lock()
			return Started{}, false, ctx.Err()
		}
		e.mu.Lock()
		// It may have started meanwhile; j still came before its run, so it
		// joins as it would have while the job was queued.
		if admitting.inserted && admitting.stopEnd == nil {
			return Started{Job: admitting.j.ID, Position: admitting.position(), Joined: true}, true, nil
		}
	}
}

// admit writes the record of en, which holds its place in the bounds, and
// queues it. A CredentialEnded, a covering start, a Hold(all) or a shutdown
// during the write ends it instead.
func (e *Executor[P]) admit(ctx context.Context, en *job[P]) (Started, error) {
	answered := false
	defer func() {
		// A panic out of Records.Insert, which wrote nothing then.
		if !answered {
			e.drop(en)
		}
	}()
	replay, err := e.insert(ctx, en)
	answered = true
	switch {
	case replay != "":
		e.drop(en)
		return Started{Job: replay, Joined: true}, nil
	case err != nil:
		e.drop(en)
		return Started{}, err
	}
	e.mu.Lock()
	if en.stopEnd == nil && (e.closed || e.holds > 0) {
		stop := End{Result: ResultInterrupted} // as the hold or shutdown ends a queued job
		en.stopEnd = &stop
	}
	if en.stopEnd != nil {
		// It stays live, so a Cancel finds it, until ended has written its end.
		e.unwaitLocked(en)
		en.state = stateEnded
		close(en.admitted)
		e.mu.Unlock()
		e.post(en, &Transition{Phase: PhaseQueued, At: en.rec.QueuedAt})
		e.ended(ctx, en, *en.stopEnd)
		return Started{Job: en.j.ID}, nil
	}
	en.state = stateQueued
	en.inserted = true
	if en.waits() {
		en.w = en.slot.join(tierOf(en.class))
	}
	s := Started{Job: en.j.ID, Position: en.position()}
	e.post(en, &Transition{Phase: PhaseQueued, At: en.rec.QueuedAt})
	e.wg.Add(1)
	go e.await(en)
	close(en.admitted)
	e.mu.Unlock()
	e.flush(en)
	return s, nil
}

// drop takes an admitting job whose record was not written out of the live
// jobs and the bounds.
func (e *Executor[P]) drop(en *job[P]) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unwaitLocked(en)
	en.state = stateEnded
	e.removeLocked(en)
	close(en.admitted)
}

// queueFull is the end of a request the queue refused after its record was
// written.
var queueFull = End{Result: ResultSkipped, Reasons: []Reason{{Code: ReasonQueueFull}}}

// newJob builds the admitting job of j with its queued record.
func (e *Executor[P]) newJob(spec *KindSpec[P], j Job[P]) (*job[P], error) {
	j.ID = ID(newID())
	en := &job[P]{
		spec:     spec,
		slot:     e.slots[spec.Slot],
		admitted: make(chan struct{}),
		gone:     make(chan struct{}),
		finished: make(chan struct{}),
		j:        j,
		class:    classOf(j.Trigger),
	}
	en.rec = Record{
		QueuedAt: time.Now(),
		ID:       j.ID,
		Kind:     j.Kind,
		Scope:    j.Scope,
		Trigger:  j.Trigger,
		Owner:    j.Owner,
		State:    StateQueued,
		Phase:    PhaseQueued,
		Rev:      1,
	}
	en.rec.Step.Of = len(spec.Phases)
	if spec.MaxItems > 0 {
		n := spec.Items(j.Payload)
		if n < 1 || n > spec.MaxItems {
			return nil, ErrBatchSize
		}
		en.rec.Lines = make([]Line, n)
		for i := range en.rec.Lines {
			en.rec.Lines[i].State = LineQueued
		}
	}
	if spec.Kept > 0 {
		b, err := json.Marshal(j.Payload)
		if err != nil {
			return nil, fmt.Errorf("scheduler: payload of kind %q: %w", j.Kind, err)
		}
		en.rec.Payload = b
	}
	return en, nil
}

// joinLocked answers j from a waiting job: one of its kind and scope (rule 1),
// else one whose kind covers it (rule 2). A match whose record is still being
// written answers that job to wait on instead.
func (e *Executor[P]) joinLocked(j *Job[P]) (s Started, admitting *job[P], ok bool) {
	match := func(q *job[P]) (Started, *job[P], bool) {
		if q.state == stateAdmitting {
			return Started{}, q, false
		}
		return Started{Job: q.j.ID, Position: q.position(), Joined: true}, nil, true
	}
	for _, q := range e.live {
		if q.waiting() && q.sameScope(j) {
			return match(q)
		}
	}
	for _, q := range e.live {
		if q.waiting() && q.spec.Covers != nil && q.spec.Covers(q.j, *j) {
			return match(q)
		}
	}
	return Started{}, nil, false
}

// sameScope reports whether the job is of j's kind and scope, and of j's
// owner for a proposing kind, whose proposal is its owner's.
func (en *job[P]) sameScope(j *Job[P]) bool {
	return en.j.Kind == j.Kind && en.j.Scope == j.Scope && (!en.spec.Proposes || en.j.Owner == j.Owner)
}

// waiting reports whether the job is admitting or queued.
func (en *job[P]) waiting() bool { return en.state == stateAdmitting || en.state == stateQueued }

// answerLocked answers j from the newest job that serves it (rule 3): one
// that started inside the kind's Fresh window joins it, and so does a timer
// request, which never queues a follow-up behind it, and a proposing kind's,
// whose scope holds its expectation. Otherwise a Fresh kind refuses j with
// *RecentError inside the window after its scope's last moving end (rule 4).
// ok false and a nil error leave j to be queued.
func (e *Executor[P]) answerLocked(spec *KindSpec[P], j *Job[P]) (s Started, ok bool, err error) {
	var run *job[P]
	for _, q := range e.live {
		if q.serves(j) && (run == nil || q.startedAt.After(run.startedAt)) {
			run = q
		}
	}
	now := time.Now()
	if run != nil {
		if j.Trigger == TriggerSchedule || spec.Proposes || (spec.Fresh != nil && now.Sub(run.startedAt) < spec.Fresh(j.Scope)) {
			return Started{Job: run.j.ID, Joined: true}, true, nil
		}
		return Started{}, false, nil
	}
	if until, held := e.recent[ScheduledScope{Kind: j.Kind, Scope: j.Scope}]; held && now.Before(until) {
		return Started{}, false, &RecentError{Until: until}
	}
	return Started{}, false, nil
}

// fullLocked reports whether a new waiting job of j's owner is past a bound.
func (e *Executor[P]) fullLocked(j *Job[P]) bool {
	return e.waiting >= e.maxQueued || (j.Owner != "" && e.owners[j.Owner] >= e.maxPerOwner)
}

// insert writes en's queued record with its key and keeps the record the
// store answers, whose QueuedAt is the store's stamp. A key a stored record
// already holds writes nothing and answers that record's id as replay.
// ErrCapacity reaches the caller as it is, since it names the store's bound
// and not a fault.
func (e *Executor[P]) insert(ctx context.Context, en *job[P]) (replay ID, err error) {
	if en.spec.Kept == 0 {
		return "", nil
	}
	stored, replayed, err := e.records.Insert(ctx, en.rec, en.j.Key)
	if err != nil {
		if errors.Is(err, ErrCapacity) {
			return "", err
		}
		return "", storeFault(err)
	}
	if replayed {
		return stored.ID, nil
	}
	en.recMu.Lock()
	en.rec = stored
	en.recMu.Unlock()
	return "", nil
}

// refuse writes the record of a request the queue has no place for and ends
// it skipped, queue_full; a replayed key answers its stored job instead.
func (e *Executor[P]) refuse(ctx context.Context, en *job[P]) (Started, error) {
	replay, err := e.insert(ctx, en)
	switch {
	case err != nil:
		return Started{}, err
	case replay != "":
		return Started{Job: replay, Joined: true}, nil
	}
	e.post(en, &Transition{Phase: PhaseQueued, At: en.rec.QueuedAt})
	e.ended(ctx, en, queueFull)
	return Started{}, ErrQueueFull
}

// ended writes the end of a job that never ran, takes it out of the live
// jobs and tells the Observer. A panic out of the end's write leaves the
// record open, as a failed write does, and still takes the job out.
func (e *Executor[P]) ended(ctx context.Context, en *job[P], end End) {
	gone := false
	defer func() {
		if !gone {
			e.unlive(en, time.Now())
		}
	}()
	stored, at, _ := e.end(ctx, en, end)
	e.unlive(en, at)
	gone = true
	e.emit(en, &Transition{Phase: en.rec.Phase, Result: stored.Result, At: at})
}

// unlive takes a job that never ran, ended at at, out of the live jobs and
// re-arms a scheduled scope whose stand-in wake waited on it.
func (e *Executor[P]) unlive(en *job[P], at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removeLocked(en)
	e.releaseStandInLocked(en.j.ID, at)
}

// endedAll writes the end each job left the queue with, as ended does. Each
// later end is written by defer, so a panic out of app code in one still
// ends the rest before it propagates.
func (e *Executor[P]) endedAll(ctx context.Context, ens []*job[P]) {
	if len(ens) == 0 {
		return
	}
	defer e.endedAll(ctx, ens[1:])
	e.ended(ctx, ens[0], *ens[0].stopEnd)
}

// endDequeued is endedAll for the jobs a hold or shutdown dequeued under the
// write writeLocked counted, which it ends by defer.
func (e *Executor[P]) endDequeued(ctx context.Context, ens []*job[P]) {
	defer e.wrote()
	e.endedAll(ctx, ens)
}
