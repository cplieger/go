package xmlx_test

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/go/xmlx"
)

// newBudget builds a Budget for a test, failing on a configuration mistake.
func newBudget(t *testing.T, maxField, maxTotal int) *xmlx.Budget {
	t.Helper()
	b, err := xmlx.NewBudget(xmlx.BudgetLimits{MaxFieldBytes: maxField, MaxTotalBytes: maxTotal})
	if err != nil {
		t.Fatalf("NewBudget(%d, %d): %v", maxField, maxTotal, err)
	}
	return b
}

// mustBudget builds a Budget outside a *testing.T (examples, fuzz bodies,
// properties), panicking on a configuration mistake because those callers pass
// constants.
func mustBudget(maxField, maxTotal int) *xmlx.Budget {
	b, err := xmlx.NewBudget(xmlx.BudgetLimits{MaxFieldBytes: maxField, MaxTotalBytes: maxTotal})
	if err != nil {
		panic(err)
	}
	return b
}

// decodeFirstElementText enters the document's root element and returns what
// b.DecodeText makes of its content, so a case states only the document and the
// budget.
func decodeFirstElementText(t *testing.T, doc string, b *xmlx.Budget) (string, error) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(doc))
	for {
		tok, err := d.Token()
		if err != nil {
			t.Fatalf("no start element in %q: %v", doc, err)
		}
		if _, ok := tok.(xml.StartElement); ok {
			return b.DecodeText(d)
		}
	}
}

// TestBudgetChargeEnforcesBothCaps pins the two independent bounds: one value too
// large, and many honest values summing past the document allowance. Without the
// second, a document amplifies by repetition while every individual field passes.
func TestBudgetChargeEnforcesBothCaps(t *testing.T) {
	t.Parallel()

	t.Run("per field", func(t *testing.T) {
		t.Parallel()
		b := newBudget(t, 8, 1000)
		if err := b.Charge("12345678"); err != nil {
			t.Errorf("a value exactly at the field cap = %v, want accepted", err)
		}
		err := b.Charge("123456789")
		le, ok := errors.AsType[*xmlx.LimitError](err)
		if !ok || le.Kind != xmlx.KindField {
			t.Fatalf("a value one byte over the field cap = %v, want KindField", err)
		}
		if le.Limit != 8 {
			t.Errorf("Limit = %d, want the configured 8", le.Limit)
		}
	})

	t.Run("cumulative", func(t *testing.T) {
		t.Parallel()
		b := newBudget(t, 8, 20)
		for i := range 2 {
			if err := b.Charge("12345678"); err != nil {
				t.Fatalf("charge %d = %v, want accepted (total %d/20)", i, err, b.Total())
			}
		}
		err := b.Charge("12345678")
		if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindTotalText {
			t.Errorf("the charge crossing the document cap = %v, want KindTotalText", err)
		}
	})
}

// TestBudgetChargeRejectsOverLargeFieldWithoutSpendingBudget pins that a rejected
// value is not charged: a decoder that fails the whole parse does not care, but
// one that treats an over-large field as skippable would otherwise have its
// document allowance drained by values it never kept.
func TestBudgetChargeRejectsOverLargeFieldWithoutSpendingBudget(t *testing.T) {
	t.Parallel()
	b := newBudget(t, 4, 1000)
	if err := b.Charge(strings.Repeat("x", 500)); err == nil {
		t.Fatal("an over-large field was accepted")
	}
	if b.Total() != 0 {
		t.Errorf("Total() = %d after a rejected field, want 0", b.Total())
	}
	if b.Remaining() != 1000 {
		t.Errorf("Remaining() = %d after a rejected field, want the full 1000", b.Remaining())
	}
}

// TestBudgetTotalAndRemainingTrackCharges pins the two accessors a consumer logs
// or asserts on, including that Remaining never reports negative once the cap is
// crossed.
func TestBudgetTotalAndRemainingTrackCharges(t *testing.T) {
	t.Parallel()
	b := newBudget(t, 10, 15)
	if b.Total() != 0 || b.Remaining() != 15 {
		t.Fatalf("fresh budget: Total=%d Remaining=%d, want 0 and 15", b.Total(), b.Remaining())
	}
	if err := b.Charge("1234567890"); err != nil {
		t.Fatalf("charge: %v", err)
	}
	if b.Total() != 10 || b.Remaining() != 5 {
		t.Errorf("after 10 bytes: Total=%d Remaining=%d, want 10 and 5", b.Total(), b.Remaining())
	}
	if err := b.Charge("1234567890"); err == nil {
		t.Fatal("the charge crossing the cap was accepted")
	}
	// A rejection leaves the budget exactly as it was: the value was never
	// admitted, so it must not have spent the document's allowance.
	if b.Total() != 10 || b.Remaining() != 5 {
		t.Errorf("after a rejected charge: Total=%d Remaining=%d, want the pre-charge 10 and 5",
			b.Total(), b.Remaining())
	}
	// The allowance that IS left is still spendable.
	if err := b.Charge("12345"); err != nil {
		t.Errorf("charging the exact remaining 5 bytes = %v, want accepted", err)
	}
	if b.Remaining() != 0 {
		t.Errorf("Remaining() = %d at the cap, want 0", b.Remaining())
	}
}

// TestBudgetDecodeTextParityWithDecodeElement is the oracle: for any value both
// accept, DecodeText must return byte-for-byte what DecodeElement would. The
// point of the primitive is WHEN the cap applies, never WHAT the text is, so any
// divergence here is a bug: it would mean adopting the budget silently changed
// a decoded value.
func TestBudgetDecodeTextParityWithDecodeElement(t *testing.T) {
	t.Parallel()
	docs := []string{
		`<a>plain</a>`,
		`<a></a>`,
		`<a/>`,
		`<a>  spaced  </a>`,
		`<a>Frieren &amp; Co &lt;S01&gt;</a>`,
		`<a><![CDATA[<not>markup</not>]]></a>`,
		`<a>before<![CDATA[ mid ]]>after</a>`,
		`<a>a&#65;b</a>`,
		"<a>line1\nline2</a>",
		`<a>outer<b>inner</b>tail</a>`,
		`<a><!--comment-->text</a>`,
		`<a><?pi data?>text</a>`,
	}
	for _, doc := range docs {
		t.Run(doc, func(t *testing.T) {
			t.Parallel()
			got, err := decodeFirstElementText(t, doc, newBudget(t, 1024, 1<<20))
			if err != nil {
				t.Fatalf("DecodeText(%q) = %v", doc, err)
			}

			var want string
			d := xml.NewDecoder(strings.NewReader(doc))
			for {
				tok, tokErr := d.Token()
				if tokErr != nil {
					t.Fatalf("no start element in %q", doc)
				}
				if start, ok := tok.(xml.StartElement); ok {
					if err := d.DecodeElement(&want, &start); err != nil {
						t.Fatalf("DecodeElement(%q) = %v", doc, err)
					}
					break
				}
			}
			if got != want {
				t.Errorf("DecodeText(%q) = %q, want DecodeElement parity %q", doc, got, want)
			}
		})
	}
}

// TestBudgetDecodeTextIsLooserThanDecodeElementAtTheDecoderCeiling is the
// boundary complement of TestBudgetDecodeTextParityWithDecodeElement: the ONE
// place the documented swap is not byte-identical.
//
// encoding/xml guards its unmarshal recursion by sampling the decoder's live
// open-element count on each entry, against a fixed internal ceiling (10000, and
// 5000 when GOARCH is wasm; introduced for CVE-2022-30633, rebuilt for
// CVE-2026-56859). DecodeElement enters that recursion and inherits the ceiling.
// DecodeText is iterative (Token plus Skip), so it does not, which makes it the
// looser of the two above that depth.
//
// The ceiling is unexported, so this test MEASURES it rather than asserting a
// number, and it therefore holds on wasm too. What it pins is the RELATIONSHIP:
// they agree at the last accepted depth, and one level deeper DecodeElement
// refuses while DecodeText still returns the value. If a later Go release moves
// or removes the ceiling, this fails and the package doc needs revisiting.
func TestBudgetDecodeTextIsLooserThanDecodeElementAtTheDecoderCeiling(t *testing.T) {
	t.Parallel()

	last := lastDepthDecodeElementAccepts(t)
	if last < 4000 {
		t.Fatalf("measured ceiling at %d open elements, implausibly low; the probe is wrong", last)
	}

	// At the last accepted depth the two agree, value included.
	textAt, errAt := decodeTextAtDepth(t, last)
	elemAt, elemErrAt := decodeElementAtDepth(t, last)
	if errAt != nil || elemErrAt != nil {
		t.Fatalf("at %d open elements: DecodeText = %v, DecodeElement = %v; want both accepted",
			last, errAt, elemErrAt)
	}
	if textAt != elemAt {
		t.Errorf("at %d open elements: DecodeText = %q, want DecodeElement parity %q", last, textAt, elemAt)
	}

	// One level deeper the decoder refuses and DecodeText does not.
	textOver, errOver := decodeTextAtDepth(t, last+1)
	_, elemErrOver := decodeElementAtDepth(t, last+1)
	if elemErrOver == nil {
		t.Fatalf("at %d open elements DecodeElement accepted; the measured ceiling %d is wrong", last+1, last)
	}
	if errOver != nil {
		t.Errorf("at %d open elements DecodeText = %v, want the value (it carries no recursion to bound)",
			last+1, errOver)
	}
	if textOver != elemAt {
		t.Errorf("at %d open elements DecodeText = %q, want the same value %q it returns below the ceiling",
			last+1, textOver, elemAt)
	}

	// The stdlib rejection is unclassifiable, which is why Preflight's KindDepth
	// is the bound a caller should rely on.
	if _, ok := errors.AsType[*xmlx.LimitError](elemErrOver); ok {
		t.Errorf("DecodeElement rejection = %v, unexpectedly a *xmlx.LimitError", elemErrOver)
	}
	if errors.Unwrap(elemErrOver) != nil {
		t.Errorf("DecodeElement rejection %v unwraps to %v, want an unwrapped error",
			elemErrOver, errors.Unwrap(elemErrOver))
	}
}

// lastDepthDecodeElementAccepts binary-searches the greatest open-element count
// at which DecodeElement still reads a value, which is encoding/xml's unexported
// ceiling. Measuring it keeps this suite honest on any GOARCH.
func lastDepthDecodeElementAccepts(t *testing.T) int {
	t.Helper()
	lo, hi := 1, 20001 // lo accepts, hi must not
	if _, err := decodeElementAtDepth(t, hi); err == nil {
		t.Fatalf("DecodeElement accepted %d open elements; encoding/xml has no reachable ceiling", hi)
	}
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if _, err := decodeElementAtDepth(t, mid); err == nil {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// deepTextDoc wraps a one-character text element in wrappers-1 further elements,
// so entering the innermost element leaves the decoder holding exactly `wrappers`
// open elements.
func deepTextDoc(wrappers int) string {
	var sb strings.Builder
	sb.WriteString(strings.Repeat("<a>", wrappers-1))
	sb.WriteString("<v>t</v>")
	sb.WriteString(strings.Repeat("</a>", wrappers-1))
	return sb.String()
}

// enterDeepestElement advances a decoder over deepTextDoc(wrappers) to the
// innermost start element and returns it, so the decoder holds `wrappers` open
// elements when the caller reads the value.
func enterDeepestElement(t *testing.T, wrappers int) (*xml.Decoder, xml.StartElement) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(deepTextDoc(wrappers)))
	for seen := 0; ; {
		tok, err := d.Token()
		if err != nil {
			t.Fatalf("walking to depth %d: %v", wrappers, err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			seen++
			if seen == wrappers {
				return d, start
			}
		}
	}
}

// decodeTextAtDepth reads the innermost value with Budget.DecodeText.
func decodeTextAtDepth(t *testing.T, wrappers int) (string, error) {
	t.Helper()
	d, _ := enterDeepestElement(t, wrappers)
	return newBudget(t, 1024, 1<<20).DecodeText(d)
}

// decodeElementAtDepth reads the innermost value with encoding/xml's own
// DecodeElement, the primitive DecodeText documents itself against.
func decodeElementAtDepth(t *testing.T, wrappers int) (string, error) {
	t.Helper()
	d, start := enterDeepestElement(t, wrappers)
	var s string
	err := d.DecodeElement(&s, &start)
	return s, err
}

// TestBudgetDecodeTextRejectsAValueSplitAcrossTokens is the case that justifies
// the primitive over DecodeElement. Each chunk is small, every one would pass a
// lexical text-run bound, but their concatenation is not, and DecodeElement can
// only report that after building the whole string. DecodeText stops at the token
// that would cross the cap.
func TestBudgetDecodeTextRejectsAValueSplitAcrossTokens(t *testing.T) {
	t.Parallel()
	const chunk = 16
	const chunks = 40
	var sb strings.Builder
	sb.WriteString("<a>")
	for range chunks {
		sb.WriteString("<![CDATA[" + strings.Repeat("x", chunk) + "]]>")
	}
	sb.WriteString("</a>")
	doc := sb.String()

	// The budget allows any single chunk but not the concatenation.
	b := newBudget(t, chunk*4, 1<<20)
	_, err := decodeFirstElementText(t, doc, b)
	if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindField {
		t.Fatalf("a %d-chunk split value = %v, want KindField", chunks, err)
	}

	// DecodeElement is the contrast: it accepts the same document, which is why
	// the caller cannot just use it and check the length afterwards.
	var whole string
	d := xml.NewDecoder(strings.NewReader(doc))
	tok, tokErr := d.Token()
	if tokErr != nil {
		t.Fatalf("token: %v", tokErr)
	}
	start, ok := tok.(xml.StartElement)
	if !ok {
		t.Fatalf("first token %T, want a StartElement", tok)
	}
	if err := d.DecodeElement(&whole, &start); err != nil {
		t.Fatalf("DecodeElement = %v; the contrast case is stale", err)
	}
	if len(whole) != chunk*chunks {
		t.Errorf("DecodeElement built %d bytes, want %d; the contrast case is stale", len(whole), chunk*chunks)
	}
}

// TestBudgetDecodeTextChargesEveryOccurrence pins that a repeated element costs
// budget every time. Only the last occurrence survives in the caller's field, so
// charging just that one would leave repetition unbounded, thousands of decodes
// for one retained value.
func TestBudgetDecodeTextChargesEveryOccurrence(t *testing.T) {
	t.Parallel()
	b := newBudget(t, 8, 20)
	doc := `<r><a>12345678</a><a>12345678</a><a>12345678</a></r>`
	d := xml.NewDecoder(strings.NewReader(doc))

	var lastErr error
	charged := 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "a" {
			continue
		}
		if _, lastErr = b.DecodeText(d); lastErr != nil {
			break
		}
		charged++
	}
	if charged != 2 {
		t.Errorf("charged %d occurrences before the cap, want 2 (8+8 fits in 20, the third does not)", charged)
	}
	if le, ok := errors.AsType[*xmlx.LimitError](lastErr); !ok || le.Kind != xmlx.KindTotalText {
		t.Errorf("third occurrence = %v, want KindTotalText", lastErr)
	}
}

// TestBudgetDecodeTextSkipsNestedMarkupWhole pins that a nested element is
// consumed, not left for the caller's loop to trip over, and that its text does
// not leak into the value. It matches DecodeElement's behavior for a plain-text
// destination inside encoding/xml's acceptance set, which is what makes the swap
// safe. The one place the two stop agreeing is the decoder's own depth ceiling,
// pinned by TestBudgetDecodeTextIsLooserThanDecodeElementAtTheDecoderCeiling.
func TestBudgetDecodeTextSkipsNestedMarkupWhole(t *testing.T) {
	t.Parallel()
	b := newBudget(t, 1024, 1<<20)
	d := xml.NewDecoder(strings.NewReader(`<r><a>keep<b><c>drop</c></b>tail</a><next/></r>`))
	// Enter <r>, then <a>.
	for range 2 {
		for {
			tok, err := d.Token()
			if err != nil {
				t.Fatalf("token: %v", err)
			}
			if _, ok := tok.(xml.StartElement); ok {
				break
			}
		}
	}
	got, err := b.DecodeText(d)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if got != "keeptail" {
		t.Errorf("DecodeText = %q, want %q (nested text dropped)", got, "keeptail")
	}
	// The next token must be <next/>: </a> was consumed by DecodeText.
	for {
		tok, tokErr := d.Token()
		if tokErr != nil {
			t.Fatalf("after DecodeText the decoder is not positioned at the sibling: %v", tokErr)
		}
		if start, ok := tok.(xml.StartElement); ok {
			if start.Name.Local != "next" {
				t.Errorf("next start element = %q, want next", start.Name.Local)
			}
			return
		}
	}
}

// TestNewBudgetRefusesNegativeCaps pins the fail-closed configuration
// contract: a negative cap is a caller mistake, so the constructor refuses
// rather than handing back something that bounds nothing. The field name is in
// the error because a caller staring at one is looking for which cap they got
// wrong.
func TestNewBudgetRefusesNegativeCaps(t *testing.T) {
	t.Parallel()
	tests := map[string]xmlx.BudgetLimits{
		"BudgetLimits.MaxFieldBytes": {MaxFieldBytes: -1, MaxTotalBytes: 10},
		"BudgetLimits.MaxTotalBytes": {MaxFieldBytes: 10, MaxTotalBytes: -1},
	}
	for param, lim := range tests {
		t.Run(param, func(t *testing.T) {
			t.Parallel()
			b, err := xmlx.NewBudget(lim)
			if b != nil {
				t.Error("NewBudget returned a Budget alongside an error")
			}
			if !errors.Is(err, xmlx.ErrInvalidLimits) {
				t.Fatalf("NewBudget(%+v) = %v, want ErrInvalidLimits", lim, err)
			}
			if errors.Is(err, xmlx.ErrLimit) {
				t.Error("a configuration mistake reported as ErrLimit: the caller would blame the document")
			}
			if ce, ok := errors.AsType[*xmlx.ConfigError](err); !ok || ce.Field != param {
				t.Errorf("error = %v, want a *ConfigError naming %s", err, param)
			}
		})
	}
}

// TestDefaultBudgetPairsWithDefaultLimits pins the documented relationship
// between the two presets: the raw text bound must be LOOSER than the decoded
// field cap, because entity escaping only ever expands text. If a future edit
// inverted them, every entity-heavy field that decoded legally would be rejected
// at the lexical gate instead.
func TestDefaultBudgetPairsWithDefaultLimits(t *testing.T) {
	t.Parallel()
	lim := xmlx.DefaultLimits()
	b := xmlx.DefaultBudget()
	if lim.MaxTextRunBytes <= b.MaxFieldBytes() {
		t.Errorf("DefaultLimits.MaxTextRunBytes (%d) <= DefaultBudget.MaxFieldBytes (%d): no entity-expansion headroom",
			lim.MaxTextRunBytes, b.MaxFieldBytes())
	}
	if b.MaxTotalBytes() < b.MaxFieldBytes() {
		t.Errorf("DefaultBudget.MaxTotalBytes (%d) < MaxFieldBytes (%d): no field could ever be charged",
			b.MaxTotalBytes(), b.MaxFieldBytes())
	}
	if err := xmlx.Preflight([]byte(`<a>x</a>`), lim); err != nil {
		t.Errorf("DefaultLimits rejects a trivial document: %v", err)
	}
	if err := b.Charge("x"); err != nil {
		t.Errorf("DefaultBudget rejects a trivial field: %v", err)
	}
}

// TestBudgetDecodeTextPropagatesDecoderErrors pins that a genuine parse failure
// surfaces as the decoder's error, not as a bounds rejection: the two mean
// different things to an operator, and the budget must not mask a malformed
// document as an oversized one.
func TestBudgetDecodeTextPropagatesDecoderErrors(t *testing.T) {
	t.Parallel()
	b := newBudget(t, 1024, 1<<20)
	_, err := decodeFirstElementText(t, `<a>unterminated`, b)
	if err == nil {
		t.Fatal("DecodeText on a truncated element = nil error")
	}
	if errors.Is(err, xmlx.ErrLimit) || errors.Is(err, xmlx.ErrInvalidLimits) {
		t.Errorf("DecodeText on a truncated element = %v, want the decoder's own error", err)
	}
}

// TestBudgetDecodeTextBoundedByRemainingAllowance pins the interaction between the
// two caps while a value is BUILDING. Without it the per-value cap alone governs
// the builder, so a document with one byte of allowance left could still make the
// caller construct a full 4 KiB string before the inevitable rejection: the
// cumulative cap would bound what is kept and not what is built.
func TestBudgetDecodeTextBoundedByRemainingAllowance(t *testing.T) {
	t.Parallel()
	// Field cap is generous; the document allowance is nearly spent.
	b := newBudget(t, 1024, 10)
	if err := b.Charge("123456789"); err != nil {
		t.Fatalf("seeding the budget: %v", err)
	}
	if b.Remaining() != 1 {
		t.Fatalf("Remaining() = %d after seeding, want 1", b.Remaining())
	}

	_, err := decodeFirstElementText(t, `<a>`+strings.Repeat("x", 500)+`</a>`, b)
	le, ok := errors.AsType[*xmlx.LimitError](err)
	if !ok {
		t.Fatalf("DecodeText = %v, want a *LimitError", err)
	}
	if le.Kind != xmlx.KindTotalText {
		t.Errorf("Kind = %v, want KindTotalText (the document allowance bound, not the field cap)", le.Kind)
	}
	if b.Total() != 9 {
		t.Errorf("Total() = %d after the rejection, want the pre-charge 9", b.Total())
	}
	// A value that fits the remaining byte is still accepted.
	if got, err := decodeFirstElementText(t, `<a>x</a>`, b); err != nil || got != "x" {
		t.Errorf("DecodeText of the last affordable byte = %q, %v; want \"x\", nil", got, err)
	}
}

// TestBudgetDecodeTextPropagatesSkipErrors pins that a failure inside a nested
// element the value skips surfaces as the decoder's error, not as a bounds
// rejection. The nested subtree is not part of the value, so a caller must not
// read its malformation as an oversized field.
func TestBudgetDecodeTextPropagatesSkipErrors(t *testing.T) {
	t.Parallel()
	b := newBudget(t, 1024, 1<<20)
	_, err := decodeFirstElementText(t, `<a>text<b><c></b></a>`, b)
	if err == nil {
		t.Fatal("DecodeText over a malformed nested element = nil error")
	}
	if errors.Is(err, xmlx.ErrLimit) || errors.Is(err, xmlx.ErrInvalidLimits) {
		t.Errorf("DecodeText = %v, want the decoder's own error", err)
	}
}

// TestBudgetDecodeTextStopsAtTheChunkThatCrossesACap pins WHICH cap a split
// value's rejection names. The value is refused at the chunk that would carry it
// past a cap, so the error names the cap the accumulation crossed, and that is
// not always the cap the assembled value would have crossed: here the first chunk
// already outruns what the document can still afford, while the two chunks
// together also outrun the per-field cap. A builder that assembled the whole
// value first and checked afterwards would hold every byte of it in memory and
// then blame the field cap for a document that ran out of allowance at its first
// chunk, which is the diagnosis a caller sizes its next request from.
func TestBudgetDecodeTextStopsAtTheChunkThatCrossesACap(t *testing.T) {
	t.Parallel()
	const maxField = 16
	const maxTotal = 4
	b := newBudget(t, maxField, maxTotal)

	doc := `<a>` + strings.Repeat("x", 8) + `<![CDATA[` + strings.Repeat("y", maxField) + `]]></a>`
	_, err := decodeFirstElementText(t, doc, b)
	le, ok := errors.AsType[*xmlx.LimitError](err)
	if !ok {
		t.Fatalf("DecodeText = %v, want a *LimitError", err)
	}
	if le.Kind != xmlx.KindTotalText {
		t.Errorf("Kind = %v, want KindTotalText: the first chunk already outran the document allowance", le.Kind)
	}
	if le.Limit != maxTotal {
		t.Errorf("Limit = %d, want the configured maxTotalBytes %d", le.Limit, maxTotal)
	}
	if b.Total() != 0 {
		t.Errorf("Total() = %d after the rejection, want 0: a refused value charges nothing", b.Total())
	}
}

// TestNewBudgetZeroCapTakesItsDefault pins that a zero cap is bounded: it
// takes DefaultBudget's value, never unbounded, so a BudgetLimits literal that
// sets one cap keeps the other bounded.
func TestNewBudgetZeroCapTakesItsDefault(t *testing.T) {
	t.Parallel()
	def := xmlx.DefaultBudget()
	for _, tc := range []struct {
		lim                  xmlx.BudgetLimits
		wantField, wantTotal int
	}{
		{xmlx.BudgetLimits{}, def.MaxFieldBytes(), def.MaxTotalBytes()},
		{xmlx.BudgetLimits{MaxFieldBytes: 7}, 7, def.MaxTotalBytes()},
		{xmlx.BudgetLimits{MaxTotalBytes: 9}, def.MaxFieldBytes(), 9},
	} {
		b, err := xmlx.NewBudget(tc.lim)
		if err != nil {
			t.Fatalf("NewBudget(%+v) = %v, want a Budget", tc.lim, err)
		}
		if b.MaxFieldBytes() != tc.wantField || b.MaxTotalBytes() != tc.wantTotal {
			t.Errorf("NewBudget(%+v) caps = (%d, %d), want (%d, %d)",
				tc.lim, b.MaxFieldBytes(), b.MaxTotalBytes(), tc.wantField, tc.wantTotal)
		}
	}
}

// TestZeroBudgetRejectsEveryNonEmptyCharge pins the zero Budget's documented
// behaviour: it has no allowance, so it is never mistaken for a working one.
func TestZeroBudgetRejectsEveryNonEmptyCharge(t *testing.T) {
	t.Parallel()
	var b xmlx.Budget
	if err := b.Charge("x"); !errors.Is(err, xmlx.ErrLimit) {
		t.Errorf("zero Budget Charge(%q) = %v, want ErrLimit", "x", err)
	}
	if err := b.Charge(""); err != nil {
		t.Errorf("zero Budget Charge(\"\") = %v, want nil", err)
	}
}
