package httpwire

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/textsafe"
)

const (
	codeInternal    = "internal_error"
	codeNotFound    = "not_found"
	codeBadBody     = "bad_body"
	internalMessage = "internal error"
)

// libraryCodes are the codes httpwire's own layers answer, each with its
// default remediation.
var libraryCodes = map[string]httpserver.CodeSpec{
	codeBadBody:                    {Status: http.StatusBadRequest, Remediation: "Fix the request body as the fields describe, then resend it."},
	"bad_query":                    {Status: http.StatusBadRequest, Remediation: "Fix the query parameters as the fields describe, then resend the request."},
	"body_too_large":               {Status: http.StatusRequestEntityTooLarge, Remediation: "Send a smaller request body."},
	"cursor_invalid":               {Status: http.StatusBadRequest, Remediation: "Read the list again from its first page, without a cursor."},
	"if_match_required":            {Status: http.StatusPreconditionRequired, Remediation: "Read the row, then resend with If-Match set to the tag the read returned."},
	"if_match_invalid":             {Status: http.StatusBadRequest, Remediation: "Send If-Match as the quoted tag a read of the row returned."},
	"stale_version":                {Status: http.StatusPreconditionFailed, Remediation: "Read the row again, reapply the change, and resend with the new tag."},
	"wire_mismatch":                {Status: http.StatusConflict, Remediation: "Reload the client: the server's API changed since it loaded."},
	"idempotency_key_invalid":      {Status: http.StatusBadRequest, Remediation: `Send the key as a quoted string: Idempotency-Key: "<key>".`},
	"idempotency_key_required":     {Status: http.StatusBadRequest, Remediation: `Resend with an Idempotency-Key header: Idempotency-Key: "<key>".`},
	"idempotency_key_unsupported":  {Status: http.StatusBadRequest, Remediation: "Resend without the Idempotency-Key header; this endpoint does not take one."},
	"idempotency_key_reused":       {Status: http.StatusUnprocessableEntity, Remediation: "Use a new Idempotency-Key for a different request."},
	"idempotency_in_flight":        {Status: http.StatusConflict, Remediation: "Wait, then resend with the same Idempotency-Key to get the first request's answer.", NoEffect: true},
	"idempotency_store_full":       {Status: http.StatusServiceUnavailable, Remediation: "Wait for earlier keyed requests to expire, then retry.", NoEffect: true},
	"idempotency_answer_too_large": {Status: http.StatusConflict, Remediation: "The request completed; its answer is too large to replay."},
	"upgrade_required":             {Status: http.StatusBadRequest, Remediation: "Install the client release the message names.", NoEffect: true},
	"held_limit":                   {Status: http.StatusTooManyRequests, Remediation: "Wait for an open request or stream to end, then retry.", NoEffect: true},
	"person_required":              {Status: http.StatusForbidden, Remediation: "Do this from a signed-in session; an API key cannot."},
	"store_unavailable":            {Status: http.StatusServiceUnavailable, Remediation: "Wait for the server's data store to open, then retry.", NoEffect: true},
	"accounts_unavailable":         {Status: http.StatusServiceUnavailable, Remediation: "Wait for the server's account store to open, then retry.", NoEffect: true},
	"maintenance":                  {Status: http.StatusServiceUnavailable, Remediation: "Wait for the Retry-After delay, then retry.", NoEffect: true},
}

// Catalog is the one code table of an app's listeners: every code any layer
// answers, with its status and remediation. It is safe for concurrent use.
type Catalog struct {
	specs map[string]httpserver.CodeSpec // each code as it was added
	words map[string]string              // the app's remediation, replacing the added one
	mu    sync.RWMutex
}

// NewCatalog returns a catalog holding httpwire's own codes and every code of
// httpserver.CodeSpecs.
func NewCatalog() *Catalog {
	c := &Catalog{specs: maps.Clone(libraryCodes), words: map[string]string{}}
	for code, s := range httpserver.CodeSpecs() {
		c.specs[string(code)] = s
	}
	return c
}

// Add adds an app's own code. It refuses a code already in the catalog, a
// code outside the grammar, a status outside 400 to 599, and a remediation
// that is empty, over 4 KiB or more than one line of valid UTF-8.
func (c *Catalog) Add(code string, s httpserver.CodeSpec) error {
	if err := checkSpec(code, s); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.specs[code]; ok {
		return fmt.Errorf("httpwire: code %q is already in the catalog", code)
	}
	c.specs[code] = s
	return nil
}

// AddOwned adds the codes another module answers itself, under Add's rules.
// A code already present with the identical spec is skipped, so a module may
// add its codes twice; one present with another spec is refused. A refusal
// adds none of specs.
func (c *Catalog) AddOwned(specs map[httpserver.ErrorCode]httpserver.CodeSpec) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	add := map[string]httpserver.CodeSpec{}
	for _, code := range slices.Sorted(maps.Keys(specs)) {
		s := specs[code]
		if err := checkSpec(string(code), s); err != nil {
			errs = append(errs, err)
			continue
		}
		if have, ok := c.specs[string(code)]; ok {
			if have != s {
				errs = append(errs, fmt.Errorf("httpwire: code %q is in the catalog as %+v, not %+v", code, have, s))
			}
			continue
		}
		add[string(code)] = s
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	maps.Copy(c.specs, add)
	return nil
}

// SetRemediation replaces the remediation of a code already in the catalog,
// keeping its status: the app's words for a library's code. It refuses an
// unknown code and a remediation Add would refuse.
func (c *Catalog) SetRemediation(code, remediation string) error {
	if err := checkLine(remediation); err != nil {
		return fmt.Errorf("httpwire: code %q: %w", code, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.specs[code]; !ok {
		return fmt.Errorf("httpwire: code %q is not in the catalog", code)
	}
	c.words[code] = remediation
	return nil
}

// Lookup returns code's status and remediation.
func (c *Catalog) Lookup(code string) (httpserver.CodeSpec, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.specs[code]
	if w, set := c.words[code]; set {
		s.Remediation = w
	}
	return s, ok
}

func checkSpec(code string, s httpserver.CodeSpec) error {
	if !isCode(code) || len(code) > maxEnvelopeString {
		return fmt.Errorf("httpwire: %q is not a code: one or more of a-z, 0-9 and _", textsafe.SanitizeSingleLineBounded(code, maxReason))
	}
	if code == string(httpserver.InvalidErrorCode) {
		return fmt.Errorf("httpwire: %q is httpserver's substitute for a malformed code", code)
	}
	if s.Status < 400 || s.Status > 599 {
		return fmt.Errorf("httpwire: code %q: status %d is not an error status", code, s.Status)
	}
	if err := checkLine(s.Remediation); err != nil {
		return fmt.Errorf("httpwire: code %q: %w", code, err)
	}
	return nil
}

func checkLine(s string) error {
	switch {
	case s == "":
		return errors.New("empty remediation")
	case len(s) > maxEnvelopeString:
		return fmt.Errorf("remediation of %d bytes, over %d", len(s), maxEnvelopeString)
	case !utf8.ValidString(s):
		return errors.New("remediation is not valid UTF-8")
	case strings.ContainsFunc(s, textsafe.IsUnsafeSingleLine):
		return errors.New("remediation holds a line break or a control character")
	}
	return nil
}

// WriteError answers e as the error envelope, at its code's catalog status
// and with its remediation. It keeps every header the caller set, setting
// only Content-Type, X-Request-Id and, when e carries one, Retry-After. The
// body always decodes with DecodeEnvelope: text is capped to the envelope's
// bounds, and internal_error carries the message "internal error" and
// nothing else. A code missing from the catalog, a reason outside the
// grammar and an upstream service outside it are wiring bugs: each is logged
// at ERROR and answered as internal_error, or left out. r may be nil.
func (c *Catalog) WriteError(w http.ResponseWriter, r *http.Request, e *Error) {
	h := w.Header()
	env, status := c.envelope(r, e)
	env.RequestID = requestID(r, h)
	if h.Get(httpserver.HeaderRequestID) != env.RequestID {
		h.Set(httpserver.HeaderRequestID, env.RequestID)
	}
	if env.RetryAfterMS > 0 {
		h.Set("Retry-After", strconv.FormatInt((env.RetryAfterMS+999)/1000, 10))
	}
	httpserver.WriteJSONStatus(w, status, env)
}

func (c *Catalog) envelope(r *http.Request, e *Error) (env Envelope, status int) {
	if e == nil {
		logError(r, "httpwire: a nil *Error was answered")
		e = &Error{Code: codeInternal}
	}
	spec, ok := c.Lookup(e.Code)
	if !ok {
		logError(r, "httpwire: error code with no catalog row", "code", textsafe.SanitizeSingleLineBounded(e.Code, maxReason))
		spec, _ = c.Lookup(codeInternal)
		e = &Error{Code: codeInternal}
	}
	env = Envelope{Code: e.Code, Remediation: spec.Remediation}
	if e.Code == codeInternal {
		env.Message = internalMessage
		return env, spec.Status
	}
	env.Message = boundText(e.Message)
	env.Reason = reasonOf(r, e)
	env.Fields = boundFields(e.Fields)
	if e.RetryAfter > 0 {
		env.RetryAfterMS = ceilDiv(e.RetryAfter, time.Millisecond)
	}
	env.Upstream = upstreamOf(r, e)
	return env, spec.Status
}

func reasonOf(r *http.Request, e *Error) string {
	if e.Reason == "" || isCode(e.Reason) && len(e.Reason) <= maxReason {
		return e.Reason
	}
	logError(r, "httpwire: error reason outside the code grammar or over 64 bytes, left out", "code", e.Code, "reason", textsafe.SanitizeSingleLineBounded(e.Reason, maxReason))
	return ""
}

func boundFields(fields []FieldError) []FieldError {
	if len(fields) == 0 {
		return nil
	}
	out := make([]FieldError, min(len(fields), maxFields))
	for i := range out {
		f := fields[i]
		out[i] = FieldError{Path: boundText(f.Path), Code: boundText(f.Code), Message: boundText(f.Message)}
	}
	return out
}

func upstreamOf(r *http.Request, e *Error) *Upstream {
	u := e.Upstream
	if u == nil {
		return nil
	}
	if !isCode(u.Service) || len(u.Service) > maxEnvelopeString {
		logError(r, "httpwire: upstream service outside the code grammar, left out", "code", e.Code, "service", textsafe.SanitizeSingleLineBounded(u.Service, maxReason))
		return nil
	}
	out := &Upstream{Service: u.Service}
	if u.Status >= 100 && u.Status <= 599 {
		out.Status = u.Status
	}
	out.Message, _ = textsafe.SanitizeSingleLineCapped(u.Message, maxUpstreamMessage, "…")
	return out
}

// Responder adapts WriteError to httpserver's ErrorResponder, for the
// listener's own writers (host policy, SPA, not found): the catalog's status
// replaces the one the caller passes, and a code missing from the catalog
// answers internal_error.
func (c *Catalog) Responder() httpserver.ErrorResponder {
	return func(w http.ResponseWriter, r *http.Request, _ int, code httpserver.ErrorCode, msg string) {
		c.WriteError(w, r, &Error{Code: string(code), Message: msg})
	}
}

// requestID is the id the request carries, else one already set on the
// response, else a fresh one.
func requestID(r *http.Request, h http.Header) string {
	if r != nil {
		if id := httpserver.RequestIDFromContext(r.Context()); httpserver.ValidRequestID(id) {
			return id
		}
	}
	if id := h.Get(httpserver.HeaderRequestID); httpserver.ValidRequestID(id) {
		return id
	}
	return rand.Text()
}

// boundText makes s valid UTF-8 of at most the envelope's string bound. The
// raw cap comes first so a huge s costs nothing, and the second one covers
// an invalid byte growing into the three-byte U+FFFD.
func boundText(s string) string {
	s = textsafe.CapBytes(s, maxEnvelopeString)
	return textsafe.CapBytes(strings.ToValidUTF8(s, "\uFFFD"), maxEnvelopeString)
}

// ceilDiv is d in whole units, rounded up, without overflowing near the
// largest Duration.
func ceilDiv(d, unit time.Duration) int64 {
	n := d / unit
	if d%unit != 0 {
		n++
	}
	return int64(n)
}

// logError writes a wiring bug's line to slog's default logger as it is when
// the line is written.
func logError(r *http.Request, msg string, args ...any) {
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	slog.Default().ErrorContext(ctx, msg, args...)
}
