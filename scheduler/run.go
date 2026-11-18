package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"slices"
	"time"
)

// Run is a running job's handle, passed to its kind's Run func. Its methods
// are safe for concurrent use.
type Run[P any] struct {
	e  *Executor[P]
	en *job[P]
}

// Job answers the job being run.
func (r *Run[P]) Job() Job[P] { return r.en.j }

// Class answers the job's class, from its trigger.
func (r *Run[P]) Class() Class { return r.en.class }

// Reached moves the record's phase and step to p and keeps p's first reached
// time, one record write per change. A phase outside the kind's Phases is a
// programming error: it panics, and the job ends failed with run_panicked.
// Once the job is cancelled it records nothing, so the record keeps the phase
// the cancel came in.
func (r *Run[P]) Reached(p Phase) {
	n := slices.Index(r.en.spec.Phases, p)
	if n < 0 {
		panic(fmt.Sprintf("scheduler: kind %s reached phase %q, which is not in its Phases", r.en.j.Kind, p))
	}
	r.e.mu.Lock()
	stopped := r.en.stopped
	r.e.mu.Unlock()
	if !stopped {
		r.e.reach(r.en, p, n+1)
	}
}

// Progress sets the job's done and total counts, which Live answers and a
// change of which Observer.Moved reports; it writes nothing to the record.
func (r *Run[P]) Progress(done, total int) {
	r.e.mu.Lock()
	defer r.e.mu.Unlock()
	if r.en.done != done || r.en.total != total {
		r.en.done, r.en.total = done, total
		r.e.moved()
	}
}

// Commit begins the commit step at phase committing, after which Cancel
// answers ErrCommitting. It answers false when a cancel came first, and the
// job then commits nothing.
func (r *Run[P]) Commit() bool {
	r.e.mu.Lock()
	if r.en.stopped {
		r.e.mu.Unlock()
		return false
	}
	r.en.committing = true
	r.e.mu.Unlock()
	r.e.reach(r.en, PhaseCommitting, 0)
	return true
}

// reach moves en's record to phase p, which is step n of the kind's Phases,
// or an executor phase with n 0, and writes it once per change.
func (e *Executor[P]) reach(en *job[P], p Phase, n int) {
	en.recMu.Lock()
	// A pipeline may name an executor phase (started) as its own first step.
	if en.rec.Phase == p && (n == 0 || en.rec.Step.N == n) {
		en.recMu.Unlock()
		return
	}
	now := time.Now()
	en.rec.Phase = p
	if n > 0 {
		en.rec.Step.N = n
		if !slices.ContainsFunc(en.rec.Phases, func(a PhaseAt) bool { return a.Phase == p }) {
			en.rec.Phases = append(en.rec.Phases, PhaseAt{Phase: p, At: now})
		}
	}
	e.put(en)
	e.post(en, &Transition{Phase: p, At: now})
	en.recMu.Unlock()
	e.flush(en)
}

// guarded runs en's Run, ending the job failed with run_panicked when it
// panics, so one job's programming error never stops the process.
func (e *Executor[P]) guarded(ctx context.Context, en *job[P]) (end End) {
	defer func() {
		if v := recover(); v != nil {
			e.log.Error("job panicked", "kind", en.j.Kind, "scope", en.j.Scope, "job", en.j.ID,
				"panic", v, "stack", string(debug.Stack()))
			end = End{Result: ResultFailed, Reasons: []Reason{{Code: ReasonRunPanicked}}}
		}
	}()
	return en.spec.Run(ctx, &Run[P]{e: e, en: en})
}

// The bounds of an end and a line.
const (
	maxReasons = 32
	maxFacts   = 2048
)

// Cut is what CloseEnd dropped from an end, zero when nothing was: Reasons
// counts the end's reasons not kept and FactsBytes the length of its facts
// dropped. Its caller logs a non-zero Cut at ERROR with the job's fields.
type Cut struct{ Reasons, FactsBytes int }

// CloseEnd is the one close of an end, its bounds included. Each line still
// queued or running goes to review with reason not_run, or cancelled or
// interrupted for such an end, and an ok end answers partial when any line
// is then skipped or in review. The end keeps its 32 most severe reasons,
// and facts that are not a JSON object of at most 2 KiB are dropped for the
// reason facts_too_large, added once within the 32. It answers copies and
// leaves what it was given as it is.
func CloseEnd(lines []Line, end End) ([]Line, End, Cut) {
	lines, end = closeBatch(lines, end)
	var cut Cut
	if len(end.Facts) > 0 && !validFacts(end.Facts) {
		cut.FactsBytes = len(end.Facts)
		end.Facts = nil
	}
	n := min(len(end.Reasons), maxReasons)
	add := cut.FactsBytes > 0 && !slices.ContainsFunc(end.Reasons[:n], func(r Reason) bool { return r.Code == ReasonFactsTooLarge })
	if add {
		n = min(n, maxReasons-1)
	}
	cut.Reasons = len(end.Reasons) - n
	reasons := slices.Clone(end.Reasons[:n])
	if add {
		reasons = append(reasons, Reason{Code: ReasonFactsTooLarge})
	}
	end.Reasons = reasons
	return lines, end, cut
}

// validFacts reports whether b is a JSON object of at most 2 KiB.
func validFacts(b json.RawMessage) bool {
	t := bytes.TrimLeft(b, " \t\r\n")
	return len(b) <= maxFacts && len(t) > 0 && t[0] == '{' && json.Valid(b)
}
