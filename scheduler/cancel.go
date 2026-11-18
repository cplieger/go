package scheduler

import "context"

// Cancel ends a queued job cancelled without running, and cancels a running
// or yielded job's context so it ends cancelled at the phase it reached,
// committing nothing. stopping is true for a job that still has to return. It
// answers ErrCommitting once the job's Commit answered true, ErrNotCancellable
// for an ended job and ErrUnknownJob for an unknown id; a repeat answers as
// the first cancel did.
func (e *Executor[P]) Cancel(ctx context.Context, id ID) (stopping bool, err error) {
	e.mu.Lock()
	if en := e.findLocked(id); en != nil && en.state == stateQueued {
		e.dequeueLocked(en)
		e.mu.Unlock()
		e.ended(ctx, en, End{Result: ResultCancelled})
		return false, nil
	}
	e.mu.Unlock()
	return false, errStub
}

// CredentialEnded stops each queued or running job of an OnBehalf kind that
// credential started: it ends cancelled with reason credential_ended.
func (e *Executor[P]) CredentialEnded(credential string) {}
