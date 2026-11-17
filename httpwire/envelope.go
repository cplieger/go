package httpwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// The envelope's bounds, shared by the writer and both decoders: string
// lengths are UTF-8 bytes.
const (
	maxEnvelopeString  = 4096
	maxReason          = 64
	maxFields          = 100
	maxUpstreamMessage = 512
	maxSafeInteger     = 1<<53 - 1 // the largest integer a JS number holds exactly
)

// Envelope is the decoded error body every listener of an app answers.
//
//nolint:govet // fieldalignment: the declaration order is the wire's member order.
type Envelope struct {
	Code        string       `json:"code"`
	Message     string       `json:"message"`
	Remediation string       `json:"remediation"`
	Reason      string       `json:"reason,omitempty"`
	Fields      []FieldError `json:"fields,omitempty"`
	// RetryAfterMS is the wait before a retry, in milliseconds; 0 is none.
	RetryAfterMS int64     `json:"retry_after_ms,omitempty"`
	Upstream     *Upstream `json:"upstream,omitempty"`
	RequestID    string    `json:"request_id"`
}

// DecodeEnvelope decodes an error body by the rules the TypeScript
// decodeEnvelope holds too: code, message, remediation and request_id
// present and strings; code, reason and upstream.service in the code grammar;
// reason at most 64 bytes, fields at most 100 entries, upstream.message at
// most 512 bytes and every other string at most 4 KiB; retry_after_ms an
// integer from 0 to 2^53 - 1 and upstream.status one from 100 to 599. A
// member present as null breaks its rule, and an unknown member is ignored.
func DecodeEnvelope(body []byte) (Envelope, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return Envelope{}, fmt.Errorf("httpwire: error envelope: %w", err)
	}
	if top == nil {
		return Envelope{}, errors.New("httpwire: error envelope: not an object")
	}
	var env Envelope
	var errs []error
	note := func(member string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("httpwire: error envelope %s: %w", member, err))
		}
	}
	var err error
	env.Code, err = codeMember(top, "code", maxEnvelopeString)
	note("code", err)
	env.Message, err = stringMember(top, "message", maxEnvelopeString)
	note("message", err)
	env.Remediation, err = stringMember(top, "remediation", maxEnvelopeString)
	note("remediation", err)
	env.RequestID, err = stringMember(top, "request_id", maxEnvelopeString)
	note("request_id", err)
	if _, ok := top["reason"]; ok {
		env.Reason, err = codeMember(top, "reason", maxReason)
		note("reason", err)
	}
	if raw, ok := top["fields"]; ok {
		env.Fields, err = decodeFields(raw)
		note("fields", err)
	}
	if raw, ok := top["retry_after_ms"]; ok {
		env.RetryAfterMS, err = decodeInteger(raw, 0, maxSafeInteger)
		note("retry_after_ms", err)
	}
	if raw, ok := top["upstream"]; ok {
		env.Upstream, err = decodeUpstream(raw)
		note("upstream", err)
	}
	if len(errs) > 0 {
		return Envelope{}, errors.Join(errs...)
	}
	return env, nil
}

func decodeFields(raw json.RawMessage) ([]FieldError, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil, errors.New("not an array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	if len(items) > maxFields {
		return nil, fmt.Errorf("%d entries, over %d", len(items), maxFields)
	}
	out := make([]FieldError, len(items))
	for i, item := range items {
		obj, err := decodeObject(item)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		var errs []error
		for _, m := range []struct {
			dst  *string
			name string
		}{{&out[i].Path, "path"}, {&out[i].Code, "code"}, {&out[i].Message, "message"}} {
			if *m.dst, err = stringMember(obj, m.name, maxEnvelopeString); err != nil {
				errs = append(errs, fmt.Errorf("[%d].%s: %w", i, m.name, err))
			}
		}
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
	}
	return out, nil
}

func decodeUpstream(raw json.RawMessage) (*Upstream, error) {
	obj, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	var u Upstream
	var errs []error
	if u.Service, err = codeMember(obj, "service", maxEnvelopeString); err != nil {
		errs = append(errs, fmt.Errorf("service: %w", err))
	}
	if u.Message, err = stringMember(obj, "message", maxUpstreamMessage); err != nil {
		errs = append(errs, fmt.Errorf("message: %w", err))
	}
	if s, ok := obj["status"]; ok {
		status, err := decodeInteger(s, 100, 599)
		if err != nil {
			errs = append(errs, fmt.Errorf("status: %w", err))
		}
		u.Status = int(status)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &u, nil
}

func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, errors.New("not an object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// stringMember reads a required string member of at most limit bytes.
func stringMember(obj map[string]json.RawMessage, name string, limit int) (string, error) {
	raw, ok := obj[name]
	if !ok {
		return "", errors.New("missing")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", errors.New("not a string")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	if len(s) > limit {
		return "", fmt.Errorf("%d bytes, over %d", len(s), limit)
	}
	return s, nil
}

func codeMember(obj map[string]json.RawMessage, name string, limit int) (string, error) {
	s, err := stringMember(obj, name, limit)
	if err != nil {
		return "", err
	}
	if !isCode(s) {
		return "", fmt.Errorf("%q is not a code", s)
	}
	return s, nil
}

// decodeInteger reads a JSON number as a JS reader does, as a float64, and
// requires a whole value from lo to hi, so 1.5e3 is the integer 1500 in both
// decoders.
func decodeInteger(raw json.RawMessage, lo, hi float64) (int64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return 0, errors.New("not a number")
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, err
	}
	if f != math.Trunc(f) || f < lo || f > hi {
		return 0, fmt.Errorf("%s is not an integer from %.0f to %.0f", raw, lo, hi)
	}
	return int64(f), nil
}

// isCode reports whether s is in the code grammar: one or more lowercase
// ASCII letters, digits and underscores.
func isCode(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}
