package scheduler

// Propose holds v for owner to apply by id until 15 minutes after the job
// ends. key is what makes two proposals the same, so a later request whose
// proposal has a held key answers that proposal's job. It answers
// ErrQueueFull past 16 held proposals for owner or 256 in all.
func (r *Run[P]) Propose(owner, key string, v any) (ProposalID, error) {
	return "", errStub
}

// Proposal answers owner's held proposal id; false for another owner's id, an
// expired one and one never held.
func (e *Executor[P]) Proposal(owner string, id ProposalID) (v any, ok bool) {
	return nil, false
}
