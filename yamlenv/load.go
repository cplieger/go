package yamlenv

import (
	"errors"
	"os"
	"reflect"

	"go.yaml.in/yaml/v4"
)

// UnmarshalerError is a decode problem Load attributes to the config type's
// own code, the only error WithErrorPassthrough's predicate sees: it wraps no
// go-yaml error and carries none of a go-yaml message's fixed words (listed in
// the README). Load cannot vouch for other text a hook writes, so claim the
// app's own error type, not this one. The predicate gets one per problem; a
// passed-through error joins every claimed one (errors.Join) in document
// order. Error returns Err's text unchanged. Only Load constructs it, always
// with a non-nil Err.
type UnmarshalerError struct {
	// Err is the config type's own error for one problem, or errors.Join of
	// several in document order.
	Err error
}

// Error returns the unmarshaler's message byte for byte: the caller chose that
// vocabulary for its operator surfaces.
func (e *UnmarshalerError) Error() string { return e.Err.Error() }

// Unwrap exposes the unmarshaler's error so errors.Is and errors.As reach
// whatever typed errors the caller's own UnmarshalYAML constructed.
func (e *UnmarshalerError) Unwrap() error { return e.Err }

// LoadOption configures Load. The zero configuration is the safe default:
// every go-yaml error is sanitized and the unknown-key name is redacted.
type LoadOption func(*loadOptions)

// loadOptions carries Load's policy switches.
type loadOptions struct {
	passthrough func(error) bool
	lookup      func(string) (string, bool)
	sanitize    []SanitizeOption
}

// WithLookupEnv makes Load read every ${VAR} through lookup instead of the
// process environment, for both expansion and the unresolved names it
// returns (ExpandWith's contract). A nil lookup keeps the process environment.
func WithLookupEnv(lookup func(name string) (string, bool)) LoadOption {
	return func(o *loadOptions) { o.lookup = lookup }
}

// WithSanitizeOptions forwards SanitizeOptions to every error Load
// sanitizes, so the caller sets its policy once for the whole pipeline.
// WithUnknownKeyEcho(true) reaches the raw-bytes unknown-key check only: the
// decode after expansion always redacts a key, because a config type that
// strict-loads its own node there sees expanded values.
func WithSanitizeOptions(opts ...SanitizeOption) LoadOption {
	return func(o *loadOptions) { o.sanitize = append(o.sanitize, opts...) }
}

// WithErrorPassthrough returns a decode error unchanged instead of sanitized
// when every problem in it came from the config type and pred claims each;
// any other error never reaches pred. Load calls pred once per problem, in
// document order, stopping at the first it refuses, as an *UnmarshalerError
// holding that problem alone. Claim the app's own type, as with
// errors.AsType[*myconfig.Error]: a TextUnmarshaler field's error counts as the
// config type's and can echo its input (netip.Addr), and a hook copying
// go-yaml's text is withheld only when it matches a pinned marker.
func WithErrorPassthrough(pred func(error) bool) LoadOption {
	return func(o *loadOptions) { o.passthrough = pred }
}

// Load runs the safe pipeline into out, a non-nil pointer holding the
// caller's defaults: one parse that rejects a second document
// (ErrMultipleDocuments), the unknown-key check on the raw bytes, ExpandWith
// (allow, and WithLookupEnv's lookup), the decode, and error sanitization. An
// empty document leaves out unchanged; unresolved is ExpandWith's return.
// Every error is safe to log, except a decode error whose every problem pred
// claimed through WithErrorPassthrough, which arrives as an *UnmarshalerError.
func Load(data []byte, out any, allow func(name string) bool, opts ...LoadOption) (unresolved []string, err error) {
	var o loadOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.lookup == nil {
		o.lookup = os.LookupEnv
	}
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, errors.New("yamlenv: Load out must be a non-nil pointer")
	}
	doc, err := parseSingleDocument(data)
	if errors.Is(err, ErrMultipleDocuments) {
		return nil, err
	}
	if err != nil {
		// No caller code ran yet, so a parse error is never app-owned;
		// it can embed pasted literal secrets and is always sanitized.
		return nil, SanitizeDecodeError(err, o.sanitize...)
	}
	if doc == nil {
		return nil, nil // empty document: out keeps its pre-set defaults
	}
	cfgType := rv.Type().Elem()
	if err := checkUnknownKeysFiltered(data, doc, cfgType, o); err != nil {
		return nil, err
	}
	unresolved = ExpandWith(doc, allow, o.lookup)
	if err := doc.Decode(out); err != nil {
		if claimed := claimedError(o.passthrough, err); claimed != nil {
			return unresolved, claimed
		}
		so := o.sanitizeOptions(cfgType, doc)
		so.echoUnknownKey = false
		return unresolved, sanitize(err, so)
	}
	return unresolved, nil
}

// sanitizeOptions applies the caller's SanitizeOptions and attaches what Load
// knows about cfgType and doc.
func (o loadOptions) sanitizeOptions(cfgType reflect.Type, doc *yaml.Node) sanitizeOptions {
	var so sanitizeOptions
	for _, opt := range o.sanitize {
		opt(&so)
	}
	so.src = newProvenance(cfgType, doc)
	return so
}

// claimedError returns the decode error to pass through unchanged, or nil
// when pred is nil, the config type does not own every problem, or pred
// refuses one. pred judges each cause on its own: errors.As finds a type in
// any member of a join, so a predicate for the app's own type would claim a
// TextUnmarshaler field's echo beside it.
func claimedError(pred func(error) bool, err error) *UnmarshalerError {
	if pred == nil {
		return nil
	}
	causes := classifyDecodeError(err)
	if causes == nil {
		return nil
	}
	for _, c := range causes {
		if !pred(&UnmarshalerError{Err: c}) {
			return nil
		}
	}
	if len(causes) == 1 {
		return &UnmarshalerError{Err: causes[0]}
	}
	return &UnmarshalerError{Err: errors.Join(causes...)}
}

// classifyDecodeError returns the errors the config type's own code returned,
// one per decode problem in document order, or nil when any problem is
// go-yaml's. go-yaml v4 relays a method's error as a constructor-stage
// LoadError that unwraps to it, but its own entries share the stage and also
// unwrap to a fresh error, so the message shape is what tells them apart. A
// single *LoadError is go-yaml stopping at its own failure and owns nothing.
func classifyDecodeError(err error) []error {
	errs, ok := errors.AsType[*yaml.LoadErrors](err)
	if !ok || len(errs.Errors) == 0 {
		return nil
	}
	causes := make([]error, 0, len(errs.Errors))
	for _, e := range errs.Errors {
		cause := unmarshalerCause(e)
		if cause == nil {
			return nil
		}
		causes = append(causes, cause)
	}
	return causes
}

// unmarshalerCause returns the error a config type's method returned for one
// entry, or nil when the entry is go-yaml's own.
func unmarshalerCause(e *yaml.LoadError) error {
	if e == nil || e.Stage != yaml.ConstructorStage || isGoYAMLMessage(e.Message) {
		return nil
	}
	cause := e.Unwrap()
	if cause == nil {
		return nil
	}
	if _, ok := errors.AsType[*yaml.LoadErrors](cause); ok {
		return nil
	}
	if _, ok := errors.AsType[*yaml.LoadError](cause); ok {
		return nil
	}
	return cause
}

// checkUnknownKeysFiltered runs the CheckUnknownKeys probe against a fresh
// value of the config type and keeps ONLY unknown-key findings. The rest are
// pre-expansion artifacts the post-expansion decode owns: a wrong-type entry,
// or a custom UnmarshalYAML rejecting a still-literal ${VAR}. A probe go-yaml
// stopped early (a single *LoadError) reports nothing; the decode re-raises it.
// doc must be unexpanded, so an echoed key is the one the operator wrote.
func checkUnknownKeysFiltered(data []byte, doc *yaml.Node, cfgType reflect.Type, o loadOptions) error {
	errs, ok := errors.AsType[*yaml.LoadErrors](CheckUnknownKeys(data, reflect.New(cfgType).Interface()))
	if !ok {
		return nil
	}
	var unknown []*yaml.LoadError
	for _, e := range errs.Errors {
		if _, ok := unknownKey(e.Message); ok {
			unknown = append(unknown, e)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return sanitize(&yaml.LoadErrors{Errors: unknown}, o.sanitizeOptions(cfgType, doc))
}
