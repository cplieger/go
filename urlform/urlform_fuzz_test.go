package urlform

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzClassify checks the classifier's bounded-output and metamorphic
// invariants over untrusted link strings, never a second copy of the class
// rules; each check's failure message names the invariant it pins. The
// ASCII-only Host fold leaves non-ASCII homograph bytes for the fail-closed
// IsASCIIHost gates.
func FuzzClassify(f *testing.F) {
	f.Add("   ")
	f.Add("https://nyaa.si/view/1")
	f.Add("https://nyaa.si/\x7f")
	f.Add("https:/animebytes.tv/x")
	f.Add("https:animebytes.tv/x")
	f.Add("https://anime\tbytes.tv/x")
	f.Add("ht\ntps://animebytes.tv/x")
	f.Add("\x00\x1fhttps://animebytes.tv/x\x1f ")
	f.Add("animebytes.tv:443/x")
	f.Add("https://:443/x")
	f.Add("javascript:alert(1)")
	f.Add(`non-special:\\opaque\x`)
	f.Add("//animebytes.tv/x")
	f.Add("///animebytes.tv/x")
	f.Add(`\\animebytes.tv/x`)
	f.Add(`/\animebytes.tv/x`)
	f.Add("animebytes.tv/torrents.php?id=1")
	f.Add("ANIMEBYTES.TV/torrents.php?id=1")
	f.Add("https://an\u0130mebytes.tv/x")
	f.Add("?x:y")
	f.Add("foo bar@animebytes.tv/x")
	f.Add("https:/anime bytes@tv/x")
	f.Add("/torrents.php?id=1")
	f.Add("1a:b")
	f.Add("https://h/p\\q?a\\b#c\\d")
	f.Fuzz(func(t *testing.T, raw string) {
		fm := Classify(raw)

		switch fm.Class {
		case ClassEmpty, ClassMalformed, ClassAbsolute, ClassHiddenHost,
			ClassProtocolRelative, ClassSchemelessHost, ClassRelative:
		default:
			t.Errorf("Class = %v outside the enum for %q", fm.Class, raw)
		}
		if fm.Trimmed != strings.TrimSpace(fm.Trimmed) {
			t.Errorf("Trimmed = %q still carries surrounding whitespace", fm.Trimmed)
		}
		if strings.ContainsAny(fm.Trimmed, "\t\n\r") {
			t.Errorf("Trimmed = %q still carries embedded tab/newline after the preprocessing", fm.Trimmed)
		}
		noParse := fm.Class == ClassEmpty || fm.Class == ClassMalformed
		if noParse && (fm.Scheme != "" || fm.Port != "" || fm.HasUserInfo) {
			t.Errorf("Scheme=%q Port=%q HasUserInfo=%v without a parse for %q, want zero facts", fm.Scheme, fm.Port, fm.HasUserInfo, raw)
		}
		if fm.Host != asciiLower(fm.Host) {
			t.Errorf("Host = %q carries ASCII uppercase; the ASCII-only fold must apply", fm.Host)
		}

		// The scheme prefix schemeEnd delimits is pure ASCII, so the package's
		// ASCII fold and a Unicode fold are the same operation on it - which is
		// what makes canonicalizeSlashes' ASCII-only special-scheme test
		// behavior-identical rather than merely safe today. Stated as the
		// differential, so widening schemeEnd's alphabet to a byte a Unicode
		// fold maps into ASCII (U+0130 -> 'i', U+212A -> 'k') fails here
		// instead of silently changing which strings get their backslashes
		// read as host evidence.
		if end := schemeEnd(raw); end >= 0 {
			if prefix := raw[:end]; asciiLower(prefix) != strings.ToLower(prefix) {
				t.Errorf("schemeEnd(%q) delimited %q, where the ASCII fold and a Unicode fold disagree", raw, prefix)
			}
		}
		switch fm.Class {
		case ClassEmpty, ClassMalformed, ClassRelative:
			if fm.Host != "" {
				t.Errorf("Host = %q for class %v, want empty (the class carries no extractable host evidence)", fm.Host, fm.Class)
			}
		case ClassAbsolute:
			if fm.Host == "" {
				t.Errorf("Host empty for ClassAbsolute input %q", raw)
			}
		}
		if fm.HostUnrecoverable && fm.Class != ClassSchemelessHost && fm.Class != ClassHiddenHost {
			t.Errorf("HostUnrecoverable set on class %v, want only the authority-reparse classes", fm.Class)
		}

		// The normalized path reading: rooted or absent, free of the dot
		// segments its whole job is to resolve, bounded by the input it came
		// from (rooting adds at most one byte and decoding only shrinks), and
		// absent entirely for the two no-facts classes.
		np := fm.NormalizedPath()
		if np != "" && np[0] != '/' {
			t.Errorf("NormalizedPath = %q for %q, want it rooted or empty", np, raw)
		}
		for segment := range strings.SplitSeq(np, "/") {
			if segment == "." || segment == ".." {
				t.Errorf("NormalizedPath = %q for %q still carries a %q segment", np, raw, segment)
			}
		}
		if len(np) > len(fm.Trimmed)+1 {
			t.Errorf("NormalizedPath = %q (%d bytes) for trimmed %q (%d bytes), want a bounded reading", np, len(np), fm.Trimmed, len(fm.Trimmed))
		}
		if noParse && np != "" {
			t.Errorf("NormalizedPath = %q for class %v, want empty (the class carries no facts)", np, fm.Class)
		}

		// The WHATWG tab/newline removal is position-independent: stripping
		// the bytes from the raw input up front must yield identical facts,
		// with only the smuggling flag differing. The law is stated over
		// valid UTF-8 because the removal itself (strings.Map, in this
		// package and in this test) also rewrites an invalid byte to U+FFFD:
		// on invalid input the transform is not information-preserving, so
		// its premise - that the two strings differ only in the removed
		// bytes - does not hold. Every other invariant above still runs on
		// invalid input.
		if utf8.ValidString(raw) {
			stripped := Classify(strings.Map(dropTabOrNewline, raw))
			if stripped.HasTabOrNewline {
				t.Errorf("Classify of a tab/newline-free input set HasTabOrNewline for %q", raw)
			}
			if stripped.Class != fm.Class || stripped.Host != fm.Host || stripped.Trimmed != fm.Trimmed ||
				stripped.Scheme != fm.Scheme || stripped.Port != fm.Port || stripped.HasUserInfo != fm.HasUserInfo ||
				stripped.HasBackslash != fm.HasBackslash || stripped.HostUnrecoverable != fm.HostUnrecoverable ||
				stripped.NormalizedPath() != np {
				t.Errorf("Classify(%q) diverges from its tab/newline-stripped form on facts other than HasTabOrNewline", raw)
			}
		}

		// Stability: the preprocessed string re-classifies to the same
		// facts, with the preprocessing flag cleared (nothing left to strip).
		again := Classify(fm.Trimmed)
		if again.HasTabOrNewline {
			t.Errorf("re-classifying Trimmed %q set HasTabOrNewline", fm.Trimmed)
		}
		if again.Class != fm.Class || again.Host != fm.Host || again.Trimmed != fm.Trimmed ||
			again.Scheme != fm.Scheme || again.Port != fm.Port || again.HasUserInfo != fm.HasUserInfo ||
			again.HasBackslash != fm.HasBackslash || again.HostUnrecoverable != fm.HostUnrecoverable ||
			again.NormalizedPath() != np {
			t.Errorf("Classify(%q) is not stable on its own Trimmed form %q", raw, fm.Trimmed)
		}
	})
}

// FuzzHostMatchesDomain fuzzes the fail-closed host-evidence matcher with the
// security invariants it exists to hold, never a reimplementation of the rule:
// a match implies the ASCII-folded host either equals the folded domain or ends
// in "."+domain (so no substring or parent-domain spoof can match); a match
// implies neither side carries an empty DNS label; a non-ASCII host never
// matches an ASCII domain (the ASCII-only fold cannot launder a homograph into
// an outright match, which is what keeps IsASCIIHost's gate meaningful - note
// this is stated on the EQUALITY arm only, since a non-ASCII SUBDOMAIN label
// under an ASCII domain is a truthful match, not a spoof); the relation is
// reflexive exactly for a well-formed non-empty host; and an empty host or
// domain never matches.
func FuzzHostMatchesDomain(f *testing.F) {
	f.Add("nyaa.si", "nyaa.si")
	f.Add("sukebei.nyaa.si", "nyaa.si")
	f.Add("evilnyaa.si", "nyaa.si")
	f.Add("nyaa.si.evil.example", "nyaa.si")
	f.Add(".nyaa.si", "nyaa.si")
	f.Add("a..nyaa.si", "nyaa.si")
	f.Add("an\u0130mebytes.tv", "animebytes.tv")
	f.Add("rutrac\u212Aer.org", "rutracker.org")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, host, domain string) {
		got := HostMatchesDomain(host, domain)

		if host == "" || domain == "" {
			if got {
				t.Fatalf("HostMatchesDomain(%q, %q) = true, want false for an empty side", host, domain)
			}
			return
		}

		if got {
			lowHost, lowDomain := asciiLower(host), asciiLower(domain)
			if lowHost != lowDomain && !strings.HasSuffix(lowHost, "."+lowDomain) {
				t.Errorf("HostMatchesDomain(%q, %q) = true but the host is neither the domain nor under it", host, domain)
			}
			if hasEmptyLabel(host) || hasEmptyLabel(domain) {
				t.Errorf("HostMatchesDomain(%q, %q) = true with an empty DNS label", host, domain)
			}
			if !IsASCIIHost(host) && IsASCIIHost(domain) && len(host) == len(domain) {
				t.Errorf("HostMatchesDomain(%q, %q) = true: a non-ASCII host matched an ASCII domain outright", host, domain)
			}
		}

		// Reflexivity holds exactly for a well-formed name: a value carrying an
		// empty label is refused even against itself, so the malformation never
		// lends itself to a match.
		if want := !hasEmptyLabel(host); HostMatchesDomain(host, host) != want {
			t.Errorf("HostMatchesDomain(%q, %q) = %v, want %v", host, host, !want, want)
		}
	})
}

// FuzzRawQueryNames fuzzes the raw-query name walk with the superset invariant
// that is its whole reason to exist: every key url.ParseQuery recovers from a
// query must also be reported by the raw walk (the raw reading is a strict
// superset of the parsed one, so a gate built on it cannot be evaded by a
// smuggled separator the parsed view drops). Plus the bounded-work invariants:
// the walk terminates, yields no more names than the input could hold, and
// never yields a name longer than the input.
func FuzzRawQueryNames(f *testing.F) {
	f.Add("")
	f.Add("id=1")
	f.Add("apikey=SECRET;foo=x")
	f.Add("torrent%69d=1")
	f.Add("api%zzkey=1&ok=2")
	f.Add("&&a=1;;&b=2&")
	f.Add("?apikey=1")

	f.Fuzz(func(t *testing.T, rawQuery string) {
		seen := make(map[string]bool)
		count := 0
		for name := range RawQueryNames(rawQuery) {
			count++
			seen[name] = true
			if len(name) > len(rawQuery) {
				t.Fatalf("RawQueryNames(%q) yielded a longer name %q", rawQuery, name)
			}
			if count > len(rawQuery)+1 {
				t.Fatalf("RawQueryNames(%q) yielded more names than the input can hold", rawQuery)
			}
		}

		parsed, err := url.ParseQuery(rawQuery)
		if err != nil {
			// A query with an undecodable escape fails ParseQuery wholesale;
			// the raw walk deliberately still reports its names, so there is
			// no parsed set to compare against.
			return
		}
		for key := range parsed {
			if !seen[key] {
				t.Errorf("RawQueryNames(%q) dropped key %q that url.ParseQuery recovered", rawQuery, key)
			}
		}
	})
}

// FuzzRawQueryPairs fuzzes the name-and-value walk with the two invariants
// that make it substitutable for the parsed view: it reports exactly the names
// the names-only walk reports (the two share one split-and-decode discipline,
// so a gate keyed on one reading and a redaction pass keyed on the other can
// never disagree about which parameters a URL carries), and every name/value
// url.ParseQuery recovers is also reported here (the raw reading is a strict
// superset of the parsed one, so a gate built on it cannot be evaded by a
// smuggled separator the parsed view drops). Plus the bounded-work invariants:
// the walk terminates and yields neither more pairs nor longer text than the
// input can hold.
func FuzzRawQueryPairs(f *testing.F) {
	f.Add("")
	f.Add("id=1")
	f.Add("apikey=SECRET;foo=x")
	f.Add("torrent%69d=%2Fview%2F1")
	f.Add("api%zzkey=%zz")
	f.Add("&&a=1;;&b=2&")
	f.Add("q=a=b=c")
	f.Add("torrentid")
	f.Add("id=1&id=2")
	f.Add("?apikey=1")

	f.Fuzz(func(t *testing.T, rawQuery string) {
		values := make(map[string][]string)
		var names []string
		for name, value := range RawQueryPairs(rawQuery) {
			names = append(names, name)
			values[name] = append(values[name], value)
			if len(name) > len(rawQuery) || len(value) > len(rawQuery) {
				t.Fatalf("RawQueryPairs(%q) yielded oversized pair (%q, %q)", rawQuery, name, value)
			}
			if len(names) > len(rawQuery)+1 {
				t.Fatalf("RawQueryPairs(%q) yielded more pairs than the input can hold", rawQuery)
			}
		}

		var wantNames []string
		for name := range RawQueryNames(rawQuery) {
			wantNames = append(wantNames, name)
		}
		if !slices.Equal(names, wantNames) {
			t.Fatalf("RawQueryPairs(%q) names = %q, want RawQueryNames' %q", rawQuery, names, wantNames)
		}

		parsed, err := url.ParseQuery(rawQuery)
		if err != nil {
			// A semicolon separator or an undecodable escape fails ParseQuery
			// wholesale; the raw walk deliberately still reports those pairs,
			// so there is no parsed set to compare against.
			return
		}
		for key, parsedValues := range parsed {
			for _, want := range parsedValues {
				if !slices.Contains(values[key], want) {
					t.Errorf("RawQueryPairs(%q) dropped %q=%q that url.ParseQuery recovered", rawQuery, key, want)
				}
			}
		}
	})
}

// FuzzEqualASCIIFold fuzzes the ASCII-only comparison with the laws that keep it
// substitutable for the host fold and stricter than the stdlib one, never a
// reimplementation of the byte rule: the comparison agrees with folding both
// sides (FoldHostASCII), so the package's three ASCII-fold spellings can never
// drift - the invalid-UTF-8 inputs matter most here, since that is the only
// class where a byte-wise comparison and a rune-mapping fold could disagree; the
// relation is reflexive and symmetric; a match implies the two strings differ
// ONLY in ASCII-letter case, so no non-ASCII byte was ever folded and a match
// implies equal byte length; and every match is also a strings.EqualFold match,
// which states the security posture as a subset relation - this fold accepts a
// strict subset of what Unicode folding accepts, so it can never be the looser
// of the two on a homograph spelling of an ASCII protocol token.
func FuzzEqualASCIIFold(f *testing.F) {
	f.Add("", "")
	f.Add("torrentid", "torrentid")
	f.Add("TorrentID", "torrentid")
	f.Add("/TORRENTS.PHP", "/torrents.php")
	f.Add("torrent\u0130d", "torrentid")
	f.Add("api\u212Aey", "apikey")
	f.Add("/torrent\u017F.php", "/torrents.php")
	f.Add("ny\xffaa.si", "ny\uFFFDaa.si")
	f.Add("NY\xffAA.SI", "ny\xffaa.si")
	f.Add("\u00c9.nyaa.si", "\u00e9.nyaa.si")

	f.Fuzz(func(t *testing.T, a, b string) {
		got := EqualASCIIFold(a, b)

		if want := FoldHostASCII(a) == FoldHostASCII(b); got != want {
			t.Fatalf("EqualASCIIFold(%q, %q) = %v but folding both sides says %v", a, b, got, want)
		}
		if !EqualASCIIFold(a, a) {
			t.Errorf("EqualASCIIFold(%q, %q) = false, want reflexive", a, a)
		}
		if EqualASCIIFold(b, a) != got {
			t.Errorf("EqualASCIIFold(%q, %q) is asymmetric", a, b)
		}

		if !got {
			return
		}
		if len(a) != len(b) {
			t.Fatalf("EqualASCIIFold(%q, %q) = true across differing byte lengths %d and %d", a, b, len(a), len(b))
		}
		for i := range len(a) {
			if a[i] == b[i] {
				continue
			}
			ca, cb := a[i], b[i]
			if ca >= utf8.RuneSelf || cb >= utf8.RuneSelf {
				t.Errorf("EqualASCIIFold(%q, %q) = true while byte %d differs on a non-ASCII byte (%#x vs %#x)", a, b, i, ca, cb)
				continue
			}
			if ca|caseBit != cb|caseBit || !isASCIILetter(ca) || !isASCIILetter(cb) {
				t.Errorf("EqualASCIIFold(%q, %q) = true while byte %d differs by more than ASCII case (%#x vs %#x)", a, b, i, ca, cb)
			}
		}
		if !strings.EqualFold(a, b) {
			t.Errorf("EqualASCIIFold(%q, %q) = true where strings.EqualFold = false; the ASCII fold must accept a strict subset", a, b)
		}
	})
}

// caseBit is the single bit that separates an ASCII letter's two cases, used by
// the fuzz law to state "differs by ASCII case only" without restating the
// package's fold.
const caseBit = 'a' - 'A'

// isASCIILetter reports whether c is an ASCII letter in either case.
func isASCIILetter(c byte) bool {
	lower := c | caseBit
	return lower >= 'a' && lower <= 'z'
}
