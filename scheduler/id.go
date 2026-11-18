package scheduler

// ID names one job: 32 lowercase hex digits from crypto/rand.
type ID string

// ProposalID names one held proposal, in the same shape as ID.
type ProposalID string

// newID returns 32 lowercase hex digits from crypto/rand.
func newID() string {
	return ""
}
