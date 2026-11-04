package xmlx

import (
	"errors"
	"strconv"
)

// Sentinel errors, matched with errors.Is through the wrapped errors this
// package returns.
var (
	// ErrLimit reports that a document exceeded one of the caller's bounds.
	// Every rejection from Preflight and Budget wraps it, so a consumer can
	// classify "the document was too big to be worth decoding" with one
	// errors.Is and read *LimitError only when it wants the specific bound.
	ErrLimit = errors.New("xmlx: limit exceeded")
	// ErrInvalidLimits reports a non-positive bound: a configuration mistake in
	// the CALLER, not a property of the document. It is deliberately not
	// treated as an unbounded setting, see the package doc.
	ErrInvalidLimits = errors.New("xmlx: invalid limits")
)

// Kind names which bound a *LimitError reports. It exists so a consumer can
// branch or log on the specific bound without parsing an error string: a
// depth rejection and an oversized-field rejection say different things about
// the upstream that sent the document.
type Kind uint8

// The bounds this package enforces. KindUnknown is the zero value and names no
// bound; it never appears in a returned error.
const (
	KindUnknown Kind = iota
	// KindTextRun: one contiguous run of raw character data exceeded
	// Limits.MaxTextRunBytes.
	KindTextRun
	// KindCDATA: one CDATA section's content exceeded Limits.MaxTextRunBytes.
	// It shares the text bound because a CDATA section IS character data, just
	// spelled so that markup bytes inside it are literal.
	KindCDATA
	// KindComment: one comment exceeded Limits.MaxTokenBytes.
	KindComment
	// KindProcInst: one processing instruction exceeded Limits.MaxTokenBytes.
	KindProcInst
	// KindToken: one tag exceeded Limits.MaxTokenBytes.
	KindToken
	// KindTagAttrs: one start tag carried more than Limits.MaxTagAttrs XML
	// attributes.
	KindTagAttrs
	// KindDepth: element nesting exceeded Limits.MaxDepth.
	KindDepth
	// KindElements: the document carried more elements than
	// Limits.MaxElements.
	KindElements
	// KindDirective: the document carried an XML directive (a DOCTYPE, ENTITY,
	// ATTLIST or NOTATION declaration), or a truncated `<!` opener that could
	// only become one. Preflight rejects the whole class; see the package doc
	// for why. Its LimitError carries no numeric bound.
	KindDirective
	// KindField: one decoded value exceeded Budget.MaxFieldBytes.
	KindField
	// KindTotalText: the cumulative decoded text charged to one Budget
	// exceeded Budget.MaxTotalBytes.
	KindTotalText
)

// what returns the noun this bound is about, for the error message.
func (k Kind) what() string {
	switch k {
	case KindTextRun:
		return "text run"
	case KindCDATA:
		return "CDATA section"
	case KindComment:
		return "comment"
	case KindProcInst:
		return "processing instruction"
	case KindToken:
		return "markup token"
	case KindTagAttrs:
		return "attributes on one start tag"
	case KindDepth:
		return "element nesting depth"
	case KindElements:
		return "element count"
	case KindDirective:
		return "XML directive"
	case KindField:
		return "decoded field"
	case KindTotalText:
		return "cumulative decoded text"
	default:
		return "unknown bound"
	}
}

// String implements fmt.Stringer with the same noun the error message uses.
func (k Kind) String() string { return k.what() }

// LimitError reports which bound a document exceeded. Match it with
// errors.AsType; match the class with errors.Is against ErrLimit.
//
// It carries no excerpt of the document. That is deliberate: the offending bytes
// are untrusted input, and an error built from them would be an unbounded,
// unsanitized string on its way to a log line, which is the amplification this
// package exists to stop reintroduced through its own diagnostics. Offset is the
// safe half of that diagnostic need: one bounded integer, no attacker-chosen
// content, and enough to find the offending token in a saved payload.
type LimitError struct {
	// Kind names the bound that fired.
	Kind Kind
	// Limit is the configured bound, or 0 for a bound that is not numeric
	// (KindDirective).
	Limit int
	// Offset is the byte offset in the document where the rejection was
	// detected. Preflight sets it; Budget leaves it 0, because a decoded value
	// has no single offset and the caller's decoder knows which field it was
	// reading (wrap the error with that name).
	Offset int
}

// Error implements error.
func (e *LimitError) Error() string {
	return "xmlx: " + e.detail() + e.where()
}

// detail renders the bound that fired.
func (e *LimitError) detail() string {
	switch e.Kind {
	case KindDirective:
		return "XML directives are not allowed"
	case KindDepth, KindTagAttrs, KindElements:
		return e.Kind.what() + " over " + strconv.Itoa(e.Limit)
	default:
		return e.Kind.what() + " longer than " + strconv.Itoa(e.Limit) + " bytes"
	}
}

// where renders the offset suffix, omitted when there is none.
func (e *LimitError) where() string {
	if e.Offset <= 0 {
		return ""
	}
	return " at byte " + strconv.Itoa(e.Offset)
}

// Is reports ErrLimit for every LimitError, so a consumer that only cares about
// the class does not have to enumerate kinds.
func (e *LimitError) Is(target error) bool { return target == ErrLimit }

// limitErr builds the rejection for one bound, without an offset.
func limitErr(kind Kind, limit int) error {
	return &LimitError{Kind: kind, Limit: limit}
}

// at stamps a rejection with the byte offset where it was detected. It is a
// no-op for any other error, so a decoder error passes through untouched.
func at(err error, offset int) error {
	if le, ok := errors.AsType[*LimitError](err); ok {
		le.Offset = offset
	}
	return err
}

// ConfigError reports a non-positive bound, naming the field that is wrong. It
// wraps ErrInvalidLimits.
type ConfigError struct {
	// Field is the struct field name of the offending bound.
	Field string
	// Value is what the caller set it to.
	Value int
}

// Error implements error.
func (e *ConfigError) Error() string {
	return "xmlx: " + e.Field + " must be positive, got " + strconv.Itoa(e.Value)
}

// Unwrap returns ErrInvalidLimits.
func (e *ConfigError) Unwrap() error { return ErrInvalidLimits }

// requirePositive returns a ConfigError unless n is positive.
func requirePositive(field string, n int) error {
	if n > 0 {
		return nil
	}
	return &ConfigError{Field: field, Value: n}
}
