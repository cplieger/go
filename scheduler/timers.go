package scheduler

// Schedule sets the live cadence of one scheduled scope of kind. A changed
// cadence restarts the wait from the change and fires nothing at it, and
// TriggerOnly has Unschedule's effect. Before changing anything it refuses an
// unknown kind (ErrUnknownKind), a scope outside its grammar (ErrScope) and a
// new scope past 64 (ErrTooManyScopes; re-scheduling one never counts).
func (e *Executor[P]) Schedule(kind Kind, scope string, c Cadence) error {
	return errStub
}

// Unschedule removes a scheduled scope's timer and watch, so Deadline answers
// zero for it and an app's deadline series drops it. It does nothing for a
// scope that is not scheduled.
func (e *Executor[P]) Unschedule(kind Kind, scope string) {}
