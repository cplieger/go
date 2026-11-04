package textsafe

import "strings"

// Budget is a byte budget several untrusted values are sanitized under,
// together, retaining ONE truncation fact for the whole aggregate. Each value
// is capped on a rune boundary BEFORE the sanitizer walks it, so the budget
// bounds the sanitizer's WORK and not merely its output, and the marker is
// charged inside the budget exactly as SanitizeCapped charges it.
//
// It is a SEPARATE primitive from the Capped pair rather than an option on it,
// because SanitizeCapped sanitizes all of s by construction — its cap must
// survive sanitization growth — and no marker placement changes that. A caller
// whose value is NOT known to be small needs the opposite order, and a caller
// assembling SEVERAL values under one shared bound needs a running remainder and
// a latched fact that a single-call function cannot express. What the two share —
// the rune-boundary cut, the marker charged inside the cap, the cut FACT returned
// rather than inferred from the marker — is identical on purpose, so a call site
// can move between them without re-reading either contract.
//
// The order is the whole point of the type. A multi-megabyte upstream value
// walked by strings.Map in a memory-limited process is a work-amplification
// denial of service (CWE-400) whatever the output bound is, so a caller reading
// an upstream field whose size it does not control caps first. For a value that
// carries no unsafe rune and is valid UTF-8 the two orders are byte-identical,
// so adopting this one changes nothing an honest value emits; the two diverge
// for exactly one input class, described on SanitizeBudgeted.
//
// A Budget is a running remainder over values that ACCUMULATE. A caller
// bounding several separate output fields by re-measuring an envelope after
// each trim (a marshaled payload against a vendor limit) is not that shape and
// keeps its own loop over the Capped pair.
//
// Use NewBudget or NewSingleLineBudget; the zero value has no budget and would
// treat every write as a cut. A Budget must not be copied after its first
// write, and it is not safe for concurrent use.
type Budget struct {
	unsafe    func(rune) bool
	marker    string
	b         strings.Builder
	total     int
	remaining int
	cut       bool
}

// NewBudget returns a Budget of n bytes under the multi-line policy ([IsUnsafeMultiLine]) — CR and
// LF survive, for a sink whose encoder escapes them (JSON, slog's handlers) —
// appending marker to the aggregate when anything was cut. It is the
// Sanitize / SanitizeCapped policy in aggregate form;
// NewSingleLineBudget is the strict twin.
//
// n bounds the TOTAL the aggregate can ever reach, marker included: whatever
// the writes do, Result returns at most max(n, 0) bytes. A non-positive n
// therefore accepts nothing, and reports a cut for the first non-empty write.
//
// marker is emitted verbatim, never sanitized: it is the caller's own program
// text, so a marker assembled from untrusted input must be sanitized by the
// caller first. An empty marker is legal and yields a silent cap with the fact
// still returned by Result.
func NewBudget(n int, marker string) *Budget {
	return &Budget{marker: marker, total: n, remaining: n, unsafe: IsUnsafeMultiLine}
}

// NewSingleLineBudget returns a Budget of n bytes under the strict
// single-line policy ([IsUnsafeSingleLine]): CR and LF become spaces along with every other unsafe
// rune, for a single-line sink where a raw newline forges a record boundary — a
// plain-text log line, a one-line error message, a rendered table cell. The
// budget, marker and cut contract are identical to NewBudget's; the CR/LF
// policy is a second constructor rather than a parameter for the same reason
// SanitizeSingleLineCapped is a second function, so a call site names its sink
// instead of passing an opaque boolean.
func NewSingleLineBudget(n int, marker string) *Budget {
	return &Budget{marker: marker, total: n, remaining: n, unsafe: IsUnsafeSingleLine}
}

// Write appends the sanitized prefix of raw that still fits the remaining
// budget and reports whether the aggregate is still WHOLE — that is, whether
// nothing has been dropped yet. It caps raw on a rune boundary at the remaining
// budget FIRST, so the sanitizer never walks more bytes than the budget allows
// however long raw is; sanitizing can then grow the chunk (each invalid UTF-8
// byte becomes the three-byte U+FFFD), so the result is re-capped on a rune
// boundary. Either cut latches the fact Result reports.
//
// Once anything has been cut, or the budget is spent, further writes append
// nothing: a non-empty one is refused whole rather than partially, because an
// aggregate that mixed a truncated value with a later whole one would read as
// if the two were contiguous. A write of "" never sets the fact.
//
// The return value is the loop condition an aggregating caller wants, and it is
// deliberately NOT "the budget has room left": a caller must ATTEMPT the write
// it cannot fit, because the refusal is what latches the truncation fact.
// Stopping on a spent-but-uncut budget instead would drop the remaining values
// silently, and Result would call the aggregate whole.
//
// A fixed separator between values goes through Write too, so a hostile piece
// COUNT cannot grow the aggregate past the budget either. Sanitizing an ASCII
// separator is a no-op, so an aggregate of honest values is byte-identical to
// the joined-then-capped form.
//
// Write is deliberately not io.Writer: a sink that drops bytes on purpose
// cannot honour that interface's "wrote all of p, or returned an error"
// contract, and reporting a short write as an error would make every ordinary
// truncation look like a failure.
func (b *Budget) Write(raw string) bool {
	if b.cut || b.remaining <= 0 {
		b.cut = b.cut || raw != ""
		return !b.cut
	}
	chunk := CapBytes(raw, b.remaining)
	if len(chunk) < len(raw) {
		b.cut = true
	}
	clean := strings.Map(func(r rune) rune {
		if b.unsafe(r) {
			return ' '
		}
		return r
	}, chunk)
	if len(clean) > b.remaining {
		clean = CapBytes(clean, b.remaining)
		b.cut = true
	}
	b.b.WriteString(clean)
	b.remaining -= len(clean)
	return !b.cut
}

// Result returns the aggregate and the one truncation fact latched across every
// write. An aggregate that fit comes back whole and unmarked; one that lost
// bytes anywhere is cut back on a rune boundary to make room for the marker and
// marked with it, so the text never exceeds max(n, 0) bytes — the SanitizeCapped
// bound, applied to the whole aggregate instead of one value.
//
// cut reports whether bytes were DROPPED, not whether sanitizing rewrote
// anything, and it is the fact a caller reports beside the value (a
// "…_truncated" log attribute, a decision to keep a fuller copy elsewhere). A
// marker cannot stand in for it: a value may end in the marker on its own.
//
// A budget too small to hold the marker intact drops the marker rather than
// emitting a fragment of it, and reports the elision through cut alone.
//
// Result reads the Budget without spending it: calling it twice returns the
// same pair, and a Write after it still appends under whatever budget is left.
func (b *Budget) Result() (text string, cut bool) {
	text = b.b.String()
	if !b.cut {
		return text, false
	}
	head, mark := markedCut(text, b.total, b.marker)
	return head + mark, true
}

// markedCut is the cut half of the marker-inside-the-cap rule: the rune-safe
// head of text and marker within n bytes, or, when n cannot hold the marker
// intact, a head of at most n bytes and no marker. Comparing before
// subtracting keeps a very negative n from wrapping into a huge room.
func markedCut(text string, n int, marker string) (head, mark string) {
	if n < len(marker) {
		return CapBytes(text, n), ""
	}
	return CapBytes(text, n-len(marker)), marker
}

// SanitizeBudgeted is the one-value form of NewBudget: a Budget of n bytes, one
// Write of s, and its Result. It is SanitizeCapped with the cap moved AHEAD of
// the sanitizer — s is capped on a rune boundary at n bytes first, the chunk is
// sanitized under the multi-line policy, sanitization growth is re-capped,
// and the caller's marker is charged inside n — so the budget bounds the WORK
// done on s rather than only the size of what comes back. Reach for it when s
// comes from an upstream whose size the caller does not control (a response
// field, an error message interpolating one, a file name), where walking all of
// it is itself the exposure; reach for SanitizeCapped when s is already known
// to be small and only its output needs bounding.
//
// The two orders agree on every valid-UTF-8 value that carries no unsafe rune,
// byte for byte, cut for cut, so a call site can move to this one without
// changing what an honest value emits. They diverge for exactly one input
// class: a value whose RAW form exceeds n but whose SANITIZED form would have
// fitted, which needs enough multi-byte unsafe runes to collapse it under the
// cap (each becomes a single-byte space) — the hostile shape the work bound
// exists for. This function cuts and marks such a value where SanitizeCapped
// returns it whole and unmarked, and the mark is honest either way: bytes
// really were dropped, before sanitizing.
//
// SanitizeSingleLineBudgeted is the strict twin. Neither serves a caller
// aggregating SEVERAL values under one shared budget — that is Budget itself,
// which is what keeps ONE truncation fact across the writes instead of one per
// value.
func SanitizeBudgeted(s string, n int, marker string) (text string, cut bool) {
	b := NewBudget(n, marker)
	b.Write(s)
	return b.Result()
}

// SanitizeSingleLineBudgeted is SanitizeBudgeted under the strict
// single-line policy: CR and LF become spaces along with every other unsafe
// rune, for a single-line sink where a raw newline forges a record boundary.
// The pre-cap, re-cap, marker and cut contract are identical; see
// SanitizeBudgeted for the full contract and for the one input class on which
// the pre-cap order diverges from SanitizeSingleLineCapped's.
func SanitizeSingleLineBudgeted(s string, n int, marker string) (text string, cut bool) {
	b := NewSingleLineBudget(n, marker)
	b.Write(s)
	return b.Result()
}
