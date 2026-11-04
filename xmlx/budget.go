package xmlx

import (
	"encoding/xml"
	"strings"
)

// noCopy makes a copy of the enclosing type a vet error. It has no behavior:
// the Lock/Unlock pair exists only so `go vet`'s copylocks check recognizes the
// type as one that must not be copied.
type noCopy struct{}

// Lock is a no-op required by the copylocks convention.
func (*noCopy) Lock() {}

// Unlock is a no-op required by the copylocks convention.
func (*noCopy) Unlock() {}

// Budget is the decode-time text accounting for ONE document: a per-field cap on
// any single decoded value, and a cumulative cap on everything the document
// retains. It is the half of this package that Preflight cannot cover, because
// raw bytes and decoded text are different quantities: entity references, CDATA
// seams, and repeated elements all break the correspondence, so a document can
// pass every lexical bound and still hand a schema decoder more text than the
// caller means to hold.
//
// What it bounds is what the caller BUILDS and KEEPS. The per-token allocation
// happens inside encoding/xml before any check here can run, which is precisely
// why Preflight exists: only a gate over the raw bytes bounds what the decoder
// must materialize. The two are complements, not alternatives.
//
// Use one Budget per document, threaded through the decoders that charge it.
// Create it with NewBudget or DefaultBudget: the caps are immutable afterwards,
// so a document cannot be part-way through its allowance when the allowance
// changes. Copying a Budget would fork its running total, leaving two copies each
// believing they hold the whole allowance, so a copy is a vet error. It is not
// safe for concurrent use; a document decodes on one goroutine.
//
// A Budget does NOT bound element count or nesting. A caller using Budget alone
// still lets encoding/xml skip unknown children through Decoder.Skip, which is
// iterative and unbounded, so the decoder's element stack still grows to the
// document's true depth. Only Preflight bounds that.
type Budget struct {
	_ noCopy

	maxField int
	maxTotal int
	total    int
}

// NewBudget returns a Budget bounding one document to maxFieldBytes per decoded
// value and maxTotalBytes of decoded text overall. Both must be positive: a
// non-positive cap is a configuration mistake (ErrInvalidLimits), never read as
// unbounded.
//
// Size maxTotalBytes from what the caller is willing to HOLD, which is usually
// far below the transport cap. A consumer parsing a catalogue-scale document
// sets it at that document's real ceiling; the DefaultBudget values are for a
// small structured document and will reject a multi-megabyte payload.
func NewBudget(maxFieldBytes, maxTotalBytes int) (*Budget, error) {
	if err := requirePositive("maxFieldBytes", maxFieldBytes); err != nil {
		return nil, err
	}
	if err := requirePositive("maxTotalBytes", maxTotalBytes); err != nil {
		return nil, err
	}
	return &Budget{maxField: maxFieldBytes, maxTotal: maxTotalBytes}, nil
}

// DefaultBudget returns the decode-time counterpart of DefaultLimits: 4 KiB per
// decoded value, 4 MiB of decoded text per document. The gap between this and
// DefaultLimits.MaxTextRunBytes is entity-expansion headroom (see that field).
func DefaultBudget() *Budget {
	return &Budget{maxField: 4 << 10, maxTotal: 4 << 20}
}

// MaxFieldBytes reports the per-value cap this Budget was built with.
func (b *Budget) MaxFieldBytes() int { return b.maxField }

// MaxTotalBytes reports the document-wide cap this Budget was built with.
func (b *Budget) MaxTotalBytes() int { return b.maxTotal }

// Total reports how many decoded bytes have been ADMITTED so far. A rejected
// value is not counted: a rejection leaves the budget exactly as it was, so a
// caller that treats one field as skippable is not silently drained.
func (b *Budget) Total() int { return b.total }

// Remaining reports how many decoded bytes the document may still charge.
func (b *Budget) Remaining() int { return b.maxTotal - b.total }

// Charge accounts one already-decoded value against both caps and reports
// whether it may be retained. It is what a decoder calls for a value
// encoding/xml has handed it whole, such as an attribute value off a
// StartElement.Attr, where the allocation has happened but retention has not.
//
// Call it BEFORE storing the value: a decoder that stores first and charges
// after has already kept the document it was about to reject.
//
// Every occurrence is charged, including repeats of the same element name. That
// is deliberate: a document that sends <title> ten thousand times overwrites one
// destination field but costs ten thousand decodes, so charging only the value
// that survives would leave that repetition unaccounted. Note the bound is on
// BYTES, so repetition of EMPTY values costs nothing here; bounding the number
// of elements is Limits.MaxElements's job, at the lexical gate.
//
// Charge each value exactly once. A value returned by DecodeText has already
// been charged.
func (b *Budget) Charge(s string) error {
	return b.charge(len(s))
}

// charge applies both caps to a value of n decoded bytes, mutating nothing on
// rejection.
func (b *Budget) charge(n int) error {
	if n > b.maxField {
		return limitErr(KindField, b.maxField)
	}
	if n > b.Remaining() {
		return limitErr(KindTotalText, b.maxTotal)
	}
	b.total += n
	return nil
}

// DecodeText reads the text content of the element the decoder has just entered,
// bounded and charged, and returns it. It replaces
// d.DecodeElement(&s, &start) for a plain text field.
//
// The difference that matters is WHEN the cap applies. DecodeElement
// concatenates every CharData token in the element and hands back the finished
// string, so a value split across CDATA seams, each chunk individually small
// enough to pass any lexical bound, is only measurable after it exists.
// DecodeText accumulates under the per-field cap AND under the document's
// remaining allowance, stopping at the token that would cross either, so the
// string it builds never exceeds what the caller would accept.
//
// Nested markup is skipped whole; comments and processing instructions are
// ignored, as DecodeElement ignores them.
//
// # Where the two stop agreeing, measured
//
// The swap is byte-identical to DecodeElement for every value inside
// encoding/xml's own acceptance set, and the fuzz oracle pins that. It is NOT
// identical outside it, in ONE respect, and the difference is a real acceptance-set
// divergence rather than a wording caveat.
//
// encoding/xml guards its unmarshal recursion by sampling the decoder's live
// open-element count on each entry into that recursion, against a fixed internal
// ceiling: 10000, and 5000 when GOARCH is wasm. DecodeElement enters that
// recursion, so it inherits the ceiling. Measured on go1.27.0 by entering an
// element at a known open depth: the two agree to open depth 9999, and from open
// depth 10000 DecodeElement refuses with an unexported, unwrapped
// errors.New("exceeded max depth") while DecodeText returns the value. So
// DecodeText is the LOOSER of the two above that depth.
//
// That is deliberate rather than an omission. DecodeText is iterative (Token plus
// Skip), so it carries none of the recursion the ceiling exists to bound, and
// nesting is [Preflight]'s bound in this package's split, not a Budget's: a
// Budget bounds bytes retained, never document shape. A caller that wants the
// decoder's ceiling enforced sets [Limits.MaxDepth] at or below it and runs
// Preflight, which rejects first and reports [KindDepth] wrapping [ErrLimit] --
// a classifiable error, which the stdlib's is not.
//
// On success the element's end tag is consumed, so the caller's token loop
// continues at the next sibling. On ANY error the document is over, and the
// caller must abandon it: a rejected value can leave the decoder part-way
// through the element, and a decoder error leaves it wherever encoding/xml
// stopped. There is no defined position to resume from.
func (b *Budget) DecodeText(d *xml.Decoder) (string, error) {
	var sb strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if err := b.appendText(&sb, t); err != nil {
				return "", err
			}
		case xml.StartElement:
			if err := d.Skip(); err != nil {
				return "", err
			}
		case xml.EndElement:
			s := sb.String()
			if err := b.charge(len(s)); err != nil {
				return "", err
			}
			return s, nil
		}
	}
}

// appendText adds one CharData chunk to the value under construction, refusing
// the chunk that would carry it past the per-value cap or past what is left of
// the document's allowance. Checking both here is what keeps the builder itself
// bounded: a value may not grow beyond what the document could still afford, even
// when the per-value cap alone would permit it.
func (b *Budget) appendText(sb *strings.Builder, chunk []byte) error {
	next := sb.Len() + len(chunk)
	if next > b.maxField {
		return limitErr(KindField, b.maxField)
	}
	if next > b.Remaining() {
		return limitErr(KindTotalText, b.maxTotal)
	}
	sb.Write(chunk)
	return nil
}
