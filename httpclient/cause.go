package httpclient

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
)

// Cause names why a request failed below HTTP. Status answers are
// [CheckStatus]'s; a Cause names only what happened to the connection or
// the body. It encodes as text by its name, so a JSON log field or stored
// state carries "refused", never the number.
type Cause uint8

// The causes [CauseOf] reports.
const (
	CauseUnknown  Cause = iota // nil, or an error no other cause names
	CauseDNS                   // the host name did not resolve
	CauseRefused               // nothing accepted the connection
	CauseReset                 // the peer reset or closed the connection mid-exchange
	CauseTimeout               // a deadline or timeout expired
	CauseTLS                   // the TLS handshake failed: a certificate, an alert, or a plain-HTTP answer
	CauseTooLarge              // a body went over its cap
)

var causeNames = [...]string{"unknown", "dns", "refused", "reset", "timeout", "tls", "too_large"}

// String returns the cause's snake_case name, or Cause(n) for a value outside
// the constants.
func (c Cause) String() string {
	if int(c) < len(causeNames) {
		return causeNames[c]
	}
	return "Cause(" + strconv.Itoa(int(c)) + ")"
}

// MarshalText returns the cause's name, and refuses a value outside the
// constants.
func (c Cause) MarshalText() ([]byte, error) {
	if int(c) >= len(causeNames) {
		return nil, fmt.Errorf("httpclient: no cause %d", uint8(c))
	}
	return []byte(causeNames[c]), nil
}

// UnmarshalText reads a name [Cause.MarshalText] writes, and refuses any other.
func (c *Cause) UnmarshalText(text []byte) error {
	for i, name := range causeNames {
		if string(text) == name {
			*c = Cause(i)
			return nil
		}
	}
	return fmt.Errorf("httpclient: unknown cause %q", text)
}

// CauseOf names the first cause it recognizes in err's chain, trying too large,
// TLS, DNS, refused, reset, then timeout, so a DNS lookup that timed out reads
// CauseDNS. It sees through [Permanent], [MarkTransient] and [AttemptTimeout],
// and answers CauseUnknown for nil, a canceled context and a status error.
func CauseOf(err error) Cause {
	switch {
	case isTooLarge(err):
		return CauseTooLarge
	case isTLSFailure(err):
		return CauseTLS
	case isDNSFailure(err):
		return CauseDNS
	case errors.Is(err, syscall.ECONNREFUSED):
		return CauseRefused
	case isReset(err):
		return CauseReset
	case isTimeout(err):
		return CauseTimeout
	}
	return CauseUnknown
}

func isTooLarge(err error) bool {
	if _, ok := errors.AsType[*ResponseTooLargeError](err); ok {
		return true
	}
	_, ok := errors.AsType[*http.MaxBytesError](err)
	return ok
}

// isTLSFailure reports a handshake the peer or the verifier refused. A peer's
// fatal alert is a *net.OpError with Op "remote error" because crypto/tls keeps
// its alert type unexported (crypto/tls/conn.go, readRecordOrCCS).
func isTLSFailure(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		return true
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}
	if errors.Is(err, http.ErrSchemeMismatch) {
		return true
	}
	op, ok := errors.AsType[*net.OpError](err)
	return ok && op.Op == "remote error"
}

func isDNSFailure(err error) bool {
	_, ok := errors.AsType[*net.DNSError](err)
	return ok
}

// isReset reports a connection the peer is no longer on. A peer RST gives
// ECONNRESET on the first write and EPIPE on every later one (Go ignores
// SIGPIPE), so which errno surfaces is a timing accident and both count.
func isReset(err error) bool {
	if urlErr, ok := errors.AsType[*url.Error](err); ok && urlErr != nil &&
		errors.Is(nameHangUp(urlErr.Err), errServerClosed) {
		return true
	}
	return errors.Is(err, errServerClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// errServerClosed names the bare io.EOF a round trip returns when the server
// closed the connection before its first response byte (net/http
// transport.go, Transport.roundTrip, golang/go#16465), which a chain holding
// io.EOF alone cannot tell from an empty body.
var errServerClosed = fmt.Errorf("server closed the connection before responding: %w", io.EOF)

// nameHangUp returns errServerClosed for err, a round trip's error, when it is
// the bare io.EOF, and err otherwise.
func nameHangUp(err error) error {
	if err == io.EOF { //nolint:errorlint // only the unwrapped value is net/http's hang-up
		return errServerClosed
	}
	return err
}

// isTimeout covers a context deadline too: context.DeadlineExceeded is a
// net.Error whose Timeout reports true.
func isTimeout(err error) bool {
	netErr, ok := errors.AsType[net.Error](err)
	return ok && netErr.Timeout()
}
