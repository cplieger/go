package xmlx

// Limits are Preflight's lexical bounds over the raw document bytes. Every field
// must be positive; a non-positive value is a configuration mistake
// (ErrInvalidLimits), never an unbounded setting.
//
// Size these from the document CONTRACT, not from the transport cap. The question
// each bound answers is "what would a legitimate document from this endpoint
// never exceed?", and the useful answer is usually orders of magnitude below the
// byte cap: that gap is the amplification headroom the preflight removes.
type Limits struct {
	// MaxTextRunBytes caps CHARACTER DATA: one contiguous run of raw text, or
	// one CDATA section's content. Both are measured as content, which is what
	// the decoder hands back as a CharData token.
	//
	// This bounds RAW bytes, while a decoded-value cap (Budget) bounds resolved
	// text. The two are not interchangeable and the raw one must be the looser:
	// a reference expands to fewer bytes than it occupies, and the widest
	// predefined one is 6 to 1 (`&quot;` and `&apos;` are six raw bytes for one
	// decoded), so a run that would decode within a 4 KiB value cap can
	// legitimately arrive as tens of KiB of raw bytes. Setting this at or below
	// the decoded cap would reject valid documents.
	MaxTextRunBytes int
	// MaxTokenBytes caps one MARKUP TOKEN whole, delimiters included: a tag
	// (`<` through `>`), a comment (`<!--` through `-->`), or a processing
	// instruction (`<?` through `?>`). A start tag carrying MaxTagAttrs
	// attributes of the largest value the caller accepts should still fit with
	// margin.
	MaxTokenBytes int
	// MaxTagAttrs caps the XML attributes on ONE start tag.
	//
	// This is a LEXICAL bound on attributes written inside a tag
	// (`<enclosure url=".." length=".."/>` is two), which is a different
	// question from how many child ELEMENTS the caller's schema permits. The
	// latter is schema cardinality and belongs at the caller's decode site.
	//
	// Attributes are counted as `=` bytes outside a quoted value, which is
	// exact for well-formed XML (the grammar is Name Eq AttValue) and for
	// everything the DEFAULT decoder accepts. It is not exact for a decoder
	// with Strict disabled, which accepts a bare name as an attribute whose
	// value is its own name; such a tag carries attributes this bound does not
	// see. See Preflight's decoder-configuration note.
	MaxTagAttrs int
	// MaxDepth caps element nesting.
	//
	// This is the bound with the widest gap between wire size and cost: the
	// decoder pushes one heap-allocated entry per open element, and its
	// Decoder.Skip path (which every child the caller's schema does not model
	// goes through) has no depth bound at all, so a body of three-byte start
	// tags converts each 3 bytes of wire into a live stack entry for the whole
	// decode. Set it to the document's real shape (a syndication feed is about
	// 4 deep), not to a round number.
	//
	// It bounds the PEAK number of simultaneously open ELEMENTS, which is what
	// the decoder's stack holds. A self-closing element counts: encoding/xml
	// pushes `<e/>` and pops it on the synthesized end tag, so `<a><b/></a>`
	// reaches depth 2, not 1. The decoder additionally pushes one entry per
	// namespace declaration it meets, so the stack LENGTH is bounded by
	// MaxDepth * (1 + MaxTagAttrs) rather than by MaxDepth alone. Those
	// namespace entries do NOT feed the decoder's own depth guard below, which
	// counts start elements only.
	//
	// # The decoder's own ceiling, and where it overlaps this bound
	//
	// encoding/xml carries a fixed internal ceiling of its own on the same
	// quantity: 10000 open elements, and 5000 when GOARCH is wasm. It is not an
	// alternative to this bound, because the decoder SAMPLES it only on each
	// entry into its unmarshal recursion rather than tracking the document's
	// peak. Measured on go1.27.0: a document 349,525 elements deep under a
	// schema that models only the root is accepted, and Decoder.Skip walks it
	// unbounded, so a caller relying on the decoder's ceiling has no depth
	// bound at all for the part its schema ignores. That is the case this
	// bound exists for.
	//
	// Where the two do overlap is a schema whose own nesting follows the
	// document's, which is what makes the region of MaxDepth above the
	// decoder's ceiling unreachable for such a schema: measured at MaxDepth
	// 10001, a 10001-deep document passes Preflight and is then refused by
	// xml.Unmarshal with an unexported, unwrapped errors.New("exceeded max
	// depth") that no errors.Is can classify. So keep MaxDepth at or below
	// 10000 (5000 for a wasm build) whenever the schema is nested as deeply as
	// the document, and this package reports the rejection instead, as
	// KindDepth wrapping ErrLimit.
	MaxDepth int
	// MaxElements caps the total number of elements in the document.
	//
	// It is the bound for amplification by COUNT rather than by size: a body of
	// millions of three-byte elements passes every per-token bound above (each
	// token is tiny, the nesting is flat, there is no text at all) and still
	// expands into a decoded object graph many times the wire size. Depth
	// cannot catch it and neither can a text budget, because empty elements
	// carry no text to charge.
	//
	// This is a lexical count of start tags, not schema cardinality: it needs
	// no vocabulary from the caller, and unlike an "at most N <item>" rule it
	// protects a plain xml.Unmarshal consumer that has no custom decode site to
	// put such a rule in.
	MaxElements int
}

// DefaultLimits returns bounds sized for a small structured document, such as a
// syndication feed or an XML API response, where fields are short, elements carry
// a handful of attributes, and nesting is shallow.
//
// They are a starting point, not a recommendation. A caller that knows its
// document contract should tighten them, and one parsing genuinely large
// documents must raise MaxTextRunBytes, MaxDepth and MaxElements deliberately
// rather than discovering the rejection in production: a catalogue-scale dump of
// tens of thousands of records will exceed MaxElements, which is the point of the
// bound but not a surprise worth having at 3am.
func DefaultLimits() Limits {
	return Limits{
		MaxTextRunBytes: 64 << 10,
		MaxTokenBytes:   128 << 10,
		MaxTagAttrs:     16,
		MaxDepth:        64,
		MaxElements:     100_000,
	}
}

// validate reports the first non-positive bound.
func (l Limits) validate() error {
	for _, f := range []struct {
		name string
		n    int
	}{
		{"Limits.MaxTextRunBytes", l.MaxTextRunBytes},
		{"Limits.MaxTokenBytes", l.MaxTokenBytes},
		{"Limits.MaxTagAttrs", l.MaxTagAttrs},
		{"Limits.MaxDepth", l.MaxDepth},
		{"Limits.MaxElements", l.MaxElements},
	} {
		if err := requirePositive(f.name, f.n); err != nil {
			return err
		}
	}
	return nil
}
