package yamlenv

import (
	"errors"
	"os"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// UnmarshalerError wraps a decode failure that the config type's OWN
// yaml.Unmarshaler produced — an error returned by a custom UnmarshalYAML
// method — as opposed to an error yaml.v3 itself constructed. It is the
// yaml-side analogue of encoding/json's MarshalerError: the library reporting
// that the caller's code, not the document machinery, refused the value.
//
// Load applies the classification to the post-expansion decode step before
// the WithErrorPassthrough predicate sees the error, so a caller that keeps
// its own error vocabulary on its operator surfaces detects its errors by
// TYPE (errors.As, or errors.AsType[*UnmarshalerError]) instead of matching
// message text against yaml.v3's "yaml:" prefix. Such a predicate can never
// claim one of yaml.v3's own errors, because those are never wrapped in this
// type.
//
// Error returns the unmarshaler's message verbatim — the wrap classifies, it
// does not rephrase — and Unwrap exposes the original error, so an
// errors.Is/errors.As chain a caller built against its own error types keeps
// working through the wrap. The classification is textual on one edge by
// necessity (yaml.v3 returns unmarshaler errors verbatim, so there is no
// structural marker): a custom UnmarshalYAML error that itself starts with
// "yaml:" is indistinguishable from the library's and is NOT wrapped; do not
// spell app-owned errors that way.
//
// Only Load constructs it, always with a non-nil Err; the zero value is not
// meaningful.
type UnmarshalerError struct {
	// Err is the error the config type's UnmarshalYAML returned, unchanged.
	Err error
}

// Error implements the error interface, returning the unmarshaler's own
// message byte for byte: the caller chose that vocabulary for its operator
// surfaces, and classifying it must not rewrite it.
func (e *UnmarshalerError) Error() string { return e.Err.Error() }

// Unwrap exposes the unmarshaler's error so errors.Is and errors.As reach
// whatever typed errors the caller's own UnmarshalYAML constructed.
func (e *UnmarshalerError) Unwrap() error { return e.Err }

// LoadOption configures Load. The zero configuration is the safe default:
// every yaml.v3 error is sanitized and the unknown-key name is redacted.
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

// WithErrorPassthrough registers pred as the caller's own-error detector for
// the decode step: a decode error for which pred reports true is returned
// unchanged instead of sanitized. It exists for config types whose
// UnmarshalYAML implementations return errors with an app-owned, value-safe
// vocabulary that the caller wants to keep on its operator surfaces. Load
// hands pred the CLASSIFIED error: a failure the config type's own
// UnmarshalYAML produced arrives wrapped in *UnmarshalerError, so the
// recommended predicate is the type check —
//
//	yamlenv.WithErrorPassthrough(func(err error) bool {
//		_, ok := errors.AsType[*yamlenv.UnmarshalerError](err)
//		return ok
//	})
//
// — which by construction can never claim one of yaml.v3's own errors and so
// cannot re-open the leak SanitizeDecodeError closes through THAT door. One
// honest bound: yaml.v3 relays an encoding.TextUnmarshaler failure verbatim,
// so a stdlib field type whose error echoes its input (netip.Addr, time.Time)
// classifies as app-supplied and passes through unsanitized — the same
// exposure the string predicate always had, carried, not created, by the
// type. A predicate matching broader shapes carries the full safety argument
// itself. Parse errors and unknown-key findings never pass through pred; no
// caller code produced them.
func WithErrorPassthrough(pred func(error) bool) LoadOption {
	return func(o *loadOptions) { o.passthrough = pred }
}

// Load runs the safe pipeline into out, a non-nil pointer holding the
// caller's defaults: CheckSingleDocument and the unknown-key check on the raw
// bytes, ExpandWith (allow, and WithLookupEnv's lookup), the decode, and error
// sanitization. An empty document leaves out unchanged. The key check reports
// only unknown keys; value errors come from the post-expansion decode.
// unresolved is ExpandWith's return. Every error is safe to log, except a
// decode error claimed through WithErrorPassthrough, which arrives classified
// (*UnmarshalerError for the config type's own) and otherwise unchanged.
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
	if err := CheckSingleDocument(data); err != nil {
		return nil, err // static ErrMultipleDocuments, safe unsanitized
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// No caller code ran yet, so a parse error is never app-owned;
		// it can embed pasted literal secrets and is always sanitized.
		return nil, SanitizeDecodeError(err, o.sanitize...)
	}
	if doc.Kind == 0 {
		return nil, nil // empty document: out keeps its pre-set defaults
	}
	if err := checkUnknownKeysFiltered(data, rv.Type().Elem(), o); err != nil {
		return nil, err
	}
	unresolved = ExpandWith(&doc, allow, o.lookup)
	if err := doc.Decode(out); err != nil {
		err = classifyDecodeError(err)
		if o.passthrough != nil && o.passthrough(err) {
			return unresolved, err
		}
		return unresolved, SanitizeDecodeError(err, o.sanitize...)
	}
	return unresolved, nil
}

// classifyDecodeError wraps a decode failure that came from the config type's
// own UnmarshalYAML in *UnmarshalerError, and returns every error yaml.v3
// itself constructed unchanged. The split is the one yaml.v3's decoder
// leaves observable: the library's failures are a *yaml.TypeError (possibly
// wrapped) or carry the "yaml:" message prefix, while an error a custom
// unmarshaler returned surfaces verbatim with neither marker. Sanitization is
// unaffected either way — *UnmarshalerError matches none of the sanitizer's
// rebuilt shapes, so on the fail-closed path it withholds exactly what the
// unwrapped error withheld.
func classifyDecodeError(err error) error {
	if _, ok := errors.AsType[*yaml.TypeError](err); ok {
		return err
	}
	if strings.HasPrefix(err.Error(), "yaml:") {
		return err
	}
	return &UnmarshalerError{Err: err}
}

// checkUnknownKeysFiltered runs the CheckUnknownKeys probe against a fresh
// value of the config type and keeps ONLY unknown-key findings, sanitized per
// the caller's policy. Everything else the probe can produce is a
// pre-expansion artifact whose diagnostic the post-expansion decode owns and
// re-raises when genuine: a wrong-type entry (dropped here, raised again by
// the decode if the expanded document still mismatches), and a probe-aborting
// error from a custom UnmarshalYAML rejecting a still-literal ${VAR} that
// expansion will satisfy (which also suppresses unknown-key detection for
// that document — the cost of never false-rejecting a valid env-referenced
// config).
func checkUnknownKeysFiltered(data []byte, cfgType reflect.Type, o loadOptions) error {
	err := CheckUnknownKeys(data, reflect.New(cfgType).Interface())
	if err == nil {
		return nil
	}
	typeErr, ok := errors.AsType[*yaml.TypeError](err)
	if !ok {
		return nil
	}
	var unknown []string
	for _, entry := range typeErr.Errors {
		if _, _, _, ok := lineEntryParts(entry, unknownKeyMarker, unknownKeyInType); ok {
			unknown = append(unknown, entry)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return SanitizeDecodeError(&yaml.TypeError{Errors: unknown}, o.sanitize...)
}
