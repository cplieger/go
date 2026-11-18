package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Start admits j, writing its record (a batch's with one queued line per item)
// before it is queued. A queued job of its kind and scope or one covering it,
// or a running one of its scope that started inside the kind's Fresh window,
// answers it instead (Joined); a running one answers a timer request. It
// answers *RecentError under Fresh after a recent end, ErrQueueFull past a
// queue bound (the record ended skipped, queue_full), ErrStoreUnavailable when
// the record cannot be written, ErrBatchSize for an item count outside 1 to
// MaxItems, ErrUnknownKind and ErrScope.
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
	for {
		s, admitting, ok := e.joinLocked(&en.j)
		if ok {
			e.mu.Unlock()
			return s, nil
		}
		if admitting == nil {
			break
		}
		e.mu.Unlock()
		select {
		case <-admitting:
		case <-ctx.Done():
			return Started{}, ctx.Err()
		}
		e.mu.Lock()
	}
	if s, ok, err := e.answerLocked(spec, &en.j); ok || err != nil {
		e.mu.Unlock()
		return s, err
	}
	if e.fullLocked(&en.j) {
		e.mu.Unlock()
		return Started{}, e.refuse(ctx, en)
	}
	e.live = append(e.live, en)
	e.waitLocked(en)
	e.mu.Unlock()
	return e.admit(ctx, en)
}

// admit writes the record of en, which holds its place in the bounds, and
// queues it.
func (e *Executor[P]) admit(ctx context.Context, en *job[P]) (Started, error) {
	err := e.insert(ctx, en)
	e.mu.Lock()
	if err == nil && e.closed {
		err = errClosed
	}
	if err != nil {
		e.unwaitLocked(en)
		en.state = stateEnded
		e.removeLocked(en)
		close(en.admitted)
		e.mu.Unlock()
		if errors.Is(err, errClosed) {
			e.post(en, &Transition{Phase: PhaseQueued, At: en.rec.QueuedAt})
			e.ended(ctx, en, queueFull)
			return Started{}, ErrQueueFull
		}
		return Started{}, err
	}
	en.state = stateQueued
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

// errClosed is a Start that met the executor's end; it answers as a full
// queue.
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
// written answers its admitted channel to wait on instead.
func (e *Executor[P]) joinLocked(j *Job[P]) (s Started, admitting <-chan struct{}, ok bool) {
	match := func(q *job[P]) (Started, <-chan struct{}, bool) {
		if q.state == stateAdmitting {
			return Started{}, q.admitted, false
		}
		return Started{Job: q.j.ID, Position: q.position(), Joined: true}, nil, true
	}
	for _, q := range e.live {
		if q.waiting() && q.j.Kind == j.Kind && q.j.Scope == j.Scope {
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

// waiting reports whether the job is admitting or queued.
func (en *job[P]) waiting() bool { return en.state == stateAdmitting || en.state == stateQueued }

// answerLocked answers j from a running job of its kind and scope that no
// cancel has stopped (rule 3):
// one that started inside the kind's Fresh window joins it, and so does a
// timer request, which never queues a follow-up behind it. Otherwise a Fresh
// kind refuses j with *RecentError inside the window after its scope's last
// moving end (rule 4). ok false and a nil error leave j to be queued.
func (e *Executor[P]) answerLocked(spec *KindSpec[P], j *Job[P]) (s Started, ok bool, err error) {
	var run *job[P]
	for _, q := range e.live {
		if q.state == stateRunning && !q.stopped && q.j.Kind == j.Kind && q.j.Scope == j.Scope &&
			(run == nil || q.startedAt.After(run.startedAt)) {
			run = q
		}
	}
	now := time.Now()
	if run != nil {
		if j.Trigger == TriggerSchedule || (spec.Fresh != nil && now.Sub(run.startedAt) < spec.Fresh(j.Scope)) {
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

// insert writes en's queued record. ErrCapacity reaches the caller as it is,
// since it names the store's bound and not a fault.
func (e *Executor[P]) insert(ctx context.Context, en *job[P]) error {
	if en.spec.Kept == 0 {
		return nil
	}
	if _, _, err := e.records.Insert(ctx, en.rec, nil); err != nil {
		if errors.Is(err, ErrCapacity) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	return nil
}

// refuse writes the record of a request the queue has no place for and ends
// it skipped, queue_full.
func (e *Executor[P]) refuse(ctx context.Context, en *job[P]) error {
	if err := e.insert(ctx, en); err != nil {
		return err
	}
	e.post(en, &Transition{Phase: PhaseQueued, At: en.rec.QueuedAt})
	e.ended(ctx, en, queueFull)
	return ErrQueueFull
}

// ended writes the end of a job that never ran, takes it out of the live
// jobs and tells the Observer.
func (e *Executor[P]) ended(ctx context.Context, en *job[P], end End) {
	stored, at := e.end(ctx, en, end)
	e.mu.Lock()
	e.removeLocked(en)
	e.mu.Unlock()
	e.emit(en, &Transition{Phase: en.rec.Phase, Result: stored.Result, At: at})
}
