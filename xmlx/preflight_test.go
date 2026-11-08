package xmlx_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/cplieger/go/xmlx"
)

// tightLimits are small enough that every bound is reachable from a short test
// document, so a case exercises the bound it names rather than some other one
// firing first.
func tightLimits() xmlx.Limits {
	return xmlx.Limits{MaxTextRunBytes: 32, MaxTokenBytes: 64, MaxTagAttrs: 3, MaxDepth: 4, MaxElements: 1 << 20}
}

// hugeLimits are as loose as an int can express: under them the ONLY rejection
// Preflight can produce is the directive class, which is the containment law the
// fuzz target checks too.
func hugeLimits() xmlx.Limits {
	return xmlx.Limits{
		MaxTextRunBytes: math.MaxInt,
		MaxTokenBytes:   math.MaxInt,
		MaxTagAttrs:     math.MaxInt,
		MaxDepth:        math.MaxInt,
		MaxElements:     math.MaxInt,
	}
}

// TestPreflightAcceptsRealDocuments pins that the gate passes every shape a
// legitimate document carries. A bounds check that rejects valid input is worse
// than none: it fails a live integration for a reason the operator cannot act
// on.
func TestPreflightAcceptsRealDocuments(t *testing.T) {
	t.Parallel()
	docs := map[string]string{
		"empty":                    "",
		"text only":                "hello",
		"declaration and root":     `<?xml version="1.0" encoding="utf-8"?><a/>`,
		"nested elements":          `<a><b><c>text</c></b></a>`,
		"self closing":             `<a><b/><b/><b/></a>`,
		"attributes":               `<enclosure url="https://example.test/x" length="12" type="application/x-bittorrent"/>`,
		"comment":                  `<a><!-- a note --></a>`,
		"cdata":                    `<a><![CDATA[<not>markup</not>]]></a>`,
		"entity escaped text":      `<a>Frieren &amp; Co &lt;S01&gt;</a>`,
		"namespace prefixed":       `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel/></rss>`,
		"angle bracket in attr":    `<a href="x?q=1&gt;2" title="a > b"><b/></a>`,
		"single quoted attr":       `<a href='a > b'/>`,
		"whitespace between tags":  "<a>\n  <b>1</b>\n  <b>2</b>\n</a>",
		"trailing text after root": `<a/>trailing`,
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := xmlx.Preflight([]byte(doc), xmlx.DefaultLimits()); err != nil {
				t.Errorf("Preflight(%q) = %v, want it accepted", doc, err)
			}
		})
	}
}

// TestPreflightRejectsEachBound pins one reachable case per bound, asserting the
// SPECIFIC Kind: a consumer branches on Kind to tell "the upstream sent a
// 10 MB field" from "the upstream sent a 5000-deep body", and a test that only
// checked for some error would let those collapse into each other.
func TestPreflightRejectsEachBound(t *testing.T) {
	t.Parallel()
	lim := tightLimits()
	tests := []struct {
		name string
		doc  string
		want xmlx.Kind
	}{
		{
			name: "raw text run",
			doc:  "<a>" + strings.Repeat("x", lim.MaxTextRunBytes+1) + "</a>",
			want: xmlx.KindTextRun,
		},
		{
			name: "cdata section",
			doc:  "<a><![CDATA[" + strings.Repeat("x", lim.MaxTextRunBytes+1) + "]]></a>",
			want: xmlx.KindCDATA,
		},
		{
			name: "comment",
			doc:  "<a><!--" + strings.Repeat("x", lim.MaxTokenBytes+1) + "--></a>",
			want: xmlx.KindComment,
		},
		{
			name: "processing instruction",
			doc:  "<?" + strings.Repeat("x", lim.MaxTokenBytes+1) + "?><a/>",
			want: xmlx.KindProcInst,
		},
		{
			name: "markup token",
			doc:  "<a" + strings.Repeat(" ", lim.MaxTokenBytes+1) + "/>",
			want: xmlx.KindToken,
		},
		{
			name: "attributes on one start tag",
			doc:  `<a p1="1" p2="2" p3="3" p4="4"/>`,
			want: xmlx.KindTagAttrs,
		},
		{
			name: "element nesting depth",
			doc:  strings.Repeat("<a>", lim.MaxDepth+1),
			want: xmlx.KindDepth,
		},
		{
			name: "doctype directive",
			doc:  `<!DOCTYPE rss><rss/>`,
			want: xmlx.KindDirective,
		},
		{
			name: "entity directive",
			doc:  `<!ENTITY x "y"><a/>`,
			want: xmlx.KindDirective,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := xmlx.Preflight([]byte(tt.doc), lim)
			if !errors.Is(err, xmlx.ErrLimit) {
				t.Fatalf("Preflight = %v, want an ErrLimit", err)
			}
			le, ok := errors.AsType[*xmlx.LimitError](err)
			if !ok {
				t.Fatalf("Preflight = %v, want a *LimitError", err)
			}
			if le.Kind != tt.want {
				t.Errorf("Kind = %v, want %v", le.Kind, tt.want)
			}
			if tt.want != xmlx.KindDirective && le.Limit <= 0 {
				t.Errorf("Limit = %d, want the configured bound", le.Limit)
			}
		})
	}
}

// TestPreflightBoundIsInclusive pins the off-by-one on every byte-sized bound: a
// token exactly AT the limit is accepted, one byte over is not. An exclusive
// bound would silently reject documents the caller believes it allowed. Note the
// two measurement bases the bounds document: MaxTextRunBytes measures character
// DATA (a raw run, a CDATA section's content) while MaxTokenBytes measures a
// markup TOKEN whole, delimiters included.
func TestPreflightBoundIsInclusive(t *testing.T) {
	t.Parallel()
	lim := tightLimits()
	tests := []struct {
		name string
		doc  func(n int) string
		// max is the largest value of the doc function's argument that must be
		// accepted.
		max int
	}{
		{
			name: "raw text run",
			doc:  func(n int) string { return "<a>" + strings.Repeat("x", n) + "</a>" },
			max:  lim.MaxTextRunBytes,
		},
		{
			name: "cdata content",
			doc:  func(n int) string { return "<a><![CDATA[" + strings.Repeat("x", n) + "]]></a>" },
			max:  lim.MaxTextRunBytes,
		},
		{
			name: "comment token",
			doc:  func(n int) string { return "<a><!--" + strings.Repeat("x", n) + "--></a>" },
			max:  lim.MaxTokenBytes - len("<!--") - len("-->"),
		},
		{
			name: "processing instruction token",
			doc:  func(n int) string { return "<?" + strings.Repeat("x", n) + "?><a/>" },
			max:  lim.MaxTokenBytes - len("<?") - len("?>"),
		},
		{
			name: "tag token",
			doc:  func(n int) string { return "<a" + strings.Repeat(" ", n) + "/>" },
			max:  lim.MaxTokenBytes - len("<a") - len("/>"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := xmlx.Preflight([]byte(tt.doc(tt.max)), lim); err != nil {
				t.Errorf("a %s at exactly the bound = %v, want it accepted", tt.name, err)
			}
			if err := xmlx.Preflight([]byte(tt.doc(tt.max+1)), lim); !errors.Is(err, xmlx.ErrLimit) {
				t.Errorf("a %s one byte over the bound = %v, want a rejection", tt.name, err)
			}
		})
	}
}

// TestPreflightTokenBoundCoversDelimiters pins the measurement basis explicitly:
// MaxTokenBytes is the whole lexical span, so the same configured number means the
// same thing for a tag, a comment and a processing instruction. Measuring content
// only would give each class a different silent slack.
func TestPreflightTokenBoundCoversDelimiters(t *testing.T) {
	t.Parallel()
	tokens := map[string]string{
		"tag":                    `<abcdefgh>`,
		"comment":                `<!--abc-->`,
		"processing instruction": `<?abcdef?>`,
	}
	for name, tok := range tokens {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if len(tok) != 10 {
				t.Fatalf("the %s fixture is %d bytes, want 10", name, len(tok))
			}
			exact := xmlx.Limits{MaxTextRunBytes: 1 << 20, MaxTokenBytes: 10, MaxTagAttrs: 4, MaxDepth: 4, MaxElements: 8}
			if err := xmlx.Preflight([]byte(tok), exact); err != nil {
				t.Errorf("a 10-byte %s at MaxTokenBytes=10 = %v, want accepted", name, err)
			}
			tight := exact
			tight.MaxTokenBytes = 9
			if err := xmlx.Preflight([]byte(tok), tight); !errors.Is(err, xmlx.ErrLimit) {
				t.Errorf("a 10-byte %s at MaxTokenBytes=9 = %v, want a rejection", name, err)
			}
		})
	}
}

// TestPreflightQuotedAngleBracketDoesNotTerminateTag pins the quote awareness
// the attribute count depends on. If a '>' inside an attribute value ended the
// tag, the scan would resynchronize mid-tag and read the REST of the tag as
// document text, so a tag stuffed with attributes after such a value would slip
// the per-tag cap entirely.
func TestPreflightQuotedAngleBracketDoesNotTerminateTag(t *testing.T) {
	t.Parallel()
	lim := xmlx.Limits{MaxTextRunBytes: 1 << 20, MaxTokenBytes: 1 << 20, MaxTagAttrs: 2, MaxDepth: 8, MaxElements: 1 << 20}

	// Two attributes, the first holding a '>', within the cap.
	if err := xmlx.Preflight([]byte(`<a p="1 > 0" q="2"/>`), lim); err != nil {
		t.Errorf("two attributes with a quoted '>' = %v, want them accepted", err)
	}
	// Four attributes, the first holding a '>'. Only a scan that kept reading
	// past the quoted '>' counts them all and rejects.
	err := xmlx.Preflight([]byte(`<a p="1 > 0" q="2" r="3" s="4"/>`), lim)
	if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindTagAttrs {
		t.Errorf("four attributes after a quoted '>' = %v, want KindTagAttrs", err)
	}
}

// TestPreflightSelfClosingAndEndTagsDoNotAccumulateDepth pins the depth
// arithmetic against the two tag forms that must NOT open a level. Counting a
// self-closing tag as nesting would reject a flat document of N siblings at
// depth N, the single most common real shape (a feed of items).
func TestPreflightSelfClosingAndEndTagsDoNotAccumulateDepth(t *testing.T) {
	t.Parallel()
	lim := xmlx.Limits{MaxTextRunBytes: 1 << 20, MaxTokenBytes: 1 << 20, MaxTagAttrs: 8, MaxDepth: 2, MaxElements: 1 << 20}

	flat := "<root>" + strings.Repeat("<item/>", 500) + "</root>"
	if err := xmlx.Preflight([]byte(flat), lim); err != nil {
		t.Errorf("500 self-closing siblings at depth 1 = %v, want accepted (MaxDepth=%d)", err, lim.MaxDepth)
	}
	paired := "<root>" + strings.Repeat("<item></item>", 500) + "</root>"
	if err := xmlx.Preflight([]byte(paired), lim); err != nil {
		t.Errorf("500 paired siblings at depth 2 = %v, want accepted (MaxDepth=%d)", err, lim.MaxDepth)
	}
	// A self-closing tag carrying a quoted '/' before the '>' is still
	// self-closing only if the '/' is the byte before the terminator.
	if err := xmlx.Preflight([]byte(`<root><item p="a/"/></root>`), lim); err != nil {
		t.Errorf(`self-closing tag after a quoted '/' = %v, want accepted`, err)
	}
}

// TestPreflightStrayEndTagsClampDepth pins that depth never goes negative, so a
// body of end tags cannot buy nesting headroom for the start tags that follow.
func TestPreflightStrayEndTagsClampDepth(t *testing.T) {
	t.Parallel()
	lim := xmlx.Limits{MaxTextRunBytes: 1 << 20, MaxTokenBytes: 1 << 20, MaxTagAttrs: 8, MaxDepth: 3, MaxElements: 1 << 20}
	doc := strings.Repeat("</a>", 100) + strings.Repeat("<a>", 4)
	err := xmlx.Preflight([]byte(doc), lim)
	if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindDepth {
		t.Errorf("100 stray end tags then 4 start tags = %v, want KindDepth", err)
	}
}

// TestPreflightEndTagAttributesAreNotCounted pins that an end tag's bytes do not
// feed the attribute counter. An end tag carries no attributes, so counting '='
// bytes there would reject a document over a value that is not an attribute at
// all.
func TestPreflightEndTagAttributesAreNotCounted(t *testing.T) {
	t.Parallel()
	lim := xmlx.Limits{MaxTextRunBytes: 1 << 20, MaxTokenBytes: 1 << 20, MaxTagAttrs: 1, MaxDepth: 8, MaxElements: 1 << 20}
	if err := xmlx.Preflight([]byte(`<a></a === >`), lim); err != nil {
		t.Errorf("'=' bytes inside an end tag = %v, want them uncounted", err)
	}
}

// TestPreflightNeverValidates pins the contract boundary: Preflight reads
// surface structure, not well-formedness. A malformed body within the bounds
// must pass through so the decoder reports the real parse error, a bounds
// library that also half-validated would produce two different error
// vocabularies for the same broken document.
func TestPreflightNeverValidates(t *testing.T) {
	t.Parallel()
	malformed := []string{
		`<a>`,
		`</a>`,
		`<a><b></a></b>`,
		`<a`,
		`<`,
		`<a =/>`,
		`<a href=unquoted>`,
		`<![CDATA[unterminated`,
		`<!--unterminated`,
		`<?unterminated`,
		"\xff\xfe not utf-8",
	}
	for _, doc := range malformed {
		t.Run(doc, func(t *testing.T) {
			t.Parallel()
			if err := xmlx.Preflight([]byte(doc), xmlx.DefaultLimits()); err != nil {
				t.Errorf("Preflight(%q) = %v, want malformed-but-bounded input to pass through", doc, err)
			}
		})
	}
}

// TestPreflightRefusesNegativeLimits pins the fail-closed configuration
// contract: a negative bound is a caller mistake, never "unbounded". The field
// name is in the error because a caller staring at one is looking for which of
// five numbers they got wrong.
func TestPreflightRefusesNegativeLimits(t *testing.T) {
	t.Parallel()
	base := xmlx.DefaultLimits()
	tests := map[string]func(*xmlx.Limits){
		"Limits.MaxTextRunBytes": func(l *xmlx.Limits) { l.MaxTextRunBytes = -1 },
		"Limits.MaxTokenBytes":   func(l *xmlx.Limits) { l.MaxTokenBytes = -1 },
		"Limits.MaxTagAttrs":     func(l *xmlx.Limits) { l.MaxTagAttrs = -1 },
		"Limits.MaxDepth":        func(l *xmlx.Limits) { l.MaxDepth = -1 },
		"Limits.MaxElements":     func(l *xmlx.Limits) { l.MaxElements = -1 },
	}
	for field, mutate := range tests {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			lim := base
			mutate(&lim)
			err := xmlx.Preflight([]byte(`<a/>`), lim)
			if !errors.Is(err, xmlx.ErrInvalidLimits) {
				t.Fatalf("Preflight with %s negative = %v, want ErrInvalidLimits", field, err)
			}
			if errors.Is(err, xmlx.ErrLimit) {
				t.Error("a configuration mistake reported as ErrLimit: the caller would blame the document")
			}
			if ce, ok := errors.AsType[*xmlx.ConfigError](err); !ok || ce.Field != field {
				t.Errorf("error = %v, want a *ConfigError naming %s", err, field)
			}
		})
	}
}

// TestPreflightZeroLimitTakesItsDefault pins that a zero field is bounded by
// its DefaultLimits value, never unbounded: the zero Limits rejects what
// DefaultLimits rejects, and a literal that sets one field keeps the others at
// their defaults.
func TestPreflightZeroLimitTakesItsDefault(t *testing.T) {
	t.Parallel()
	def := xmlx.DefaultLimits()
	deep := []byte(strings.Repeat("<a>", def.MaxDepth+1) + strings.Repeat("</a>", def.MaxDepth+1))
	for name, lim := range map[string]xmlx.Limits{
		"zero Limits":          {},
		"one field set":        {MaxTextRunBytes: 8},
		"DefaultLimits itself": def,
	} {
		err := xmlx.Preflight(deep, lim)
		if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindDepth || le.Limit != def.MaxDepth {
			t.Errorf("%s: Preflight(%d deep) = %v, want KindDepth at the default %d", name, def.MaxDepth+1, err, def.MaxDepth)
		}
	}
	long := []byte("<a>" + strings.Repeat("x", 9) + "</a>")
	if le, ok := errors.AsType[*xmlx.LimitError](xmlx.Preflight(long, xmlx.Limits{MaxTextRunBytes: 8})); !ok || le.Kind != xmlx.KindTextRun || le.Limit != 8 {
		t.Errorf("a set field must still apply: got %v", xmlx.Preflight(long, xmlx.Limits{MaxTextRunBytes: 8}))
	}
}

// TestPreflightUnderHugeLimitsOnlyRejectsDirectives is the containment law: with
// every numeric bound at math.MaxInt, no numeric bound can fire, so the only
// remaining rejection is the directive class. It also pins that a bound set to
// the largest expressible int does not wrap when the scan adds a delimiter
// length to it, which would turn the loosest possible configuration into one
// that rejects everything.
func TestPreflightUnderHugeLimitsOnlyRejectsDirectives(t *testing.T) {
	t.Parallel()
	accepted := []string{
		`<a>` + strings.Repeat("x", 4096) + `</a>`,
		`<a><![CDATA[` + strings.Repeat("x", 4096) + `]]></a>`,
		`<a><!--` + strings.Repeat("x", 4096) + `--></a>`,
		strings.Repeat("<a>", 5000),
		`<a ` + strings.Repeat(`p="1" `, 200) + `/>`,
		`<![CDATA[unterminated`,
	}
	for _, doc := range accepted {
		if err := xmlx.Preflight([]byte(doc), hugeLimits()); err != nil {
			t.Errorf("under huge limits a %d-byte document = %v, want accepted", len(doc), err)
		}
	}
	err := xmlx.Preflight([]byte(`<!DOCTYPE a><a/>`), hugeLimits())
	if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindDirective {
		t.Errorf("directive under huge limits = %v, want KindDirective", err)
	}
}

// TestPreflightDoesNotModifyBody pins the documented no-mutation contract: the
// caller hands the same slice to the decoder next, so a scan that wrote into it
// would corrupt the decode.
func TestPreflightDoesNotModifyBody(t *testing.T) {
	t.Parallel()
	body := []byte(`<?xml version="1.0"?><a href="x > y"><!--c--><![CDATA[d]]>text</a>`)
	before := bytes.Clone(body)
	if err := xmlx.Preflight(body, xmlx.DefaultLimits()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !bytes.Equal(body, before) {
		t.Errorf("Preflight modified body:\n got %q\nwant %q", body, before)
	}
}

// TestPreflightDirectiveRejectionIsNotAboutComments pins the boundary between
// the rejected class and its two bounded siblings: `<!--` and `<![CDATA[` are
// their own branches and must keep working, or the rejection would take the
// whole `<!` prefix down with it.
func TestPreflightDirectiveRejectionIsNotAboutComments(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{`<a><!--c--></a>`, `<a><![CDATA[c]]></a>`, `<a><!----></a>`, `<a><![CDATA[]]></a>`} {
		if err := xmlx.Preflight([]byte(doc), xmlx.DefaultLimits()); err != nil {
			t.Errorf("Preflight(%q) = %v, want accepted", doc, err)
		}
	}
	for _, doc := range []string{`<!DOCTYPE a>`, `<!ENTITY a "b">`, `<!ATTLIST a b CDATA #IMPLIED>`, `<!NOTATION a SYSTEM "b">`, `<!x>`, `<!>`} {
		if err := xmlx.Preflight([]byte(doc), xmlx.DefaultLimits()); !errors.Is(err, xmlx.ErrLimit) {
			t.Errorf("Preflight(%q) = %v, want the directive class rejected", doc, err)
		}
	}
}

// TestPreflightSelfClosingElementOccupiesOneLevel pins the peak-depth semantics
// against encoding/xml's actual stack behavior: the decoder pushes `<e/>` and
// pops it on the synthesized end tag, so a self-closing leaf DOES occupy a level.
// Counting only the net delta would admit a document one level past the caller's
// bound.
func TestPreflightSelfClosingElementOccupiesOneLevel(t *testing.T) {
	t.Parallel()
	lim := xmlx.Limits{MaxTextRunBytes: 1 << 20, MaxTokenBytes: 1 << 20, MaxTagAttrs: 8, MaxDepth: 2, MaxElements: 1 << 20}

	// <a><b/></a> peaks at 2, exactly the bound.
	if err := xmlx.Preflight([]byte(`<a><b/></a>`), lim); err != nil {
		t.Errorf("depth-2 document with a self-closing leaf = %v, want accepted", err)
	}
	// <a><b><c/></b></a> peaks at 3, one over.
	err := xmlx.Preflight([]byte(`<a><b><c/></b></a>`), lim)
	if le, ok := errors.AsType[*xmlx.LimitError](err); !ok || le.Kind != xmlx.KindDepth {
		t.Errorf("depth-3 document whose deepest element is self-closing = %v, want KindDepth", err)
	}
	// The decoder agrees the peak is 3, which is what makes the bound honest.
	if got := countDecoderPeakDepth(t, `<a><b><c/></b></a>`); got != 3 {
		t.Errorf("encoding/xml peak depth = %d, want 3; the premise of the bound is stale", got)
	}
}

// countDecoderPeakDepth measures a document's peak open-element count with
// encoding/xml itself.
func countDecoderPeakDepth(t *testing.T, doc string) int {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(doc))
	depth, peak := 0, 0
	for {
		tok, err := d.Token()
		if err != nil {
			return peak
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
			peak = max(peak, depth)
		case xml.EndElement:
			depth--
		}
	}
}

// TestPreflightAcceptsWhatTheDecoderAcceptsForRealFeeds is the end-to-end
// sanity check on a document of the shape the library was written for: the
// preflight passes it AND encoding/xml parses it. The two halves must agree on
// real input or the gate is not deployable.
func TestPreflightAcceptsWhatTheDecoderAcceptsForRealFeeds(t *testing.T) {
	t.Parallel()
	const feed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>Example</title>
    <item>
      <title>Show &amp; Tale S01 [1080p]</title>
      <guid>https://example.test/1</guid>
      <enclosure url="https://example.test/1.torrent" length="123" type="application/x-bittorrent"/>
      <torznab:attr name="seeders" value="42"/>
    </item>
  </channel>
</rss>`
	if err := xmlx.Preflight([]byte(feed), xmlx.DefaultLimits()); err != nil {
		t.Fatalf("Preflight(real feed) = %v, want accepted", err)
	}
	var doc struct {
		Channel struct {
			Title string `xml:"title"`
			Items []struct {
				Title string `xml:"title"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal([]byte(feed), &doc); err != nil {
		t.Fatalf("xml.Unmarshal(real feed) = %v; the acceptance case is stale", err)
	}
	if len(doc.Channel.Items) != 1 || doc.Channel.Items[0].Title != "Show & Tale S01 [1080p]" {
		t.Errorf("decoded %+v, want the one item with its entity-decoded title", doc)
	}
}

// TestPreflightBoundsElementCount pins the bound for amplification by COUNT. A
// flat run of tiny empty elements passes every per-token bound (each token is
// four bytes, nesting is depth 1, there is no text at all) and still expands into
// a decoded object graph many times the wire size, so without this bound the
// cheapest adoption path leaves that path open. It counts start tags, self-closing
// included, and ignores end tags.
func TestPreflightBoundsElementCount(t *testing.T) {
	t.Parallel()
	lim := xmlx.Limits{MaxTextRunBytes: 1 << 20, MaxTokenBytes: 1 << 20, MaxTagAttrs: 8, MaxDepth: 64, MaxElements: 10}

	if err := xmlx.Preflight([]byte(strings.Repeat("<a/>", 10)), lim); err != nil {
		t.Errorf("10 elements at MaxElements=10 = %v, want accepted", err)
	}
	err := xmlx.Preflight([]byte(strings.Repeat("<a/>", 11)), lim)
	le, ok := errors.AsType[*xmlx.LimitError](err)
	if !ok || le.Kind != xmlx.KindElements {
		t.Fatalf("11 elements at MaxElements=10 = %v, want KindElements", err)
	}
	if le.Limit != 10 {
		t.Errorf("Limit = %d, want the configured 10", le.Limit)
	}

	// Paired elements count once, not twice: an end tag is not an element.
	if err := xmlx.Preflight([]byte(strings.Repeat("<a></a>", 10)), lim); err != nil {
		t.Errorf("10 paired elements at MaxElements=10 = %v, want accepted", err)
	}
	// The bound is a document total, not per level: nesting does not exempt it.
	nestedErr := xmlx.Preflight([]byte(strings.Repeat("<a>", 11)), lim)
	if nested, ok := errors.AsType[*xmlx.LimitError](nestedErr); !ok || nested.Kind != xmlx.KindElements {
		t.Errorf("11 nested elements at MaxElements=10 = %v, want KindElements", nestedErr)
	}
	// Comments, processing instructions and CDATA are not elements.
	noise := `<?xml version="1.0"?><!--c--><a><![CDATA[x]]></a>`
	tight := lim
	tight.MaxElements = 1
	if err := xmlx.Preflight([]byte(noise), tight); err != nil {
		t.Errorf("one element among non-element tokens at MaxElements=1 = %v, want accepted", err)
	}
}

// TestPreflightRejectionCarriesTheOffset pins the safe half of the diagnostic
// need: the byte offset where the scan gave up. It is one bounded integer with no
// attacker-chosen content, and it is what turns "text run longer than 65536" into
// something an operator can locate in a saved payload.
func TestPreflightRejectionCarriesTheOffset(t *testing.T) {
	t.Parallel()
	lim := xmlx.Limits{MaxTextRunBytes: 8, MaxTokenBytes: 32, MaxTagAttrs: 2, MaxDepth: 4, MaxElements: 8}

	// The over-long run starts after "<pad/><a>" = 9 bytes.
	err := xmlx.Preflight([]byte(`<pad/><a>`+strings.Repeat("x", 100)+`</a>`), lim)
	le, ok := errors.AsType[*xmlx.LimitError](err)
	if !ok {
		t.Fatalf("Preflight = %v, want a *LimitError", err)
	}
	if le.Offset != 9 {
		t.Errorf("Offset = %d, want 9 (where the over-long run begins)", le.Offset)
	}
	if !strings.Contains(err.Error(), "at byte 9") {
		t.Errorf("Error() = %q, want the offset rendered", err.Error())
	}

	// A rejection at offset 0 renders no offset suffix, since 0 is also the
	// zero value a Budget rejection carries.
	zeroOffset := xmlx.Preflight([]byte(`<!DOCTYPE a>`), lim)
	if strings.Contains(zeroOffset.Error(), "at byte") {
		t.Errorf("Error() = %q, want no offset suffix at offset 0", zeroOffset.Error())
	}
}

// TestPreflightDecoderConfigurationIsDocumentedNotEnforced pins the one
// documented soundness precondition with a live counterexample, so the limitation
// is a tested fact rather than a doc claim: with Strict disabled the decoder
// accepts a bare attribute name as an attribute, and the lexical `=` count cannot
// see it.
func TestPreflightDecoderConfigurationIsDocumentedNotEnforced(t *testing.T) {
	t.Parallel()
	const doc = `<a one two three/>`
	lim := xmlx.Limits{MaxTextRunBytes: 1 << 20, MaxTokenBytes: 1 << 20, MaxTagAttrs: 1, MaxDepth: 4, MaxElements: 8}

	// The scan counts zero attributes: there is no '=' byte.
	if err := xmlx.Preflight([]byte(doc), lim); err != nil {
		t.Fatalf("Preflight(%q) = %v, want accepted (no '=' bytes to count)", doc, err)
	}

	// The DEFAULT decoder rejects it, which is what makes the bound sound for
	// the configuration the package documents.
	var v struct{}
	if err := xml.Unmarshal([]byte(doc), &v); err == nil {
		t.Error("the default decoder accepted a bare attribute name; the documented precondition is stale")
	}

	// With Strict disabled it decodes with three attributes the scan never saw.
	d := xml.NewDecoder(strings.NewReader(doc))
	d.Strict = false
	tok, err := d.Token()
	if err != nil {
		t.Fatalf("non-strict Token: %v", err)
	}
	start, ok := tok.(xml.StartElement)
	if !ok {
		t.Fatalf("first token %T, want a StartElement", tok)
	}
	if len(start.Attr) != 3 {
		t.Errorf("non-strict decode produced %d attributes, want 3; the documented limitation is stale", len(start.Attr))
	}
}

// TestPreflightAcceptsTheSmallestTokenOfEachDelimitedClass pins the token bound
// at its floor. A close-delimited class has a minimum lexical span its
// delimiters alone occupy, and a bound sized to exactly that span must still
// admit the empty token: a caller who set MaxTokenBytes to fit one comment
// delimiter pair means to allow an empty comment, not to forbid the class. One
// byte below that span nothing of the class can fit, and the rejection names the
// class so the caller can see which bound is too small.
func TestPreflightAcceptsTheSmallestTokenOfEachDelimitedClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		smallest string
		want     xmlx.Kind
	}{
		{name: "comment", smallest: `<!---->`, want: xmlx.KindComment},
		{name: "processing instruction", smallest: `<??>`, want: xmlx.KindProcInst},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			exact := xmlx.Limits{
				MaxTextRunBytes: 32,
				MaxTokenBytes:   len(tt.smallest),
				MaxTagAttrs:     2,
				MaxDepth:        4,
				MaxElements:     8,
			}
			if err := xmlx.Preflight([]byte(tt.smallest), exact); err != nil {
				t.Errorf("Preflight(%q) at MaxTokenBytes=%d = %v, want accepted", tt.smallest, exact.MaxTokenBytes, err)
			}

			tight := exact
			tight.MaxTokenBytes = len(tt.smallest) - 1
			err := xmlx.Preflight([]byte(tt.smallest), tight)
			le, ok := errors.AsType[*xmlx.LimitError](err)
			if !ok || le.Kind != tt.want {
				t.Fatalf("Preflight(%q) at MaxTokenBytes=%d = %v, want %v", tt.smallest, tight.MaxTokenBytes, err, tt.want)
			}
			if le.Limit != tight.MaxTokenBytes {
				t.Errorf("Limit = %d, want the configured %d", le.Limit, tight.MaxTokenBytes)
			}
		})
	}
}

// TestPreflightEmptyDelimitedTokenDoesNotEndTheScan pins the span of a
// close-delimited token whose content is empty: it is exactly its delimiters, so
// the bytes after it are still document. A token whose span was misreported as
// the whole remainder would make `<![CDATA[]]>` a universal bypass, since every
// bound after it would go unenforced while the document still passed.
func TestPreflightEmptyDelimitedTokenDoesNotEndTheScan(t *testing.T) {
	t.Parallel()
	empties := map[string]string{
		"comment":                `<!---->`,
		"processing instruction": `<??>`,
		"cdata":                  `<![CDATA[]]>`,
	}
	for name, empty := range empties {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc := `<r>` + empty + `<a/></r>`
			lim := xmlx.Limits{MaxTextRunBytes: 32, MaxTokenBytes: 64, MaxTagAttrs: 2, MaxDepth: 4, MaxElements: 2}
			if err := xmlx.Preflight([]byte(doc), lim); err != nil {
				t.Errorf("Preflight(%q) at MaxElements=2 = %v, want accepted", doc, err)
			}

			// The element after the empty token is the second one, so a bound of
			// one must reject it.
			tight := lim
			tight.MaxElements = 1
			err := xmlx.Preflight([]byte(doc), tight)
			le, ok := errors.AsType[*xmlx.LimitError](err)
			if !ok || le.Kind != xmlx.KindElements {
				t.Fatalf("Preflight(%q) at MaxElements=1 = %v, want KindElements: the element after the empty token still counts", doc, err)
			}
		})
	}
}

// TestPreflightMarkupInsideCDATAIsNotMarkup pins that a CDATA section's content
// is character data whatever it spells. Tags written inside it are literal bytes
// and reach neither the element count nor the depth accounting; a section whose
// span was measured short would resume the scan inside its own content and read
// those bytes as document structure, rejecting a legitimate document over
// elements it invented.
func TestPreflightMarkupInsideCDATAIsNotMarkup(t *testing.T) {
	t.Parallel()
	// The document holds exactly one element, its root, so any element the scan
	// finds inside the section is a rejection.
	lim := xmlx.Limits{MaxTextRunBytes: 64, MaxTokenBytes: 64, MaxTagAttrs: 2, MaxDepth: 4, MaxElements: 1}
	tests := []struct {
		name    string
		content string
	}{
		{name: "start tag", content: `<a>`},
		{name: "nested start tags", content: `<a><b><c>`},
		{name: "paired tags", content: `<not>markup</not>`},
		{name: "end tag", content: `</x>`},
		{name: "self closing tag", content: `<a/>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := `<r><![CDATA[` + tt.content + `]]></r>`
			if err := xmlx.Preflight([]byte(doc), lim); err != nil {
				t.Errorf("Preflight(%q) at MaxElements=1 = %v, want accepted: only <r> is an element", doc, err)
			}
		})
	}
}

// TestPreflightUnterminatedTagAtTheTokenBoundIsLeftToTheDecoder pins the tail
// case of the token bound. A tag that never closes is a well-formedness fault,
// not an oversized token, so long as the bytes it occupies stay within the bound:
// reporting a limit error for a truncated document would blame its size and hide
// the parse error that says what is actually wrong with it. One byte past the
// bound the size claim becomes true and the rejection is correct.
func TestPreflightUnterminatedTagAtTheTokenBoundIsLeftToTheDecoder(t *testing.T) {
	t.Parallel()
	const bound = 8
	lim := xmlx.Limits{MaxTextRunBytes: 32, MaxTokenBytes: bound, MaxTagAttrs: 2, MaxDepth: 4, MaxElements: 8}

	atBound := "<" + strings.Repeat("a", bound-1)
	if err := xmlx.Preflight([]byte(atBound), lim); err != nil {
		t.Errorf("Preflight(%q), %d bytes at MaxTokenBytes=%d = %v, want it passed through", atBound, len(atBound), bound, err)
	}
	// The decoder is the one that reports what is wrong with it.
	if err := xml.Unmarshal([]byte(atBound), &struct{}{}); err == nil {
		t.Error("the decoder accepted an unterminated tag; the division of labor is stale")
	}

	overBound := "<" + strings.Repeat("a", bound)
	err := xmlx.Preflight([]byte(overBound), lim)
	le, ok := errors.AsType[*xmlx.LimitError](err)
	if !ok || le.Kind != xmlx.KindToken {
		t.Fatalf("Preflight(%q), %d bytes at MaxTokenBytes=%d = %v, want KindToken", overBound, len(overBound), bound, err)
	}
	if le.Limit != bound {
		t.Errorf("Limit = %d, want the configured %d", le.Limit, bound)
	}
}
