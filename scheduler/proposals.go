package scheduler

// Propose holds v as the job's proposal, keyed by its Owner, Kind and Scope,
// until 15 minutes after the job ends. It answers ErrProposed when the job
// already proposed. A kind without Proposes panics, and the job ends failed
// with run_panicked.
func (r *Run[P]) Propose(v any) (ProposalID, error) {
	return "", errStub
}

// Proposal answers owner's held proposal id; false for another owner's id, an
// expired one and one never held.
func (e *Executor[P]) Proposal(owner string, id ProposalID) (v any, ok bool) {
	return nil, false
}

// DigestScope answers a scope for a key too long to spell in one: prefix, a
// slash and the lowercase hex SHA-256 of parts, each part led by its length as
// a uvarint so no two part lists collide. A prefix outside the scope grammar
// or over 63 bytes panics.
func DigestScope(prefix string, parts ...[]byte) string {
	return ""
}
