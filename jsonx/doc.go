// Package jsonx provides defensive decoding of untrusted upstream JSON -
// number-or-string integer fields, parsed by one hardened core under an
// explicit, pluggable tolerance Policy.
//
// Upstream APIs are inconsistent about numeric fields: the same field
// arrives as 14 on one endpoint and "14" on another, and odd rows carry
// null, "", "unknown", floats, negatives, or absurdly large values. Three
// apps had hand-rolled this decode with three deliberate - and drifting -
// policies (seadex-scout's tolerant-zero Fribb decoder was the only copy
// with NaN/fractional/range integrity guards; subflux's strict provider
// core and plex-language-sync's strict decoder each lacked them). This
// package is the shared, hardened replacement.
//
// Classify extracts one value's syntactic facts (shape, was-string, float
// form, fractional, negative, overflow, padding) without judging them. A
// Policy then decides per fact whether to accept the parsed value,
// tolerate the oddity as zero, or reject it with a typed *ParseError.
// Three ready-made policies reproduce the origin decoders: TolerantZero,
// Strict, and StrictAbsentZero. Ready-made json.Unmarshaler field types
// (TolerantInt, StrictInt, StrictAbsentZeroInt) apply them directly in
// struct definitions.
//
// # Capped decoding
//
// A [Decoder] decodes untrusted upstream JSON under cardinality caps,
// with the semantics of json.Unmarshal.
//
// json.Unmarshal materializes the whole decoded value before a caller can
// count anything, so a small body of compact elements can decode into slices
// and structs far larger than the byte cap allowed. A [Decoder] walks the
// token stream instead and lets the caller reject an element before it is
// decoded: [Decoder.Array] and [Decoder.Map] take a per-container cap, and
// every walk charges one aggregate element budget.
//
// The building blocks reproduce encoding/json's observable behavior, so a
// schema decoder built from them is a drop-in for json.Unmarshal on
// well-formed input:
//
//   - [Decoder.Object] decodes a struct-shaped object and leaves the target untouched
//     on a JSON null.
//   - [Decoder.Array] decodes a slice, and [Decoder.Map] decodes a Go map. A
//     null gives nil.
//   - [Decoder.Skip] passes over an unknown field without materializing it.
//   - [Decoder.Decode] decodes a scalar value.
//
// Key dispatch stays with the caller. Match keys with strings.EqualFold to
// reproduce json.Unmarshal's case-insensitive field matching.
//
// [Preflight] is a separate pass that rejects a body with a repeated object
// key before any decode runs, for a caller that cannot accept the ambiguity
// json.Unmarshal resolves silently.
//
// Errors wrap [ErrArrayCap], [ErrMapCap], [ErrElementBudget] and
// [ErrDuplicateKey], which match with errors.Is. The null, empty and
// repeated-key rules in full are in docs/capped-decoding-internals.md.
package jsonx
