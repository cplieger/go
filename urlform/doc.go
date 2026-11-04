// Package urlform classifies the structural form of a raw, untrusted URL
// string where a browser's reading, through the WHATWG URL parser, differs
// from net/url's. It covers whitespace-smuggled URLs, backslash authorities,
// slash-count fixups after a special scheme, protocol-relative and
// schemeless-host forms, hidden-host parses and userinfo spoofing.
//
// It answers a question about a string headed for a person whose browser will
// read it. Validating a URL your own process will fetch is a different job,
// for net/url, the dialer and an SSRF guard.
//
// urlform is a classifier over net/url with a bounded, enumerated set of
// WHATWG readings on top, not a conformant WHATWG parser. Conformance fixtures
// in testdata/whatwg-fixtures.json pin the supported set, and the README lists
// what it leaves out, such as IDNA mapping.
//
// [Classify] never errors. Every input lands in exactly one [Class], with the
// facts a consumer needs on the returned [Form]: Host, Scheme, Port,
// HasUserInfo, HasBackslash and HasTabOrNewline. [Form.NormalizedPath] is the
// path a browser resolves, with dot segments removed. The classification
// carries no judgment, so each consumer applies its own fail direction.
//
// [FoldHostASCII], [EqualASCIIFold], [IsASCIIHost] and [HostMatchesDomain] share
// one ASCII-only folding rule, because a full Unicode fold would turn
// homograph bytes such as U+0130 or U+212A into ASCII. [RawQueryNames] and
// [RawQueryPairs] read query parameters from the raw bytes, split on both '&'
// and ';', because url.ParseQuery silently drops a malformed pair.
//
// docs/contract.md has the full contract.
package urlform
