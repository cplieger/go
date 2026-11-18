package scheduler

import (
	"context"
	"fmt"
	"slices"
)

// Item runs a batch's item i: it takes the kind's slot in Batch class, runs
// fn and records the Line fn answers, which must be done, skipped or review.
// It answers ctx's error once the job is cancelled, ErrUnknownLine for an i
// outside the batch, and nil, running nothing, for a line already taken. A
// panic in fn or a line that is not an end ends the batch failed, the line in
// review, both with run_panicked. A line still queued at the job's end goes
// to review: cancelled or interrupted with such an end, else not_run, and an
// ok end then reads partial.
func (r *Run[P]) Item(ctx context.Context, i int, fn func(ctx context.Context) Line) error {
	en, e := r.en, r.e
	if claimed, err := en.claim(i); !claimed {
		return err
	}
	release, err := e.takeItem(ctx, en)
	if err != nil {
		en.unclaim(i)
		return err
	}
	defer release()
	ended := false
	defer func() {
		if !ended {
			e.setLine(en, i, Line{State: LineReview, Reason: ReasonRunPanicked})
		}
	}()
	e.setLine(en, i, Line{State: LineRunning})
	l := fn(ctx)
	if l.State != LineDone && l.State != LineSkipped && l.State != LineReview {
		panic(fmt.Sprintf("scheduler: kind %s item %d answered line state %q, which is not an end", en.j.Kind, i, l.State))
	}
	if len(l.Facts) > 0 && !validFacts(l.Facts) {
		e.log.Error("job line facts dropped", "kind", en.j.Kind, "scope", en.j.Scope, "job", en.j.ID,
			"line", i, "bytes", len(l.Facts), "reason", ReasonFactsTooLarge)
		l.Facts = nil
		en.recMu.Lock()
		en.factsDropped = true
		en.recMu.Unlock()
	}
	e.setLine(en, i, l)
	ended = true
	return nil
}

// claim reserves line i for one Item call: false with ErrUnknownLine for an
// i outside the batch, and false with nil for a line another call holds or
// one no longer queued.
func (en *job[P]) claim(i int) (bool, error) {
	en.recMu.Lock()
	defer en.recMu.Unlock()
	if i < 0 || i >= len(en.rec.Lines) {
		return false, ErrUnknownLine
	}
	if en.claimed == nil {
		en.claimed = make([]bool, len(en.rec.Lines))
	}
	if en.claimed[i] || en.rec.Lines[i].State != LineQueued {
		return false, nil
	}
	en.claimed[i] = true
	return true, nil
}

func (en *job[P]) unclaim(i int) {
	en.recMu.Lock()
	defer en.recMu.Unlock()
	en.claimed[i] = false
}

// takeItem takes en's slot in Batch class for one item and answers its
// release. It answers context.Canceled once the job is stopped, and ctx's
// error when ctx ends first.
func (e *Executor[P]) takeItem(ctx context.Context, en *job[P]) (func(), error) {
	if err := e.stoppedErr(ctx, en); err != nil {
		return nil, err
	}
	if en.slot == nil {
		return func() {}, nil
	}
	w := en.slot.join(tierBatch)
	release, err := en.slot.wait(ctx, w)
	if err != nil {
		return nil, err
	}
	if e.observer.SlotWait != nil {
		e.observer.SlotWait(en.spec.Slot, Batch, en.slot.waited(w))
	}
	if err := e.stoppedErr(ctx, en); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// stoppedErr answers ctx's error, or context.Canceled once en is stopped.
func (e *Executor[P]) stoppedErr(ctx context.Context, en *job[P]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if en.stopped {
		return context.Canceled
	}
	return nil
}

// setLine writes line i of en's record.
func (e *Executor[P]) setLine(en *job[P], i int, l Line) {
	en.recMu.Lock()
	defer en.recMu.Unlock()
	en.rec.Lines[i] = l
	e.put(en)
}

// closeLines puts each line still queued in review for end, as Item's godoc
// states, writes the record when a line moved and answers the batch's end.
func (e *Executor[P]) closeLines(en *job[P], end End) End {
	en.recMu.Lock()
	defer en.recMu.Unlock()
	if len(en.rec.Lines) == 0 {
		return end
	}
	reason := ReasonNotRun
	switch end.Result {
	case ResultCancelled:
		reason = ReasonCancelled
	case ResultInterrupted:
		reason = ReasonInterrupted
	default:
	}
	moved := false
	for i, l := range en.rec.Lines {
		if l.State == LineQueued || l.State == LineRunning {
			en.rec.Lines[i] = Line{State: LineReview, Reason: reason}
			moved = true
		}
	}
	if moved {
		e.put(en)
	}
	if end.Result == ResultOK && slices.ContainsFunc(en.rec.Lines, func(l Line) bool {
		return l.State == LineSkipped || l.State == LineReview
	}) {
		end.Result = ResultPartial
	}
	return end
}
