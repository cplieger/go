package scheduler

import "context"

// Hold quiets the executor for maintenance. With all, it stops the timers,
// cancels each running or yielded job and waits up to 10 seconds for them,
// ends every queued job interrupted and drops every proposal; Start answers
// ErrQueueFull until resume, which restarts the timers from Records.LastEnds.
// Without all it changes nothing and resume does nothing.
func (e *Executor[P]) Hold(ctx context.Context, all bool) (resume func(), err error) {
	return func() {}, errStub
}
