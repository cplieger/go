package textsafe

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsBidiControl reports whether r is one of Unicode's Bidi_Control format
// characters: the singleton marks U+061C (ALM) and U+200E/U+200F (LRM/RLM),
// the override/embedding range U+202A-U+202E (LRE/RLE/PDF/LRO/RLO), and the
// isolate range U+2066-U+2069 (LRI/RLI/FSI/PDI). Any of them in untrusted
// text can visually reorder rendered output (Trojan-Source-style report and
// link spoofing), so every output sanitizer treats the full set as unsafe.
// The set matches unicode.Bidi_Control exactly, without the table lookup; a
// conformance sweep pins the two rune by rune, so a later Unicode adding a
// member fails the tests instead of silently diverging.
func IsBidiControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') ||
		(r >= '\u2066' && r <= '\u2069')
}

// IsUnsafeMultiLine reports whether r is unsafe in untrusted text bound for
// a sink whose encoder escapes CR and LF itself (JSON, a quoting logger): a
// C0 control other than CR/LF, DEL, a C1 control (U+0080-U+009F, single-rune
// terminal-escape introducers), a Unicode bidi control (IsBidiControl), or
// the U+2028/U+2029 line separators. CR and LF are SAFE under this policy.
// It is the per-rune predicate behind [Sanitize]; the single-line policy is
// [IsUnsafeSingleLine].
func IsUnsafeMultiLine(r rune) bool {
	return r != '\n' && r != '\r' && isUnsafeCore(r)
}

// IsUnsafeSingleLine reports whether r is unsafe in untrusted text bound for
// a single-line sink, where a raw CR or LF forges a new record: everything
// [IsUnsafeMultiLine] refuses, plus CR and LF. It is the per-rune predicate
// behind [SanitizeSingleLine].
func IsUnsafeSingleLine(r rune) bool {
	return r == '\n' || r == '\r' || isUnsafeCore(r)
}

// isUnsafeCore is the CR/LF-independent shared policy: C0 controls (the
// CR/LF decision is the callers'), DEL, C1 controls, bidi controls, and the
// U+2028/U+2029 line separators.
func isUnsafeCore(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) ||
		IsBidiControl(r) || r == '\u2028' || r == '\u2029'
}

// IsUnsafeNonASCII reports whether r is an unsafe rune above the ASCII
// range: a C1 control (U+0080-U+009F), a Unicode bidi control
// (IsBidiControl), or the U+2028/U+2029 line separators. It is the IsUnsafe
// policy minus C0, DEL, and the CR/LF axis (all ASCII), for composed
// escapers whose sink already covers ASCII: a URL percent-encoder escapes
// C0, DEL, and whitespace itself, but url.Parse accepts these non-ASCII
// runes raw, and a terminal or Markdown viewer must never receive them.
// The CR/LF distinction is moot above ASCII, so one predicate serves both
// policies: IsUnsafeNonASCII(r) equals IsUnsafeMultiLine(r) && r >
// unicode.MaxASCII, and IsUnsafeSingleLine(r) && r > unicode.MaxASCII.
func IsUnsafeNonASCII(r rune) bool {
	return r > unicode.MaxASCII && IsUnsafeMultiLine(r)
}

// Sanitize makes an untrusted string safe for slog/JSON sinks by replacing
// each unsafe rune with a space: C0 controls (except CR/LF, which JSON
// encoders escape), DEL, C1 controls (U+0080-U+009F, single-rune
// terminal-escape introducers emitted raw by encoding/json and slog's
// JSONHandler), Unicode bidi controls, and the U+2028/U+2029 line
// separators. Invalid UTF-8 bytes become U+FFFD (a strings.Map property),
// so the result is always valid UTF-8. Apply it to every untrusted
// attribute at the emit boundary — one policy shared by all of an app's
// sinks, so they cannot drift. For a single-line sink where CR/LF must also
// go, use SanitizeSingleLine.
func Sanitize(s string) string {
	return strings.Map(mapMultiLine, s)
}

// SanitizeSingleLine makes an untrusted string safe for a single-line sink —
// a plain-text log line, a one-line error message, a rendered table cell —
// by replacing each unsafe rune with a space under the single-line policy
// (IsUnsafeSingleLine): everything Sanitize replaces, plus CR and LF, whose raw appearance
// in a single-line sink forges a record boundary. Invalid UTF-8 bytes become
// U+FFFD, so the result is always valid UTF-8 and carries no line break.
func SanitizeSingleLine(s string) string {
	return strings.Map(mapSingleLine, s)
}

// SanitizeCapped is Sanitize followed by a byte cap that INCLUDES the
// marker. It sanitizes s under the multi-line policy (IsUnsafeMultiLine: CR
// and LF survive, for a sink whose encoder escapes them — JSON, slog's
// handlers), and if the result exceeds n bytes it is cut on a rune boundary
// at n-len(marker) bytes with marker appended, so the returned text is at most
// max(n, 0) bytes whatever marker is. A within-cap value comes back
// untouched — byte-identical to Sanitize, no marker.
//
// cut reports whether the sanitized form was SHORTENED, not whether
// sanitizing rewrote anything: it is true exactly when that form did not fit
// in n bytes, and false whenever the returned text is the whole sanitized
// form. It exists because a marker cannot prove a cut — a value may end in
// the marker on its own — so a caller that must report truncation as a fact
// would otherwise re-implement this composition just to set its own flag.
//
// n bounds the TOTAL, which is what a caller with a real budget needs: a
// record persisted under a write limit, a payload assembled under a vendor
// byte cap, a fixed-width column. SanitizeSingleLineBounded puts its marker
// OUTSIDE the cap (a truncated result runs to n+3 bytes).
//
// A marker longer than n cannot be shown intact, and a partial marker is
// indistinguishable from content, so for n < len(marker) the result is the
// rune-boundary prefix of the sanitized form alone — the empty string for a
// non-positive n — and the elision is reported through cut only. An empty
// marker is legal and yields a silent cap (CapBytes over the sanitized form)
// with the fact still returned. An empty s returns "" and false under any n,
// negative included. marker is emitted verbatim, never sanitized: it is the
// caller's own program text, and rewriting it would silently alter a chosen
// mark — a marker assembled from untrusted input must be sanitized by the
// caller first.
//
// The CR/LF policy is a second function rather than a parameter, so a call site
// names its sink instead of passing an opaque boolean next to two other tuning
// arguments. SanitizeSingleLineCapped is the strict twin.
//
// The cap is applied AHEAD of the sanitizer: s is cut on a rune boundary at n
// bytes first, that chunk is sanitized, sanitization growth is re-capped, and
// the marker is charged inside n. So n bounds the WORK done on s as well as
// the size of what comes back, which matters when s comes from an upstream
// whose size the caller does not control. For a valid-UTF-8 value with no
// unsafe rune this is byte for byte what sanitizing first and then capping
// gives. A value whose raw form exceeds n but whose sanitized form would fit
// (enough multi-byte unsafe runes, each becoming one space) is cut and
// marked, and the mark is honest: bytes were dropped before sanitizing.
//
// Two shapes this pair does not serve: several values under one shared
// budget, which is Budget itself and keeps ONE truncation fact across the
// writes; and keeping the TAIL behind a PREFIXED marker (a path's file name),
// for which CapBytesTail is the rune-boundary walk and the marker stays the
// caller's.
func SanitizeCapped(s string, n int, marker string) (text string, cut bool) {
	// The rune-boundary cut, the marker charged inside the cap and the cut
	// FACT are the Budget engine's, so there is one copy of that arithmetic.
	b := NewBudget(n, marker)
	b.Write(s)
	return b.Result()
}

// SanitizeSingleLineCapped is SanitizeCapped under the single-line policy
// (IsUnsafeSingleLine): CR and LF become spaces along with every other unsafe
// rune, for a single-line sink where a raw newline forges a record boundary —
// a plain-text log line, a one-line error message, a rendered table cell. The
// cap-first order, the marker counted INSIDE n, the n < len(marker)
// degenerate case and the cut fact are identical; see SanitizeCapped for the
// full contract, for why the CR/LF policy is a separate function, and for the
// shapes the pair does not serve.
func SanitizeSingleLineCapped(s string, n int, marker string) (text string, cut bool) {
	b := NewSingleLineBudget(n, marker)
	b.Write(s)
	return b.Result()
}

// SanitizeSingleLineBounded is SanitizeSingleLine followed by a byte cap: an
// over-cap result is truncated to at most n bytes on a rune boundary
// (CapBytes) with "..." appended to mark the cut, while a result within the
// cap is returned untouched — no marker, byte-identical to the unbounded
// form. It is the log-bound preset for VOLUME as well as rune safety: an
// upstream-controlled value headed for a log attribute (a query parameter, a
// JSON key name, an upstream error body) must not balloon a record past
// downstream log-pipeline limits, and every consumer hand-rolled exactly this
// sanitize-cap-mark composition. The cap applies to the SANITIZED form
// (sanitizing can grow invalid bytes into the three-byte U+FFFD, so a
// pre-sanitize cap does not survive), and the marker sits outside the cap: a
// truncated result is at most n+3 bytes and always ends in the marker. The
// converse does not hold — a within-cap input may itself end in "..." — so
// the marker marks the cut without proving one; a caller that must know
// whether truncation occurred, supply its own marker, or hold the total
// (marker included) under a hard budget uses SanitizeSingleLineCapped, which
// returns the fact and counts the marker inside the cap. A non-positive n
// yields "..." alone for a non-empty input ("" stays "", whatever n is).
// The marker's placement outside the cap is settled contract: it stays as it is
// so every existing call site keeps its byte-for-byte output.
func SanitizeSingleLineBounded(s string, n int) string {
	// The marker rides OUTSIDE the cap here, so this preset shares no
	// arithmetic with the marker-inside-the-cap bound the Capped pair and the
	// Budget engine hold in common; what is left is a short composition of the
	// package's own primitives. The empty-input guard is what keeps "" empty
	// under a non-positive cap instead of marking it.
	clean := strings.Map(mapSingleLine, s)
	if clean == "" || len(clean) <= n {
		return clean
	}
	return CapBytes(clean, n) + defaultMarker
}

// defaultMarker is the truncation marker SanitizeSingleLineBounded appends.
// The Capped pair takes the marker from the caller instead.
const defaultMarker = "..."

// mapMultiLine and mapSingleLine are the named strings.Map functions behind
// Sanitize and SanitizeSingleLine: each replaces an unsafe rune with a space
// (strings.Map also converts each invalid UTF-8 byte to U+FFFD, a safe rune
// under both policies). Named package-level functions rather than closures
// over a predicate parameter so the per-rune call stays direct and inlinable
// — the closure form measured +25% on Sanitize.
func mapMultiLine(r rune) rune {
	if IsUnsafeMultiLine(r) {
		return ' '
	}
	return r
}

func mapSingleLine(r rune) rune {
	if IsUnsafeSingleLine(r) {
		return ' '
	}
	return r
}

// CapBytes truncates s to at most n bytes without splitting a multi-byte
// rune at the cut: the cut backs off to the nearest rune start, so the
// result never ends in a partial rune. It exists because sanitizing can grow
// a string (each invalid UTF-8 byte becomes the three-byte U+FFFD), so a
// pre-sanitize byte cap does not survive sanitization, and a naive re-cap
// can split a rune — leaving a partial-rune tail whose raw 0x80-0x9F bytes
// a non-UTF-8 terminal reads as C1 escape introducers, re-minting the very
// class of byte the sanitizer removed. For valid UTF-8 input (any Sanitize or
// SanitizeSingleLine output) the backoff discards at most three bytes below
// n and the result is a valid-UTF-8 prefix of s. A non-positive n returns
// the empty string.
func CapBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// CapBytesTail keeps at most the last n bytes of s without splitting a
// multi-byte rune: the cut advances FORWARD to the next rune start, so the
// result is a valid-UTF-8 suffix no longer than n bytes. Forward holds that
// bound where CapBytes' backward backoff would exceed it, and it drops the
// partial LEADING rune encoding/json rewrites to U+FFFD, so a capped field
// cannot open on a replacement rune. Non-positive n returns "". The cut is
// rune-safe, not grapheme-safe: it may fall between a base rune and its
// combining mark, which is this package's documented Unicode non-goal.
func CapBytesTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
