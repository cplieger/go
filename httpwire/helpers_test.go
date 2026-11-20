package httpwire_test

import (
	"slices"

	"github.com/cplieger/go/httpwire"
)

func envelopeEqual(a, b httpwire.Envelope) bool {
	if (a.Upstream == nil) != (b.Upstream == nil) || a.Upstream != nil && *a.Upstream != *b.Upstream {
		return false
	}
	return a.Code == b.Code && a.Message == b.Message && a.Remediation == b.Remediation &&
		a.Reason == b.Reason && a.RequestID == b.RequestID && a.RetryAfterMS == b.RetryAfterMS &&
		slices.Equal(a.Fields, b.Fields)
}
