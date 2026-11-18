package scheduler

import "time"

// LiveJob is the in-memory state of a queued, running or yielded job, which
// its record does not hold.
type LiveJob struct {
	Until       time.Time // a yielded job's until; zero otherwise
	Position    int       // jobs queued ahead of it; 0 once it runs
	Done, Total int       // its Run.Progress; 0 and 0 until it reports
}

// Live answers the in-memory state of each queued, running or yielded job;
// a job that is not live is absent.
func (e *Executor[P]) Live() map[ID]LiveJob { return nil }
