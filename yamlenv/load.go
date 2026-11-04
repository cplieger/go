package yamlenv

import (
	"errors"
	"os"
	"reflect"

	"go.yaml.in/yaml/v4"
)

// UnmarshalerError is a decode failure the config type's own UnmarshalYAML
// produced, not one go-yaml constructed. Load classifies the decode error
// before WithErrorPassthrough's predicate sees it, so a caller claims its own
// errors by type and never one of go-yaml's. Load wraps only when every entry
// came from the config type, and not when the method's error wraps a go-yaml
// error or reads like one of go-yaml's messages. Err is the method's error,
// or errors.Join of several in document order; Error returns its text
// unchanged. Only Load constructs it, always with a non-nil Err.
type UnmarshalerError struct {
	// Err is the error the config type's UnmarshalYAML returned, unchanged.
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

// WithSanitizeOptions forwards SanitizeOptions (e.g. WithUnknownKeyEcho(true))
// to every SanitizeDecodeError call Load makes, so the caller sets its
// sanitization policy once for the whole pipeline.
func WithSanitizeOptions(opts ...SanitizeOption) LoadOption {
	return func(o *loadOptions) { o.sanitize = append(o.sanitize, opts...) }
}

// WithErrorPassthrough returns a decode error unchanged instead of sanitized
// when pred reports true, for config types whose UnmarshalYAML speaks an
// app-owned, value-safe vocabulary. pred sees the classified error, so
// errors.AsType[*UnmarshalerError] is the predicate that can never claim one
// of go-yaml's errors. go-yaml relays an encoding.TextUnmarshaler failure like
// UnmarshalYAML's, so a field type whose error echoes its input (netip.Addr,
// time.Time) passes through unsanitized under it. Parse errors and
// unknown-key findings never reach pred.
func WithErrorPassthrough(pred func(error) bool) LoadOption {
	return func(o *loadOptions) { o.passthrough = pred }
}

// Load runs the safe pipeline into out, a non-nil pointer holding the
// caller's defaults: one parse that rejects a second document
// (ErrMultipleDocuments), the unknown-key check on the raw bytes, ExpandWith
// (allow, and WithLookupEnv's lookup), the decode, and error sanitization. An
// empty document leaves out unchanged; unresolved is ExpandWith's return.
// Every error is safe to log, except a decode error claimed through
// WithErrorPassthrough, which arrives classified (*UnmarshalerError for the
// config type's own) and otherwise unchanged.
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
	if err := checkUnknownKeysFiltered(data, rv.Type().Elem(), o); err != nil {
		return nil, err
	}
	unresolved = ExpandWith(doc, allow, o.lookup)
	if err := doc.Decode(out); err != nil {
		if o.passthrough != nil {
			if classified := classifyDecodeError(err); o.passthrough(classified) {
				return unresolved, classified
			}
		}
		return unresolved, SanitizeDecodeError(err, o.sanitize...)
	}
	return unresolved, nil
}

// classifyDecodeError wraps a decode failure in *UnmarshalerError when every
// entry came from the config type's own code. go-yaml v4 relays a method's
// error as a constructor-stage LoadError that unwraps to it, but its own
// entries share the stage and also unwrap to a fresh error, so the message
// shape is what tells them apart. A single *LoadError is go-yaml stopping at
// its own failure and is never wrapped.
func classifyDecodeError(err error) error {
	errs, ok := errors.AsType[*yaml.LoadErrors](err)
	if !ok || len(errs.Errors) == 0 {
		return err
	}
	causes := make([]error, 0, len(errs.Errors))
	for _, e := range errs.Errors {
		cause := unmarshalerCause(e)
		if cause == nil {
			return err
		}
		causes = append(causes, cause)
	}
	if len(causes) == 1 {
		return &UnmarshalerError{Err: causes[0]}
	}
	return &UnmarshalerError{Err: errors.Join(causes...)}
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
func checkUnknownKeysFiltered(data []byte, cfgType reflect.Type, o loadOptions) error {
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
	return SanitizeDecodeError(&yaml.LoadErrors{Errors: unknown}, o.sanitize...)
}
