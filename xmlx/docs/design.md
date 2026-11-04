# How xmlx bounds a document

This page explains what each xmlx bound measures and why, for a developer choosing bound values or reading a rejection.

## Why a byte cap is not enough

A byte cap on the response body is not a bound on decoding. `encoding/xml` materializes each token before any check in your code can run, so a wire-capped body can still force allocations far past its own size. It does so in four ways.

- One token can be as large as the body. A single text node, attribute value or start tag inside an 8 MB response is an 8 MB allocation before your struct field sees it. The decoder's internal buffer never shrinks, so the largest token sets the memory high point for the whole decode.
- Element count amplifies. Millions of three-byte elements fit in a few megabytes of wire and expand into a decoded object graph many times larger.
- Nesting grows the decoder's element stack. The tokenizer pushes one heap-allocated entry per open element. `Unmarshal` carries a fixed internal ceiling on its own recursion, described below. Every child your schema does not model goes through `Decoder.Skip`, which is iterative and has no depth bound. Measured on go1.27.0, a document 349,525 elements deep decodes clean under a schema that models only the root.
- Concurrency multiplies all three. Each in-flight request holds its own copy of the worst case.

xmlx closes that gap with two tools, one for each place the cost is paid. `Preflight` gates the raw bytes before the decoder sees them. It makes one sequential pass with no allocation and no copies, and it neither modifies nor retains the body. `Budget` accounts for the decoded text your code keeps.

## Two quantities, two bounds

Raw bytes and decoded text are different measurements, and neither substitutes for the other. Entity references expand, so `&quot;` is six raw bytes for one decoded byte. CDATA seams split one value across many tokens. Repeated elements overwrite one field while costing many decodes.

`Limits` bounds what arrives and `Budget` bounds what is kept. Size the raw text bound looser than the decoded value cap. The widest predefined entity sets the floor at 6 to 1, and the defaults leave more. `MaxTextRunBytes` is 64 KiB, 16 times the default `Budget` value cap of 4 KiB.

Only the raw-byte gate can stop the decoder from building an oversized token in the first place. `Budget` sees values the decoder has already produced, and stops them before they are joined and kept. That is why the two complement each other.

## Two measurement bases

`MaxTextRunBytes` measures character data, either a raw text run or a CDATA section's content, because that is what the decoder hands back as a `CharData` token. `MaxTokenBytes` measures a markup token whole, delimiters included. The same number then means the same thing for a tag, a comment and a processing instruction.

## Peak depth, not net depth

`MaxDepth` caps the greatest number of elements open at once, which is what the decoder's stack holds. Popped entries are recycled through a free list, so live cost tracks the peak.

A self-closing element counts. The decoder pushes `<e/>` and pops it on the end tag it adds itself, so `<a><b/></a>` reaches depth 2. Tracking only the net change would admit a document one level past your bound through a self-closing leaf.

## The decoder's own depth ceiling

`encoding/xml` guards its unmarshal recursion at 10000 open elements, or 5000 on wasm. The guard was introduced for CVE-2022-30633 and rebuilt for CVE-2026-56859, which closed a `DecodeElement` bypass. The decoder checks that count only on each entry into its recursion, never as the document's peak. It says nothing about the part of the document your schema does not model. Both consequences below were measured on go1.27.0.

- Under a schema that models only the root, a document 349,525 elements deep decodes clean and `Decoder.Skip` walks it with no bound. `MaxDepth` exists for this case.
- Under a schema nested as deeply as the document, a `MaxDepth` above 10000 is unreachable. At `MaxDepth` 10001, a document 10001 elements deep passes `Preflight`. `xml.Unmarshal` then refuses it with an unexported, unwrapped `errors.New("exceeded max depth")` that no `errors.Is` can classify.

In that second case, set `MaxDepth` at 10000 or below, or 5000 on wasm. xmlx then reports the rejection itself, as `KindDepth` wrapping `ErrLimit`.

## How DecodeText differs from DecodeElement

`Budget.DecodeText` reads the text of the element the decoder has just entered. It accumulates under the per-value cap and the document's remaining allowance, and stops at the token that would cross either. Nested markup is skipped whole, comments and processing instructions are ignored, and on success the end tag is consumed. After any error the document is over, because the decoder has no defined position to resume from.

The result is byte-identical to `DecodeElement` for every value `encoding/xml` accepts, with one measured exception at the decoder's depth ceiling. `DecodeText` is iterative, built from `Token` and `Skip`, so it does not enter the unmarshal recursion and does not inherit that ceiling. The two return the same value up to the ceiling. One element deeper, `DecodeElement` refuses while `DecodeText` still returns the value, so `DecodeText` is the looser of the two above that depth.

That split is intended. A `Budget` bounds bytes retained and never document shape, and nesting is the bound `Preflight` owns. If you want the ceiling enforced, set `MaxDepth` at or below it, which also turns the unclassifiable standard-library error into `KindDepth` wrapping `ErrLimit`.

## Charging values

`Budget.Charge` accounts for one value the decoder has already handed you whole, such as an attribute off `StartElement.Attr`. Call it before you store the value. Charge each value once. A value returned by `DecodeText` is already charged.

Every occurrence is charged, including repeats of one element name, because a document that sends `<title>` ten thousand times costs ten thousand decodes. The bound is on bytes, so repeated empty values cost nothing here. `MaxElements` bounds the element count at the raw-byte gate.

## No silent defaults

Every bound is explicit. A bound of zero or less is a configuration mistake, reported as `ErrInvalidLimits` with a `ConfigError` naming the field, and is never read as unbounded. A bounds library whose zero value bounded nothing would be the failure it exists to prevent.

## Rejections change nothing

A refused value leaves the budget exactly as it was. A caller that treats one field as skippable is not drained by the attempt.

## Errors carry a bound and an offset

A rejection names the bound that fired and, from `Preflight`, the byte offset, as in `xmlx: text run longer than 65536 bytes at byte 1024`. It never carries document bytes. An excerpt of the input would be an unbounded, unsanitized string on its way to a log line, which reintroduces the amplification this library stops. A byte offset is one bounded integer with no attacker-chosen content, and it is enough to find the offending token in a saved payload.

## Element count without a schema

How many `<item>` elements a document may carry is your contract, and it is one comparison at your decode site. The schema-free total is `MaxElements`, which protects a plain `xml.Unmarshal` consumer with no custom decode site to hold such a rule.

## Sizing for large documents

`DefaultLimits` and `DefaultBudget` suit a small structured document with short fields, a few attributes per element, shallow nesting, and thousands of elements rather than millions.

A catalogue-scale consumer, such as one parsing a metadata dump of several megabytes, will exceed `MaxElements` and the `Budget` document cap on its first real payload. That is the bound working, so find it on purpose. Set `MaxElements` and `maxTotalBytes` from that document's real ceiling. If the body arrives compressed, run `Preflight` on the inflated bytes, because the transport cap is not the bound that matters there.
