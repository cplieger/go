package xmlx

import (
	"bytes"
	"math"
)

// Raw markup delimiters the scan dispatches on.
var (
	cdataOpen     = []byte("<![CDATA[")
	cdataClose    = []byte("]]>")
	commentOpen   = []byte("<!--")
	commentClose  = []byte("-->")
	piOpen        = []byte("<?")
	piClose       = []byte("?>")
	directiveOpen = []byte("<!")
)

// Preflight walks the raw bytes of an XML document and rejects one that is
// already outside lim, BEFORE any decoder allocates a token from it. It is a
// single allocation-free pass: one sequential scan, no buffering, no copies.
//
// It rejects an overlong raw text or CDATA run, an overlong markup token, a start
// tag carrying more than lim.MaxTagAttrs XML attributes, more than
// lim.MaxElements elements, element nesting whose PEAK exceeds lim.MaxDepth (a
// self-closing element counts as one level), and any XML directive (see the
// package doc). Each rejection is a *LimitError naming the bound and the byte
// offset where it was detected, and every one wraps ErrLimit.
//
// Preflight is a bounds check, not a validator. It reads the document's surface
// structure only, enough to know where one token ends and the next begins, so a
// malformed body that stays within the bounds passes through for the decoder to
// reject with its own parse error. The converse does not hold: malformed input
// can also trip a bound (a run of `=` bytes reads as attributes; a truncated
// `<!` opener is refused with the directive class), so a rejection does not prove
// the document was oversized, only that it was outside the contract.
//
// # Decoder configuration
//
// The bounds model the DEFAULT decoder: Strict enabled, AutoClose empty, no
// caller-supplied Entity map, no CharsetReader. Each of those knobs changes what
// the bytes mean. With Strict disabled a bare attribute name becomes an attribute
// this scan does not count; an Entity map can expand a short reference into an
// arbitrarily long value after the raw bound has passed; a CharsetReader
// transforms the very bytes that were scanned. A caller that sets any of them
// keeps the token and depth bounds but should treat the attribute and text bounds
// as advisory.
//
// body is not modified or retained.
func Preflight(body []byte, lim Limits) error {
	if err := lim.validate(); err != nil {
		return err
	}
	scan := docScan{lim: lim}
	for i := 0; i < len(body); {
		if body[i] != '<' {
			run := textRunLen(body[i:])
			if run > lim.MaxTextRunBytes {
				return at(limitErr(KindTextRun, lim.MaxTextRunBytes), i)
			}
			i += run
			continue
		}
		n, err := scan.markupToken(body[i:])
		if err != nil {
			return at(err, i)
		}
		i += n
	}
	return nil
}

// docScan carries the document-wide accounting across tokens: the caller's
// bounds, the current nesting depth, and the running element count.
type docScan struct {
	lim      Limits
	depth    int
	elements int
}

// textRunLen returns the length of the character-data run at the head of body:
// everything up to the next '<', or the whole remainder when no markup follows.
func textRunLen(body []byte) int {
	if n := bytes.IndexByte(body, '<'); n >= 0 {
		return n
	}
	return len(body)
}

// markupToken bounds one markup token starting at body[0] == '<' and applies its
// effect on the document-wide accounting, returning how many bytes it spans.
//
// Close-delimited forms (CDATA, comments, processing instructions) are scanned to
// their closing delimiter within their bound; every remaining `<!` form is a
// directive and is rejected; anything else is a tag scanned to its unquoted '>'.
func (s *docScan) markupToken(body []byte) (int, error) {
	switch {
	case bytes.HasPrefix(body, cdataOpen):
		// A CDATA section is character data, so it answers to the text bound,
		// measured as CONTENT: the content is exactly what the decoder returns
		// as a CharData token.
		return delimited(body, len(cdataOpen), cdataClose,
			s.lim.MaxTextRunBytes, s.lim.MaxTextRunBytes, KindCDATA)
	case bytes.HasPrefix(body, commentOpen):
		return delimitedToken(body, len(commentOpen), commentClose, s.lim.MaxTokenBytes, KindComment)
	case bytes.HasPrefix(body, piOpen):
		return delimitedToken(body, len(piOpen), piClose, s.lim.MaxTokenBytes, KindProcInst)
	case bytes.HasPrefix(body, directiveOpen):
		// Rejected as a class, not bounded. encoding/xml accumulates a directive
		// until a '>' at nesting depth zero, tracking nested '<'/'>' pairs,
		// quoted strings and nested comments; a scan that stopped at the first
		// unquoted '>' would report a short token where the decoder retains one
		// as large as the whole body, so a directive stuffed with balanced
		// `<a></a>` fragments would read as many short shallow tags here while
		// the decoder held megabytes. The objection is not that reproducing that
		// tokenizer is laborious; it is that a silent divergence in a copied
		// tokenizer is unfalsifiable at the call site, while a refusal is
		// falsifiable immediately. A truncated `<!` opener lands here too, since
		// the only token it could complete into is a directive.
		return 0, limitErr(KindDirective, 0)
	default:
		return s.tag(body)
	}
}

// tag scans one tag from body[0] == '<' to its unquoted '>' (a '>' inside a
// quoted attribute value never terminates the tag), bounding the whole token at
// MaxTokenBytes and, on a start tag, counting unquoted '=' bytes against
// MaxTagAttrs. On termination it applies the tag's effect on depth and the
// element count. An unterminated tail within the token bound is left for the
// decoder to reject; it opens nothing, since nothing follows it.
func (s *docScan) tag(body []byte) (int, error) {
	countAttrs := len(body) < 2 || body[1] != '/'
	limit := min(len(body), s.lim.MaxTokenBytes)
	scan := tagScan{countAttrs: countAttrs, maxAttrs: s.lim.MaxTagAttrs}
	for i := 1; i < limit; i++ {
		terminated, err := scan.consume(body[i])
		if err != nil {
			return 0, err
		}
		if terminated {
			if err := s.account(tagDepthDelta(body, i)); err != nil {
				return 0, err
			}
			return i + 1, nil
		}
	}
	if len(body) > s.lim.MaxTokenBytes {
		return 0, limitErr(KindToken, s.lim.MaxTokenBytes)
	}
	return len(body), nil
}

// account applies one tag's effect on the document-wide nesting depth and element
// count.
//
// Depth is checked on the level the token OPENS, not on the depth it leaves
// behind, because the quantity being bounded is the decoder's open-element stack
// at its peak. A self-closing element is the case that distinguishes the two:
// encoding/xml returns a StartElement for `<e/>` and pushes it, then synthesizes
// the matching EndElement and pops it, so it really does occupy a stack entry.
// Transiently, but the peak is what a memory bound is about, and the decoder
// recycles popped entries through a free list, so live cost tracks peak depth
// rather than element count. Tracking only the net delta would let a document
// reach MaxDepth+1 through a self-closing leaf.
//
// A depth that would go negative clamps to zero: stray end tags are a
// well-formedness question, left for the decoder.
func (s *docScan) account(delta int, opens bool) error {
	if !opens {
		s.depth = max(s.depth+delta, 0)
		return nil
	}
	s.elements++
	if s.elements > s.lim.MaxElements {
		return limitErr(KindElements, s.lim.MaxElements)
	}
	if s.depth+1 > s.lim.MaxDepth {
		return limitErr(KindDepth, s.lim.MaxDepth)
	}
	s.depth += delta
	return nil
}

// delimitedToken bounds one close-delimited token whose WHOLE lexical span
// answers to maxToken, delimiters included, so the configured number means the
// same thing here as it does for a tag.
func delimitedToken(body []byte, openLen int, closeDelim []byte, maxToken int, kind Kind) (int, error) {
	maxContent := maxToken - openLen - len(closeDelim)
	if maxContent < 0 {
		// The bound is smaller than the token's own delimiters, so no token of
		// this class can fit.
		return 0, limitErr(kind, maxToken)
	}
	return delimited(body, openLen, closeDelim, maxContent, maxToken, kind)
}

// delimited scans one close-delimited token: its content must end within
// maxContent bytes. An unterminated token whose remaining bytes are themselves
// within the bound is left for the decoder to reject. reportedLimit is the bound
// the CALLER configured, which is what a rejection names (it differs from
// maxContent when the bound covers the delimiters too).
func delimited(body []byte, openLen int, closeDelim []byte, maxContent, reportedLimit int, kind Kind) (int, error) {
	window := body[openLen:]
	bound := min(len(window), saturateAdd(maxContent, len(closeDelim)))
	if j := bytes.Index(window[:bound], closeDelim); j >= 0 {
		return openLen + j + len(closeDelim), nil
	}
	if len(window) > bound {
		return 0, limitErr(kind, reportedLimit)
	}
	return len(body), nil
}

// saturateAdd adds two non-negative ints, clamping at math.MaxInt instead of
// wrapping. A caller may legitimately set a bound to the largest int it can
// express ("effectively unbounded"), and the scan adds a small delimiter length
// to it; wrapping there would turn the loosest possible bound into a negative one
// and reject every document.
func saturateAdd(a, b int) int {
	if a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}

// tagScan carries tag's byte-loop state: the open quote character (0 outside a
// quoted attribute value), the running unquoted '=' count and its cap, and
// whether this token is a start tag whose attributes are counted (an end tag's
// are not).
type tagScan struct {
	attrs      int
	maxAttrs   int
	quote      byte
	countAttrs bool
}

// consume advances the scan by one byte, reporting whether that byte terminated
// the tag (an unquoted '>'). A '>' inside a quoted attribute value never
// terminates, and a start tag carrying more than maxAttrs unquoted '=' bytes
// (exactly one per XML attribute in the grammar the default decoder enforces)
// fails closed.
func (s *tagScan) consume(c byte) (terminated bool, err error) {
	switch {
	case s.quote != 0:
		if c == s.quote {
			s.quote = 0
		}
	case c == '"' || c == '\'':
		s.quote = c
	case c == '=' && s.countAttrs:
		s.attrs++
		if s.attrs > s.maxAttrs {
			return false, limitErr(KindTagAttrs, s.maxAttrs)
		}
	case c == '>':
		return true, nil
	}
	return false, nil
}

// tagDepthDelta classifies the nesting effect of one tag whose terminating
// unquoted '>' sits at body[end]. It returns the net depth delta and whether the
// tag OPENS a level:
//
//   - an end tag (</...>) closes one: delta -1, opens false;
//   - a self-closing start tag (.../>; the byte before the terminating '>' is
//     '/', necessarily unquoted since the '>' itself is) leaves the depth
//     unchanged but does occupy a level while open: delta 0, opens true;
//   - any other start tag opens one and keeps it: delta +1, opens true.
//
// The scan already visits both classification bytes, so this costs two
// byte-compares. No `<!`-prefixed token reaches here: markupToken routes CDATA
// and comments to delimited and rejects every remaining directive.
func tagDepthDelta(body []byte, end int) (delta int, opens bool) {
	switch {
	case body[1] == '/':
		return -1, false
	case body[end-1] == '/':
		return 0, true
	default:
		return 1, true
	}
}
