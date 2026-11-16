package httpserver

import (
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"sync"
)

// JSONHeaders sets the standard JSON response headers: an application/json
// content type and the X-Content-Type-Options: nosniff guard against MIME
// sniffing. Call it before WriteHeader.
//
// The nosniff guard is set only when the header is still unset, so a stack
// that composes SecurityHeaders keeps that middleware as the header's single
// writer while a stack without it still gets the guard on every JSON body
// this library writes.
func JSONHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	if h.Get("X-Content-Type-Options") == "" {
		h.Set("X-Content-Type-Options", "nosniff")
	}
}

// WriteJSON writes v as a JSON body with a 200 status.
func WriteJSON(w http.ResponseWriter, v any) {
	WriteJSONStatus(w, http.StatusOK, v)
}

// WriteJSONStatus sets the JSON headers, writes the status code, and encodes v
// as the response body. The status is committed before encoding begins, so an
// encode failure cannot change it; such a failure is logged at Warn rather than
// returned, because the response line is already on the wire.
func WriteJSONStatus(w http.ResponseWriter, status int, v any) {
	JSONHeaders(w)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("httpserver: json encode failed after status committed", "status", status, "error", err)
	}
}

// OK writes a 200 response with the JSON body {"ok":true}. It is the canonical
// success acknowledgement for an endpoint that has no other payload.
func OK(w http.ResponseWriter) {
	WriteJSON(w, struct {
		OK bool `json:"ok"`
	}{OK: true})
}

// ErrorCode is the machine token in the envelope's "code" field, the value a
// client branches on and a log query keys on. It is typed apart from the
// message so a call site cannot transpose the pair.
//
// The grammar is one or more lowercase ASCII letters, digits and underscores
// ("host_not_allowed"). A code outside it, the empty code included, is never
// repaired and never panics: the envelope carries InvalidErrorCode instead and
// the offender is logged once per process.
type ErrorCode string

// InvalidErrorCode is written in place of a malformed code. It is its own code,
// not internal_error, so a broken code never relabels a 400 as a server fault.
const InvalidErrorCode ErrorCode = "invalid_error_code"

// ErrorResponse is the JSON error envelope every writer in this package
// answers. Every key is always present: RequestID is the request's id, or a
// fresh one when its context has none, and Remediation is the code's CodeSpec
// default, or empty for a code outside CodeSpecs.
type ErrorResponse struct {
	Code        ErrorCode `json:"code"`
	Message     string    `json:"message"`
	Remediation string    `json:"remediation"`
	RequestID   string    `json:"request_id"`
}

// CodeSpec is what an error code means to a client: the status it is answered
// with, one line telling a person or a script what to do next, and whether the
// refused request changed nothing, so a resend is safe.
type CodeSpec struct {
	Remediation string
	Status      int
	NoEffect    bool
}

// codeSpecs holds exactly the codes this package's writers answer;
// httpservertest.CheckCodeSpecs holds it to the source.
var codeSpecs = map[ErrorCode]CodeSpec{
	"internal_error":         {Status: http.StatusInternalServerError, Remediation: "Retry the request; if it fails again, report its request id."},
	"method_not_allowed":     {Status: http.StatusMethodNotAllowed, Remediation: "Resend with a method the Allow header lists."},
	"bad_request":            {Status: http.StatusBadRequest, Remediation: "Fix the request as the message describes, then resend it."},
	"loopback_only":          {Status: http.StatusForbidden, Remediation: "Call this endpoint from the server's own host."},
	"too_many_auth_failures": {Status: http.StatusTooManyRequests, Remediation: "Wait for the Retry-After delay, then retry with a valid credential.", NoEffect: true},
	"rate_limited":           {Status: http.StatusTooManyRequests, Remediation: "Wait for the Retry-After delay, then retry.", NoEffect: true},
	"host_not_allowed":       {Status: http.StatusForbidden, Remediation: "Use one of the server's configured host names."},
	"timeout":                {Status: http.StatusServiceUnavailable, Remediation: "Retry the request; it may have run."},
	"not_found":              {Status: http.StatusNotFound, Remediation: "Check the path; nothing is served at it."},
	"ui_unbuilt":             {Status: http.StatusServiceUnavailable, Remediation: "Build the web UI with go generate ./..., then restart the server."},
}

// CodeSpecs returns every code a writer in this package answers, with its
// status and default remediation. The map is the caller's own copy.
func CodeSpecs() map[ErrorCode]CodeSpec {
	return maps.Clone(codeSpecs)
}

// WriteError writes the ErrorResponse for code and msg with the given status.
// It sets Content-Type, and X-Request-ID when the response has none, and keeps
// every other header the caller set. r may be nil.
//
// WriteError ships the mechanism only; each application keeps its own code
// taxonomy on top of it.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code ErrorCode, msg string) {
	resp := errorEnvelope(r, code, msg)
	if w.Header().Get(HeaderRequestID) == "" {
		w.Header().Set(HeaderRequestID, resp.RequestID)
	}
	WriteJSONStatus(w, status, resp)
}

// errorEnvelope builds the ErrorResponse for r. Every library error body, live
// or pre-rendered by errorBodyJSON, goes through it, so the code grammar and
// the request-id rule hold on all of them.
func errorEnvelope(r *http.Request, code ErrorCode, msg string) ErrorResponse {
	resp := ErrorResponse{Code: checkedErrorCode(code), Message: msg}
	resp.Remediation = codeSpecs[resp.Code].Remediation
	if r != nil {
		resp.RequestID = RequestIDFromContext(r.Context())
	}
	if resp.RequestID == "" {
		resp.RequestID = rand.Text()
	}
	return resp
}

// warnOnce logs one Warn per instance and swallows every later call. It exists
// because the malformed-error-code diagnostic sits on a per-request error path:
// the condition is a programming error, so logging it per occurrence would turn
// one bad literal into a log flood on whichever route hits it. A type rather
// than a bare sync.Once so the at-most-once behavior is a unit of its own.
type warnOnce struct {
	once sync.Once
}

// warn logs msg with args the first time it is called on this instance.
func (o *warnOnce) warn(msg string, args ...any) {
	o.once.Do(func() { slog.Warn(msg, args...) })
}

// malformedCodeWarn bounds the malformed-error-code diagnostic to one line per
// process. Every later occurrence stays visible on the wire instead, because the
// response itself carries InvalidErrorCode.
var malformedCodeWarn warnOnce

// checkedErrorCode returns code when it matches ErrorCode's grammar and
// InvalidErrorCode when it does not, logging the offender once per process.
//
// It REFUSES rather than repairs (the same stance CanonicalHost takes): there is
// no rewriting of a sentence into a token, because any such rewrite would put a
// value on the wire that no client contract contains. And it does not PANIC,
// which is the other tempting shape and is wrong here for a reason worth
// stating: this runs per request, on the path a request takes when something has
// already gone wrong, so a panic would convert a handled 400 into either a
// dropped connection or — under Recoverer — a 500 that misreports the client's
// error as a server fault. A validating panic belongs at construction time,
// where it fires once before serving starts and cannot be reached by traffic;
// an error code arrives per call and has no construction step to guard.
func checkedErrorCode(code ErrorCode) ErrorCode {
	if validErrorCode(code) {
		return code
	}
	malformedCodeWarn.warn("httpserver: malformed error code replaced in the error envelope; a code is a machine token [a-z0-9_], and a sentence belongs in the message",
		"code", string(code), "replacement", string(InvalidErrorCode))
	return InvalidErrorCode
}

// validErrorCode reports whether code matches ErrorCode's grammar.
func validErrorCode(code ErrorCode) bool {
	if code == "" {
		return false
	}
	for i := range len(code) {
		switch c := code[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

// errorBodyJSON renders the body WriteError would write for r, for an API
// that takes its body before the request runs (http.TimeoutHandler).
func errorBodyJSON(r *http.Request, code ErrorCode, msg string) string {
	b, _ := json.Marshal(errorEnvelope(r, code, msg)) // a struct of strings always marshals
	return string(b)
}

// ErrorResponder renders an error response body. It has the exact signature of
// WriteError, which is its canonical instance and the library-wide default, so
// the JSON envelope is the zero-config behavior. Middleware that emits an error
// body (Recoverer via WithRecoverResponder, RateLimiter via
// WithRateLimitResponder) accepts one so a non-JSON endpoint - an XML or
// plain-text service - can keep its error body on its own
// content type instead of the default JSON. A responder owns writing the status
// and any headers, and is invoked only when the response has not been committed.
//
// A responder receives the ErrorCode as the library resolved it from its
// caller — unvalidated, because the grammar is enforced by the JSON envelope
// encoder and a responder rendering another content type owns its own body. A
// responder that emits the code into a machine-readable slot should route it
// through this package's WriteError, or check it the same way.
type ErrorResponder func(w http.ResponseWriter, r *http.Request, status int, code ErrorCode, msg string)
