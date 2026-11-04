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
package jsonx
