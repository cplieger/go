package scheduler

// Run is a running job's handle, passed to its kind's Run func.
type Run[P any] struct {
	job   Job[P]
	class Class
}

// Job answers the job being run.
func (r *Run[P]) Job() Job[P] { return r.job }

// Class answers the job's class, from its trigger.
func (r *Run[P]) Class() Class { return r.class }

// Reached moves the record's phase and step to p and keeps p's first reached
// time, one record write per change. A phase outside the kind's Phases is a
// programming error: it panics, and the job ends failed with run_panicked.
func (r *Run[P]) Reached(p Phase) {}

// Progress sets the job's done and total counts, held in memory for the live
// view; it writes nothing to the record.
func (r *Run[P]) Progress(done, total int) {}

// Commit begins the commit step, after which Cancel answers ErrCommitting. It
// answers false when a cancel came first, and the job then commits nothing.
func (r *Run[P]) Commit() bool { return false }
