package urlform

import (
	"iter"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Class names the structural form of a raw, untrusted URL string -
// specifically the browser-vs-net/url parse quirks that decide whether the
// string really carries a host. It is the single home of that quirk
// vocabulary; see Form.
type Class int

const (
	// ClassEmpty is a string that is empty after the input preprocessing
	// (edge trimming plus tab/newline removal; see Classify).
	ClassEmpty Class = iota
	// ClassMalformed is a string the canonicalized parse rejected; no
	// structural facts (and no host evidence) can be extracted from it.
	ClassMalformed
	// ClassAbsolute is a scheme-and-host absolute URL ("https://host/x");
	// Host carries the parsed hostname.
	ClassAbsolute
	// ClassHiddenHost is a scheme-bearing parse with no hostname, where
	// net/url sees no host but a browser may navigate to one. For the
	// authority-carrying special schemes (http, https, ws, wss, ftp) the
	// WHATWG parser skips ANY run of slashes after the scheme - zero
	// ("https:host/x"), one ("https:/host/x"), or many - and reads the
	// authority, so the classifier runs the same authority reparse it uses
	// for schemeless forms and recovers the browser's reading into
	// Host/Port/HasUserInfo (HostUnrecoverable marks a failed recovery; a
	// port-only authority such as "https://:443/x" recovers no host, which
	// matches the browser - the WHATWG parser fails on an empty special
	// host). For every other scheme ("host:443/x" parsing the host as an
	// opaque scheme, "javascript:alert(1)", "mailto:x") the browser reads an
	// opaque path with no authority, so the facts stay empty - there the
	// host evidence, if any, is genuinely hidden.
	ClassHiddenHost
	// ClassProtocolRelative is a network-path reference: "//host/x" (Host
	// carries the parsed host a browser would resolve against the ambient
	// scheme) or a leading-"//" form with no extractable host evidence - a
	// three-or-more-slash form ("///x": Go parses a rooted path while
	// browsers read an authority) or an empty-authority form ("//", "//?q").
	// Host is the discriminator between the two sub-forms: consumers that
	// need host evidence treat an empty Host here as ambiguous and fail
	// closed.
	ClassProtocolRelative
	// ClassSchemelessHost is a scheme-free, non-rooted form ("host/x"):
	// net/url parses a bare path, but a browser address bar navigates to the
	// first segment as a host. Host carries that authority-reparse evidence
	// (empty for a query- or fragment-only form such as "?x:y");
	// HostUnrecoverable marks a failed reparse.
	ClassSchemelessHost
	// ClassRelative is a rooted, host-free relative path ("/x").
	ClassRelative
)

// Form is the structural classification of one raw, untrusted URL string
// (an upstream API field, a scraped link, operator input). It names the
// browser-vs-net/url parse-quirk classes ONCE - backslash authorities,
// protocol-relative and schemeless-host forms, hidden-host parses - so every
// consumer branches on the same facts while keeping its own fail direction
// as policy: a publisher drops what it cannot vouch for (publish-or-drop),
// while an evidence gate hides what it cannot classify
// (extract-evidence-or-hide). Fields are ordered for govet fieldalignment.
type Form struct {
	// path is the raw (dot-segments intact) path of the reading whose facts
	// this Form reports: the canonicalized parse's for a form net/url reads
	// hierarchically, and the AUTHORITY REPARSE's wherever that reparse is
	// what supplied Host, since the main parse of a schemeless or
	// hidden-host form reads the host as part of its path
	// ("animebytes.tv/x"). Empty when no browser-resolvable path was
	// extracted - no parse, a failed reparse, or an opaque-path reading.
	// It stays private: NormalizedPath reports the semantic reading, so
	// which parse supplied it never crosses the package boundary.
	path string
	// Trimmed is the preprocessed raw string the classification read: edges
	// trimmed and embedded ASCII tab/newline removed (the WHATWG input
	// preprocessing, recorded by HasTabOrNewline), with backslashes NOT
	// canonicalized. It is what a publisher emits or prefixes - already free
	// of the whitespace-smuggling bytes a browser would silently drop, and
	// never otherwise rewritten.
	Trimmed string
	// Host is the lowercased host evidence a browser would navigate to, when
	// extractable: the parsed hostname of an absolute or protocol-relative
	// form, or the authority reparse of a schemeless-host or recoverable
	// hidden-host form. Empty when the string carries none (or the form
	// hides it; see Class). The fold is ASCII-only by design (see
	// asciiLower): a full-Unicode fold would launder homograph bytes
	// (U+0130 -> 'i', U+212A -> 'k') into ASCII, so non-ASCII host evidence
	// survives here unfolded for a consumer's fail-closed ASCII-only host
	// gates.
	Host string
	// Scheme is the canonicalized parse's scheme, which url.Parse folds to
	// lowercase (an "HTTPS://" source reads "https", RFC 3986 canonical
	// form), so the value is already case-folded; empty when the string
	// carries none or did not parse. Case-insensitive comparison by consumers
	// remains correct as defense in depth.
	Scheme string
	// Port is the canonicalized parse's port string; empty when none is
	// present or the string did not parse. net/url only accepts an
	// all-digit port, but it does not range-check it - consumers that need
	// a valid 16-bit port (a link publisher) validate the range. (The WHATWG
	// parser rejects an out-of-range port outright; reporting the fact and
	// leaving the fail direction to the consumer is this package's model.)
	Port string
	// Class is the structural form.
	Class Class
	// HasBackslash records a '\' anywhere in the trimmed string. Browsers
	// (WHATWG URL parser) treat '\' as '/' for the special schemes (http,
	// https, ws, wss, ftp, file) and for schemeless forms (where the address
	// bar's ambient scheme is special), so for those the parsed facts
	// (Scheme/Host/Port/Class) describe the canonicalized reading - a
	// `/\host/x` form classifies protocol-relative, not as a host-less
	// rooted path. For a non-special scheme a backslash is an ordinary
	// character and is NOT canonicalized (a browser reads an opaque path).
	// Either way the flag lets a publisher that must emit the raw string
	// reject it outright.
	HasBackslash bool
	// HasTabOrNewline records that the edge-trimmed string contained
	// embedded ASCII tab or newline (U+0009, U+000A, U+000D), which the
	// WHATWG parser - and therefore this classification - removes wherever
	// they appear ("https://anime\tbytes.tv/x" navigates to animebytes.tv).
	// Trimmed already has them removed, so emitting Trimmed is safe; the
	// flag records the smuggling attempt for publishers that treat the
	// ORIGINAL string as emittable or want to reject de-smuggled input
	// outright.
	HasTabOrNewline bool
	// HostUnrecoverable marks a ClassSchemelessHost or recoverable
	// ClassHiddenHost whose authority reparse failed (e.g. a space before an
	// "@"): browser-visible host evidence may exist but cannot be extracted,
	// so evidence-driven consumers treat the form like a parse failure.
	HostUnrecoverable bool
	// HasUserInfo records a userinfo authority component ("user@host") in
	// the canonicalized parse - a visual-spoofing vector
	// ("https://trusted@evil/") a link publisher typically rejects. For a
	// ClassSchemelessHost or recovered ClassHiddenHost the fact comes from
	// the same authority reparse that supplies Host (so "user@host/x"
	// reports it alongside the recovered host). Always false when the
	// string did not parse.
	HasUserInfo bool
}

// Classify classifies a raw URL string into its structural Form. It
// never errors: every input lands in exactly one class, and unparseable input
// is ClassMalformed. Consumers apply their own policy over the returned
// facts (see Form).
//
// Classification starts with the WHATWG basic parser's input preprocessing,
// so string-level whitespace smuggling cannot hide a URL from the facts:
// leading/trailing C0 controls and whitespace are trimmed (trimEdges), and
// embedded ASCII tab/newline are removed everywhere (recorded by
// HasTabOrNewline). This is the same hardening CPython adopted for
// urllib.parse (CVE-2022-0391).
func Classify(raw string) Form {
	f := Form{Trimmed: trimEdges(raw)}
	if strings.ContainsAny(f.Trimmed, "\t\n\r") {
		f.HasTabOrNewline = true
		f.Trimmed = strings.Map(dropTabOrNewline, f.Trimmed)
	}
	f.HasBackslash = strings.Contains(f.Trimmed, `\`)
	if f.Trimmed == "" {
		f.Class = ClassEmpty
		return f
	}
	canonical := canonicalizeSlashes(f.Trimmed)
	parsed, err := url.Parse(canonical)
	if err != nil {
		f.Class = ClassMalformed
		return f
	}
	f.Scheme = parsed.Scheme
	f.Port = parsed.Port()
	f.HasUserInfo = parsed.User != nil
	f.path = parsed.Path
	// Hostname() drops the port and userinfo; asciiLower folds case for the
	// byte-wise host predicates downstream while leaving non-ASCII homograph
	// bytes intact for the fail-closed IsASCIIHost gates.
	f.Host = asciiLower(parsed.Hostname())
	switch {
	case parsed.Scheme != "" && f.Host != "":
		f.Class = ClassAbsolute
	case parsed.Scheme != "":
		f.Class = ClassHiddenHost
		f.recoverHiddenAuthority(canonical)
	case f.Host != "":
		f.Class = ClassProtocolRelative
	case strings.HasPrefix(canonical, "//"):
		// A leading "//" whose parse yielded no host: three or more slashes
		// (Go parsed a rooted path while browsers read an authority) or an
		// empty-authority form ("//", "//?q"). Either way the string is a
		// network-path reference with no extractable host evidence, so Host
		// stays empty and the form classifies protocol-relative.
		f.Class = ClassProtocolRelative
	case strings.HasPrefix(canonical, "/"):
		f.Class = ClassRelative
	default:
		f.Class = ClassSchemelessHost
		rehost, rerr := url.Parse("//" + canonical)
		if rerr != nil {
			f.HostUnrecoverable = true
			// The main parse read the host as part of its path, and the
			// reparse that would have separated them failed: there is no
			// extracted reading left to report a path from.
			f.path = ""
			return f
		}
		f.Host = asciiLower(rehost.Hostname())
		f.HasUserInfo = rehost.User != nil
		f.path = rehost.Path
	}
	return f
}

// recoverHiddenAuthority extracts the browser's authority reading from a
// scheme-bearing parse that hid it. The WHATWG scheme state routes every
// special scheme to "special authority ignore slashes", which skips any run
// of slashes (and, already canonicalized here, backslashes) after the colon
// and reads an authority - so "https:/host/x" and "https:host/x" both
// navigate to host. The recovery reuses the classifier's authority-reparse
// heuristic on that remainder. It applies only to the special schemes that
// carry an authority (file is special for slash handling but its slash forms
// yield an empty host, and non-special schemes read an opaque path - no
// browser-visible host exists to recover for either).
func (f *Form) recoverHiddenAuthority(canonical string) {
	if !isAuthorityScheme(f.Scheme) {
		return
	}
	rest := strings.TrimLeft(canonical[len(f.Scheme)+1:], "/")
	rehost, err := url.Parse("//" + rest)
	if err != nil {
		f.HostUnrecoverable = true
		f.path = ""
		return
	}
	f.Host = asciiLower(rehost.Hostname())
	f.Port = rehost.Port()
	f.HasUserInfo = rehost.User != nil
	f.path = rehost.Path
}

// NormalizedPath returns the browser's reading of the path with dot segments
// removed ("/view/1/../2" reads "/view/2"), via net/url's RFC 3986 §5.2.4
// resolution over the DECODED path, so "%2e%2e" resolves like ".." and "%2F"
// reads as a separator (compare the escaped path to keep it distinct).
// Repeated slashes are preserved. A non-empty result is rooted; an
// authority-only form reads "/". It is empty when there is no
// browser-resolvable path: ClassEmpty, ClassMalformed, HostUnrecoverable, an
// opaque path, a host-less ClassProtocolRelative form or a query-only reference.
func (f *Form) NormalizedPath() string {
	if f.Class == ClassProtocolRelative && f.Host == "" {
		return ""
	}
	if f.path == "" {
		if f.Host == "" {
			return ""
		}
		return "/"
	}
	rooted := url.URL{Path: "/"}
	return rooted.ResolveReference(&url.URL{Path: f.path}).Path
}

// trimEdges removes leading and trailing C0 controls and space (the WHATWG
// basic parser's edge rule: every byte <= 0x20) plus the wider Unicode
// whitespace set (strings.TrimSpace's rule, kept deliberately: an NBSP- or
// ideographic-space-wrapped link still classifies with facts, and
// over-trimming errs fail-safe for evidence gates where a WHATWG-strict edge
// would return no facts at all). The delta from the spec is edge-only and
// documented; embedded characters are never touched here.
//
// unicode.IsSpace is the package's ONLY Unicode-table read, which makes this
// the one place a Unicode revision can change what the package does; every
// case fold here is the ASCII byte rule instead (see asciiLowerByte), which no
// revision can move. A future Unicode bump therefore needs the White_Space set
// diffed and nothing else. Measured across the Unicode 15 to 17 jump (Go 1.26
// to 1.27): the set is byte-identical at 25 runes, so that bump trimmed
// exactly what it trimmed before.
func trimEdges(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r <= 0x20 || unicode.IsSpace(r)
	})
}

// dropTabOrNewline is the strings.Map dropper for the WHATWG "remove all
// ASCII tab or newline from input" preprocessing step.
func dropTabOrNewline(r rune) rune {
	if r == '\t' || r == '\n' || r == '\r' {
		return -1
	}
	return r
}

// schemeEnd returns the index of the ':' terminating a leading URL scheme
// (RFC 3986 / WHATWG grammar: ALPHA *( ALPHA / DIGIT / "+" / "-" / "." )),
// or -1 when the string carries no scheme prefix.
func schemeEnd(s string) int {
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		case i > 0 && c == ':':
			return i
		default:
			return -1
		}
	}
	return -1
}

// isSpecialScheme reports whether scheme (already lowercased) is a WHATWG
// special scheme - the set for which the spec treats '\' as '/'.
func isSpecialScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "ws", "wss", "ftp", "file":
		return true
	}
	return false
}

// isAuthorityScheme reports whether scheme (already lowercased) is a special
// scheme that carries a recoverable authority after arbitrary slash runs -
// the special set minus file, whose slash forms read an empty host.
func isAuthorityScheme(scheme string) bool {
	return scheme != "file" && isSpecialScheme(scheme)
}

// canonicalizeSlashes rewrites '\' to '/' exactly where the WHATWG parser
// treats them as equivalent: for special-scheme forms and schemeless forms
// (where the address bar's ambient scheme is special), and only ahead of the
// query/fragment (past the first '?' or '#' a backslash is an ordinary
// character even for special schemes). A non-special scheme's backslashes
// are ordinary characters everywhere - rewriting them would fabricate host
// evidence a browser never sees ("non-special:\\x" reads an opaque path,
// not an authority).
//
// The scheme it tests is folded with the package's own ASCII rule
// (asciiLower), not strings.ToLower, because a scheme is one of the fixed
// ASCII protocol tokens EqualASCIIFold exists for and this test decides
// whether a string's backslashes become host evidence. The two folds are the
// same operation here rather than merely the same today: schemeEnd accepts
// only ASCII bytes, so the prefix carries nothing a Unicode fold could
// launder, and FuzzClassify pins that as a differential law.
func canonicalizeSlashes(s string) string {
	if end := schemeEnd(s); end >= 0 && !isSpecialScheme(asciiLower(s[:end])) {
		return s
	}
	stop := strings.IndexAny(s, "?#")
	if stop == -1 {
		stop = len(s)
	}
	if !strings.Contains(s[:stop], `\`) {
		return s
	}
	return strings.ReplaceAll(s[:stop], `\`, "/") + s[stop:]
}

// asciiLowerByte is the package's ASCII-only case rule, defined on ONE byte:
// A-Z map to lowercase, every other byte - including every byte of a
// multi-byte or invalid UTF-8 sequence - is returned unchanged. It is the
// single home of the rule, read by the string fold (asciiLower, and through it
// Form.Host, FoldHostASCII, HostMatchesDomain and the special-scheme test in
// canonicalizeSlashes) and by the equality comparison (EqualASCIIFold), so no
// two of those spellings can disagree about what folding ASCII means.
//
// Spelling the rule out on bytes rather than delegating to strings.ToLower or
// unicode.ToLower is also what makes it version-stable: it reads no Unicode
// table, so no Unicode revision can change which bytes it folds. That matters
// in a fold whose job is to NOT launder the codepoints a Unicode fold maps
// into ASCII, because a revision is exactly what can add such a mapping.
func asciiLowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// asciiLower lowercases only the ASCII letters A-Z, leaving every other byte
// untouched. Form.Host folds with this instead of strings.ToLower because
// the full-Unicode fold has ASCII-producing mappings (U+0130 LATIN CAPITAL
// LETTER I WITH DOT ABOVE -> 'i', U+212A KELVIN SIGN -> 'k') that would
// launder a homograph host into ASCII before a consumer's fail-closed
// ASCII-only host gates ever see the non-ASCII evidence they exist to
// reject. It is the single string implementation of that fold: FoldHostASCII
// is the exported name for it and EqualASCIIFold is the same rule applied to
// a comparison, so a consumer's fold and Form.Host can never disagree.
//
// It walks BYTES rather than mapping runes (strings.Map) because the rule is
// defined on bytes and rune mapping would rewrite an invalid UTF-8 byte to
// U+FFFD: that both contradicts "every other byte untouched" and launders
// distinct invalid host evidence onto one canonical spelling, in a fold whose
// entire job is to leave non-ASCII evidence intact for the fail-closed gates.
// Nothing is allocated when the string carries no ASCII uppercase.
func asciiLower(s string) string {
	var folded []byte
	for i := range len(s) {
		c := asciiLowerByte(s[i])
		if c == s[i] {
			continue
		}
		if folded == nil {
			folded = []byte(s)
		}
		folded[i] = c
	}
	if folded == nil {
		return s
	}
	return string(folded)
}

// FoldHostASCII lowercases only the ASCII letters A-Z of host, leaving every
// other byte untouched. It is the exported form of the fold Form.Host already
// applies and HostMatchesDomain already compares with, for consumers holding
// host evidence from somewhere else - a configured domain, a request header, a
// host parsed by net/url directly - that must be compared against those facts
// under the same rule.
//
// The ASCII-only restriction is the whole point, and the reason to call this
// instead of strings.ToLower or strings.EqualFold: both have ASCII-PRODUCING
// mappings for a case operation on a host. strings.ToLower folds U+0130 LATIN
// CAPITAL LETTER I WITH DOT ABOVE to 'i' and U+212A KELVIN SIGN to 'k', and
// strings.EqualFold additionally reads U+017F LATIN SMALL LETTER LONG S as
// 's', so either one launders a homograph host into a canonical ASCII domain
// BEFORE the fail-closed gate (IsASCIIHost) can see the non-ASCII bytes it
// exists to reject. This fold cannot: a host that is not ASCII stays not
// ASCII.
//
// It folds case and nothing else - no trimming, no IDNA/punycode mapping, no
// percent-decoding (see the package docs for that boundary) - and it is not a
// substitute for the gate. Callers still run IsASCIIHost first; the fold makes
// host evidence safe to COMPARE case-insensitively, not safe to trust.
func FoldHostASCII(host string) string {
	return asciiLower(host)
}

// EqualASCIIFold reports whether a and b are equal under ASCII-only case
// folding: A-Z fold to a-z and nothing else does. It is FoldHostASCII's rule as
// a comparison (both read the single byte rule, so the two can never disagree),
// for the strings that are NOT hosts - a URL path token, a query parameter
// name, any fixed ASCII protocol token an untrusted string is matched against.
// A structural gate reading "/torrents.php" or "torrentid" out of an untrusted
// URL is doing that comparison, and calling a host fold on a path would misname
// the operation.
//
// The ASCII-only restriction is the whole point, and the reason not to reach
// for strings.EqualFold: full Unicode simple folding has ASCII-PRODUCING
// mappings, so it reads U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE as 'i',
// U+212A KELVIN SIGN as 'k' and U+017F LATIN SMALL LETTER LONG S as 's'. Under
// that fold a homograph spelling of a protocol token compares EQUAL to the
// ASCII token - "torrent\u0130d" passes a strings.EqualFold check for
// "torrentid" - which hands an evidence gate a match on bytes no server ever
// routes and no operator ever reads as that token. This fold cannot: a string
// that is not ASCII can never equal an ASCII token here.
//
// It folds case and nothing else: no trimming, no percent-decoding (see
// RawQueryNames for that reading), no Unicode normalization. Length is compared
// first because the fold is byte-length-preserving - unlike a Unicode fold,
// where differing lengths can still fold equal, which is the same property that
// makes strings.EqualFold's laundering possible.
func EqualASCIIFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if asciiLowerByte(a[i]) != asciiLowerByte(b[i]) {
			return false
		}
	}
	return true
}

// IsASCIIHost reports whether every byte of host is ASCII (below
// utf8.RuneSelf). It is the fail-closed companion of Form.Host's
// ASCII-only fold: a consumer matching host evidence against known ASCII
// domains gates on it first, so a homograph host (Cyrillic lookalikes, a
// fold-laundering U+0130 or U+212A) never string-matches a canonical
// domain. Callers that must ACCEPT international hosts convert punycode
// explicitly instead of relaxing this predicate.
func IsASCIIHost(host string) bool {
	for i := range len(host) {
		if host[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// HostMatchesDomain reports whether host equals domain or is a real
// dot-delimited subdomain of it. It is the matcher IsASCIIHost's documented
// scenario leads to: a consumer that has gated untrusted host evidence on
// IsASCIIHost then has to compare it against a known ASCII domain, and plain
// suffix matching is wrong in three ways this closes.
//
//   - Suffix confusion: "evilnyaa.si" ends in "nyaa.si" without being under
//     it, so the separating dot is required, not incidental.
//   - Parent-domain spoofing: "nyaa.si.evil.example" contains the domain but
//     is owned by evil.example, so the domain must be the SUFFIX, not a
//     substring.
//   - Empty DNS labels: a bare suffix test also accepts ".nyaa.si" (its
//     leading dot) and "a..nyaa.si" (an inner one). No resolvable DNS name
//     carries an empty label, so both forms are adversarial and every label
//     of the subdomain prefix must be non-empty.
//
// It is fail-closed and total. An empty host or domain matches nothing (an
// empty domain would otherwise match an empty host), and a domain that itself
// carries an empty label matches nothing rather than lending its malformation
// to the comparison. Comparison folds ASCII-only, for the same reason
// Form.Host does: a full-Unicode fold has ASCII-producing mappings that would
// launder a homograph host into a match. That fold is a convenience, NOT a
// substitute for the gate - a non-ASCII host can never equal an ASCII domain
// here, but callers still run IsASCIIHost first, because it is the check that
// refuses the evidence outright instead of merely failing to match one
// domain.
//
// It does NOT require an ASCII host, and deliberately so: a non-ASCII byte in
// a SUBDOMAIN label ("\u00e9.nyaa.si" against "nyaa.si") is a truthful match,
// since that host really is under the domain - the spoofing risk lives in the
// region aligned with the domain, which is compared byte-wise against ASCII
// and so cannot hold laundered bytes. Refusing non-ASCII evidence outright is
// IsASCIIHost's separate, stricter job, which is why consumers run it first
// rather than expecting this to imply it.
//
// Normalization beyond the ASCII fold stays the caller's: this compares the
// host it is given, so a caller holding raw evidence trims its own
// surrounding ASCII space and its own trailing root dot ("nyaa.si." does not
// match "nyaa.si") before calling. Doing it here would mean applying
// Unicode-aware trimming to a string whose non-ASCII bytes are exactly what
// the caller's gate exists to see.
func HostMatchesDomain(host, domain string) bool {
	if host == "" || domain == "" {
		return false
	}
	if hasEmptyLabel(domain) {
		return false
	}
	// Compared under the ASCII fold WITHOUT folding a copy of either argument.
	//
	// This used to be `host, domain = asciiLower(host), asciiLower(domain)`, and
	// that was the expensive line, not the "."+domain concatenation removed
	// alongside it. asciiLower copies the whole string as soon as it meets one
	// uppercase byte, and `host` is attacker-controlled: measured 72 bytes of
	// allocation on a 16-byte uppercase host rising to 28416 on an 8 KiB one,
	// about 3.5x the host, on a per-request security gate. EqualASCIIFold is the
	// same fold applied to a comparison instead of to a value and allocates
	// nothing at any length, so the whole predicate is now allocation-free
	// whatever case the caller was handed.
	//
	// hasEmptyLabel needs no fold: it looks for empty dot-delimited labels, and
	// case cannot create or remove one.
	if EqualASCIIFold(host, domain) {
		return true
	}
	// A subdomain is strictly longer than its domain, so equality above has
	// already handled the only case where cut could be zero.
	if len(host) <= len(domain) {
		return false
	}
	cut := len(host) - len(domain)
	if host[cut-1] != '.' {
		return false
	}
	if !EqualASCIIFold(host[cut:], domain) {
		return false
	}
	return !hasEmptyLabel(host[:cut-1])
}

// hasEmptyLabel reports whether any dot-delimited label of s is empty,
// including the degenerate empty string itself.
func hasEmptyLabel(s string) bool {
	if s == "" {
		return true
	}
	for label := range strings.SplitSeq(s, ".") {
		if label == "" {
			return true
		}
	}
	return false
}

// RawQueryNames iterates the percent-decoded parameter NAMES of a raw query
// string, in order, without consulting url.Values. It exists because the
// parsed view can be evaded: url.ParseQuery (and therefore u.Query()) drops a
// malformed pair WHOLESALE, so an unescaped semicolon deletes the pair it sits
// in - "apikey=SECRET;foo=x" disappears from the parsed map while the bytes
// stay in RawQuery for every outgoing request and every logged URL. A
// consumer whose gate must not be evadable therefore needs the raw reading,
// which is a strict superset of the parsed one:
//
//   - Pairs are split on BOTH '&' and ';' (the historic separator whose
//     removal from url.ParseQuery is what creates the gap), empty fields
//     skipped.
//   - The name is the text before the first '=' (a pair with no '=' yields
//     its whole field as a name, which is how a bare flag parameter reads).
//   - Each name is percent-decoded, so an encoded spelling cannot hide from a
//     literal comparison. A name whose escapes do not decode is yielded RAW
//     rather than skipped, so a malformed pair still reaches the caller's
//     predicate instead of vanishing the way the parsed view vanishes it.
//
// The iteration is judgment-free, like the Class facts: it reports names and
// takes no view of them, because consumers need opposite fail directions over
// the same walk - a credential-in-URL warning wants any suspicious name to
// match (over-matching is safe), while a structural identity gate wants only
// the name it recognizes (over-matching admits a URL it should refuse).
//
// The argument is a raw query WITHOUT its leading '?' - u.RawQuery's shape. A
// '?' is a legal literal inside a query, so it is not trimmed: a caller
// holding a whole URL takes u.RawQuery (or cuts at the first '?') rather than
// passing the URL, exactly as IsASCIIHost takes a host and not a URL.
//
// RawQueryPairs is the same walk carrying each name's VALUE alongside, for the
// consumers whose predicate reads it.
func RawQueryNames(rawQuery string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for pair := range strings.FieldsFuncSeq(rawQuery, isQuerySeparator) {
			name, _, _ := strings.Cut(pair, "=")
			if !yield(queryText(name)) {
				return
			}
		}
	}
}

// RawQueryPairs iterates the percent-decoded NAME and VALUE of each pair in a
// raw query string, in order, under RawQueryNames' parsing discipline and for
// the same reason: the parsed view can be evaded, so a gate that must not be
// evadable reads the wire instead. It is the companion for the consumers whose
// predicate needs the value too - a credential warning that must not fire on a
// parameter carrying nothing, a redaction pass that has to locate the secret
// text, an identity gate matching one expected id - and those are exactly the
// consumers url.ParseQuery fails hardest: it drops a malformed pair WHOLESALE,
// so the value disappears from the parsed map while the bytes stay in RawQuery
// for every outgoing request and every logged URL.
//
//   - Pairs are split on BOTH '&' and ';' (the historic separator whose
//     removal from url.ParseQuery creates the gap), empty fields skipped.
//   - The name is the text before the first '=' and the value is everything
//     after it, so a '=' inside a value is part of the value.
//   - Name and value are percent-decoded INDEPENDENTLY, each falling back to
//     its own raw text when its escapes do not decode, so one malformed half
//     never hides the other from the caller's predicate the way the parsed
//     view hides both.
//   - A field with no '=' yields its whole text as the name and an empty
//     value, which is how a bare flag parameter reads. "x" and "x=" are
//     therefore one reading here; a caller that must tell them apart reads
//     the raw field itself.
//
// The iteration is judgment-free, like RawQueryNames and the Class facts: it
// reports pairs and takes no view of them, because consumers need opposite
// fail directions over the same walk. The argument is a raw query WITHOUT its
// leading '?' - u.RawQuery's shape; a '?' is a legal literal inside a query,
// so it is not trimmed.
func RawQueryPairs(rawQuery string) iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for pair := range strings.FieldsFuncSeq(rawQuery, isQuerySeparator) {
			name, value, _ := strings.Cut(pair, "=")
			if !yield(queryText(name), queryText(value)) {
				return
			}
		}
	}
}

// queryText percent-decodes one raw query name or value, yielding the RAW text
// when the escapes do not decode rather than skipping it: a malformed pair must
// still reach the caller's predicate instead of vanishing the way the parsed
// view vanishes it. It is the single home of that decode rule, shared by the
// names-only and name-and-value walks so the two can never drift on what a
// pair reads as.
func queryText(s string) string {
	if decoded, err := url.QueryUnescape(s); err == nil {
		return decoded
	}
	return s
}

// isQuerySeparator reports whether r separates two query pairs under the raw
// reading: '&' plus the historic ';' url.ParseQuery no longer accepts.
func isQuerySeparator(r rune) bool {
	return r == '&' || r == ';'
}
