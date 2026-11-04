package xmlx_test

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/go/xmlx"
	"pgregory.net/rapid"
)

// xmlBytesGen draws byte strings from the alphabet that makes XML structure
// decisions: the markup delimiters, the quote characters, the '=' the attribute
// counter keys on, plus a little ordinary text. Uniform random bytes almost never
// produce a `<![CDATA[` or a quoted attribute, so an unbiased generator would
// spend its whole budget on the plain-text branch.
func xmlBytesGen() *rapid.Generator[string] {
	pieces := []string{
		"<", ">", "/", "!", "?", "-", "[", "]", "=", `"`, "'", " ", "a", "&amp;",
		"<a>", "</a>", "<a/>", `<a p="v">`, "<!--", "-->", "<![CDATA[", "]]>", "<?x", "?>", "<!D",
	}
	return rapid.Custom(func(t *rapid.T) string {
		return strings.Join(rapid.SliceOfN(rapid.SampledFrom(pieces), 0, 40).Draw(t, "pieces"), "")
	})
}

// limitsGen draws Limits across the range where bounds actually bite on the
// documents xmlBytesGen produces.
func limitsGen() *rapid.Generator[xmlx.Limits] {
	return rapid.Custom(func(t *rapid.T) xmlx.Limits {
		return xmlx.Limits{
			MaxTextRunBytes: rapid.IntRange(1, 200).Draw(t, "MaxTextRunBytes"),
			MaxTokenBytes:   rapid.IntRange(1, 200).Draw(t, "MaxTokenBytes"),
			MaxTagAttrs:     rapid.IntRange(1, 8).Draw(t, "MaxTagAttrs"),
			MaxDepth:        rapid.IntRange(1, 12).Draw(t, "MaxDepth"),
			MaxElements:     rapid.IntRange(1, 40).Draw(t, "MaxElements"),
		}
	})
}

// TestPropPreflightMonotoneInLimits is the every-PR property behind the whole
// configuration surface (the weekly fuzz corpus does not persist, so rapid is the
// durable net): loosening a bound can only ever ACCEPT more, and tightening one
// can only ever REJECT more. Any non-monotone bound would mean a caller cannot
// reason about its own limits at all, raising a number to admit a document could
// start rejecting a different one.
func TestPropPreflightMonotoneInLimits(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		body := []byte(xmlBytesGen().Draw(rt, "body"))
		tight := limitsGen().Draw(rt, "tight")
		loose := xmlx.Limits{
			MaxTextRunBytes: tight.MaxTextRunBytes + rapid.IntRange(0, 500).Draw(rt, "dText"),
			MaxTokenBytes:   tight.MaxTokenBytes + rapid.IntRange(0, 500).Draw(rt, "dToken"),
			MaxTagAttrs:     tight.MaxTagAttrs + rapid.IntRange(0, 20).Draw(rt, "dAttrs"),
			MaxDepth:        tight.MaxDepth + rapid.IntRange(0, 50).Draw(rt, "dDepth"),
			MaxElements:     tight.MaxElements + rapid.IntRange(0, 100).Draw(rt, "dElements"),
		}
		if xmlx.Preflight(body, tight) == nil && xmlx.Preflight(body, loose) != nil {
			rt.Fatalf("accepted under tight limits %+v but rejected under looser %+v: body %q",
				tight, loose, body)
		}
	})
}

// TestPropPreflightRejectsOnlyDirectivesWhenUnbounded is the containment law: with
// every numeric bound out of reach, no numeric bound can fire, so the only
// rejection left is the directive class. It pins that the scan has no hidden
// rejection path and that a maximal bound does not wrap when the scan adds a
// delimiter length to it.
func TestPropPreflightRejectsOnlyDirectivesWhenUnbounded(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		body := []byte(xmlBytesGen().Draw(rt, "body"))
		err := xmlx.Preflight(body, hugeLimits())
		if err == nil {
			return
		}
		if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindDirective {
			rt.Fatalf("under unbounded limits body %q = %v, want at most a KindDirective", body, err)
		}
	})
}

// TestPropPreflightIsDeterministicAndTotal pins that the scan terminates on
// arbitrary structural input, never panics, and returns the same verdict twice -
// the baseline a gate on the request path needs, since a panic here is a 500 on
// untrusted input and a nondeterministic verdict is an unreproducible outage.
func TestPropPreflightIsDeterministicAndTotal(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		body := []byte(xmlBytesGen().Draw(rt, "body"))
		lim := limitsGen().Draw(rt, "limits")
		first := xmlx.Preflight(body, lim)
		second := xmlx.Preflight(body, lim)
		if (first == nil) != (second == nil) {
			rt.Fatalf("nondeterministic on %q: %v then %v", body, first, second)
		}
	})
}

// TestPropPreflightDepthMatchesTheDecoder is the external oracle for the bound
// with the widest wire-cost gap. On a WELL-FORMED document (built here, so its
// true nesting depth is known independently) Preflight must reject exactly when
// that depth exceeds MaxDepth, one off-by-one in either direction is either a
// rejected legitimate document or an unbounded element stack.
func TestPropPreflightDepthMatchesTheDecoder(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		depth := rapid.IntRange(1, 20).Draw(rt, "depth")
		maxDepth := rapid.IntRange(1, 20).Draw(rt, "maxDepth")
		selfClosingLeaf := rapid.Bool().Draw(rt, "selfClosingLeaf")

		doc := nestedDoc(depth, selfClosingLeaf)
		if got := decoderMaxDepth(rt, doc); got != depth {
			rt.Fatalf("generated document %q has decoder depth %d, want %d", doc, got, depth)
		}

		lim := xmlx.Limits{
			MaxTextRunBytes: 1 << 20,
			MaxTokenBytes:   1 << 20,
			MaxTagAttrs:     8,
			MaxDepth:        maxDepth,
			MaxElements:     1 << 20,
		}
		err := xmlx.Preflight([]byte(doc), lim)
		rejected := err != nil
		if want := depth > maxDepth; rejected != want {
			rt.Fatalf("depth %d against MaxDepth %d: rejected = %v (%v), want %v (doc %q)",
				depth, maxDepth, rejected, err, want, doc)
		}
		if rejected {
			if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindDepth {
				rt.Fatalf("depth rejection = %v, want KindDepth", err)
			}
		}
	})
}

// nestedDoc builds a well-formed document nested exactly depth elements deep.
// With selfClosingLeaf the innermost element is written `<e/>`, which must NOT
// add a level, the form a feed of siblings is made of.
func nestedDoc(depth int, selfClosingLeaf bool) string {
	var sb strings.Builder
	open := depth
	if selfClosingLeaf {
		open = depth - 1
	}
	for range open {
		sb.WriteString("<e>")
	}
	if selfClosingLeaf {
		sb.WriteString("<e/>")
	}
	for range open {
		sb.WriteString("</e>")
	}
	return sb.String()
}

// decoderMaxDepth measures a well-formed document's true maximum element nesting
// with encoding/xml itself, the independent oracle, so the property never
// re-derives depth the way Preflight does.
func decoderMaxDepth(rt *rapid.T, doc string) int {
	d := xml.NewDecoder(strings.NewReader(doc))
	depth, deepest := 0, 0
	for {
		tok, err := d.Token()
		if err != nil {
			return deepest
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
			deepest = max(deepest, depth)
		case xml.EndElement:
			depth--
		}
		if depth < 0 {
			rt.Fatalf("document %q is not well formed", doc)
		}
	}
}

// TestPropBudgetChargeNeverExceedsItsCaps pins the invariant a caller relies on
// across a whole document: after any sequence of charges, the accepted total is
// within MaxTotalBytes and every accepted value was within MaxFieldBytes. The
// bound is on what the budget ADMITS, so this is the property that says the
// accounting cannot drift.
func TestPropBudgetChargeNeverExceedsItsCaps(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		maxField := rapid.IntRange(1, 64).Draw(rt, "MaxFieldBytes")
		maxTotal := rapid.IntRange(1, 512).Draw(rt, "MaxTotalBytes")
		b := mustBudget(maxField, maxTotal)

		lengths := rapid.SliceOfN(rapid.IntRange(0, 80), 0, 30).Draw(rt, "lengths")
		accepted := 0
		for _, n := range lengths {
			err := b.Charge(strings.Repeat("x", n))
			if err == nil {
				if n > maxField {
					rt.Fatalf("accepted a %d-byte value over MaxFieldBytes %d", n, maxField)
				}
				accepted += n
				continue
			}
			le, ok := errors.AsType[*xmlx.LimitError](err)
			if !ok {
				rt.Fatalf("Charge(%d bytes) = %v, want a *LimitError", n, err)
			}
			switch le.Kind {
			case xmlx.KindField:
				if n <= maxField {
					rt.Fatalf("rejected a %d-byte value as over MaxFieldBytes %d", n, maxField)
				}
			case xmlx.KindTotalText:
				if accepted+n <= maxTotal {
					rt.Fatalf("rejected at total %d+%d against MaxTotalBytes %d", accepted, n, maxTotal)
				}
				// The document is over budget; nothing more may be charged.
				return
			default:
				rt.Fatalf("Charge = unexpected kind %v", le.Kind)
			}
		}
		if b.Total() != accepted {
			rt.Fatalf("Total() = %d, want the accepted sum %d", b.Total(), accepted)
		}
		if accepted > maxTotal {
			rt.Fatalf("admitted %d bytes past MaxTotalBytes %d", accepted, maxTotal)
		}
		if b.Remaining() != maxTotal-accepted {
			rt.Fatalf("Remaining() = %d, want %d", b.Remaining(), maxTotal-accepted)
		}
	})
}
