package xmlx_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cplieger/go/xmlx"
)

// fuzzSeeds are the structural shapes worth keeping in the committed corpus: the
// coverage-guided corpus the weekly run builds is discarded, so these seeds ARE
// the durable reach.
var fuzzSeeds = []string{
	"",
	"text",
	`<a/>`,
	`<a><b>t</b></a>`,
	`<?xml version="1.0"?><a/>`,
	`<a href="x > y"/>`,
	`<a href='x > y'/>`,
	`<a><!-- c --></a>`,
	`<a><![CDATA[<not>markup]]></a>`,
	`<!DOCTYPE a><a/>`,
	`<!ENTITY x "y">`,
	`<a` + strings.Repeat(` p="1"`, 40) + `/>`,
	strings.Repeat("<a>", 200),
	strings.Repeat("</a>", 200) + "<a>",
	`<![CDATA[unterminated`,
	`<!--unterminated`,
	`<?unterminated`,
	`<a =/>`,
	"<",
	"<>",
	"</>",
	`<a/>trailing`,
}

// FuzzPreflight drives arbitrary bytes through the lexical gate. Invariants, none
// of them a reimplementation of the scan:
//
//   - it never panics and always terminates (a gate on the request path that
//     panics is a 500 on untrusted input);
//   - it is deterministic (an unreproducible verdict is an undiagnosable outage);
//   - it never modifies or retains the caller's buffer, which the caller hands to
//     the decoder next;
//   - every rejection is a *LimitError wrapping ErrLimit, so a consumer can
//     classify the whole class with one errors.Is;
//   - it is MONOTONE in its limits: whatever it accepts under one configuration it
//     accepts under any looser one;
//   - with every numeric bound at math.MaxInt no numeric bound can fire, so the
//     only rejection left is the directive class. That is the containment law showing
//     the scan has no hidden rejection path.
func FuzzPreflight(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		lim := xmlx.Limits{MaxTextRunBytes: 32, MaxTokenBytes: 64, MaxTagAttrs: 3, MaxDepth: 4, MaxElements: 1 << 20}

		before := bytes.Clone(body)
		err := xmlx.Preflight(body, lim)
		if !bytes.Equal(body, before) {
			t.Fatalf("Preflight modified body: got %q, want %q", body, before)
		}
		if again := xmlx.Preflight(body, lim); (again == nil) != (err == nil) {
			t.Fatalf("nondeterministic on %q: %v then %v", body, err, again)
		}
		if err != nil {
			le, ok := errors.AsType[*xmlx.LimitError](err)
			if !ok {
				t.Fatalf("Preflight(%q) = %v, want a *LimitError", body, err)
			}
			if !errors.Is(err, xmlx.ErrLimit) {
				t.Fatalf("Preflight(%q) = %v, which does not match ErrLimit", body, err)
			}
			if le.Kind == xmlx.KindUnknown {
				t.Fatalf("Preflight(%q) = %v with KindUnknown", body, err)
			}
		}

		loose := xmlx.Limits{
			MaxTextRunBytes: lim.MaxTextRunBytes * 4,
			MaxTokenBytes:   lim.MaxTokenBytes * 4,
			MaxTagAttrs:     lim.MaxTagAttrs * 4,
			MaxDepth:        lim.MaxDepth * 4,
			MaxElements:     lim.MaxElements * 4,
		}
		if err == nil && xmlx.Preflight(body, loose) != nil {
			t.Fatalf("accepted under %+v but rejected under looser %+v: %q", lim, loose, body)
		}

		unbounded := xmlx.Preflight(body, hugeLimits())
		if unbounded != nil {
			if le, ok := errors.AsType[*xmlx.LimitError](unbounded); !ok || le.Kind != xmlx.KindDirective {
				t.Fatalf("under unbounded limits %q = %v, want at most a KindDirective", body, unbounded)
			}
		}
	})
}

// FuzzPreflightThenDecode drives the pair the library is deployed as: the gate,
// then encoding/xml over the same bytes. Two directions, both measured with
// encoding/xml and never with the scan:
//
//   - No FALSE POSITIVES: whenever the decoder accepts a body AND that body's own
//     measured shape is inside the limits, Preflight must accept it too. A gate
//     that rejected a document the decoder would have taken is an outage with no
//     action for the operator.
//   - SOUNDNESS: whenever Preflight accepts, the body's measured shape must be
//     inside the limits. This is the direction that matters for the bound to mean
//     anything, and it is the one a monotonicity or containment property cannot
//     reach.
func FuzzPreflightThenDecode(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		lim := xmlx.Limits{
			MaxTextRunBytes: 1 << 20,
			MaxTokenBytes:   1 << 20,
			MaxTagAttrs:     1 << 20,
			MaxDepth:        16,
			MaxElements:     64,
		}
		err := xmlx.Preflight(body, lim)

		shape, ok := decoderShape(body)
		if !ok {
			// The decoder rejects it: Preflight makes no promise either way.
			return
		}

		if err == nil {
			if shape.peakDepth > lim.MaxDepth {
				t.Fatalf("Preflight accepted a body whose decoder depth is %d > %d: %q",
					shape.peakDepth, lim.MaxDepth, body)
			}
			if shape.elements > lim.MaxElements {
				t.Fatalf("Preflight accepted a body with %d elements > %d: %q",
					shape.elements, lim.MaxElements, body)
			}
			if shape.directives > 0 {
				t.Fatalf("Preflight accepted a body carrying %d directives: %q", shape.directives, body)
			}
			return
		}

		if shape.directives == 0 && shape.peakDepth <= lim.MaxDepth && shape.elements <= lim.MaxElements {
			t.Fatalf("Preflight rejected a decodable in-bounds document: %v (depth %d, elements %d, body %q)",
				err, shape.peakDepth, shape.elements, body)
		}
	})
}

// docShape is what encoding/xml reports about a body, used as the oracle: peak
// open-element count, how many elements it carries, and how many directives.
type docShape struct {
	peakDepth  int
	elements   int
	directives int
}

// decoderShape measures body with encoding/xml, reporting ok=false if the decoder
// rejects it at any point.
func decoderShape(body []byte) (docShape, bool) {
	d := xml.NewDecoder(bytes.NewReader(body))
	var shape docShape
	depth := 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return shape, true
		}
		if err != nil {
			return shape, false
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
			shape.elements++
			shape.peakDepth = max(shape.peakDepth, depth)
		case xml.EndElement:
			depth--
		case xml.Directive:
			shape.directives++
		}
	}
}

// FuzzBudgetDecodeText drives the decode-time half. Invariants: it never panics;
// whatever it returns is within the field cap and was charged exactly; and, the
// oracle, a value it accepts is byte-identical to what encoding/xml's own
// DecodeElement produces for the same element, so adopting the budget can never
// silently change a decoded value.
func FuzzBudgetDecodeText(f *testing.F) {
	f.Add(`<a>plain</a>`, 1024)
	f.Add(`<a>Frieren &amp; Co</a>`, 1024)
	f.Add(`<a><![CDATA[<not>markup]]></a>`, 1024)
	f.Add(`<a>a<b>skip</b>c</a>`, 1024)
	f.Add(`<a>a<![CDATA[<z>]]>b</a>`, 1024)
	f.Add(`<a><!--c--><?pi d?>t</a>`, 1024)
	f.Add(`<a></a>`, 1)
	f.Add(`<a>too long</a>`, 1)
	f.Add(`<a>unterminated`, 1024)
	f.Add(``, 1024)

	f.Fuzz(func(t *testing.T, doc string, maxField int) {
		if maxField <= 0 || maxField > 1<<20 {
			return
		}
		b := mustBudget(maxField, 1<<20)

		got, err := firstElementText(doc, func(d *xml.Decoder) (string, error) { return b.DecodeText(d) })
		if err != nil {
			return
		}
		if len(got) > maxField {
			t.Fatalf("DecodeText returned %d bytes over the field cap %d", len(got), maxField)
		}
		if b.Total() != len(got) {
			t.Fatalf("Total() = %d after one accepted value of %d bytes", b.Total(), len(got))
		}

		// The independent oracle: encoding/xml's own DecodeElement, entered at
		// the same start element.
		want, wantErr := decodeElementOracle(doc)
		if wantErr != nil {
			t.Fatalf("DecodeText accepted %q but DecodeElement failed: %v", doc, wantErr)
		}
		if got != want {
			t.Fatalf("DecodeText(%q) = %q, want DecodeElement parity %q", doc, got, want)
		}
	})
}

// firstElementText enters the document's first start element and applies read to
// its content.
func firstElementText(doc string, read func(*xml.Decoder) (string, error)) (string, error) {
	d := xml.NewDecoder(strings.NewReader(doc))
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		if _, ok := tok.(xml.StartElement); ok {
			return read(d)
		}
	}
}

// decodeElementOracle decodes the document's first element into a string the way
// encoding/xml does it, by calling DecodeElement rather than re-implementing its
// token loop. A hand-written loop could share a bug with the code it is meant to
// check.
func decodeElementOracle(doc string) (string, error) {
	d := xml.NewDecoder(strings.NewReader(doc))
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var s string
		if err := d.DecodeElement(&s, &start); err != nil {
			return "", err
		}
		return s, nil
	}
}
