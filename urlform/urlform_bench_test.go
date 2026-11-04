package urlform

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// The tests below gate the package's allocation claims (docs/contract.md
// "bounded and linear in the input", asciiLower's zero-allocation fast path,
// RawQueryNames/RawQueryPairs beating url.ParseQuery) with exact
// testing.AllocsPerRun counts: == 0 where the claim is zero, smallest input
// against largest where it is boundedness. Benchmarks are size-parameterised
// so a quadratic walk shows as a super-linear jump, and each builds its own
// fixture because the weekly run passes -run='^$'.

// benchQuery builds a raw query of n pairs (u.RawQuery's shape, no leading '?')
// whose names and values carry no percent escapes and no '+'. That is the shape
// the allocation-free walk is claimed for: url.QueryUnescape returns its
// argument untouched when there is nothing to decode, so every yielded string
// is a subslice of the input.
func benchQuery(n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte('&')
		}
		fmt.Fprintf(&b, "name%d=value%d", i, i)
	}
	return b.String()
}

// benchQueryEncoded builds a raw query of n pairs whose names AND values both
// carry percent escapes, so every yielded string has to be decoded into fresh
// storage. It is the walk's expensive path and the only one that allocates.
func benchQueryEncoded(n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte('&')
		}
		fmt.Fprintf(&b, "na%%6De%d=va%%6Cue%%20%d", i, i)
	}
	return b.String()
}

// benchURL builds an absolute URL whose path carries n ordinary segments, for
// the length-and-depth sweep. It is the shape with no adversarial feature at
// all, so it is the baseline the shapes in classifyShapes are read against.
func benchURL(segments int) string {
	return "https://animebytes.tv/" + strings.Repeat("segment/", segments) + "x?id=42"
}

// Host fixtures. The short ones are real host and domain lengths, which is what
// the per-request path actually sees; the long ones exist so the byte loops can
// be read for linearity.
const (
	// benchHostLower is already ASCII-lowercase, so FoldHostASCII takes its
	// no-copy fast path over it.
	benchHostLower = "sukebei.nyaa.si"
	// benchDomain is the known domain a gate matches host evidence against. It
	// is deliberately under 32 bytes; see TestHostPredicatesAreAllocationFree.
	benchDomain = "nyaa.si"
	// benchHostSuffixNearMiss ENDS with benchDomain without being under it,
	// which is the reading a plain strings.HasSuffix test wrongly accepts and
	// the case HostMatchesDomain exists to refuse.
	benchHostSuffixNearMiss = "evilnyaa.si"
	// benchToken and benchTokenMixed are an ASCII protocol token as a gate
	// holds it and as an untrusted URL spells it - EqualASCIIFold's subject,
	// not a host.
	benchToken      = "torrentid"
	benchTokenMixed = "TorrentID"
)

// benchHostLowerLong and benchHostMixedLong are the long-host fixtures, built
// once at init so no benchmark pays for strings.Repeat inside its timed loop.
var (
	benchHostLowerLong = strings.Repeat("sub.", 63) + benchDomain
	benchHostMixedLong = strings.Repeat("Sub.", 63) + benchDomain
	benchTokenLong     = strings.Repeat("segment-", 32)
	benchTokenLongUp   = strings.Repeat("Segment-", 32)
)

// classifyShapes are the adversarial input shapes a classifier of untrusted
// strings really sees, each parameterised by a repeat count so the same table
// can drive the bounded-allocation contract (which reads the smallest input
// against the largest) and the tracker's trend series.
//
// The chart field scopes which shapes get a permanent tracker series rather
// than which code path runs: the whole table is measured by the contract, and
// the three copy-forcing shapes are contract-only because their extra work is a
// single strings.ReplaceAll, strings.Map or byte copy whose per-byte cost the
// one_long_segment series already tracks. Charting them would spend a second of
// every weekly run each to re-measure the same memmove.
//
// chartRepeats is the repeat count the charted series use, chosen per shape so
// every charted fixture lands near the same PATH LENGTH in bytes. Without that
// the five series differ mostly by input size - a repeat count means 1 byte for
// redundant_slashes and 9 for percent_dense - and reading one against another
// says nothing about shape, which is the axis this family exists to isolate.
var classifyShapes = []struct {
	name         string
	build        func(n int) string
	chartRepeats int
	chart        bool
}{
	{
		// redundant_slashes: net/url reads a run of slashes as part of the
		// path and the WHATWG parser preserves them, so nothing collapses
		// them. A scan that became quadratic in slash runs would show here.
		name:         "redundant_slashes",
		build:        func(n int) string { return "https://animebytes.tv/" + strings.Repeat("/", n) + "x" },
		chartRepeats: 2048,
		chart:        true,
	},
	{
		// dot_segments: the input NormalizedPath has to resolve away. This is
		// the shape whose resolution cost an attacker controls directly.
		name:         "dot_segments",
		build:        func(n int) string { return "https://animebytes.tv/" + strings.Repeat("a/../", n) + "x" },
		chartRepeats: 410,
		chart:        true,
	},
	{
		// one_long_segment: no structure at all, just length, so it isolates
		// the per-byte cost of the scans from the per-segment cost.
		name:         "one_long_segment",
		build:        func(n int) string { return "https://animebytes.tv/" + strings.Repeat("a", n*8) },
		chartRepeats: 256,
		chart:        true,
	},
	{
		// percent_dense: every byte is an escape, which is the input that
		// makes url.Parse unescape the whole path into fresh storage.
		name:         "percent_dense",
		build:        func(n int) string { return "https://animebytes.tv/" + strings.Repeat("%2e%2e%2f", n) },
		chartRepeats: 228,
		chart:        true,
	},
	{
		// backslash_authority forces canonicalizeSlashes to rewrite, which is
		// one of the three paths that copies the input.
		name:  "backslash_authority",
		build: func(n int) string { return `https://animebytes.tv/` + strings.Repeat(`a\b\`, n) },
	},
	{
		// tab_smuggled forces the WHATWG tab/newline removal to rebuild the
		// string through strings.Map.
		name:  "tab_smuggled",
		build: func(n int) string { return "https://animebytes.tv/" + strings.Repeat("a\tb", n) },
	},
	{
		// uppercase_host forces asciiLower to copy the host, the third
		// copying path.
		name:  "uppercase_host",
		build: func(n int) string { return "https://" + strings.Repeat("A", n) + ".TV/x" },
	},
}

// countRawQueryNames returns how many names RawQueryNames yields. It exists so
// an allocation measurement over the walk cannot pass vacuously: a walk that
// yielded nothing would be allocation-free for the wrong reason, which is the
// vacuous-gate failure mode in benchmark form. It cannot fail, so it takes no
// testing.TB.
func countRawQueryNames(rawQuery string) int {
	var n int
	for range RawQueryNames(rawQuery) {
		n++
	}
	return n
}

// countRawQueryPairs returns how many pairs RawQueryPairs yields, for the same
// anti-vacuity reason as countRawQueryNames.
func countRawQueryPairs(rawQuery string) int {
	var n int
	for range RawQueryPairs(rawQuery) {
		n++
	}
	return n
}

// TestRawQueryWalksAreAllocationFree pins the property that is the whole reason
// to prefer these iterators over url.ParseQuery: they yield SUBSLICES of the
// caller's query string, so walking a query costs no allocation at all, at any
// pair count.
//
// The assertion is `== 0` rather than a threshold because AllocsPerRun is exact
// and the property is absolute. A refactor that starts copying each field - a
// strings.Clone for safety, a []byte round trip, a decoded-name cache, an
// interface boxing - turns this red at merge time, which is the point: once the
// walk allocates per pair it is no longer cheaper than the map it replaced, and
// a chart would show that as a gradual slope nobody attributes to a cause.
//
// The pair counts span four orders of magnitude because the claim is about the
// walk, not about a size. A per-call allocation would show at 1; a per-pair
// allocation would show at 4096.
func TestRawQueryWalksAreAllocationFree(t *testing.T) {
	for _, pairs := range []int{1, 16, 256, 4096} {
		query := benchQuery(pairs)

		t.Run(fmt.Sprintf("names_%d", pairs), func(t *testing.T) {
			if got := countRawQueryNames(query); got != pairs {
				t.Fatalf("RawQueryNames(query with %d pairs) yielded %d names, want %d: "+
					"the allocation measurement below would pass vacuously over a walk "+
					"that yields nothing", pairs, got, pairs)
			}
			got := testing.AllocsPerRun(50, func() {
				for name := range RawQueryNames(query) {
					_ = name
				}
			})
			if got != 0 {
				t.Errorf("RawQueryNames(query with %d pairs) allocated %v times per run, "+
					"want 0: the walk exists to yield subslices of the input rather than "+
					"build url.ParseQuery's map, and a per-pair allocation forfeits that",
					pairs, got)
			}
		})

		t.Run(fmt.Sprintf("pairs_%d", pairs), func(t *testing.T) {
			if got := countRawQueryPairs(query); got != pairs {
				t.Fatalf("RawQueryPairs(query with %d pairs) yielded %d pairs, want %d: "+
					"the allocation measurement below would pass vacuously over a walk "+
					"that yields nothing", pairs, got, pairs)
			}
			got := testing.AllocsPerRun(50, func() {
				for name, value := range RawQueryPairs(query) {
					_, _ = name, value
				}
			})
			if got != 0 {
				t.Errorf("RawQueryPairs(query with %d pairs) allocated %v times per run, "+
					"want 0: carrying the VALUE alongside the name must stay a second "+
					"subslice, not a copy", pairs, got)
			}
		})
	}
}

// TestRawQueryWalkShapesAreAllocationFree covers the query shapes that are not
// a plain "&"-joined list, because each reaches a different branch of the walk
// and any of them could start allocating on its own.
//
// The semicolon case is the one the iterators exist for: url.ParseQuery drops
// the pair an unescaped ';' sits in, so this walk is the only view of those
// bytes, and it must not be the expensive view.
func TestRawQueryWalkShapesAreAllocationFree(t *testing.T) {
	cases := []struct {
		name      string
		rawQuery  string
		wantNames int
	}{
		{"semicolon_separated", "apikey=SECRET;foo=x;bar=y", 3},
		{"mixed_separators", "a=1&b=2;c=3&d=4", 4},
		{"bare_flag_fields", "verbose&debug&trace", 3},
		{"empty_fields_skipped", "&&a=1&&&b=2&&", 2},
		{"value_carries_equals", "token=a=b=c&x=y", 2},
		{"empty_query", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countRawQueryNames(tc.rawQuery); got != tc.wantNames {
				t.Fatalf("RawQueryNames(%q) yielded %d names, want %d: the allocation "+
					"measurement below reads a fixture that does not parse as expected",
					tc.rawQuery, got, tc.wantNames)
			}
			if got := testing.AllocsPerRun(50, func() {
				for name := range RawQueryNames(tc.rawQuery) {
					_ = name
				}
			}); got != 0 {
				t.Errorf("RawQueryNames(%q) allocated %v times per run, want 0",
					tc.rawQuery, got)
			}
			if got := testing.AllocsPerRun(50, func() {
				for name, value := range RawQueryPairs(tc.rawQuery) {
					_, _ = name, value
				}
			}); got != 0 {
				t.Errorf("RawQueryPairs(%q) allocated %v times per run, want 0",
					tc.rawQuery, got)
			}
		})
	}
}

// TestRawQueryWalkStopsAllocationFree pins that abandoning the walk early is
// allocation-free too. A gate that returns on the first parameter it recognizes
// is the common consumer shape, and it must not pay for the pairs it never
// looked at.
func TestRawQueryWalkStopsAllocationFree(t *testing.T) {
	query := benchQuery(4096)

	if got := testing.AllocsPerRun(50, func() {
		for name := range RawQueryNames(query) {
			_ = name
			break
		}
	}); got != 0 {
		t.Errorf("RawQueryNames(query with 4096 pairs) stopped after one name and "+
			"allocated %v times per run, want 0", got)
	}
	if got := testing.AllocsPerRun(50, func() {
		for name, value := range RawQueryPairs(query) {
			_, _ = name, value
			break
		}
	}); got != 0 {
		t.Errorf("RawQueryPairs(query with 4096 pairs) stopped after one pair and "+
			"allocated %v times per run, want 0", got)
	}
}

// TestRawQueryDecodeCostIsBoundedPerDecodedField covers the other half of the
// walk: a name or value that really does carry percent escapes has to be
// decoded into fresh storage, so this path is NOT allocation-free and should
// not be expected to be.
//
// The property that matters instead is that the cost is bounded per DECODED
// FIELD, so it cannot be amplified by a query that decodes nothing. Measured on
// go1.27.0 the counts are exact - one allocation per decoded name, two per
// decoded pair - and the assertion is the inequality rather than the equality,
// because the two regressions worth catching both cross it (decoding a field
// twice, or copying the raw fallback text of the halves that need no decode)
// while a future stdlib improvement to zero would not.
func TestRawQueryDecodeCostIsBoundedPerDecodedField(t *testing.T) {
	for _, pairs := range []int{1, 16, 256} {
		query := benchQueryEncoded(pairs)

		t.Run(fmt.Sprintf("names_%d", pairs), func(t *testing.T) {
			if got := countRawQueryNames(query); got != pairs {
				t.Fatalf("RawQueryNames(encoded query with %d pairs) yielded %d names, "+
					"want %d", pairs, got, pairs)
			}
			got := testing.AllocsPerRun(50, func() {
				for name := range RawQueryNames(query) {
					_ = name
				}
			})
			if want := float64(pairs); got > want {
				t.Errorf("RawQueryNames(encoded query with %d pairs) allocated %v times "+
					"per run, want at most %v: one decode per name and nothing else",
					pairs, got, want)
			}
		})

		t.Run(fmt.Sprintf("pairs_%d", pairs), func(t *testing.T) {
			got := testing.AllocsPerRun(50, func() {
				for name, value := range RawQueryPairs(query) {
					_, _ = name, value
				}
			})
			if want := float64(2 * pairs); got > want {
				t.Errorf("RawQueryPairs(encoded query with %d pairs) allocated %v times "+
					"per run, want at most %v: one decode per name and one per value, "+
					"which is what decoding the halves INDEPENDENTLY costs", pairs, got, want)
			}
		})
	}
}

// TestHostPredicatesAreAllocationFree pins that the read-only host and token
// predicates allocate nothing. All three run on a per-request gate path, none
// of them returns a new string, and none of them has any reason to allocate;
// pinning that means a later refactor reaching for strings.ToLower, a
// strings.Split, or a []byte conversion goes red here rather than adding
// invisible garbage to every request.
//
// HostMatchesDomain used to carry a caveat here, and the caveat is why the
// function changed. It tested the subdomain suffix with CutSuffix(host,
// "."+domain), and the Go runtime keeps such a concatenation on the stack only
// while the result fits 32 bytes; above that it escaped to the heap and the
// matcher cost one allocation per call. So a short domain was free and a
// realistic long one was not, on a per-request security gate. The concat is
// gone (suffix compare, then the separator byte), and the length sweep below is
// what stops it coming back.
func TestHostPredicatesAreAllocationFree(t *testing.T) {
	t.Run("IsASCIIHost", func(t *testing.T) {
		cases := []struct {
			name string
			host string
			want bool
		}{
			{"ascii_host", benchHostLower, true},
			{"ascii_host_long", benchHostLowerLong, true},
			{"non_ascii_host", "\u0430nimebytes.tv", false},
			{"empty_host", "", true},
		}
		for _, tc := range cases {
			if got := IsASCIIHost(tc.host); got != tc.want {
				t.Errorf("IsASCIIHost(%q) = %v, want %v", tc.host, got, tc.want)
				continue
			}
			if got := testing.AllocsPerRun(100, func() {
				_ = IsASCIIHost(tc.host)
			}); got != 0 {
				t.Errorf("IsASCIIHost(%q) allocated %v times per run, want 0: the "+
					"fail-closed gate is a byte scan and must stay one", tc.host, got)
			}
		}
	})

	t.Run("EqualASCIIFold", func(t *testing.T) {
		cases := []struct {
			name string
			a, b string
			want bool
		}{
			{"equal_mixed_case", benchTokenMixed, benchToken, true},
			{"equal_long", benchTokenLongUp, benchTokenLong, true},
			{"unequal_length", benchToken, "x", false},
			{"unequal_last_byte", "torrentid", "torrentie", false},
			{"non_ascii_never_equal", "torrent\u0130d", benchToken, false},
		}
		for _, tc := range cases {
			if got := EqualASCIIFold(tc.a, tc.b); got != tc.want {
				t.Errorf("EqualASCIIFold(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
				continue
			}
			if got := testing.AllocsPerRun(100, func() {
				_ = EqualASCIIFold(tc.a, tc.b)
			}); got != 0 {
				t.Errorf("EqualASCIIFold(%q, %q) allocated %v times per run, want 0: "+
					"comparing under a fold must never build a folded copy, which is "+
					"exactly what calling strings.ToLower on both sides would do",
					tc.a, tc.b, got)
			}
		}
	})

	t.Run("HostMatchesDomain", func(t *testing.T) {
		cases := []struct {
			name         string
			host, domain string
			want         bool
		}{
			{"exact_equal", benchDomain, benchDomain, true},
			{"subdomain_match", benchHostLower, benchDomain, true},
			{"deep_subdomain_match", "a.b.c.d.e.f.g.nyaa.si", benchDomain, true},
			{"suffix_near_miss", benchHostSuffixNearMiss, benchDomain, false},
			{"parent_domain_spoof", "nyaa.si.evil.example", benchDomain, false},
			{"empty_label_leading", ".nyaa.si", benchDomain, false},
			{"empty_label_inner", "a..nyaa.si", benchDomain, false},
			{"empty_host", "", benchDomain, false},
			{"non_ascii_subdomain_label", "\u00e9.nyaa.si", benchDomain, true},
		}
		for _, tc := range cases {
			if got := HostMatchesDomain(tc.host, tc.domain); got != tc.want {
				t.Errorf("HostMatchesDomain(%q, %q) = %v, want %v",
					tc.host, tc.domain, got, tc.want)
				continue
			}
			if got := testing.AllocsPerRun(100, func() {
				_ = HostMatchesDomain(tc.host, tc.domain)
			}); got != 0 {
				t.Errorf("HostMatchesDomain(%q, %q) allocated %v times per run, want 0: "+
					"the matcher compares the strings it is given and must not build a "+
					"folded or split copy of either", tc.host, tc.domain, got)
			}
		}
	})

	// The length sweep is the half a fixed table cannot do. Any suffix test
	// written as a concatenation is free at these first two sizes and allocates
	// at the rest, so a table of realistic short domains would go on passing
	// while every real caller paid. 4096 is far past any registrable name and is
	// here to make the answer unambiguous rather than borderline.
	t.Run("HostMatchesDomain_at_every_domain_length", func(t *testing.T) {
		for _, n := range []int{8, 24, 31, 32, 33, 64, 256, 4096} {
			domain := strings.Repeat("a", n-3) + ".si"
			host := "sub." + domain
			if !HostMatchesDomain(host, domain) {
				t.Errorf("HostMatchesDomain(host, %d-byte domain) = false, want true: "+
					"the fixture must match or the allocation reading below is vacuous", n)
				continue
			}
			if got := testing.AllocsPerRun(100, func() {
				_ = HostMatchesDomain(host, domain)
			}); got != 0 {
				t.Errorf("HostMatchesDomain(host, %d-byte domain) allocated %v times per "+
					"run, want 0 at every length: a suffix test that concatenates is free "+
					"below 32 bytes and costs one allocation above it, so a regression here "+
					"is invisible to a short fixture and paid by every real caller", n, got)
			}
		}
	})
}

// TestFoldHostASCIISkipsTheCopyWhenNothingFolds pins asciiLower's documented
// fast path: "Nothing is allocated when the string carries no ASCII uppercase."
//
// That is the case a consumer hits on nearly every request, because host
// evidence arriving from Form.Host is already folded and a configured domain is
// normally written lowercase. A refactor that always materialized the folded
// copy - the obvious shape, and what a strings.Map rewrite would produce -
// would add an allocation per request per host while every test still passed.
//
// A host that DOES need folding is deliberately not asserted here. The measured
// count is 1 for a short host and 2 for a long one (the copy crosses a size
// class), so there is no single true number to pin, and pinning either one
// would encode an incidental allocator detail as a contract.
func TestFoldHostASCIISkipsTheCopyWhenNothingFolds(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"already_lowercase", benchHostLower},
		{"already_lowercase_long", benchHostLowerLong},
		{"digits_and_hyphens", "cdn-3.node-7.example"},
		// A non-ASCII host carries no A-Z either, so it takes the same no-copy
		// path. That matters for the fail-closed gates: the fold must hand
		// IsASCIIHost the original bytes, not a rebuilt string.
		{"non_ascii", "\u0430nimebytes.tv"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FoldHostASCII(tc.host); got != tc.host {
				t.Errorf("FoldHostASCII(%q) = %q, want %q unchanged", tc.host, got, tc.host)
			}
			if got := testing.AllocsPerRun(100, func() {
				_ = FoldHostASCII(tc.host)
			}); got != 0 {
				t.Errorf("FoldHostASCII(%q) allocated %v times per run, want 0: "+
					"asciiLower documents that nothing is allocated when the string "+
					"carries no ASCII uppercase", tc.host, got)
			}
		})
	}
}

// TestClassifyAllocationCountIsIndependentOfInputSize gates the README's
// "allocation is bounded" claim in the form that matters against a hostile
// input: the NUMBER of allocations Classify makes must not grow with the length
// of the string handed to it.
//
// Byte volume may grow, and for three of these shapes it does - canonicalizing
// backslashes, removing smuggled tab/newline and folding an uppercase host each
// rebuild the string, so their cost really is linear in the input, which is
// what the README says. Allocation COUNT is the separate property, and it is
// the one an attacker can attack: whoever can turn one classification into a
// thousand allocations by sending a thousand times more bytes has found an
// amplification vector inside a classifier whose contract is boundedness.
//
// So this asserts the count is EQUAL across inputs three orders of magnitude
// apart, per shape, rather than asserting any particular number. A per-segment
// url.URL, a per-dot-segment resolution, or an accumulating []string would all
// cross it.
func TestClassifyAllocationCountIsIndependentOfInputSize(t *testing.T) {
	sizes := []int{1, 16, 256, 2048}
	for _, shape := range classifyShapes {
		t.Run(shape.name, func(t *testing.T) {
			counts := make([]float64, len(sizes))
			for i, n := range sizes {
				raw := shape.build(n)
				if f := Classify(raw); f.Class == ClassEmpty {
					t.Fatalf("Classify(%s shape at n=%d) = ClassEmpty, want a "+
						"fact-bearing class: the fixture no longer exercises the shape",
						shape.name, n)
				}
				counts[i] = testing.AllocsPerRun(50, func() {
					_ = Classify(raw)
				})
			}
			for i, got := range counts {
				if got != counts[0] {
					t.Errorf("Classify(%s shape) allocated %v times per run at n=%d but "+
						"%v at n=%d: allocation count must not grow with the input, or "+
						"more bytes buy an attacker more allocations",
						shape.name, got, sizes[i], counts[0], sizes[0])
				}
			}
		})
	}
}

// TestClassifyAllocatesNothingForTheNoFactsClasses pins that the two classes
// carrying no facts cost nothing. Classify returns before parsing for an input
// that is empty after preprocessing, which is the cheapest thing an untrusted
// feed can send in bulk.
func TestClassifyAllocatesNothingForTheNoFactsClasses(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"spaces_only", "   "},
		{"c0_controls_only", "\x00\x01\x02"},
		{"tab_and_newline_only", "\t\n\r"},
		{"unicode_space_only", "\u00a0\u3000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.raw); got.Class != ClassEmpty {
				t.Fatalf("Classify(%q).Class = %v, want ClassEmpty", tc.raw, got.Class)
			}
			if got := testing.AllocsPerRun(100, func() {
				_ = Classify(tc.raw)
			}); got != 0 {
				t.Errorf("Classify(%q) allocated %v times per run, want 0: the "+
					"empty-after-preprocessing path returns before any parse",
					tc.raw, got)
			}
		})
	}
}

// TestNormalizedPathAllocationCountIsIndependentOfPathDepth is the resolution
// half of the bounded-cost contract. NormalizedPath resolves dot segments
// through net/url's own RFC 3986 section 5.2.4 pass, and the number of dot
// segments in the path is entirely the attacker's choice, so the allocation
// count must not follow it.
//
// The depths start at 64 rather than 1 deliberately: the measured count is 2
// for a resolved path short enough to fit one allocator size class and 3 above
// it, so a sweep reaching down to the smallest input would compare two
// different-sized buffers and fail on an allocator detail rather than on a cost
// regression. The floor is set by the resolved path's BYTE length, not by its
// segment count, which is why it has to clear the shortest shape here:
// redundant_slashes contributes one byte per repeat where dot_segments
// contributes five, so a floor that is comfortably past the step for one shape
// can still sit below it for another. From 64 up all three are flat, which is
// the property under test.
func TestNormalizedPathAllocationCountIsIndependentOfPathDepth(t *testing.T) {
	depths := []int{64, 512, 4096}
	cases := []struct {
		name  string
		build func(n int) string
	}{
		{"dot_segments", func(n int) string {
			return "https://animebytes.tv/" + strings.Repeat("a/../", n) + "x"
		}},
		{"plain_segments", func(n int) string {
			return "https://animebytes.tv/" + strings.Repeat("segment/", n)
		}},
		{"redundant_slashes", func(n int) string {
			return "https://animebytes.tv/" + strings.Repeat("/", n) + "x"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counts := make([]float64, len(depths))
			for i, n := range depths {
				f := Classify(tc.build(n))
				if got := f.NormalizedPath(); got == "" {
					t.Fatalf("Classify(%s shape at n=%d).NormalizedPath() = %q, want a "+
						"rooted path: the fixture no longer resolves", tc.name, n, got)
				}
				counts[i] = testing.AllocsPerRun(50, func() {
					_ = f.NormalizedPath()
				})
			}
			for i, got := range counts {
				if got != counts[0] {
					t.Errorf("Form.NormalizedPath() over the %s shape allocated %v times "+
						"per run at n=%d but %v at n=%d: resolution cost must not grow "+
						"with the number of segments the attacker sent",
						tc.name, got, depths[i], counts[0], depths[0])
				}
			}
		})
	}
}

// BenchmarkRawQueryNames measures the names-only walk across pair counts. The
// sizes are 16x apart so a quadratic split - a rewrite that re-scanned the
// query per pair, which is what indexing instead of iterating would produce -
// reads as a 256x jump rather than as a uniform slowdown.
//
// The encoded case is the same walk over a query where every name has to be
// percent-decoded. It is charted next to the plain sizes because the two paths
// regress independently: the plain path can only get slower by scanning more,
// the encoded path by decoding more.
func BenchmarkRawQueryNames(b *testing.B) {
	cases := []struct {
		name  string
		query string
	}{
		{"pairs_16", benchQuery(16)},
		{"pairs_256", benchQuery(256)},
		{"encoded_256", benchQueryEncoded(256)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.query)))
			var total int
			for b.Loop() {
				for name := range RawQueryNames(tc.query) {
					total += len(name)
				}
			}
			if total == 0 {
				b.Fatalf("RawQueryNames(%s) yielded no name bytes: the timed loop "+
					"measured nothing", tc.name)
			}
		})
	}
}

// BenchmarkRawQueryPairs measures the walk that carries each value alongside
// its name. It is the shape a consumer whose predicate reads the value uses -
// a credential warning that must not fire on an empty parameter, a redaction
// pass locating the secret text - so it is the one whose cost is paid on the
// paths that matter most, and it is charted separately because the second
// percent-decode is its own regression surface.
//
// One size only, against BenchmarkRawQueryNames' two: both walks split with the
// same strings.FieldsFuncSeq and cut with the same strings.Cut, so a
// super-linear splitter shows in the names sweep, and the cost this series adds
// over that one is the second queryText call. The package's whole benchmark run
// has a wall-time budget, and a second size here would spend a second of every
// weekly run re-measuring the same splitter.
func BenchmarkRawQueryPairs(b *testing.B) {
	cases := []struct {
		name  string
		query string
	}{
		{"pairs_256", benchQuery(256)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.query)))
			var total int
			for b.Loop() {
				for name, value := range RawQueryPairs(tc.query) {
					total += len(name) + len(value)
				}
			}
			if total == 0 {
				b.Fatalf("RawQueryPairs(%s) yielded no bytes: the timed loop measured "+
					"nothing", tc.name)
			}
		})
	}
}

// BenchmarkStdlibParseQuery is the side-by-side reading against the stdlib call
// the iterators exist to avoid, on the SAME fixtures BenchmarkRawQueryNames and
// BenchmarkRawQueryPairs use. It is not a benchmark of urlform, and it will
// never regress because of a change here; it is charted so the margin the
// library exists to provide is a recorded number rather than an assumption, and
// so a future reader can see whether the margin still justifies the API.
//
// url.ParseQuery builds a url.Values map and a []string per key, so its
// allocation count grows with the pair count while the walks stay at zero. That
// divergence, not the ns/op ratio, is the durable part of the comparison, and it
// is why one size is enough here: the margin measured 5.9x at 16 pairs and 5.1x
// at 256, so a second size would re-record a ratio this one already carries.
func BenchmarkStdlibParseQuery(b *testing.B) {
	cases := []struct {
		name  string
		query string
	}{
		{"pairs_256", benchQuery(256)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.query)))
			var total int
			for b.Loop() {
				values, err := url.ParseQuery(tc.query)
				if err != nil {
					b.Fatal(err)
				}
				total += len(values)
			}
			if total == 0 {
				b.Fatalf("url.ParseQuery(%s) parsed no values: the timed loop measured "+
					"nothing", tc.name)
			}
		})
	}
}

// BenchmarkFoldHostASCII charts the exported ASCII fold on both of its paths.
// The lowercase cases take the no-copy fast path
// TestFoldHostASCIISkipsTheCopyWhenNothingFolds gates, so their series is the
// per-byte scan cost alone; the mixed-case case pays for the copy on top. Short
// against long is what shows the scan is linear in the host, and a host is
// attacker-supplied up to whatever length the consumer's own limits allow.
func BenchmarkFoldHostASCII(b *testing.B) {
	cases := []struct {
		name string
		host string
	}{
		{"lowercase_short", benchHostLower},
		{"lowercase_long", benchHostLowerLong},
		{"mixed_case_long", benchHostMixedLong},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.host)))
			var total int
			for b.Loop() {
				total += len(FoldHostASCII(tc.host))
			}
			if total == 0 {
				b.Fatalf("FoldHostASCII(%s) returned no bytes: the timed loop measured "+
					"nothing", tc.name)
			}
		})
	}
}

// BenchmarkEqualASCIIFold charts the token comparison a structural gate runs
// against every ASCII protocol token it reads out of an untrusted URL. The
// fixture is a long token so the series reads the per-byte fold cost rather
// than call overhead.
//
// The documented length short-circuit deliberately has NO series of its own.
// EqualASCIIFold compares length first because its fold is byte-length
// preserving, so a mismatched length is O(1), and O(1) here measured 0.35 ns/op
// - below b.Loop's own overhead and far below the resolution a 150% alert
// threshold can act on, so the series would flap on toolchain noise rather than
// report a regression. Dropping that guard would raise the cost to this series'
// number, which is the reading that catches it.
func BenchmarkEqualASCIIFold(b *testing.B) {
	cases := []struct {
		name string
		a, b string
	}{
		{"equal_mixed_case", benchTokenLongUp, benchTokenLong},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if !EqualASCIIFold(tc.a, tc.b) {
				b.Fatalf("EqualASCIIFold(%q, %q) = false, want true: the fixture no "+
					"longer exercises a full fold", tc.a, tc.b)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.a)))
			var matches int
			for b.Loop() {
				if EqualASCIIFold(tc.a, tc.b) {
					matches++
				}
			}
			if matches == 0 {
				b.Fatal("EqualASCIIFold never matched: the timed loop measured nothing")
			}
		})
	}
}

// BenchmarkHostMatchesDomain charts the matcher a consumer reaches after the
// IsASCIIHost gate. All three cases are per-request costs on a link publisher's
// hot path, and each exercises a different exit:
//
//   - exact_equal returns on the byte comparison, before any suffix work.
//   - subdomain_match runs the full path: fold, suffix cut, empty-label check.
//   - suffix_near_miss is the security-relevant case, a host that ENDS with the
//     domain without being under it. It must stay cheap, because a hostile feed
//     is exactly what produces a stream of them, and it must stay FALSE - which
//     is why the case asserts the answer rather than only timing it.
func BenchmarkHostMatchesDomain(b *testing.B) {
	cases := []struct {
		name         string
		host, domain string
		want         bool
	}{
		{"exact_equal", benchDomain, benchDomain, true},
		{"subdomain_match", benchHostLower, benchDomain, true},
		{"suffix_near_miss", benchHostSuffixNearMiss, benchDomain, false},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if got := HostMatchesDomain(tc.host, tc.domain); got != tc.want {
				b.Fatalf("HostMatchesDomain(%q, %q) = %v, want %v: the fixture no longer "+
					"exercises the case this series is named for",
					tc.host, tc.domain, got, tc.want)
			}
			b.ReportAllocs()
			var hits int
			for b.Loop() {
				if HostMatchesDomain(tc.host, tc.domain) {
					hits++
				}
			}
			_ = hits
		})
	}
}

// BenchmarkClassify measures the whole classification over an ordinary absolute
// URL, parameterised by path depth and therefore by URL length. The two sizes
// are 16x apart, so the per-byte preprocessing scans (edge trim, tab/newline
// search, backslash search) and the per-segment parse cost separate: a
// regression that made any one scan quadratic reads as a 256x jump.
func BenchmarkClassify(b *testing.B) {
	for _, segments := range []int{16, 256} {
		raw := benchURL(segments)
		b.Run(fmt.Sprintf("segments_%d", segments), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			var absolute int
			for b.Loop() {
				if Classify(raw).Class == ClassAbsolute {
					absolute++
				}
			}
			if absolute == 0 {
				b.Fatalf("Classify(URL with %d segments) never returned ClassAbsolute: "+
					"the fixture no longer exercises the hierarchical parse", segments)
			}
		})
	}
}

// BenchmarkClassifyAdversarial charts the shapes a classifier of untrusted
// strings is actually sent, each at the repeat count that puts its path near
// 2 KiB, so a regression specific to one shape is attributable to that shape
// rather than diluted into an average. Equalizing the byte length is what makes
// the five series comparable with each other: read at equal repeat counts they
// differ mostly by input size, because one repeat is 1 byte of slashes and 9
// bytes of percent escapes.
//
// The size axis is charted by BenchmarkClassify and gated across four orders of
// magnitude by TestClassifyAllocationCountIsIndependentOfInputSize; what these
// series add is the SHAPE axis.
//
// The empty case is the floor. It is cheap on purpose - Classify returns before
// parsing - and it is charted because that early return is the only thing
// standing between a bulk feed of blank fields and a full parse each.
func BenchmarkClassifyAdversarial(b *testing.B) {
	for _, shape := range classifyShapes {
		if !shape.chart {
			continue
		}
		raw := shape.build(shape.chartRepeats)
		b.Run(shape.name, func(b *testing.B) {
			if got := Classify(raw); got.Class == ClassEmpty || got.Class == ClassMalformed {
				b.Fatalf("Classify(%s shape) = %v, want a fact-bearing class: the "+
					"fixture no longer exercises the shape", shape.name, got.Class)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			var classified int
			for b.Loop() {
				classified += int(Classify(raw).Class)
			}
			_ = classified
		})
	}
	b.Run("empty", func(b *testing.B) {
		b.ReportAllocs()
		var empty int
		for b.Loop() {
			if Classify("").Class == ClassEmpty {
				empty++
			}
		}
		if empty == 0 {
			b.Fatal(`Classify("").Class never returned ClassEmpty`)
		}
	})
}

// BenchmarkFormNormalizedPath measures the dot-segment resolution, which is the
// one place a consumer's cost is driven directly by a count the attacker picks.
// Classification is done in setup, outside the timed loop, because the subject
// here is the resolution and not the parse.
//
// One size, because the wall-time budget for the package's whole run is spent
// elsewhere and the constancy this series would otherwise show in-run is gated
// harder by TestNormalizedPathAllocationCountIsIndependentOfPathDepth, which
// sweeps 64 to 4096 over three path shapes. A resolution that turned quadratic
// would still land here as a jump against the previous commit's value, which is
// how the tracker reads every series.
func BenchmarkFormNormalizedPath(b *testing.B) {
	for _, segments := range []int{256} {
		f := Classify("https://animebytes.tv/" + strings.Repeat("a/../", segments) + "x")
		b.Run(fmt.Sprintf("dot_segments_%d", segments), func(b *testing.B) {
			if got := f.NormalizedPath(); got != "/x" {
				b.Fatalf("Form.NormalizedPath() over %d dot segments = %q, want %q: the "+
					"fixture no longer resolves out of its segments", segments, got, "/x")
			}
			b.ReportAllocs()
			var total int
			for b.Loop() {
				total += len(f.NormalizedPath())
			}
			if total == 0 {
				b.Fatalf("Form.NormalizedPath() over %d dot segments returned no bytes",
					segments)
			}
		})
	}
}
