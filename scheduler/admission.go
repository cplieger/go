package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Start admits j, writing its record (a batch's with one queued line per item)
// before it is queued. A stored record holding j's Key, its owner's held
// proposal of its scope, a queued job of its scope or one covering it, or a
// running one of its scope inside Fresh, of a proposing kind or serving a
// timer request, answers it instead (Joined), running nothing. It answers
// *RecentError, ErrQueueFull past a queue or proposal bound (the record ended
// skipped, queue_full) and during a Hold(all) (no record), ErrStoreUnavailable
// when the record cannot be written, ErrBatchSize, ErrUnknownKind, ErrScope.
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

	e.mu.Lock()
	if s, ok, err := e.matchLocked(ctx, spec, &en.j); ok || err != nil {
		e.mu.Unlock()
		return s, err
	}
	if e.holds > 0 {
		e.mu.Unlock()
		return Started{}, ErrQueueFull
	}
	if s, ok, err := e.answerLocked(spec, &en.j); ok || err != nil {
		e.mu.Unlock()
		return s, err
	}
	if e.fullLocked(&en.j) || e.proposalsFullLocked(spec, &en.j) {
		e.mu.Unlock()
		return e.refuse(ctx, en)
	}
	e.live = append(e.live, en)
	e.waitLocked(en)
	e.mu.Unlock()
	return e.admit(ctx, en)
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
// queues it. A CredentialEnded during the write ends it instead, cancelled.
func (e *Executor[P]) admit(ctx context.Context, en *job[P]) (Started, error) {
	replay, err := e.insert(ctx, en)
	e.mu.Lock()
	if replay != "" {
		e.unwaitLocked(en)
		en.state = stateEnded
		e.removeLocked(en)
		close(en.admitted)
		e.mu.Unlock()
		return Started{Job: replay, Joined: true}, nil
	}
	stopped := err == nil && en.stopEnd != nil
	if err == nil && !stopped && (e.closed || e.holds > 0) {
		err = errClosed
	}
	if err != nil || stopped {
		e.unwaitLocked(en)
		en.state = stateEnded
		if !stopped {
			// A stopped job stays live, so a Cancel finds it, until ended
			// has written its end.
			e.removeLocked(en)
		}
		close(en.admitted)
		e.mu.Unlock()
		switch {
		case stopped:
			e.post(en, &Transition{Phase: PhaseQueued, At: en.rec.QueuedAt})
			e.ended(ctx, en, *en.stopEnd)
			return Started{Job: en.j.ID}, nil
		case errors.Is(err, errClosed):
			e.post(en, &Transition{Phase: PhaseQueued, At: en.rec.QueuedAt})
			e.ended(ctx, en, queueFull)
			return Started{}, ErrQueueFull
		}
		return Started{}, err
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

// errClosed is a Start that met the executor's end or a Hold(all); it
// answers as a full queue.
var errClosed = errors.New("scheduler: closed")

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

// answerLocked answers j from a running job of its kind and scope that no
// cancel has stopped and that is not yielded (rule 3):
// one that started inside the kind's Fresh window joins it, and so does a
// timer request, which never queues a follow-up behind it, and a proposing
// kind's, whose scope holds its expectation. Otherwise a Fresh
// kind refuses j with *RecentError inside the window after its scope's last
// moving end (rule 4). ok false and a nil error leave j to be queued.
func (e *Executor[P]) answerLocked(spec *KindSpec[P], j *Job[P]) (s Started, ok bool, err error) {
	var run *job[P]
	for _, q := range e.live {
		if q.state == stateRunning && !q.stopped && q.yield == nil && q.sameScope(j) &&
			(run == nil || q.startedAt.After(run.startedAt)) {
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

// fullLocked reports whether a new waiting job of j's owner is past a bound,
// or the executor has stopped.
func (e *Executor[P]) fullLocked(j *Job[P]) bool {
	return e.closed || e.waiting >= e.maxQueued || (j.Owner != "" && e.owners[j.Owner] >= e.maxPerOwner)
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
		return "", fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
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
// jobs and tells the Observer.
func (e *Executor[P]) ended(ctx context.Context, en *job[P], end End) {
	stored, at, _ := e.end(ctx, en, end)
	e.mu.Lock()
	e.removeLocked(en)
	e.mu.Unlock()
	e.emit(en, &Transition{Phase: en.rec.Phase, Result: stored.Result, At: at})
}
