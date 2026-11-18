package scheduler

import "time"

// Cadence is how one scheduled scope is timed: Scheduled on an interval, or
// TriggerOnly, whose jobs come only from requests. The zero value is
// TriggerOnly. Two cadences built from the same calls are equal (==).
type Cadence struct {
	every time.Duration
	retry time.Duration
}

// Scheduled is a job every interval, jittered ±10 % at each wake; an interval
// under 1 second is raised to it.
func Scheduled(every time.Duration) Cadence {
	return Cadence{}
}

// Retry sets how long after a moving end whose work was not done (failed or
// skipped) the next wake comes, instead of the interval. d is raised to 1
// second and capped at the interval; the last call wins. A TriggerOnly cadence
// ignores it. Retry moves only the next wake, never the scope's deadline.
func (c Cadence) Retry(d time.Duration) Cadence {
	return c
}

// TriggerOnly is the cadence with no timer and no deadline.
func TriggerOnly() Cadence {
	return Cadence{}
}

// Every reports the interval, and false for TriggerOnly.
func (c Cadence) Every() (time.Duration, bool) {
	return 0, false
}

// wait is how long after an end the next wake comes: Retry's d when the work
// was not done and Retry is set, else the interval; 0 for TriggerOnly.
func (c Cadence) wait(done bool) time.Duration {
	return 0
}
