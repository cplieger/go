package httpclient

import (
	"fmt"
	"net/http"
)

// --- Conditional GET (ETag / Last-Modified revalidation) ---

// Validators carries the cache validators captured from a previous 200
// response, replayed on the next conditional request so an unchanged resource
// is a cheap 304 instead of a re-download. Persist them alongside the cached
// body; the zero value sends no conditional headers (forcing a full 200).
// DoConditional validates both fields in both directions (see its validator
// hygiene contract), so values it captured are always safe to persist and
// replay verbatim.
type Validators struct {
	// ETag is replayed as If-None-Match.
	ETag string
	// LastModified is replayed as If-Modified-Since.
	LastModified string
}

// ConditionalResult is one conditional-request outcome. Fields are ordered
// largest-alignment-first for govet fieldalignment.
type ConditionalResult struct {
	// Validators are the fresh validators captured from a 200 response's ETag /
	// Last-Modified headers (either may be empty when the server sent none).
	// Zero on a 304: the caller keeps the validators it already holds.
	Validators Validators
	// Body is the full response body of a 200, bounded by the maxBodyBytes
	// given to DoConditional. Nil on a 304.
	Body []byte
	// NotModified reports a 304: the cached representation is still current.
	NotModified bool
}

// DoConditional sends req once with If-None-Match/If-Modified-Since set only
// from v (replacing any on req) and classifies the response: 304 is
// NotModified; 200 returns the body (*ResponseTooLargeError over maxBodyBytes,
// where <= 0 means DefaultMaxBodyBytes) and its validators; a non-2xx is
// CheckHTTPStatus's error and any other 2xx a plain error. Transport errors
// pass through LogSafeError. The caller owns retry (wrap in Do, rebuild req per
// attempt) and must send zero Validators when its cached body is unusable. A
// validator failing RFC 9110 field-value grammar or 1 KiB is silently dropped.
func DoConditional(client *http.Client, req *http.Request, v Validators, maxBodyBytes int64) (ConditionalResult, error) {
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	req.Header.Del("If-None-Match")
	req.Header.Del("If-Modified-Since")
	if v.ETag != "" && validValidator(v.ETag) {
		req.Header.Set("If-None-Match", v.ETag)
	}
	if v.LastModified != "" && validValidator(v.LastModified) {
		req.Header.Set("If-Modified-Since", v.LastModified)
	}
	//nolint:bodyclose,gosec // bodyclose: closed on every path below (ReadLimitedBody on 200, DrainClose otherwise); G704: the request is caller-built, so URL/SSRF policy is the caller's, as at every httpclient entry point
	resp, err := client.Do(req)
	if err != nil {
		return ConditionalResult{}, LogSafeError(err)
	}
	switch resp.StatusCode {
	case http.StatusNotModified:
		DrainClose(resp.Body)
		return ConditionalResult{NotModified: true}, nil
	case http.StatusOK:
		body, err := ReadLimitedBody(resp.Body, maxBodyBytes)
		if err != nil {
			return ConditionalResult{}, err
		}
		return ConditionalResult{
			Body: body,
			Validators: Validators{
				ETag:         captureValidator(resp.Header.Get("ETag")),
				LastModified: captureValidator(resp.Header.Get("Last-Modified")),
			},
		}, nil
	default:
		DrainClose(resp.Body)
		if statusErr := CheckHTTPStatus(resp); statusErr != nil {
			return ConditionalResult{}, statusErr
		}
		// CheckHTTPStatus returns nil only for 2xx, so this covers a 2xx that is
		// neither the 200 nor the 304 above (a 204, 206, 201): no usable
		// representation for a conditional GET, so a plain non-transient error.
		return ConditionalResult{}, fmt.Errorf("unexpected status %d on conditional request", resp.StatusCode)
	}
}

// maxValidatorBytes bounds one validator value accepted by DoConditional in
// either direction (capture or replay). Real ETags and HTTP-dates are tens of
// bytes; anything larger is a corrupt or hostile value that would bloat every
// caller's persisted cache state on capture.
const maxValidatorBytes = 1 << 10

// validValidator reports whether v is safe to persist and replay as an HTTP
// validator header value: within maxValidatorBytes and free of bytes illegal
// in a header field value (RFC 9110 field-value grammar: control characters
// other than HTAB, or DEL). A value with a CR/LF would otherwise be rejected
// by net/http at request-write time on every replay, and one with other
// control bytes or an absurd length is corrupt or hostile either way.
func validValidator(v string) bool {
	if len(v) > maxValidatorBytes {
		return false
	}
	for i := range len(v) {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// captureValidator returns v, or empty when it fails validValidator, so an
// invalid upstream validator is captured as absent (the next request is an
// unconditional GET) rather than handed to the caller to persist.
func captureValidator(v string) string {
	if !validValidator(v) {
		return ""
	}
	return v
}
