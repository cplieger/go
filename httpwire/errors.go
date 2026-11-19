package httpwire

import (
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

// Owner is the error-code table of a package whose sentinels a handler may
// return: each sentinel answers its code, or is named in Outside with the
// reason it never reaches the wire.
type Owner struct {
	// Sentinels holds every exported Err* variable the owner package
	// declares, by name; httpwiretest.Sentinels holds it to the source.
	Sentinels map[string]error
	// Outside names a sentinel that is handled inside the package, with why.
	Outside map[string]string
	Name    string
	// Codes is matched in order, with errors.Is: the first match answers.
	Codes []OwnerCode
}

// OwnerCode maps one sentinel to the code it answers.
type OwnerCode struct {
	Err  error
	Code httpserver.ErrorCode
}

// Error is a handler's own refusal. Its Code must be in the row's error
// union; the catalog supplies the status and the remediation.
type Error struct {
	// Upstream is what another service said; the writer cleans and caps it.
	Upstream *Upstream
	Code     httpserver.ErrorCode
	Message  string
	// Reason is optional machine detail: the code grammar, at most 64 bytes.
	Reason string
	// remediation replaces the catalog's line where the binder completes
	// it for one row.
	remediation string
	// Fields names the refused parts of the request; at most 100 are written.
	Fields     []FieldError
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// Upstream is another service's answer when the app's call to it failed. It
// is untrusted text: no code or remediation depends on it.
//
//nolint:govet // fieldalignment: the declaration order is the wire's member order.
type Upstream struct {
	// Service names the other service in the code grammar ("sonarr").
	Service string `json:"service"`
	// Status is the service's HTTP status; 0, or a value outside 100 to 599,
	// is not written.
	Status int `json:"status,omitempty"`
	// Message is the service's own text, written as one line of at most 512
	// bytes.
	Message string `json:"message"`
}

// FieldError names one refused part of a request: Path is the JSON path
// ("items[1].key") or the query key, Code the field code.
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
