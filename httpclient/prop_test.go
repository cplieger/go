package httpclient

import (
	"errors"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestJitteredBackoff_property(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		d := time.Duration(rapid.Int64Range(1, math.MaxInt64/2).Draw(t, "duration"))
		result := JitteredBackoff(d)
		half := d / 2
		if result < half {
			t.Fatalf("JitteredBackoff(%v) = %v, want >= %v", d, result, half)
		}
		if result > d {
			t.Fatalf("JitteredBackoff(%v) = %v, want <= %v", d, result, d)
		}
	})
}

func TestSafeDouble_property(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		d := time.Duration(rapid.Int64Range(1, math.MaxInt64).Draw(t, "duration"))
		result := SafeDouble(d)
		if result < d {
			t.Fatalf("SafeDouble(%v) = %v, want >= %v", d, result, d)
		}
		doubled := d * 2
		if doubled >= d {
			if result != doubled {
				t.Fatalf("SafeDouble(%v) = %v, want %v", d, result, doubled)
			}
		} else {
			if result != time.Duration(math.MaxInt64) {
				t.Fatalf("SafeDouble(%v) = %v, want MaxInt64 on overflow", d, result)
			}
		}
	})
}

// TestReduction_non_nil_input_never_yields_nil_property is the invariant net
// under the *url.Error reductions across the whole shape space a caller can
// hand them: any non-nil input yields a non-nil, URL-free error. The unit
// table (redact_url_test.go) pins the specific shapes that used to return nil
// or panic; this generates the cause, the wrapping, and the prefix/secret
// combinations around them. A fuzz target is deliberately not used here — the
// inputs are error SHAPES, not the byte strings testing.F generates, and
// FuzzRedactTransportError already fuzzes the text-and-secret half (its
// signature must stay stable to keep its committed corpus).
func TestReduction_non_nil_input_never_yields_nil_property(t *testing.T) {
	t.Parallel()
	// Distinctive so a coincidental re-formation is impossible; the fuzz
	// target's rationale for the same sentinel style applies.
	const secret = "AKIAIOSFODNN7EXAMPLE"
	rapid.Check(t, func(t *rapid.T) {
		causes := []error{nil, errRequestFailed, &testError{msg: "dial tcp: connection refused"}}
		cause := causes[rapid.IntRange(0, len(causes)-1).Draw(t, "cause")]
		ue := &url.Error{
			Op:  rapid.SampledFrom([]string{"", "Get", "Post"}).Draw(t, "op"),
			URL: "https://" + secret + "@host.example/p?token=" + secret,
			Err: cause,
		}
		in := error(ue)
		if rapid.Bool().Draw(t, "wrapped") {
			in = &wrapError{msg: rapid.String().Draw(t, "wrapMsg"), err: ue}
		}
		prefix := rapid.SampledFrom([]string{"", "fetch"}).Draw(t, "prefix")
		sec := rapid.SampledFrom([]string{"", secret, "absent"}).Draw(t, "secret")

		results := map[string]error{
			"LogSafeError":         LogSafeError(in),
			"RedactTransportError": RedactTransportError(in, prefix, Secret(sec)),
		}
		for name, got := range results {
			if got == nil {
				t.Fatalf("%s(non-nil error) = nil (cause=%v prefix=%q secret=%q)", name, cause, prefix, sec)
			}
			if strings.Contains(got.Error(), secret) {
				t.Fatalf("%s leaked the URL secret: %q", name, got.Error())
			}
		}
	})
}

// wrapError is a minimal %w-style wrapper: the reductions must find a
// *url.Error through an application's own error, not just at the top.
type wrapError struct {
	err error
	msg string
}

func (e *wrapError) Error() string { return e.msg + ": " + e.err.Error() }
func (e *wrapError) Unwrap() error { return e.err }

func FuzzParseRetryAfter(f *testing.F) {
	f.Add("")
	f.Add("60")
	f.Add("-1")
	f.Add("0")
	f.Add("3600")
	f.Add("garbage!@#$%")
	f.Add("  30  ")
	f.Add("\t60\n")
	f.Add("Mon, 02 Jan 2006 15:04:05 GMT")

	f.Fuzz(func(t *testing.T, val string) {
		d := ParseRetryAfter(val)
		if d < 0 {
			t.Fatalf("ParseRetryAfter(%q) returned negative: %v", val, d)
		}
		if d > RetryAfterCap {
			t.Fatalf("ParseRetryAfter(%q) = %v exceeds cap %v", val, d, RetryAfterCap)
		}
	})
}

func FuzzParseRetryAfterResponse(f *testing.F) {
	f.Add("")
	f.Add("60")
	f.Add("-1")
	f.Add("0")
	f.Add("3600")
	f.Add("garbage!@#$%")
	f.Add("  30  ")
	f.Add("\t60\n")
	f.Add("Mon, 02 Jan 2006 15:04:05 GMT")

	f.Fuzz(func(t *testing.T, val string) {
		resp := &http.Response{Header: http.Header{}}
		if val != "" {
			resp.Header.Set("Retry-After", val)
		}
		d := ParseRetryAfterResponse(resp)
		if d < 0 {
			t.Fatalf("ParseRetryAfterResponse(%q) returned negative: %v", val, d)
		}
		// Cross-function consistency invariant: both delegate to the shared
		// parseRetryAfterValue, so they must differ only by the cap:
		// ParseRetryAfter(val) == min(ParseRetryAfterResponse(resp), cap).
		// A tolerance absorbs the sub-microsecond clock drift between the two
		// time.Until evaluations an HTTP-date value triggers; a real regression
		// (either function ceasing to delegate, or the cap being dropped) moves
		// the value by whole seconds, far past the margin.
		capped := min(d, RetryAfterCap)
		pra := ParseRetryAfter(val)
		diff := pra - capped
		if diff < 0 {
			diff = -diff
		}
		if diff > 2*time.Second {
			t.Fatalf("ParseRetryAfter(%q)=%v but min(ParseRetryAfterResponse, cap)=%v (diff %v): consistency broken",
				val, pra, capped, diff)
		}
	})
}

func FuzzRedactTransportError(f *testing.F) {
	f.Add("connection refused", "prefix", "mykey123")
	f.Add("", "prefix", "")
	f.Add("error with mykey123 inside", "fetch", "mykey123")
	f.Add("no secret here", "download", "absent")

	f.Fuzz(func(t *testing.T, errMsg, prefix, secret string) {
		var err error
		if errMsg != "" {
			err = &testError{msg: errMsg}
		}
		// Panic-safety: exercise redaction with the arbitrary fuzzed secret.
		result := RedactTransportError(err, prefix, Secret(secret))
		if err == nil {
			if result != nil {
				t.Fatal("expected nil for nil input")
			}
			return
		}
		// Leak property: a distinctive sentinel embedded in the fuzzed message
		// must never survive redaction. Asserting "output contains the arbitrary
		// fuzzed secret" is unsound — short/degenerate secrets coincidentally
		// recur in the error structure (separators, the REDACTED marker, text
		// re-joined when a marker is removed) even though every real occurrence is
		// replaced. A distinctive token cannot be coincidentally re-formed.
		const sentinel = "AKIAIOSFODNN7EXAMPLE"
		sres := RedactTransportError(&testError{msg: errMsg + sentinel}, prefix, sentinel)
		if sres != nil && strings.Contains(sres.Error(), sentinel) {
			t.Fatalf("sentinel secret survived redaction: %q", sres.Error())
		}
	})
}

func FuzzSafeDouble(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1))
	f.Add(int64(-1))
	f.Add(int64(time.Hour))
	f.Add(int64(1<<62 - 1))
	f.Add(int64(1<<63 - 1))

	f.Fuzz(func(t *testing.T, ns int64) {
		d := time.Duration(ns)
		result := SafeDouble(d)
		if d > 0 && result < 0 {
			t.Fatalf("SafeDouble(%v) returned negative: %v", d, result)
		}
		if d > 0 && result < d {
			t.Fatalf("SafeDouble(%v) returned smaller: %v", d, result)
		}
	})
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func FuzzRedirectPolicy(f *testing.F) {
	f.Add("sub.docker.com", ".docker.com", "exact.host.com")
	f.Add("maliciousdocker.com", ".docker.com", "")
	f.Add("docker.com", "docker.com", "")
	f.Add("evil.com", ".example.org", "good.com")

	f.Add("[fe80::1%25ETH0]", ".example.com", "fe80::1%eth0")
	f.Add("[2001:db8:0::A]", ".example.com", "2001:db8::a")
	f.Add("[::1%25x.example.com]", ".example.com", "")
	f.Add("192.0.2.1", ".2.1", "")

	f.Fuzz(func(t *testing.T, host, suffix, allowedHost string) {
		if host == "" || suffix == "" {
			return
		}
		u, err := url.Parse("http://" + host + "/path")
		if err != nil || u.Hostname() == "" || !dialablePort(u.Port()) {
			return
		}
		host = u.Hostname()

		var opts []RedirectOption
		if allowedHost != "" {
			opts = append(opts, WithAllowedHosts(allowedHost))
		}
		opts = append(opts, WithAllowedSuffixes(suffix))
		err = RedirectPolicy(opts...)(&http.Request{URL: u}, []*http.Request{{URL: &url.URL{Host: "origin.com"}}})

		// The suffix is trimmed, dot-anchored and ASCII-lowercased, and
		// matches a name only, never an IP literal.
		norm := strings.TrimSpace(suffix)
		if !strings.HasPrefix(norm, ".") {
			norm = "." + norm
		}
		norm = asciiLower(norm)
		_, ipErr := netip.ParseAddr(host)
		lhost := asciiLower(host)
		suffixMatch := ipErr != nil && len(norm) > 1 && (lhost == norm[1:] || strings.HasSuffix(lhost, norm))
		if want := specSameHost(host, allowedHost) || suffixMatch; want != (err == nil) {
			t.Fatalf("host %q, suffix %q, allowedHost %q: err = %v, want followed=%v", host, suffix, allowedHost, err, want)
		}
	})
}

// specSameHost is the host identity this package promises: a name by ASCII
// folding, an IP literal by address with its zone exact, never an IP literal
// equal to a name.
func specSameHost(a, b string) bool {
	aAddr, aErr := netip.ParseAddr(a)
	bAddr, bErr := netip.ParseAddr(b)
	if aErr == nil || bErr == nil {
		return aErr == nil && bErr == nil && aAddr == bAddr
	}
	return a != "" && asciiLower(a) == asciiLower(b)
}

func dialablePort(port string) bool {
	if port == "" {
		return true
	}
	_, err := strconv.ParseUint(port, 10, 16)
	return err == nil
}

// TestIsRetryableStatus_cross_classifier_invariants_property pins, for any
// code, that the status classifiers agree: one retry verdict across the status
// error (less 429, which Do leaves to its rate-limit options) and the transport
// default, every other retried code an ErrServerError, nothing CheckStatus
// calls success retried, and 408 and 429 the only retried client errors.
func TestIsRetryableStatus_cross_classifier_invariants_property(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		code := rapid.IntRange(0, 999).Draw(t, "status")
		retryable := IsRetryableStatus(code)
		if retryable && CheckStatus(&http.Response{StatusCode: code, Header: http.Header{}}) == nil {
			t.Fatalf("IsRetryableStatus(%d) = true but CheckStatus calls it success: a delivered 2xx must never be repeated", code)
		}
		if got, want := (&StatusError{Code: code}).IsTransient(), retryable && code != http.StatusTooManyRequests; got != want {
			t.Fatalf("StatusError{%d}.IsTransient() = %v, want %v (IsRetryableStatus = %v)", code, got, want, retryable)
		}
		if got, _ := defaultCheckRetry(t.Context(), &http.Response{StatusCode: code}, nil); got != retryable {
			t.Fatalf("defaultCheckRetry(HTTP %d) = %v, IsRetryableStatus = %v", code, got, retryable)
		}
		if retryable && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests &&
			!errors.Is(&StatusError{Code: code}, ErrServerError) {
			t.Fatalf("IsRetryableStatus(%d) = true but it is not an ErrServerError", code)
		}
		if retryable && code >= 400 && code < 500 &&
			code != http.StatusRequestTimeout && code != http.StatusTooManyRequests {
			t.Fatalf("IsRetryableStatus(%d) = true: the only retryable client errors are 408 and 429", code)
		}
	})
}

func TestAsciiLower_invariants_property(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := string(rapid.SliceOf(rapid.Byte()).Draw(t, "bytes"))
		got := asciiLower(s)
		// Length is preserved. strings.ToLower would fold each invalid-UTF-8 byte
		// to the 3-byte U+FFFD, changing length and collapsing distinct hosts into
		// one allowlist-matching class (the documented bypass).
		if len(got) != len(s) {
			t.Fatalf("asciiLower(%q) len = %d, want %d", s, len(got), len(s))
		}
		// Only ASCII A-Z change (to a-z); every other byte is byte-identical.
		for i := range len(s) {
			in := s[i]
			want := in
			if 'A' <= in && in <= 'Z' {
				want += 'a' - 'A'
			}
			if got[i] != want {
				t.Fatalf("asciiLower(%q) byte %d = %#x, want %#x", s, i, got[i], want)
			}
		}
		// Idempotent.
		if again := asciiLower(got); again != got {
			t.Fatalf("asciiLower not idempotent: %q -> %q -> %q", s, got, again)
		}
	})
}

func TestAsciiLower_distinct_non_ascii_bytes_never_collapse_property(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		b1 := byte(rapid.IntRange(0x80, 0xFF).Draw(t, "b1"))
		b2 := byte(rapid.IntRange(0x80, 0xFF).Draw(t, "b2"))
		if b1 == b2 {
			return
		}
		// Distinct non-ASCII bytes must stay distinct after folding — the
		// no-collapse invariant strings.ToLower violates (both would become
		// U+FFFD and compare equal, bypassing the redirect allowlist).
		if asciiLower(string(b1)) == asciiLower(string(b2)) {
			t.Fatalf("asciiLower collapsed distinct bytes %#x and %#x", b1, b2)
		}
	})
}

func FuzzRedactURL(f *testing.F) {
	f.Add("apikey")
	f.Add("weird key&=#%")
	f.Add("")
	f.Add("user:pw@evil")
	f.Fuzz(func(t *testing.T, rawKey string) {
		// Panic-safety on arbitrary URL input.
		_ = redactURL("https://h.example/p?" + rawKey)
		// Security invariant (CWE-532): a distinctive sentinel placed as a query
		// VALUE never survives redaction. rawKey is QueryEscaped into the KEY
		// position so it can neither inject extra params nor turn the sentinel into
		// a (kept) query key — the sound sentinel form FuzzRedactTransportError uses
		// to avoid short-secret coincidences.
		const sentinel = "AKIAIOSFODNN7EXAMPLE"
		out := redactURL("https://h.example/p?" + url.QueryEscape(rawKey) + "=" + sentinel)
		if strings.Contains(out, sentinel) {
			t.Fatalf("sentinel query value survived redaction: %q", out)
		}
	})
}

// FuzzSameHostRedirect exercises the WithSameHost invariant against
// specSameHost: a redirect is accepted iff its target host equals the origin
// host AND it is not an https->http downgrade. FuzzRedirectPolicy seeds
// an empty origin scheme, so it never drives the scheme-downgrade branch this
// covers.
func FuzzSameHostRedirect(f *testing.F) {
	f.Add("https", "arr.example", "https", "arr.example")            // same origin
	f.Add("http", "arr.example", "https", "arr.example")             // upgrade
	f.Add("https", "arr.example", "http", "arr.example")             // downgrade
	f.Add("https", "arr.example", "https", "other.example")          // cross-host
	f.Add("https", "ARR.example", "https", "arr.example")            // case-fold
	f.Add("https", "[fe80::1%25eth0]", "https", "[fe80::1%25ETH0]")  // zone case
	f.Add("https", "[2001:db8::a]", "https", "[2001:DB8:0::A]:8443") // IPv6 spelling
	f.Add("http", "192.0.2.1", "http", "[::ffff:192.0.2.1]")         // IPv4-mapped

	f.Fuzz(func(t *testing.T, origScheme, origHost, tgtScheme, tgtHost string) {
		if (origScheme != "http" && origScheme != "https") || (tgtScheme != "http" && tgtScheme != "https") {
			return
		}
		orig, err := url.Parse(origScheme + "://" + origHost + "/a")
		if err != nil || orig.Hostname() == "" || !dialablePort(orig.Port()) {
			return
		}
		tgt, err := url.Parse(tgtScheme + "://" + tgtHost + "/b")
		if err != nil || tgt.Hostname() == "" || !dialablePort(tgt.Port()) {
			return
		}
		via := []*http.Request{{URL: orig}}
		sameHost := specSameHost(tgt.Hostname(), orig.Hostname())
		downgrade := isSchemeDowngrade(orig.Scheme, tgt.Scheme)

		gotErr := RedirectPolicy(WithSameHost(true))(&http.Request{URL: tgt}, via)
		if want := sameHost && !downgrade; want != (gotErr == nil) {
			t.Fatalf("WithSameHost %s->%s: err=%v (sameHost=%v downgrade=%v, wantAllowed=%v)", orig, tgt, gotErr, sameHost, downgrade, want)
		}
	})
}

// TestSameOriginRedirect_property checks WithSameOrigin(true) against a model
// over random schemes, ports and host spellings in random ASCII case: a hop
// is followed iff scheme, host and effective port are equal, where a host is
// its address for an IP literal, zone case kept, and its folded
// name otherwise, and a refusal is RedirectOtherOrigin naming the target's
// origin as drawn. Adding WithSameHost(true) never changes the verdict, and
// WithSameHost(true) alone follows iff the hosts are equal and no https->http.
func TestSameOriginRedirect_property(t *testing.T) {
	t.Parallel()
	schemes := []string{"http", "https"}
	type host struct {
		id        string
		spellings []string
	}
	hosts := []host{
		{"arr.example", []string{"arr.example"}},
		{"other.example", []string{"other.example"}},
		{"127.0.0.1", []string{"127.0.0.1"}},
		{"::1", []string{"[::1]", "[0:0::1]"}},
		{"2001:db8::a", []string{"[2001:db8::a]", "[2001:db8:0:0::a]"}},
		{"fe80::1%", []string{"[fe80::1%25eth0]", "[fe80:0::1%25eth0]"}},
	}
	ports := []string{"", "80", "443", "8989"}
	effective := func(scheme, port string) string {
		switch {
		case port != "":
			return port
		case scheme == "http":
			return "80"
		}
		return "443"
	}
	rapid.Check(t, func(t *rapid.T) {
		type end struct{ scheme, host, written, port string }
		draw := func(label string, h host) end {
			e := end{
				scheme: rapid.SampledFrom(schemes).Draw(t, label+"Scheme"),
				host:   h.id,
				port:   rapid.SampledFrom(ports).Draw(t, label+"Port"),
			}
			b := []byte(rapid.SampledFrom(h.spellings).Draw(t, label+"Spelling"))
			for i, c := range b {
				if 'a' <= c && c <= 'z' && rapid.Bool().Draw(t, label+"Upper") {
					b[i] = c - ('a' - 'A')
				}
			}
			e.written = string(b)
			if _, zone, zoned := strings.Cut(e.written, "%25"); zoned {
				e.host += strings.TrimSuffix(zone, "]")
			}
			return e
		}
		parse := func(e end, path string) *url.URL {
			hostport := e.written
			if e.port != "" {
				hostport += ":" + e.port
			}
			u, err := url.Parse(e.scheme + "://" + hostport + path)
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			return u
		}
		origHost := rapid.SampledFrom(hosts).Draw(t, "origHost")
		targetHost := origHost
		if !rapid.Bool().Draw(t, "targetOnOrigHost") {
			targetHost = rapid.SampledFrom(hosts).Draw(t, "targetHost")
		}
		o, g := draw("orig", origHost), draw("target", targetHost)
		via := []*http.Request{{URL: parse(o, "/a")}}
		target := &http.Request{URL: parse(g, "/b?k=v")}
		want := o.scheme == g.scheme && o.host == g.host && effective(o.scheme, o.port) == effective(g.scheme, g.port)

		err := RedirectPolicy(WithSameOrigin(true))(target, via)
		if want != (err == nil) {
			t.Fatalf("WithSameOrigin(true) %s -> %s = %v, want followed=%v", via[0].URL, target.URL, err, want)
		}
		if err != nil {
			wantOrigin := g.scheme + "://" + g.written
			if g.port != "" && g.port != effective(g.scheme, "") {
				wantOrigin += ":" + g.port
			}
			refused, ok := errors.AsType[*RedirectRefusedError](err)
			if !ok || refused.Reason != RedirectOtherOrigin || refused.Origin != wantOrigin {
				t.Fatalf("refusal = %#v, want Reason RedirectOtherOrigin, Origin %q", err, wantOrigin)
			}
		}
		withHost := RedirectPolicy(WithSameOrigin(true), WithSameHost(true))(target, via)
		if (withHost == nil) != (err == nil) {
			t.Fatalf("adding WithSameHost(true) changed the verdict: %v, then %v", err, withHost)
		}
		wantHost := o.host == g.host && (o.scheme != "https" || g.scheme != "http")
		if hostOnly := RedirectPolicy(WithSameHost(true))(target, via); wantHost != (hostOnly == nil) {
			t.Fatalf("WithSameHost(true) %s -> %s = %v, want followed=%v", via[0].URL, target.URL, hostOnly, wantHost)
		}
	})
}

// FuzzSameOriginRedirect_host_identity checks host identity on any parsed
// URL: under WithSameOrigin(true) it is same-origin with itself, with its IP
// literal spelled canonically and with its name in other ASCII case, and
// never with its IPv6 zone in other case, which WithSameHost(true) refuses
// too.
func FuzzSameOriginRedirect_host_identity(f *testing.F) {
	f.Add("https://[fe80::1%25eth0]/a")
	f.Add("http://[2001:DB8:0::A]:8080/a")
	f.Add("https://Arr.Example/a")
	f.Add("http://192.0.2.1/a")
	f.Add("https://[::ffff:192.0.2.1%25Lo]:443/a")
	f.Add("https://[fe80::1%25%45th0]/a")

	f.Fuzz(func(t *testing.T, raw string) {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || !dialablePort(u.Port()) {
			return
		}
		via := []*http.Request{{URL: u}}
		withHost := func(hostname string) *url.URL {
			v := *u
			v.Host = hostname
			if strings.Contains(hostname, ":") {
				v.Host = "[" + hostname + "]"
			}
			if port := u.Port(); port != "" {
				v.Host += ":" + port
			}
			return &v
		}
		sameOrigin := RedirectPolicy(WithSameOrigin(true))
		follows := func(v *url.URL) {
			if err := sameOrigin(&http.Request{URL: v}, via); err != nil {
				t.Fatalf("%s -> %s = %v, want followed", u, v, err)
			}
		}
		follows(u)
		addr, err := netip.ParseAddr(u.Hostname())
		if err != nil {
			follows(withHost(flipASCIICase(u.Hostname())))
			return
		}
		follows(withHost(addr.String()))
		zone := addr.Zone()
		if flipped := flipASCIICase(zone); flipped != zone {
			v := withHost(addr.WithZone(flipped).String())
			for name, policy := range map[string]CheckRedirect{
				"WithSameOrigin": sameOrigin,
				"WithSameHost":   RedirectPolicy(WithSameHost(true)),
			} {
				var refused *RedirectRefusedError
				if err := policy(&http.Request{URL: v}, via); !errors.As(err, &refused) || refused.Reason != RedirectOtherOrigin {
					t.Fatalf("%s %s -> %s = %v, want RedirectOtherOrigin", name, u, v, err)
				}
			}
		}
	})
}

func flipASCIICase(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' {
			b[i] = c ^ ('a' - 'A')
		}
	}
	return string(b)
}
