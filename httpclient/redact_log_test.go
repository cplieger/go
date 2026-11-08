package httpclient_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

func TestStatusError_ErrorRedactsURL(t *testing.T) {
	se := httpclient.NewStatusErrorForURL("https://user:pw@host.example/api?apikey=supersecret", 429)
	msg := se.Error()
	for _, s := range []string{"supersecret", "pw"} {
		if strings.Contains(msg, s) {
			t.Errorf("StatusError.Error() = %q, leaked %q", msg, s)
		}
	}
	if !strings.Contains(msg, "HTTP 429") {
		t.Errorf("StatusError.Error() = %q, want status code", msg)
	}
	// The raw URL is kept unexported for the redacted rendering only.
	if !strings.Contains(httpclient.StatusErrorURL(se), "supersecret") {
		t.Error("the raw URL should be kept for Error() to redact")
	}
}

func TestGetBytes_doesNotLeakSecretInLogsOrError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	_, err := httpclient.GetBytes(t.Context(), srv.Client(), srv.URL+"?apikey=supersecret",
		httpclient.WithMaxAttempts(2),
		httpclient.WithBaseDelay(time.Microsecond),
		httpclient.WithLogger(logger),
	)
	if err == nil {
		t.Fatal("expected error from exhausted retries")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("returned error leaked secret: %v", err)
	}
	// errors.Is must still resolve through the (redacted) StatusError chain.
	if !errors.Is(err, httpclient.ErrServerError) {
		t.Errorf("errors.Is(err, ErrServerError) = false; chain not preserved: %v", err)
	}
	logged := buf.String()
	if strings.Contains(logged, "supersecret") {
		t.Errorf("retry logging leaked secret:\n%s", logged)
	}
	if !strings.Contains(logged, "retries exhausted") {
		t.Errorf("expected retry diagnostics in logs, got:\n%s", logged)
	}
}

// normalizeForTest reproduces the one behavior the ordering contract needs from
// a normalizing transform: an invalid UTF-8 byte becomes U+FFFD, exactly as
// textsafe's strings.Map and encoding/json both render it. It is built on
// strings.ToValidUTF8 rather than importing textsafe because httpclient carries no
// runtime dependencies.
func normalizeForTest(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}

// TestRedactSecretStringOrderingContract pins the composition documented on
// RedactSecretString: redact, normalize, redact, cap. Legs (a) and (b)
// reproduce the two single-sided failures, and leg (c) shows the canonical
// order clean, asserting at both redaction boundaries so dropping either call
// fails here rather than silently reopening the trap.
func TestRedactSecretStringOrderingContract(t *testing.T) {
	// An invalid UTF-8 byte is the cheapest value whose raw and normalized forms
	// differ, and it is the shape of the seed case this contract came from.
	secret := "tok" + "\xff" + "en"
	normalized := normalizeForTest(secret)
	if secret == normalized {
		t.Fatalf("fixture is vacuous: raw and normalized secret are both %q", secret)
	}

	// The haystack carries the value twice: once as the exact bytes the caller
	// holds, and once with a different invalid byte, which no byte-exact needle
	// matches and which the transform maps onto the secret's normalized form.
	variant := "tok" + "\xfe" + "en"
	if strings.Contains(variant, secret) || normalizeForTest(variant) != normalized {
		t.Fatalf("fixture is vacuous: variant %q must miss the raw needle and normalize onto it", variant)
	}
	haystack := "upstream rejected token=" + secret + " retried token=" + variant + " (401)"

	// (a) Shape A: redaction before the transform and none after. The transform
	// builds the secret's normalized form out of text the needle never matched,
	// so the value shows up after the mask already ran.
	beforeOnly := normalizeForTest(httpclient.RedactSecretString(haystack, httpclient.Secret(secret)))
	if !strings.Contains(beforeOnly, normalized) {
		t.Errorf("shape A no longer reproduces, fixture is stale: %q", beforeOnly)
	}

	// (b) Shape D: redaction only after the transform, with the needle in the raw
	// representation. The transform rewrote the byte inside the value, so the
	// byte-exact needle matches nothing and the normalized value survives.
	afterOnly := httpclient.RedactSecretString(normalizeForTest(haystack), httpclient.Secret(secret))
	if !strings.Contains(afterOnly, normalized) {
		t.Errorf("shape D no longer reproduces, fixture is stale: %q", afterOnly)
	}

	// (c) The canonical composition: redact, normalize, redact. Each side passes
	// the needle in the representation that side carries.
	redacted := httpclient.RedactSecretString(haystack, httpclient.Secret(secret))
	if strings.Contains(redacted, secret) {
		t.Errorf("redaction before the transform left the raw value in place: %q", redacted)
	}
	safe := httpclient.RedactSecretString(normalizeForTest(redacted), httpclient.Secret(normalized))
	if strings.Contains(safe, secret) || strings.Contains(safe, normalized) {
		t.Errorf("canonical composition leaked the secret: %q", safe)
	}
	if strings.Count(safe, "REDACTED") != 2 {
		t.Errorf("canonical composition redacted %d of 2 occurrences: %q", strings.Count(safe, "REDACTED"), safe)
	}
}

// TestStatusErrorRenderingIsANormalizingTransform pins the "this package
// performs one of those transforms" clause of RedactSecretString's ordering
// contract, in both directions.
//
// StatusError.Error() parses and re-serializes the URL, which is rule 2's
// normalizing transform. For a secret in the PATH — the one credential-bearing
// position the rendering keeps verbatim — a byte the URL encoder rewrites means
// a caller redacting AFTER the rendering matches nothing, which is the failure
// the contract exists to name. For a secret in the query or the userinfo the
// rendering removes the value outright and no redaction is needed at all.
//
// Both halves must hold or the doc is wrong: the encoded cases prove the hazard
// is real, and the pass-through case proves the fixture is not vacuous (it shows
// a secret with no encoding-sensitive byte DOES survive to be matched, so the
// misses above are the transform's doing and not an artifact of the harness).
// Measured identical on go1.26.7 and go1.27.0, so this is a standing property of
// net/url's serializer, not a toolchain-version behavior.
func TestStatusErrorRenderingIsANormalizingTransform(t *testing.T) {
	t.Parallel()

	pathCases := []struct {
		name    string
		secret  string
		want    string // the form the rendering leaves behind
		matched bool   // does a byte-exact needle still find the secret?
	}{
		{"unencoded secret passes through", "plainToken123", "plainToken123", true},
		{"plus is not re-encoded in a path", "tok+en", "tok+en", true},
		{"space becomes %20", "tok en", "tok%20en", false},
		{"non-ASCII becomes percent-encoded UTF-8", "tokén", "tok%C3%A9n", false},
		{"hash truncates at the fragment boundary", "tok#en", "tok", false},
	}
	for _, tc := range pathCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := "https://example.com/hooks/" + tc.secret + "/deliver"
			rendered := httpclient.NewStatusErrorForURL(raw, 500).Error()

			if !strings.Contains(rendered, tc.want) {
				t.Errorf("StatusError.Error() = %q, want it to contain %q", rendered, tc.want)
			}
			if got := strings.Contains(rendered, tc.secret); got != tc.matched {
				t.Errorf("rendering contains the raw secret %q = %v, want %v", tc.secret, got, tc.matched)
			}
			// The consequence the contract names: redacting only after the
			// transform leaves the rewritten form in place.
			afterOnly := httpclient.RedactSecretString(rendered, httpclient.Secret(tc.secret))
			if strings.Contains(afterOnly, tc.want) == tc.matched {
				t.Errorf("after-only redaction of %q = %q; want the rewritten form %q to survive exactly when the raw needle missed", tc.secret, afterOnly, tc.want)
			}
			// And the caller's remedy: redact before the URL reaches httpclient.
			safe := httpclient.NewStatusErrorForURL(httpclient.RedactSecretString(raw, httpclient.Secret(tc.secret)), 500).Error()
			if strings.Contains(safe, tc.secret) || strings.Contains(safe, tc.want) {
				t.Errorf("redacting before the rendering still leaked: %q", safe)
			}
		})
	}

	// A stray '%' makes the URL unparseable, and the rendering fails closed on a
	// contentless placeholder rather than echoing the raw string.
	t.Run("unparseable url fails closed", func(t *testing.T) {
		t.Parallel()
		rendered := httpclient.NewStatusErrorForURL("https://example.com/hooks/tok%en/deliver", 500).Error()
		if strings.Contains(rendered, "tok") {
			t.Errorf("StatusError.Error() = %q, want no fragment of the path", rendered)
		}
		if !strings.Contains(rendered, "[unparseable url]") {
			t.Errorf("StatusError.Error() = %q, want the fixed placeholder", rendered)
		}
	})

	// The other two credential-bearing positions need no caller redaction: the
	// rendering removes the value without matching anything, so no encoding
	// question arises. The token is spelled with URL-legal bytes only, because a
	// space in the userinfo makes the whole URL unparseable (the fail-closed case
	// above) and would prove nothing about removal.
	t.Run("query values and userinfo are removed wholesale", func(t *testing.T) {
		t.Parallel()
		rendered := httpclient.NewStatusErrorForURL("https://tokenABC:pw@example.com/p?apikey=tokenABC&sig=tokenABC", 500).Error()
		if strings.Contains(rendered, "tokenABC") || strings.Contains(rendered, "pw") {
			t.Errorf("StatusError.Error() = %q, want no fragment of the credential from the query or userinfo", rendered)
		}
		if !strings.Contains(rendered, "REDACTED") {
			t.Errorf("StatusError.Error() = %q, want the REDACTED marker", rendered)
		}
		// The query KEYS survive, which is the documented debugging affordance.
		for _, k := range []string{"apikey", "sig"} {
			if !strings.Contains(rendered, k) {
				t.Errorf("StatusError.Error() = %q, want the query key %q kept for debugging", rendered, k)
			}
		}
	})
}

// TestSchemeDowngradeFoldIsUnicodeIndependent pins that the library's one
// fold-based predicate cannot move with a Unicode upgrade.
//
// isSchemeDowngrade is the only strings.EqualFold in this package; every other
// case-insensitive comparison (the redirect host allowlist and its suffixes)
// goes through asciiLower, a byte loop that consults no Unicode table at all —
// deliberately, because strings.ToLower folds each invalid UTF-8 byte to U+FFFD
// and would collapse distinct hosts into one allowlist-matching class.
//
// Go 1.27 moves unicode from 15 to 17, changing SimpleFold for 116 runes.
// Measured on both toolchains: the fold orbits of h/t/p/s in either case are
// byte-identical (10 non-self partners on 1.26.7 and on 1.27.0), so none of the
// 116 joins an orbit this predicate reads. Two further facts bound it to zero:
// url.Parse refuses a non-ASCII scheme outright rather than producing one, and
// it lowercases the scheme it does produce — so on any URL net/http hands a
// CheckRedirect, the EqualFold is doing no folding at all. And the direction is
// safe regardless: a true verdict REFUSES the hop, so a more permissive fold can
// only refuse more, never allow more.
func TestSchemeDowngradeFoldIsUnicodeIndependent(t *testing.T) {
	t.Parallel()

	// U+017F LATIN SMALL LETTER LONG S folds with 's'/'S', and has since long
	// before Unicode 15 — it is the one non-ASCII rune reachable here, and it is
	// not part of the 1.27 delta.
	const longS = "\u017f"
	for _, from := range []string{"https", "HTTPS", "HtTpS", "http" + longS, "HTTP" + longS} {
		policy := httpclient.RedirectPolicy(httpclient.WithSameHost(true))
		orig, _ := http.NewRequest(http.MethodGet, "https://example.com/a", http.NoBody)
		orig.URL.Scheme = from // set directly: url.Parse would never emit the long-s form
		next, _ := http.NewRequest(http.MethodGet, "http://example.com/b", http.NoBody)

		err := policy(next, []*http.Request{orig})
		if err == nil {
			t.Errorf("policy(from=%q -> http) = nil, want a scheme-downgrade refusal", from)
		}
	}

	// url.Parse cannot produce a long-s scheme from the wire, so the case above
	// is only reachable by a hand-built *url.URL.
	//nolint:staticcheck // SA1007 correctly reports this literal as an invalid URL; asserting exactly that is the test.
	if u, err := url.Parse("http" + longS + "://example.com/x"); err == nil {
		t.Errorf("url.Parse(long-s scheme) = %+v, want an error; the fold exposure is only reachable via a hand-built URL", u)
	}
	// And the scheme net/url does produce is already lowercased, so EqualFold
	// has nothing to fold on any URL that came through it.
	u, err := url.Parse("HTTPS://example.com/x")
	if err != nil {
		t.Fatalf("url.Parse = %v, want nil", err)
	}
	if u.Scheme != "https" {
		t.Errorf("url.Parse(%q).Scheme = %q, want %q (net/url lowercases it)", "HTTPS://example.com/x", u.Scheme, "https")
	}
}

// nestURLErrors wraps cause in depth *url.Error layers whose URL carries
// secret as its password, each under a wrapper that re-renders the text below.
func nestURLErrors(cause error, depth int, secret string) error {
	err := cause
	for i := range depth {
		err = fmt.Errorf("hop %d: %w", i, &url.Error{
			Op:  "Get",
			URL: fmt.Sprintf("https://user:%s@hop%d.example/", secret, i),
			Err: err,
		})
	}
	return err
}

func TestLogSafeErrorFixpoint(t *testing.T) {
	t.Parallel()
	const secret = "test-key-nested"
	reductions := map[string]func(error) error{
		"LogSafeError":         httpclient.LogSafeError,
		"RedactTransportError": func(err error) error { return httpclient.RedactTransportError(err, "", "") },
	}
	for name, reduce := range reductions {
		t.Run(name+"_five_deep", func(t *testing.T) {
			t.Parallel()
			cause := errors.New("connection refused")
			in := nestURLErrors(cause, 5, secret)
			if !strings.Contains(in.Error(), secret) {
				t.Fatal("setup: the input does not carry the secret")
			}
			got := reduce(in)
			if strings.Contains(got.Error(), secret) || strings.Contains(got.Error(), "hop") {
				t.Errorf("%s(five nested *url.Error) = %q, kept URL text", name, got)
			}
			if !errors.Is(got, cause) {
				t.Errorf("%s(five nested *url.Error) = %v, want the innermost cause", name, got)
			}
		})
		t.Run(name+"_past_the_bound", func(t *testing.T) {
			t.Parallel()
			got := reduce(nestURLErrors(errors.New("connection refused"), 9, secret))
			if got == nil || got.Error() != "request failed" {
				t.Errorf("%s(nine nested *url.Error) = %v, want the contentless %q", name, got, "request failed")
			}
		})
		t.Run(name+"_self_referencing", func(t *testing.T) {
			t.Parallel()
			self := &url.Error{Op: "Get", URL: "https://user:" + secret + "@loop.example/"}
			self.Err = self
			done := make(chan error, 1)
			go func() { done <- reduce(self) }()
			select {
			case got := <-done:
				if _, ok := errors.AsType[*url.Error](got); ok {
					t.Fatalf("%s(self-referencing *url.Error) returned a *url.Error, whose text recurses forever", name)
				}
				if got == nil || got.Error() != "request failed" {
					t.Errorf("%s(self-referencing *url.Error) = %v, want the contentless %q", name, got, "request failed")
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s(self-referencing *url.Error) did not return", name)
			}
		})
	}
}
