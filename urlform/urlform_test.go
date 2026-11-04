package urlform

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

// TestClassify pins one example per structural class plus the
// backslash-canonicalization and host-extraction facts the two consumer
// fail directions branch on (publish-or-drop, extract-evidence-or-hide).
func TestClassify(t *testing.T) {
	tests := []struct {
		name              string
		raw               string
		wantClass         Class
		wantHost          string
		wantBackslash     bool
		wantTabOrNewline  bool
		wantUnrecoverable bool
	}{
		{name: "empty after trimming", raw: "   ", wantClass: ClassEmpty},
		{name: "unparseable control character", raw: "https://nyaa.si/\x7f", wantClass: ClassMalformed},
		{name: "digit-led first segment with colon is malformed", raw: "1a:b", wantClass: ClassMalformed},
		{name: "absolute with host", raw: " https://NYAA.si/view/1 ", wantClass: ClassAbsolute, wantHost: "nyaa.si"},
		{name: "non-http scheme still classifies absolute", raw: "ftp://animebytes.tv/x", wantClass: ClassAbsolute, wantHost: "animebytes.tv"},
		{name: "scheme-relative special form recovers its hidden host", raw: "https:/animebytes.tv/x", wantClass: ClassHiddenHost, wantHost: "animebytes.tv"},
		{name: "zero-slash special form recovers its hidden host", raw: "https:animebytes.tv/x", wantClass: ClassHiddenHost, wantHost: "animebytes.tv"},
		{name: "hidden-host recovery fails on an unparseable authority", raw: "https:/anime bytes@tv/x", wantClass: ClassHiddenHost, wantUnrecoverable: true},
		{name: "opaque host-as-scheme hides its host", raw: "animebytes.tv:443/x", wantClass: ClassHiddenHost},
		{name: "port-only authority hides its host", raw: "https://:443/x", wantClass: ClassHiddenHost},
		{name: "javascript scheme is hidden-host, not absolute", raw: "javascript:alert(1)", wantClass: ClassHiddenHost},
		{name: "non-special backslashes stay opaque, no fabricated host", raw: `non-special:\\opaque\x`, wantClass: ClassHiddenHost, wantBackslash: true},
		{
			// The scheme grammar is what decides whether a form's backslashes
			// become host evidence, so this token spells every character class
			// the grammar allows - letters in both cases, digits, "+", "-",
			// "." - at both ends of each range. Read the alphabet any narrower
			// and the form is taken for schemeless, its backslashes are
			// rewritten to slashes, and the authority "//host" appears out of
			// nothing a browser ever reads there.
			name:          "non-special scheme spelled with the full grammar keeps its backslashes ordinary",
			raw:           `azAZ09+-.:\\host/x`,
			wantClass:     ClassHiddenHost,
			wantBackslash: true,
		},
		{name: "protocol-relative with host", raw: "//animebytes.tv/x", wantClass: ClassProtocolRelative, wantHost: "animebytes.tv"},
		{name: "three slashes are ambiguous protocol-relative without host", raw: "///animebytes.tv/x", wantClass: ClassProtocolRelative},
		{name: "backslash authority canonicalizes to protocol-relative", raw: `\\animebytes.tv/x`, wantClass: ClassProtocolRelative, wantHost: "animebytes.tv", wantBackslash: true},
		{name: "slash-backslash canonicalizes to protocol-relative", raw: `/\animebytes.tv/x`, wantClass: ClassProtocolRelative, wantHost: "animebytes.tv", wantBackslash: true},
		{name: "schemeless host recovers the authority", raw: "animebytes.tv/torrents.php?id=1", wantClass: ClassSchemelessHost, wantHost: "animebytes.tv"},
		{name: "query-only form is schemeless without evidence", raw: "?x:y", wantClass: ClassSchemelessHost},
		{name: "space before @ makes the authority reparse fail", raw: "foo bar@animebytes.tv/x", wantClass: ClassSchemelessHost, wantUnrecoverable: true},
		{name: "rooted relative path", raw: "/torrents.php?id=1", wantClass: ClassRelative},
		{name: "embedded tab is stripped before the parse", raw: "https://anime\tbytes.tv/x", wantClass: ClassAbsolute, wantHost: "animebytes.tv", wantTabOrNewline: true},
		{name: "embedded newlines reassemble the scheme", raw: "ht\ntps://animebytes.tv/x", wantClass: ClassAbsolute, wantHost: "animebytes.tv", wantTabOrNewline: true},
		{name: "edge C0 controls trim like the browser", raw: "\x00\x1fhttps://animebytes.tv/x\x1f ", wantClass: ClassAbsolute, wantHost: "animebytes.tv"},
		{name: "interior non-tab C0 control stays malformed", raw: "https://anime\x01bytes.tv/x", wantClass: ClassMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Classify(tt.raw)
			if f.Class != tt.wantClass {
				t.Errorf("Class = %v, want %v", f.Class, tt.wantClass)
			}
			if f.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", f.Host, tt.wantHost)
			}
			if f.HasBackslash != tt.wantBackslash {
				t.Errorf("HasBackslash = %v, want %v", f.HasBackslash, tt.wantBackslash)
			}
			if f.HasTabOrNewline != tt.wantTabOrNewline {
				t.Errorf("HasTabOrNewline = %v, want %v", f.HasTabOrNewline, tt.wantTabOrNewline)
			}
			if f.HostUnrecoverable != tt.wantUnrecoverable {
				t.Errorf("HostUnrecoverable = %v, want %v", f.HostUnrecoverable, tt.wantUnrecoverable)
			}
		})
	}
}

// TestClassifySemanticFacts pins the positive extraction of the
// semantic facts a link publisher's gate keys its rejections on - Scheme,
// Port, and HasUserInfo - which the class-focused table never asserts
// non-zero:
// url.Parse folds the scheme to lowercase (an "HTTPS://" source reads
// "https"), the port string is extracted unvalidated (65536 passes through;
// range-checking is deliberately the consumer's job, per the Port doc), and
// a userinfo authority ("trusted@evil.example", the visual-spoofing vector)
// sets HasUserInfo on absolute and protocol-relative forms alike.
func TestClassifySemanticFacts(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantClass    Class
		wantHost     string
		wantScheme   string
		wantPort     string
		wantUserInfo bool
	}{
		{name: "uppercase scheme folds to lowercase", raw: "HTTPS://nyaa.si/x", wantClass: ClassAbsolute, wantHost: "nyaa.si", wantScheme: "https"},
		{name: "port extracted from absolute authority", raw: "https://nyaa.si:8080/x", wantClass: ClassAbsolute, wantHost: "nyaa.si", wantScheme: "https", wantPort: "8080"},
		{name: "userinfo spoof authority sets the flag", raw: "https://trusted@evil.example/x", wantClass: ClassAbsolute, wantHost: "evil.example", wantScheme: "https", wantUserInfo: true},
		{name: "out-of-range port passes through unvalidated", raw: "https://user:pass@animebytes.tv:65536/x", wantClass: ClassAbsolute, wantHost: "animebytes.tv", wantScheme: "https", wantPort: "65536", wantUserInfo: true},
		{name: "userinfo on a protocol-relative form", raw: "//user@animebytes.tv/x", wantClass: ClassProtocolRelative, wantHost: "animebytes.tv", wantUserInfo: true},
		{name: "userinfo recovered from a schemeless authority reparse", raw: "user@animebytes.tv/x", wantClass: ClassSchemelessHost, wantHost: "animebytes.tv", wantUserInfo: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Classify(tt.raw)
			if f.Class != tt.wantClass {
				t.Errorf("Class = %v, want %v", f.Class, tt.wantClass)
			}
			if f.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", f.Host, tt.wantHost)
			}
			if f.Scheme != tt.wantScheme {
				t.Errorf("Scheme = %q, want %q", f.Scheme, tt.wantScheme)
			}
			if f.Port != tt.wantPort {
				t.Errorf("Port = %q, want %q", f.Port, tt.wantPort)
			}
			if f.HasUserInfo != tt.wantUserInfo {
				t.Errorf("HasUserInfo = %v, want %v", f.HasUserInfo, tt.wantUserInfo)
			}
		})
	}
}

// TestClassifyHomographHostsFailClosed pins the homograph contract
// between Classify's ASCII-only Host fold (asciiLower) and the
// fail-closed IsASCIIHost gate consumers key on: a host spelled with a
// fold-laundering codepoint (U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE ->
// ASCII 'i', U+212A KELVIN SIGN -> ASCII 'k' under strings.ToLower) must
// survive classification with its non-ASCII bytes intact so the gate rejects
// it - a full-Unicode fold here would launder the spoof into a canonical
// ASCII domain and hand every consumer an already-matchable Host.
func TestClassifyHomographHostsFailClosed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "U+0130 dotted capital I, absolute form", raw: "https://an\u0130mebytes.tv/torrents.php?id=1"},
		{name: "U+0130 dotted capital I, schemeless form", raw: "an\u0130mebytes.tv/torrents.php?id=1"},
		{name: "U+212A kelvin sign, absolute form", raw: "https://rutrac\u212Aer.org/forum/viewtopic.php?t=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Classify(tt.raw)
			if f.Host == "" {
				t.Fatal("Host is empty, want the non-ASCII host evidence preserved")
			}
			if IsASCIIHost(f.Host) {
				t.Errorf("IsASCIIHost(%q) = true: the fold laundered the homograph bytes into ASCII", f.Host)
			}
		})
	}
}

// TestHostMatchesDomain pins the three unsafe readings a plain suffix test
// would accept (suffix confusion, parent-domain spoofing, empty DNS labels),
// the fail-closed handling of a degenerate host or domain, and the ASCII-only
// fold - including the homograph case, where the fold must NOT produce a match
// the IsASCIIHost gate exists to refuse.
func TestHostMatchesDomain(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		domain string
		want   bool
	}{
		{name: "exact match", host: "nyaa.si", domain: "nyaa.si", want: true},
		{name: "real subdomain", host: "sukebei.nyaa.si", domain: "nyaa.si", want: true},
		{name: "deep subdomain", host: "a.b.nyaa.si", domain: "nyaa.si", want: true},
		{name: "ascii case folds", host: "SukeBei.NYAA.si", domain: "nyaa.si", want: true},
		{name: "domain spelled in caps folds", host: "nyaa.si", domain: "NYAA.SI", want: true},

		{name: "suffix confusion without the separating dot", host: "evilnyaa.si", domain: "nyaa.si"},
		{name: "domain as a parent-spoofing prefix", host: "nyaa.si.evil.example", domain: "nyaa.si"},
		{name: "domain as an interior substring", host: "a.nyaa.si.b.example", domain: "nyaa.si"},
		{name: "leading empty label", host: ".nyaa.si", domain: "nyaa.si"},
		{name: "inner empty label", host: "a..nyaa.si", domain: "nyaa.si"},
		{name: "trailing root dot is the caller's to trim", host: "nyaa.si.", domain: "nyaa.si"},
		{name: "surrounding space is the caller's to trim", host: " nyaa.si", domain: "nyaa.si"},

		{name: "empty host", host: "", domain: "nyaa.si"},
		{name: "empty domain", host: "nyaa.si", domain: ""},
		{name: "both empty", host: "", domain: ""},
		{name: "malformed domain with a leading dot, matched exactly", host: ".nyaa.si", domain: ".nyaa.si"},
		{name: "malformed domain with an inner empty label", host: "a.nyaa..si", domain: "nyaa..si"},

		{name: "U+0130 homograph host cannot fold into the domain", host: "an\u0130mebytes.tv", domain: "animebytes.tv"},
		{name: "U+212A homograph host cannot fold into the domain", host: "rutrac\u212Aer.org", domain: "rutracker.org"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HostMatchesDomain(tt.host, tt.domain); got != tt.want {
				t.Errorf("HostMatchesDomain(%q, %q) = %v, want %v", tt.host, tt.domain, got, tt.want)
			}
		})
	}
}

// TestRawQueryNames pins the raw reading's whole contract: the ';' split that
// url.ParseQuery's wholesale pair drop creates the need for, percent-decoding,
// the raw-name fallback for an undecodable escape, order preservation, and the
// documented no-'?'-trimming boundary.
func TestRawQueryNames(t *testing.T) {
	tests := []struct {
		name     string
		rawQuery string
		want     []string
	}{
		{name: "empty query yields nothing", rawQuery: ""},
		{name: "single pair", rawQuery: "id=1", want: []string{"id"}},
		{name: "ampersand separated", rawQuery: "id=1&torrentid=2", want: []string{"id", "torrentid"}},
		{
			name:     "semicolon separated - the pair url.ParseQuery drops wholesale",
			rawQuery: "apikey=SECRET;foo=x",
			want:     []string{"apikey", "foo"},
		},
		{name: "mixed separators", rawQuery: "a=1;b=2&c=3", want: []string{"a", "b", "c"}},
		{name: "empty fields skipped", rawQuery: "&&a=1;;&b=2&", want: []string{"a", "b"}},
		{name: "bare flag parameter yields its whole field", rawQuery: "torrentid", want: []string{"torrentid"}},
		{name: "value containing a separator does not leak into a name", rawQuery: "id=1;2", want: []string{"id", "2"}},
		{name: "percent-encoded name decodes", rawQuery: "torrent%69d=1", want: []string{"torrentid"}},
		{name: "plus in a name decodes to a space", rawQuery: "api+key=1", want: []string{"api key"}},
		{
			name:     "undecodable escape yields the raw name rather than vanishing",
			rawQuery: "api%zzkey=1&ok=2",
			want:     []string{"api%zzkey", "ok"},
		},
		{
			name:     "leading '?' is part of the first name, not trimmed",
			rawQuery: "?apikey=1&b=2",
			want:     []string{"?apikey", "b"},
		},
		{name: "duplicate names are reported once each", rawQuery: "id=1&id=2", want: []string{"id", "id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for name := range RawQueryNames(tt.rawQuery) {
				got = append(got, name)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("RawQueryNames(%q) = %q, want %q", tt.rawQuery, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("RawQueryNames(%q) = %q, want %q", tt.rawQuery, got, tt.want)
				}
			}
		})
	}
}

// TestRawQueryNamesStopsOnBreak pins the iterator contract a partial consumer
// depends on: a caller that breaks out (every gate does, on its first match)
// stops the walk instead of running it to completion.
func TestRawQueryNamesStopsOnBreak(t *testing.T) {
	visited := 0
	for range RawQueryNames("a=1&b=2&c=3") {
		visited++
		break
	}
	if visited != 1 {
		t.Errorf("visited %d names after break, want 1", visited)
	}
}

// TestRawQueryNamesSupersetOfParsedView is the cross-check that states WHY the
// raw walk exists: for a query whose pairs are all well-formed the two
// readings agree, and for the semicolon-smuggled query the parsed view loses a
// name the raw walk still reports.
func TestRawQueryNamesSupersetOfParsedView(t *testing.T) {
	const smuggled = "apikey=SECRET;foo=x"
	parsed, err := url.ParseQuery(smuggled)
	if err == nil {
		if _, ok := parsed["apikey"]; ok {
			t.Fatalf("url.ParseQuery(%q) kept apikey; the premise of the raw walk no longer holds", smuggled)
		}
	}
	found := false
	for name := range RawQueryNames(smuggled) {
		if name == "apikey" {
			found = true
		}
	}
	if !found {
		t.Errorf("RawQueryNames(%q) did not report apikey", smuggled)
	}
}

// TestRawQueryPairs pins the name-and-value reading's whole contract: the ';'
// split url.ParseQuery's wholesale pair drop creates the need for, independent
// percent-decoding of each half with a raw fallback per half, the empty value a
// bare flag and an explicit "x=" both read as, a '=' inside a value staying in
// the value, repeated names each keeping their own value, and order
// preservation.
func TestRawQueryPairs(t *testing.T) {
	type pair struct {
		name  string
		value string
	}
	tests := []struct {
		name     string
		rawQuery string
		want     []pair
	}{
		{name: "empty query yields nothing", rawQuery: ""},
		{name: "single pair", rawQuery: "id=1", want: []pair{{"id", "1"}}},
		{name: "ampersand separated", rawQuery: "id=1&torrentid=2", want: []pair{{"id", "1"}, {"torrentid", "2"}}},
		{
			name:     "semicolon separated - the pair url.ParseQuery drops wholesale",
			rawQuery: "apikey=SECRET;foo=x",
			want:     []pair{{"apikey", "SECRET"}, {"foo", "x"}},
		},
		{name: "mixed separators", rawQuery: "a=1;b=2&c=3", want: []pair{{"a", "1"}, {"b", "2"}, {"c", "3"}}},
		{name: "empty fields skipped", rawQuery: "&&a=1;;&b=2&", want: []pair{{"a", "1"}, {"b", "2"}}},
		{name: "bare flag parameter has an empty value", rawQuery: "torrentid", want: []pair{{"torrentid", ""}}},
		{name: "explicit empty value reads the same as a bare flag", rawQuery: "torrentid=", want: []pair{{"torrentid", ""}}},
		{name: "equals inside the value stays in the value", rawQuery: "q=a=b=c", want: []pair{{"q", "a=b=c"}}},
		{
			name:     "percent-encoded name and value both decode",
			rawQuery: "torrent%69d=%2Fview%2F1",
			want:     []pair{{"torrentid", "/view/1"}},
		},
		{name: "plus decodes to a space in both halves", rawQuery: "api+key=two+words", want: []pair{{"api key", "two words"}}},
		{
			name:     "undecodable value yields its raw text while the name still decodes",
			rawQuery: "api%6bey=%zz",
			want:     []pair{{"apikey", "%zz"}},
		},
		{
			name:     "undecodable name yields its raw text while the value still decodes",
			rawQuery: "api%zzkey=%2Fx",
			want:     []pair{{"api%zzkey", "/x"}},
		},
		{name: "repeated names keep their own values", rawQuery: "id=1&id=2", want: []pair{{"id", "1"}, {"id", "2"}}},
		{
			name:     "a separator inside a value starts a new pair, not a value",
			rawQuery: "id=1;2",
			want:     []pair{{"id", "1"}, {"2", ""}},
		},
		{
			name:     "leading '?' is part of the first name, not trimmed",
			rawQuery: "?apikey=1&b=2",
			want:     []pair{{"?apikey", "1"}, {"b", "2"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []pair
			for name, value := range RawQueryPairs(tt.rawQuery) {
				got = append(got, pair{name, value})
			}
			if len(got) != len(tt.want) {
				t.Fatalf("RawQueryPairs(%q) = %v, want %v", tt.rawQuery, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("RawQueryPairs(%q) = %v, want %v", tt.rawQuery, got, tt.want)
				}
			}
		})
	}
}

// TestRawQueryPairsStopsOnBreak pins the iterator contract a partial consumer
// depends on: a caller that breaks out on its first match stops the walk
// instead of running it to completion.
func TestRawQueryPairsStopsOnBreak(t *testing.T) {
	visited := 0
	for range RawQueryPairs("a=1&b=2&c=3") {
		visited++
		break
	}
	if visited != 1 {
		t.Errorf("visited %d pairs after break, want 1", visited)
	}
}

// TestRawQueryPairsNamesMatchRawQueryNames is the drift guard between the two
// raw walks: they share one split-and-decode discipline, so the names the
// pair walk reports must be exactly the names-only walk's sequence. A
// divergence would mean a gate keyed on one reading and a redaction pass keyed
// on the other disagree about which parameters a URL carries.
func TestRawQueryPairsNamesMatchRawQueryNames(t *testing.T) {
	queries := []string{
		"",
		"id=1",
		"apikey=SECRET;foo=x",
		"&&a=1;;&b=2&",
		"torrentid",
		"torrent%69d=1",
		"api%zzkey=1&ok=2",
		"?apikey=1&b=2",
		"id=1;2",
		"a=1&a=2",
	}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			var names, pairNames []string
			for name := range RawQueryNames(q) {
				names = append(names, name)
			}
			for name := range RawQueryPairs(q) {
				pairNames = append(pairNames, name)
			}
			if !slices.Equal(names, pairNames) {
				t.Errorf("RawQueryNames(%q) = %q but RawQueryPairs names = %q", q, names, pairNames)
			}
		})
	}
}

// TestFoldHostASCII pins the exported fold's contract and, more importantly,
// the two stdlib case operations it exists to NOT be: strings.ToLower, whose
// ASCII-producing mappings launder U+0130 into 'i' and U+212A into 'k', and
// strings.EqualFold, which reads U+017F as 's'. A host that is not ASCII must
// stay not ASCII through the fold, so the fail-closed IsASCIIHost gate still
// sees the homograph evidence it exists to reject.
func TestFoldHostASCII(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "empty stays empty"},
		{name: "ascii uppercase folds", host: "NYAA.SI", want: "nyaa.si"},
		{name: "mixed case folds", host: "SukeBei.NYAA.si", want: "sukebei.nyaa.si"},
		{name: "already folded is unchanged", host: "animebytes.tv", want: "animebytes.tv"},
		{name: "digits, dots and hyphens are untouched", host: "A1-b2.Example", want: "a1-b2.example"},
		{
			name: "every ASCII uppercase letter folds",
			host: "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
			want: "abcdefghijklmnopqrstuvwxyz",
		},
		{
			name: "U+0130 survives while the ASCII around it folds",
			host: "AN\u0130MEBYTES.TV",
			want: "an\u0130mebytes.tv",
		},
		{
			name: "U+212A kelvin sign survives while the ASCII around it folds",
			host: "RUTRAC\u212AER.ORG",
			want: "rutrac\u212Aer.org",
		},
		{
			name: "U+017F long s survives, so it can never equal 's'",
			host: "\u017FUKEBEI.NYAA.SI",
			want: "\u017Fukebei.nyaa.si",
		},
		{name: "non-ASCII with no ASCII case mapping is untouched", host: "\u00c9.nyaa.si", want: "\u00c9.nyaa.si"},
		{
			// The fold walks bytes, so an invalid UTF-8 byte survives as
			// itself: a rune-mapping fold would rewrite it to U+FFFD, which
			// both contradicts "every other byte untouched" and launders
			// distinct invalid host evidence onto one canonical spelling.
			name: "invalid UTF-8 byte survives as itself while the ASCII around it folds",
			host: "NY\xffAA.SI",
			want: "ny\xffaa.si",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FoldHostASCII(tt.host); got != tt.want {
				t.Errorf("FoldHostASCII(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

// TestFoldHostASCIIRefusesToLaunderHomographs states the security reason the
// fold has a name of its own: for each homograph spelling, the stdlib case
// operation a caller would otherwise reach for produces the canonical ASCII
// domain (an outright match, or an EqualFold-equal one), while this fold leaves
// the non-ASCII bytes for IsASCIIHost to reject.
func TestFoldHostASCIIRefusesToLaunderHomographs(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		canonical string
	}{
		{name: "U+0130 dotted capital I", host: "AN\u0130MEBYTES.TV", canonical: "animebytes.tv"},
		{name: "U+212A kelvin sign", host: "RUTRAC\u212AER.ORG", canonical: "rutracker.org"},
		{name: "U+017F long s", host: "\u017FUKEBEI.NYAA.SI", canonical: "sukebei.nyaa.si"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folded := FoldHostASCII(tt.host)
			if folded == tt.canonical {
				t.Errorf("FoldHostASCII(%q) = %q: the fold laundered a homograph into the canonical domain", tt.host, folded)
			}
			if IsASCIIHost(folded) {
				t.Errorf("IsASCIIHost(%q) = true: the fold dropped the non-ASCII evidence the gate reads", folded)
			}
			// The stdlib operation a caller would otherwise use DOES launder
			// it, which is what makes this a security-relevant choice and not
			// a stylistic one. A case a stdlib release stopped laundering is
			// stale, not fixed - the assertion is the reason to keep folding
			// ASCII-only. Both premises re-verified on Unicode 17.0.0
			// (Go 1.27).
			if strings.ToLower(tt.host) != tt.canonical && !strings.EqualFold(tt.host, tt.canonical) {
				t.Errorf("neither strings.ToLower nor strings.EqualFold folds %q onto %q; the premise of the ASCII-only fold is stale", tt.host, tt.canonical)
			}
		})
	}
}

// TestFoldHostASCIIIsTheFoldClassifyApplies pins the one-implementation claim:
// the exported fold is the same operation Form.Host and HostMatchesDomain
// already apply, so a consumer folding its own host evidence and the
// classifier's facts can never disagree about what folding a host means.
func TestFoldHostASCIIIsTheFoldClassifyApplies(t *testing.T) {
	hosts := []string{"NYAA.si", "SukeBei.NYAA.SI", "AN\u0130MEBYTES.TV", "RUTRAC\u212AER.ORG", "animebytes.tv"}
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			if got, want := Classify("https://"+host+"/x").Host, FoldHostASCII(host); got != want {
				t.Errorf("Classify host = %q, want FoldHostASCII(%q) = %q", got, host, want)
			}
			// The matcher folds with it too, so a caller may fold either side
			// itself without changing the verdict.
			if !HostMatchesDomain(FoldHostASCII(host), FoldHostASCII(host)) {
				t.Errorf("HostMatchesDomain(%q, %q) = false on a pre-folded pair", host, host)
			}
		})
	}
}

// TestEqualASCIIFold pins the exported comparison's contract: A-Z folds and
// nothing else does, so every non-ASCII spelling of an ASCII protocol token
// compares unequal. Length differs first for the multi-byte spellings, which is
// exactly the difference from a Unicode fold - there differing byte lengths can
// still fold equal.
func TestEqualASCIIFold(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "both empty", want: true},
		{name: "empty against non-empty", b: "torrentid"},
		{name: "identical ASCII", a: "torrentid", b: "torrentid", want: true},
		{name: "ASCII case differs", a: "TorrentID", b: "torrentid", want: true},
		{name: "path token case differs", a: "/TORRENTS.PHP", b: "/torrents.php", want: true},
		{name: "different tokens", a: "torrentid", b: "torrentids"},
		{name: "same length, different letters", a: "torrentid", b: "torrentie"},
		{name: "digits and punctuation are not case-folded", a: "torrent_1", b: "torrent-1"},
		{name: "identical non-ASCII compares equal", a: "tor\u00e9nt", b: "tor\u00e9nt", want: true},
		{name: "non-ASCII case is NOT folded", a: "TOR\u00c9NT", b: "tor\u00e9nt"},
		{name: "identical invalid UTF-8 compares equal", a: "torrent\xffid", b: "torrent\xffid", want: true},
		{name: "invalid UTF-8 never equals the replacement rune", a: "torrent\xff", b: "torrent\uFFFD"},
		{name: "U+0130 never equals 'i'", a: "torrent\u0130d", b: "torrentid"},
		{name: "U+212A never equals 'k'", a: "api\u212Aey", b: "apikey"},
		{name: "U+017F never equals 's'", a: "/torrent\u017F.php", b: "/torrents.php"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EqualASCIIFold(tt.a, tt.b); got != tt.want {
				t.Errorf("EqualASCIIFold(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			// The relation is symmetric; a gate must not depend on which side
			// holds the untrusted string.
			if got := EqualASCIIFold(tt.b, tt.a); got != tt.want {
				t.Errorf("EqualASCIIFold(%q, %q) = %v, want %v (asymmetric)", tt.b, tt.a, got, tt.want)
			}
		})
	}
}

// TestEqualASCIIFoldRefusesUnicodeLaundering states the security reason this
// comparison exists instead of a stdlib one: for each homograph spelling below,
// the case-insensitive comparison a caller would otherwise reach for accepts it
// as the ASCII protocol token, handing a structural gate a match on bytes no
// server routes as that token. ASCII-only folding refuses all of them.
//
// The two stdlib operations launder DIFFERENT codepoints, which is why the
// table names them per row rather than claiming one rule: strings.EqualFold
// folds simple-fold orbits (U+212A -> 'k', U+017F -> 's') while
// strings.ToLower folds simple lowercase mappings (U+0130 -> 'i', U+212A ->
// 'k'). Each row is accepted by at least one of them, U+212A by both. The
// assertions on those operations are premise checks: a case a Go release stops
// laundering is stale, not fixed, and should be re-derived from the Unicode
// data rather than deleted.
//
// The rows were derived and last re-verified against Unicode 17.0.0 (Go 1.27),
// where the laundering set is unchanged from Unicode 15: exactly two non-ASCII
// runes lower to ASCII (U+0130 to 'i', U+212A to 'k') and exactly two are
// EqualFold-equal to an ASCII letter (U+212A to 'k', U+017F to 's'). Only
// these stdlib premises are version-bound - the fold under test reads no
// Unicode table at all.
func TestEqualASCIIFoldRefusesUnicodeLaundering(t *testing.T) {
	tests := []struct {
		name      string
		homograph string // an untrusted spelling reaching a gate
		token     string // the fixed ASCII token the gate compares against
		equalFold bool   // strings.EqualFold accepts the spelling as the token
		toLower   bool   // strings.ToLower makes the two compare equal
	}{
		{
			name:      "U+0130 dotted capital I in a query name",
			homograph: "torrent\u0130d",
			token:     "torrentid",
			toLower:   true,
		},
		{
			name:      "U+212A kelvin sign in a query name",
			homograph: "api\u212Aey",
			token:     "apikey",
			equalFold: true,
			toLower:   true,
		},
		{
			name:      "U+017F long s in a path token",
			homograph: "/torrent\u017F.php",
			token:     "/torrents.php",
			equalFold: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if EqualASCIIFold(tt.homograph, tt.token) {
				t.Errorf("EqualASCIIFold(%q, %q) = true: a homograph matched an ASCII protocol token", tt.homograph, tt.token)
			}
			if !tt.equalFold && !tt.toLower {
				t.Fatalf("row %q claims no stdlib laundering, so it does not state a reason for this fold", tt.name)
			}
			if got := strings.EqualFold(tt.homograph, tt.token); got != tt.equalFold {
				t.Errorf("strings.EqualFold(%q, %q) = %v, want %v; re-derive the row from the Unicode case data", tt.homograph, tt.token, got, tt.equalFold)
			}
			// staticcheck's SA6005 advises strings.EqualFold for exactly this
			// expression; the row above is the counterexample - the two
			// operations launder DIFFERENT codepoints, so substituting one for
			// the other changes which homographs a caller's gate accepts. The
			// pattern is asserted here precisely because callers write it.
			//nolint:staticcheck // SA6005: the ToLower comparison is the behavior under test, not a style choice.
			if got := strings.ToLower(tt.homograph) == strings.ToLower(tt.token); got != tt.toLower {
				t.Errorf("strings.ToLower comparison of %q and %q = %v, want %v; re-derive the row from the Unicode case data", tt.homograph, tt.token, got, tt.toLower)
			}
		})
	}
}

// TestEqualASCIIFoldIsTheFoldTheHostFactApplies pins the one-rule claim across
// all three exported spellings and the classifier's own fact: the comparison
// agrees with folding both sides, the host fold is that same fold, and
// Form.Host is what the classifier applied - so a consumer comparing a path
// token, a query name and a host can never be working from two different ideas
// of what folding ASCII means. Invalid UTF-8 is included deliberately: it is
// the only input class where a rune-mapping fold and a byte-wise comparison
// could drift.
func TestEqualASCIIFoldIsTheFoldTheHostFactApplies(t *testing.T) {
	values := []string{
		"", "torrentid", "TorrentID", "/torrents.php", "/TORRENTS.PHP",
		"NYAA.si", "an\u0130mebytes.tv", "AN\u0130MEBYTES.TV", "api\u212Aey",
		"\u017Fukebei.nyaa.si", "ny\xffaa.si", "ny\uFFFDaa.si",
	}
	for _, a := range values {
		for _, b := range values {
			if got, want := EqualASCIIFold(a, b), FoldHostASCII(a) == FoldHostASCII(b); got != want {
				t.Errorf("EqualASCIIFold(%q, %q) = %v but folding both sides says %v", a, b, got, want)
			}
		}
	}
	for _, host := range []string{"NYAA.si", "AN\u0130MEBYTES.TV", "RUTRAC\u212AER.ORG"} {
		if got := Classify("https://" + host + "/x").Host; !EqualASCIIFold(got, host) {
			t.Errorf("EqualASCIIFold(Classify host %q, %q) = false; the fact and the comparison disagree", got, host)
		}
	}
}

// TestIsASCIIHost pins the byte rule of the gate the fold cluster above keeps
// deferring to: every byte below utf8.RuneSelf is ASCII, anything at or above
// it is not, so a homograph host - a Cyrillic lookalike, the fold-laundering
// U+0130 and U+212A, an invalid UTF-8 byte - is refused outright rather than
// merely failing to match one domain. The two bytes either side of the
// boundary get named rows of their own because the gate is a single
// comparison, and which side 0x80 falls on is the whole posture: one step out
// and a two-byte homograph reads as ASCII evidence a consumer may then match
// against a canonical domain.
func TestIsASCIIHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "empty host is vacuously ASCII", want: true},
		{name: "ascii domain", host: "nyaa.si", want: true},
		{name: "ascii uppercase is still ascii", host: "NYAA.SI", want: true},
		{name: "ascii digits and dots", host: "127.0.0.1", want: true},
		{name: "the last ASCII byte is ASCII", host: "nyaa.si\x7f", want: true},
		{name: "the first non-ASCII byte is not", host: "nyaa.si\x80"},
		{name: "cyrillic lookalike", host: "\u0430nimebytes.tv"},
		{name: "U+0130 dotted capital I", host: "an\u0130mebytes.tv"},
		{name: "U+212A kelvin sign", host: "rutrac\u212Aer.org"},
		{name: "invalid UTF-8 byte", host: "ny\xffaa.si"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsASCIIHost(tt.host); got != tt.want {
				t.Errorf("IsASCIIHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

// TestFormNormalizedPath pins the browser's dot-segment reading per structural
// class: the resolution itself (including the paths that resolve all the way to
// root, and the trailing slash a "/.." or "/." ending leaves behind), the
// authority-reparse classes whose host must NOT leak into the path, the
// preserved repeated slashes, the percent-encoded dot segment a browser removes
// too, and the classes that carry no browser-resolvable path at all.
func TestFormNormalizedPath(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		// No facts at all: no path either.
		{name: "empty after trimming", raw: "   "},
		{name: "unparseable control character", raw: "https://nyaa.si/\x7f"},

		// Absolute forms: the path a browser shows in its address bar.
		{name: "absolute with no dot segments", raw: "https://nyaa.si/view/1", want: "/view/1"},
		{name: "absolute with an interior dot segment", raw: "https://nyaa.si/view/1/../2", want: "/view/2"},
		{name: "absolute with a single-dot segment", raw: "https://nyaa.si/view/./1", want: "/view/1"},
		{name: "authority-only absolute resolves to root", raw: "https://nyaa.si", want: "/"},
		{name: "trailing slash is preserved", raw: "https://nyaa.si/view/", want: "/view/"},
		{name: "dot segments resolving to root", raw: "https://nyaa.si/view/..", want: "/"},
		{name: "dot segments walking past root stop at root", raw: "https://nyaa.si/../../x", want: "/x"},
		{name: "trailing '..' leaves the parent's trailing slash", raw: "https://nyaa.si/a/b/..", want: "/a/"},
		{name: "trailing '.' leaves a trailing slash", raw: "https://nyaa.si/a/.", want: "/a/"},
		{name: "query and fragment are not part of the reading", raw: "https://nyaa.si/a/../b?id=1#frag", want: "/b"},
		{name: "repeated slashes are preserved like the browser's", raw: "https://nyaa.si/a//b", want: "/a//b"},
		{name: "percent-encoded dot segment resolves like the literal one", raw: "https://nyaa.si/a/%2e%2e/b", want: "/b"},
		{name: "smuggled tab in the path is stripped before resolution", raw: "https://nyaa.si/a\t/../b", want: "/b"},

		// Rooted relative values: the server-side reading a route gate compares.
		{name: "rooted relative path", raw: "/torrents.php?id=1", want: "/torrents.php"},
		{name: "rooted relative with a dot segment", raw: "/beat/api/../ghost", want: "/beat/ghost"},
		{name: "rooted relative resolving to root", raw: "/beat/api/../..", want: "/"},
		{name: "bare root", raw: "/", want: "/"},

		// The authority-reparse classes: the host must not leak into the path.
		{name: "schemeless host separates its path from the host", raw: "animebytes.tv/a/../b", want: "/b"},
		{name: "schemeless host with no path resolves to root", raw: "animebytes.tv", want: "/"},
		{name: "protocol-relative separates its path from the host", raw: "//animebytes.tv/a/../b", want: "/b"},
		{name: "backslash authority reads its canonicalized path", raw: `\\animebytes.tv\a\..\b`, want: "/b"},
		{name: "recovered hidden host separates its path from the host", raw: "https:/animebytes.tv/a/../b", want: "/b"},
		{name: "zero-slash hidden host separates its path from the host", raw: "https:animebytes.tv/a/../b", want: "/b"},

		// Forms carrying a path but no host evidence: the path still reads,
		// because a consumer comparing paths gates on Host separately.
		{name: "port-only authority still carries its path", raw: "https://:443/a/../b", want: "/b"},
		{name: "non-special scheme with a rooted path resolves it", raw: "mailto:/x/../y", want: "/y"},

		// No browser-resolvable path: opaque readings, failed reparses, and the
		// leading-"//" forms whose authority region no parse separated.
		{name: "three-slash form has no separated path reading", raw: "///a/../b"},
		{name: "empty-authority protocol-relative form", raw: "//?x=1"},
		{name: "opaque non-special scheme", raw: "javascript:alert(1)"},
		{name: "opaque host-as-scheme", raw: "animebytes.tv:443/a/../b"},
		{name: "failed authority reparse reports no path", raw: "https:/anime bytes@tv/a/../b"},
		{name: "schemeless form whose reparse fails reports no path", raw: "foo bar@animebytes.tv/a/../b"},
		{name: "query-only reference has no path of its own", raw: "?x:y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Classify(tt.raw)
			if got := f.NormalizedPath(); got != tt.want {
				t.Errorf("Classify(%q).NormalizedPath() = %q, want %q (class %v)", tt.raw, got, tt.want, f.Class)
			}
		})
	}
}

// TestFormNormalizedPathEqualsSpellingsOfOneDestination states the reading's
// purpose as a property: every spelling of one destination normalizes to the
// same path, which is what lets a consumer compare two untrusted strings that
// a browser resolves identically.
func TestFormNormalizedPathEqualsSpellingsOfOneDestination(t *testing.T) {
	groups := [][]string{
		{"https://nyaa.si/view/1", "https://nyaa.si/view/./1", "https://nyaa.si/a/../view/1", "https://nyaa.si/view/2/../1"},
		{"/beat/ghost", "/beat/api/../ghost", "/beat/./ghost", "/beat/%2e/ghost"},
		{"animebytes.tv/torrents.php", "animebytes.tv/x/../torrents.php", "//animebytes.tv/torrents.php", "https://animebytes.tv/torrents.php"},
	}
	for _, group := range groups {
		t.Run(group[0], func(t *testing.T) {
			first := Classify(group[0])
			want := first.NormalizedPath()
			if want == "" {
				t.Fatalf("Classify(%q).NormalizedPath() is empty; the group has no reading to compare", group[0])
			}
			for _, raw := range group[1:] {
				f := Classify(raw)
				if got := f.NormalizedPath(); got != want {
					t.Errorf("Classify(%q).NormalizedPath() = %q, want %q (the same destination as %q)", raw, got, want, group[0])
				}
			}
		})
	}
}
