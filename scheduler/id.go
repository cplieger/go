package scheduler

import (
	"crypto/rand"
	"encoding/hex"
)

// ID names one job: 32 lowercase hex digits from crypto/rand.
type ID string

// ProposalID names one held proposal, in the same shape as ID.
type ProposalID string

// newID returns 32 lowercase hex digits from crypto/rand.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails: https://pkg.go.dev/crypto/rand#Read
	return hex.EncodeToString(b[:])
}
