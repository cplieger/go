// Package xmlx bounds the work an untrusted XML document can cost before and
// during an encoding/xml decode.
//
// A byte cap on the response body is not a bound on decoding. One token can be
// as large as the body, millions of tiny elements expand into a much larger
// object graph, and every open element is a live entry on the decoder's stack,
// with Decoder.Skip walking unmodeled children at any depth.
//
// The package has two halves, matching where the cost is paid:
//
//   - [Preflight] is a lexical gate over the raw bytes, run before the decoder
//     sees them. One allocation-free scan rejects a body whose tokens, element
//     count or nesting are already outside the caller's [Limits].
//   - [Budget] is the decode-time text accounting a schema decoder charges each
//     retained value against, with a per-value cap and a document-wide cap.
//
// Preflight bounds what the decoder must materialize, and Budget bounds what
// the program keeps. Schema decoding stays with the caller.
//
//	if err := xmlx.Preflight(body, xmlx.DefaultLimits()); err != nil {
//		return err
//	}
//	var doc feed
//	if err := xml.Unmarshal(body, &doc); err != nil { // UnmarshalXML charges a Budget
//		return err
//	}
//
// A non-positive limit is reported as [ErrInvalidLimits], never read as
// unbounded. Start from [DefaultLimits] and [DefaultBudget] and size them to
// the document contract.
//
// Preflight refuses every XML directive other than a comment or CDATA section,
// such as <!DOCTYPE. The bounds model the default decoder settings.
// docs/design.md explains the cost model, and docs/non-goals.md covers
// directives, XXE, round-trip stability and decoder settings.
package xmlx
