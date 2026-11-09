package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// --- Stub RoundTripper helpers ---

// stubRT always returns the given response/error.
type stubRT struct {
	resp *http.Response
	err  error
}

func (s *stubRT) RoundTrip(*http.Request) (*http.Response, error) {
	return s.resp, s.err
}

// failThenSucceedRT fails the first N calls, then succeeds.
type failThenSucceedRT struct {
	successResp *http.Response
	failCount   int64
	calls       atomic.Int64
}

func (f *failThenSucceedRT) RoundTrip(*http.Request) (*http.Response, error) {
	n := f.calls.Add(1)
	if n <= f.failCount {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{},
		}, nil
	}
	return f.successResp, nil
}

func (f *failThenSucceedRT) reset() { f.calls.Store(0) }

// --- RetryRoundTripper benchmarks ---

func BenchmarkRetryRoundTripper_Success(b *testing.B) {
	okResp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     http.Header{},
	}
	rt := NewRetryRoundTripper(&stubRT{resp: okResp}, TransportConfig{MaxAttempts: 3})
	req, _ := http.NewRequestWithContext(b.Context(), http.MethodGet, "http://example.com", http.NoBody)

	for b.Loop() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatal("unexpected status")
		}
	}
}

func BenchmarkRetryRoundTripper_RetryThenSuccess(b *testing.B) {
	okResp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     http.Header{},
	}
	inner := &failThenSucceedRT{failCount: 1, successResp: okResp}
	// A 1ns base delay keeps the benchmark measuring the retry machinery, not
	// sleep (the jittered wait from a 1ns base is at most 1ns).
	rt := NewRetryRoundTripper(inner, TransportConfig{MaxAttempts: 4, BaseDelay: time.Nanosecond})
	req, _ := http.NewRequestWithContext(b.Context(), http.MethodGet, "http://example.com", http.NoBody)

	for b.Loop() {
		inner.reset()
		resp, err := rt.RoundTrip(req)
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatal("unexpected status")
		}
	}
}

// BenchmarkRetryRoundTripper_SingleTry is the path of a request the
// transport may not replay, which a door with no Observer sends straight
// through.
func BenchmarkRetryRoundTripper_SingleTry(b *testing.B) {
	okResp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     http.Header{},
	}
	rt := NewRetryRoundTripper(&stubRT{resp: okResp}, TransportConfig{})
	req, _ := http.NewRequestWithContext(b.Context(), http.MethodPost, "http://example.com", strings.NewReader("x"))

	for b.Loop() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

// --- Do benchmarks ---

func BenchmarkDo_Success(b *testing.B) {
	fn := func(context.Context) (int, error) { return 1, nil }
	for b.Loop() {
		if _, err := Do(b.Context(), fn); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDo_SuccessObserved(b *testing.B) {
	fn := func(context.Context) (int, error) { return 1, nil }
	obs := WithObserver(Observer{Attempt: func(Attempt) {}, Outcome: func(Outcome) {}})
	for b.Loop() {
		if _, err := Do(b.Context(), fn, obs); err != nil {
			b.Fatal(err)
		}
	}
}

// --- JitteredBackoff benchmark ---

func BenchmarkJitteredBackoff(b *testing.B) {
	base := time.Second
	for b.Loop() {
		_ = JitteredBackoff(base)
	}
}

// --- SafeDouble benchmark ---

func BenchmarkSafeDouble(b *testing.B) {
	d := 500 * time.Millisecond
	for b.Loop() {
		d = SafeDouble(d)
		if d < 0 {
			d = 500 * time.Millisecond // reset to prevent trivial ops at max
		}
	}
}

// --- ParseRetryAfter benchmarks ---

func BenchmarkParseRetryAfter_Seconds(b *testing.B) {
	for b.Loop() {
		_ = ParseRetryAfter("30")
	}
}

func BenchmarkParseRetryAfter_HTTPDate(b *testing.B) {
	// A fixed date string to exercise the HTTP-date parsing path.
	header := "Tue, 03 Jun 2025 08:00:00 GMT"
	for b.Loop() {
		_ = ParseRetryAfter(header)
	}
}

func BenchmarkParseRetryAfter_Empty(b *testing.B) {
	for b.Loop() {
		_ = ParseRetryAfter("")
	}
}

// --- IsTransient benchmarks ---

func BenchmarkIsTransient_UnexpectedEOF(b *testing.B) {
	// io.ErrUnexpectedEOF is the canonical transient network error path.
	err := fmt.Errorf("read body: %w", io.ErrUnexpectedEOF)
	for b.Loop() {
		_ = IsTransient(err)
	}
}

func BenchmarkIsTransient_Nil(b *testing.B) {
	for b.Loop() {
		_ = IsTransient(nil)
	}
}

func BenchmarkIsTransient_Permanent(b *testing.B) {
	err := Permanent(errors.New("bad request"))
	for b.Loop() {
		_ = IsTransient(err)
	}
}

// --- Allocation contracts ---
//
// The benchmarks above already report allocations per operation, and seven of
// them report exactly zero: JitteredBackoff, SafeDouble, ParseRetryAfter's
// seconds and empty cases, and IsTransient on an unexpected EOF, a nil error
// and a Permanent mark. Each of those zeros is a contract nobody had written
// down, and nothing in this repo asserted one until this section landed.
//
// The contracts below exist because a chart cannot hold a contract. httpclient is
// enrolled in the weekly benchmark tracker, which compares each series against
// its previous run and alerts above a ratio, and that arithmetic decides what is
// worth writing here:
//
//   - A series that goes from 0 to any non-zero number produces an infinite
//     ratio, so it alerts at every threshold. The seven zero-allocation paths
//     above are therefore already watched weekly — but a week later, as a chart
//     comment, and only if someone reads it. The `== 0` assertions below say the
//     same thing at merge time and name the function that broke.
//   - A series that goes from 3 allocations to 4 produces a ratio of 1.33, and
//     from 2 to 3 exactly 1.5, which is silent at a 1.5 threshold. So the
//     contracts the tracker CANNOT see are the valuable half of this file: a
//     bounded constant count that must not start growing with the input, and
//     counts that must stay equal across input classes. Those are the length- and
//     occurrence-scaling tests, and they are where an amplification vector would
//     appear.
//
// Every function measured here runs on the retry-decision path or on the error
// path immediately before a log line, which is the same reasoning in two shapes:
// a per-call allocation multiplies by request volume exactly when a service is
// already failing, and an input-proportional cost hands an upstream a lever it
// can pull by sending more bytes.
//
// No new Benchmark functions are added. Each b.Run name becomes a permanent
// chart series with a permanent slice of every weekly run, and per-input
// precision is cheaper and more exact in an AllocsPerRun table than in a time
// series. Where a property needs a trend rather than a gate, that is a
// deliberate follow-up, not an omission.
//
// WHAT THE MEASUREMENT FOUND, recorded so the next reader need not re-derive it
// (go1.27.0, identical with and without -race):
//
//   - IsTransient allocates NOTHING on any error class measured, in both
//     verdicts, bare and wrapped eight deep. That covers the interface lookups,
//     which was the open question: errors.AsType against the Transient and
//     net.Error interfaces boxes nothing.
//   - JitteredBackoff and SafeDouble allocate nothing at any duration, including
//     the non-positive passthroughs and the overflow clamp.
//   - ParseRetryAfter is free on the delta-seconds form and on an absent value,
//     and costs 2 allocations on the IMF-fixdate form RFC 9110 mandates (5 on
//     RFC 850, 8 on ANSI C, because http.ParseTime tries the three layouts in
//     order). An UNPARSEABLE value costs 11: both parsers run and both fail.
//   - That 11 is CONSTANT from a 1-byte header to a 1 MB one, which is the
//     property asserted below. The byte volume is not constant: it tracks the
//     header length at roughly 7x, because strconv.ParseInt clones the string
//     into its *NumError and each of http.ParseTime's three failed layouts does
//     likewise. A 1 MB Retry-After copies about 7 MB. That is a real,
//     upstream-controlled amplification and it is recorded here deliberately
//     unasserted: a byte-volume assertion measures the stdlib's error-reporting
//     internals, while the flat COUNT is what distinguishes bounded work from
//     unbounded, and it is the number that would move if this package started
//     scanning or copying the header itself.
//   - RedactSecretString's cost is constant in the number of occurrences, which
//     is the result that mattered: strings.ReplaceAll sizes one buffer up front,
//     so one occurrence and 4096 occurrences both cost a single allocation. It
//     is FREE when the secret is absent, at any haystack length, which is the
//     common case on a log path.
//   - One expected property did NOT hold, and is recorded rather than asserted:
//     "free when the secret is absent" belongs to the ERROR, not to
//     RedactTransportError. The helper has to call Error() to find out whether
//     the secret is there, so an error that BUILDS its message pays for that
//     message on the no-op path — a *StatusError costs 5 allocations to redact
//     nothing, because its Error() re-serializes the URL through redactURL.
//   - The negative path is not the expensive path for the classifier (every
//     class is 0), but it IS for the parser: a well-formed Retry-After is free
//     and a malformed one costs 11, so the cheap input is the one a cooperating
//     upstream sends. The count is small and bounded, so this is a fact to pin
//     rather than a defect to fix.
//
// Every fixture is built outside the measured closure. A strings.Repeat, an
// fmt.Errorf or an fmt.Sprintf inside the closure is counted, and would make a
// clean function look like it allocates.
//
// None of these tests may call t.Parallel: testing.AllocsPerRun pins GOMAXPROCS
// to 1 and reads process-wide allocation counters, so a concurrent sibling's
// allocations would land in this measurement.

// boolSink, durSink, strSink and errSink absorb the result of every measured
// call. AllocsPerRun's closure returns nothing, so without a store to a
// package-level variable the compiler is free to elide a pure call and leave the
// test measuring an empty loop.
var (
	boolSink bool
	durSink  time.Duration
	strSink  string
	errSink  error
)

// maxParseAllocs is a deliberately generous ceiling on Retry-After parsing. The
// point of a ceiling is that the number is bounded at all, not that it is any
// particular small value: the measured worst case is 14 (a date-shaped prefix
// with a garbage tail), and a rewrite that grew the count per input byte or per
// added layout would blow past this long before it reached it.
const maxParseAllocs = 32

// maxParseBytes is the same kind of ceiling for allocated BYTES, and it is the
// one that can actually see a header-copying parse. Measured worst case across
// every class and length is 336 bytes, all of it from the date parse on inputs
// under the date bound; the pre-fix behaviour reached roughly 7 MB on a 1 MB
// header. Anything between those two numbers is a parse that has started
// copying what the upstream sent.
const maxParseBytes = 4096

// maxRedactAllocs is the same kind of ceiling for the error-shaped redaction
// helpers, whose prefixed form measures 5.
const maxRedactAllocs = 16

// wrapChain returns err wrapped depth times with fmt.Errorf("%w"), the shape a
// consumer's own error handling hands to IsTransient after a few layers of
// context. Depth matters to this contract because every classification step is
// an unwrap walk, so a per-layer allocation would only appear on a deep chain.
func wrapChain(err error, depth int) error {
	for range depth {
		err = fmt.Errorf("attempt failed: %w", err)
	}
	return err
}

// abbrev renders a fixture for a failure message without pasting a megabyte of
// attacker-shaped bytes into the test log, while keeping the length visible —
// the length IS the variable under test in the scaling checks below.
func abbrev(s string) string {
	const keep = 24
	if len(s) <= keep {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q...(%d bytes)", s[:keep], len(s))
}

// capabilityError implements the Transient capability interface, the extension
// seam a consumer's own error type uses to declare its own retryability (arrapi's
// StatusError is one). It is here because IsTransient reaches it
// through errors.AsType against an INTERFACE, which is the one lookup in the
// classifier that could plausibly box a value and allocate.
type capabilityError struct {
	transient bool
}

func (*capabilityError) Error() string       { return "capability error" }
func (e *capabilityError) IsTransient() bool { return e.transient }

// transientErrors are the classes IsTransient reports as retryable, one per
// mechanism in the classifier: the AttemptTimeout mark, the capability
// interface, the built-in *StatusError transient band, MarkTransient, a
// net.Error timeout, io.ErrUnexpectedEOF, the dropped-connection errnos, and a
// DNS failure. Each appears bare and, where a consumer would realistically wrap
// it, at depth.
var transientErrors = map[string]error{
	"attempt_timeout":           AttemptTimeout(context.DeadlineExceeded),
	"attempt_timeout_wrapped":   wrapChain(AttemptTimeout(context.DeadlineExceeded), 8),
	"capability_says_transient": &capabilityError{transient: true},
	"http_500":                  &StatusError{Code: 500},
	"http_502":                  &StatusError{Code: 502},
	"http_503":                  &StatusError{Code: 503},
	"marked_transient":          MarkTransient(errors.New("upstream 500 always clears")),
	"net_timeout":               &benchNetError{timeout: true},
	"unexpected_eof":            io.ErrUnexpectedEOF,
	"unexpected_eof_wrapped":    fmt.Errorf("read body: %w", io.ErrUnexpectedEOF),
	"unexpected_eof_depth_8":    wrapChain(io.ErrUnexpectedEOF, 8),
	"econnreset":                syscall.ECONNRESET,
	"econnreset_wrapped":        fmt.Errorf("write tcp: %w", syscall.ECONNRESET),
	"econnrefused":              syscall.ECONNREFUSED,
	"epipe":                     syscall.EPIPE,
	"epipe_net_op_error":        &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)},
	"dns_error":                 &net.DNSError{Err: "no such host", Name: "api.example"},
	"dns_error_wrapped":         fmt.Errorf("dial: %w", &net.DNSError{Err: "no such host"}),
	"url_error_over_eof":        &url.Error{Op: "Get", URL: "https://api.example/x", Err: io.ErrUnexpectedEOF},
	"url_error_over_hang_up":    &url.Error{Op: "Get", URL: "https://api.example/x", Err: io.EOF},
	"server_closed":             errServerClosed,
}

// nonTransientErrors are the classes IsTransient refuses, one per standing
// rejection: nil, Permanent (including over an otherwise-transient error), a
// 401 and a 429 *StatusError, a caller context error in both flavours, a
// capability type that declares itself non-transient, a status outside the
// transient band, and an error the classifier recognizes not at all. The plain
// error is the classifier's longest path: every check runs and none matches.
var nonTransientErrors = map[string]error{
	"nil":                           nil,
	"permanent":                     Permanent(errors.New("bad request")),
	"permanent_over_transient":      Permanent(&StatusError{Code: 503}),
	"permanent_over_attempt_bound":  Permanent(AttemptTimeout(context.DeadlineExceeded)),
	"auth_error":                    &StatusError{Code: http.StatusUnauthorized},
	"rate_limit_error":              &StatusError{Code: http.StatusTooManyRequests},
	"caller_deadline":               context.DeadlineExceeded,
	"caller_deadline_wrapped":       wrapChain(context.DeadlineExceeded, 8),
	"caller_canceled":               context.Canceled,
	"capability_says_not_transient": &capabilityError{transient: false},
	"net_non_timeout":               &benchNetError{timeout: false},
	"net_op_error_timeout_text":     &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")},
	"http_400":                      &StatusError{Code: 400},
	"http_501":                      &StatusError{Code: 501},
	"bare_eof":                      io.EOF,
	"unrecognized":                  errors.New("something else went wrong"),
	"unrecognized_depth_8":          wrapChain(errors.New("something else went wrong"), 8),
}

// benchNetError is a net.Error whose Timeout verdict the fixture chooses, so the
// classifier's net.Error branch can be measured in both directions without a
// real socket. It mirrors fakeNetError in httpclient_test.go, kept separate so this
// file's fixtures do not depend on another file's helpers.
type benchNetError struct{ timeout bool }

func (*benchNetError) Error() string   { return "bench net error" }
func (e *benchNetError) Timeout() bool { return e.timeout }
func (*benchNetError) Temporary() bool { return false }

// TestIsTransientAllocations pins the classifier at zero allocations for every
// error class it distinguishes, in both verdicts.
//
// IsTransient runs once per failed attempt on every retry path in the package —
// Do, GetBytes and the RetryRoundTripper's default policy all funnel through it
// — so its per-call cost multiplies by the failure volume of a service that is,
// by construction, already failing. That is the wrong moment to start allocating.
//
// The assertion is a flat `== 0` and not a threshold because AllocsPerRun is
// exact: the classifier either allocates on a class or it does not, so any
// non-zero average means a step that used to be free is not. The classes are
// enumerated rather than sampled because each one exits at a different point in
// the function, and a rewrite that (say) formatted an error to inspect it would
// only show up on the classes that reach that far.
func TestIsTransientAllocations(t *testing.T) {
	for name, err := range transientErrors {
		t.Run("transient_"+name, func(t *testing.T) {
			if !IsTransient(err) {
				t.Fatalf("IsTransient(%v) = false, want true: this fixture must "+
					"exercise the retryable path or the measurement below is of "+
					"the wrong branch", err)
			}
			if got := testing.AllocsPerRun(200, func() {
				boolSink = IsTransient(err)
			}); got != 0 {
				t.Errorf("IsTransient(%v) allocated %v times per run, want 0: the "+
					"classifier runs once per failed attempt, so a per-call "+
					"allocation multiplies by request volume exactly when the "+
					"upstream is already failing", err, got)
			}
		})
	}

	for name, err := range nonTransientErrors {
		t.Run("non_transient_"+name, func(t *testing.T) {
			if IsTransient(err) {
				t.Fatalf("IsTransient(%v) = true, want false: this fixture must "+
					"exercise the refusal path or the measurement below is of the "+
					"wrong branch", err)
			}
			if got := testing.AllocsPerRun(200, func() {
				boolSink = IsTransient(err)
			}); got != 0 {
				t.Errorf("IsTransient(%v) allocated %v times per run, want 0: a "+
					"terminal failure is classified on the same hot path as a "+
					"retryable one and must be no more expensive", err, got)
			}
		})
	}
}

// TestIsTransientRefusalIsNoMoreExpensiveThanRetry states the property that
// survives even if the zero above ever becomes a non-zero: refusing an error
// must not cost more than accepting one.
//
// The refusal path is the one an upstream chooses. Auth failures, rate limits and
// unrecognized errors all come from the far end, and the plain unrecognized error
// is the classifier's longest walk — every rejection test, every capability
// lookup and both errno comparisons run before it answers false. If that path
// were the expensive one, a peer could select it by failing in the right shape.
//
// Written as a comparison rather than a second `== 0` table on purpose: the
// numbers above already gate zero, and this one keeps its meaning after any
// future change that makes the whole classifier cost something.
func TestIsTransientRefusalIsNoMoreExpensiveThanRetry(t *testing.T) {
	worstRefusal, worstName := -1.0, ""
	for name, err := range nonTransientErrors {
		if got := testing.AllocsPerRun(200, func() {
			boolSink = IsTransient(err)
		}); got > worstRefusal {
			worstRefusal, worstName = got, name
		}
	}

	cheapestRetry, cheapestName := math.Inf(1), ""
	for name, err := range transientErrors {
		if got := testing.AllocsPerRun(200, func() {
			boolSink = IsTransient(err)
		}); got < cheapestRetry {
			cheapestRetry, cheapestName = got, name
		}
	}

	if worstRefusal > cheapestRetry {
		t.Errorf("IsTransient allocated %v times per run on its most expensive "+
			"refusal (%s) but only %v on its cheapest retryable class (%s), want "+
			"refusal <= retry: the refusal path is the one an upstream picks, so "+
			"it must not be the expensive one",
			worstRefusal, worstName, cheapestRetry, cheapestName)
	}
}

// TestIsRetryableStatusAllocations pins the status half of the retry decision at
// zero. It is the same rule GetBytes applies to every response it receives, and
// it is exported precisely so a caller can apply it per response in its own
// loop, which puts it on the same per-request hot path as IsTransient.
func TestIsRetryableStatusAllocations(t *testing.T) {
	codes := map[string]int{
		"200_ok":              http.StatusOK,
		"301_moved":           http.StatusMovedPermanently,
		"404_not_found":       http.StatusNotFound,
		"408_request_timeout": http.StatusRequestTimeout,
		"429_rate_limited":    http.StatusTooManyRequests,
		"500_server_error":    http.StatusInternalServerError,
		"503_unavailable":     http.StatusServiceUnavailable,
	}
	for name, code := range codes {
		t.Run(name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				boolSink = IsRetryableStatus(code)
			}); got != 0 {
				t.Errorf("IsRetryableStatus(%d) allocated %v times per run, want 0: "+
					"the predicate is consulted once per response, and a caller "+
					"nesting a door in its own loop calls it per attempt", code, got)
			}
		})
	}
}

// TestBackoffPrimitivesAllocations pins JitteredBackoff and SafeDouble at zero
// across their input classes, including the guard paths.
//
// Both run once per inter-attempt wait, so their cost is paid on the same
// schedule as the retries themselves. Zero is the whole point of the current
// implementations: JitteredBackoff draws through rand.N generically over
// time.Duration specifically so the jitter does not round-trip through int64,
// and SafeDouble is branch-and-multiply. A rewrite reaching for a rand.Rand
// value, a big.Int or a formatted overflow diagnostic would show up here.
func TestBackoffPrimitivesAllocations(t *testing.T) {
	durations := map[string]time.Duration{
		"zero":            0,
		"negative":        -time.Second,
		"one_nanosecond":  time.Nanosecond,
		"base_delay":      defaultBaseDelay,
		"retry_after_cap": RetryAfterCap,
		"near_overflow":   time.Duration(1) << 62,
		"max_duration":    time.Duration(math.MaxInt64),
	}
	for name, d := range durations {
		t.Run("jittered_backoff_"+name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				durSink = JitteredBackoff(d)
			}); got != 0 {
				t.Errorf("JitteredBackoff(%v) allocated %v times per run, want 0: "+
					"the wait is computed once per retry, so this cost tracks the "+
					"retry rate of the whole process", d, got)
			}
		})
		t.Run("safe_double_"+name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				durSink = SafeDouble(d)
			}); got != 0 {
				t.Errorf("SafeDouble(%v) allocated %v times per run, want 0: the "+
					"exponential base advances once per retry, overflow guard "+
					"included", d, got)
			}
		})
	}
}

// TestDoorAllocationsWithoutObserver pins what a door costs with no Observer,
// and with one whose funcs are both nil: the observation stays on the door's
// stack and records nothing. Do's one allocation is its loopConfig
// (newLoopConfig's result escapes), which the Observer field grows by 16
// bytes; the transport's single try allocates nothing; a retrying GET's three
// are the request clone (Request, URL and Header). A zero is also measured
// over one run, because AllocsPerRun divides as an integer and would round a
// rare allocation down to it.
func TestDoorAllocationsWithoutObserver(t *testing.T) {
	ctx := context.Background()
	fn := func(context.Context) (int, error) { return 1, nil }
	okResp := &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}}
	rt := NewRetryRoundTripper(&stubRT{resp: okResp}, TransportConfig{})
	empty := NewRetryRoundTripper(&stubRT{resp: okResp}, TransportConfig{Observer: Observer{}})
	post, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.com", strings.NewReader("x"))
	get, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", http.NoBody)
	for _, tc := range []struct {
		name string
		run  func()
		want float64
	}{
		{"Do", func() { _, errSink = Do(ctx, fn) }, 1},
		{"transport single try", func() { _, errSink = rt.RoundTrip(post) }, 0},                    //nolint:bodyclose // the stub answers http.NoBody
		{"transport single try, empty Observer", func() { _, errSink = empty.RoundTrip(post) }, 0}, //nolint:bodyclose // the stub answers http.NoBody
		{"transport GET", func() { _, errSink = rt.RoundTrip(get) }, 3},                            //nolint:bodyclose // the stub answers http.NoBody
		{"transport GET, empty Observer", func() { _, errSink = empty.RoundTrip(get) }, 3},         //nolint:bodyclose // the stub answers http.NoBody
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, tc.run); got != tc.want {
				t.Errorf("allocations = %v, want %v", got, tc.want)
			}
			if got := testing.AllocsPerRun(1, tc.run); tc.want == 0 && got != 0 {
				t.Errorf("allocations in one run = %v, want 0", got)
			}
		})
	}
}

// TestParseRetryAfterAllocationsOnWellFormedValues pins the two input classes a
// cooperating upstream actually sends: delta-seconds, the form rate-limiters
// emit, and an absent header, what most responses carry. Both are free and on
// the per-response path: GetBytes parses Retry-After on every retryable
// status, and every door parses it on every response while an Observer is
// set. The date forms measure 2 to 8 allocations, so they are NOT in this
// table; their contract is the bounded one below.
func TestParseRetryAfterAllocationsOnWellFormedValues(t *testing.T) {
	headers := map[string]string{
		"delta_seconds":           "30",
		"delta_seconds_zero":      "0",
		"delta_seconds_negative":  "-5",
		"delta_seconds_padded":    "  30  ",
		"delta_seconds_max_int64": "9223372036854775807",
		"delta_seconds_above_cap": "3600",
		"empty":                   "",
		"whitespace_only":         "   ",
	}
	for name, h := range headers {
		t.Run(name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				durSink = ParseRetryAfter(h)
			}); got != 0 {
				t.Errorf("ParseRetryAfter(%s) allocated %v times per run, want 0: "+
					"a delta-seconds header and an absent one are what a real "+
					"upstream sends, and this parse runs on every retryable "+
					"response", abbrev(h), got)
			}
		})
	}
}

// TestParseRetryAfterAllocationsAreBounded covers the classes that are not free:
// the three HTTP-date layouts, and every value that parses as neither a number
// nor a date.
//
// The mechanism is http.ParseTime trying its layouts in order, so the cost is a
// step function of WHICH layout matches — 2 for the IMF-fixdate form RFC 9110
// mandates, 5 for the obsolete RFC 850 form, 8 for ANSI C, 11 when all three
// fail. The ceiling is generous by design: what must hold is that the count is
// bounded by the number of layouts and not by anything the sender controls.
func TestParseRetryAfterAllocationsAreBounded(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour).UTC()
	headers := map[string]string{
		"imf_fixdate":            soon.Format(http.TimeFormat),
		"imf_fixdate_in_past":    "Tue, 03 Jun 2025 08:00:00 GMT",
		"rfc850":                 soon.Format(time.RFC850),
		"ansi_c":                 soon.Format(time.ANSIC),
		"malformed":              "not-a-date",
		"numeric_above_int64":    "999999999999999999999999",
		"date_with_garbage_tail": "Mon, 02 Jan 2006 15:04:05 GMT and then some",
	}
	for name, h := range headers {
		t.Run(name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				durSink = ParseRetryAfter(h)
			}); got > maxParseAllocs {
				t.Errorf("ParseRetryAfter(%s) allocated %v times per run, want at "+
					"most %d: the cost of a date-shaped or unparseable header must "+
					"stay bounded by the fixed number of layouts tried, since the "+
					"sender chooses the value", abbrev(h), got, maxParseAllocs)
			}
		})
	}
}

// TestParseRetryAfterCostDoesNotScaleWithHeaderLength is the contract the
// benchmark tracker cannot see, and the reason this section exists.
//
// Retry-After is an attacker-or-upstream-controlled header string, parsed on
// every retryable response, before any policy has decided the peer is
// misbehaving. An upstream that can make the parse a thousand times more
// expensive by sending a thousand times more header has an amplification vector
// inside the mechanism that is supposed to bound the damage a failing upstream
// does.
//
// The measured answer is that the allocation COUNT is flat — 11 at one byte and
// 11 at a megabyte — so the assertion is equality against the smallest input's
// count rather than the literal 11. That way a stdlib change to the constant
// leaves the test green while any length dependence turns it red, which is the
// property worth gating.
//
// What is NOT asserted, and must not be read as passing: the byte volume grows
// with the header at roughly 7x, because strconv.ParseInt clones the string into
// its *NumError and each failed http.ParseTime layout does the same. A 1 MB
// Retry-After copies about 7 MB. Gating that number would gate the stdlib's
// error-reporting internals; the flat count is what says this package does not
// itself scan, split or copy the header.
func TestParseRetryAfterCostDoesNotScaleWithHeaderLength(t *testing.T) {
	classes := map[string]struct {
		build   func(n int) string
		lengths []int
	}{
		// Pure garbage: both parsers fail, which is the deepest path and the one
		// a hostile sender gets for free by sending anything non-numeric.
		"garbage": {
			build:   func(n int) string { return strings.Repeat("x", n) },
			lengths: []int{1, 10, 1_000, 100_000, 1_000_000},
		},
		// All digits: ParseInt reads the whole string before reporting a range
		// error, so this is the class where a length-proportional parse would be
		// least surprising. The ladder starts at 20 digits because 19 still fits
		// in an int64 and would parse successfully — a fixture that PARSES would
		// measure the free path and make this check vacuous.
		"digits": {
			build:   func(n int) string { return strings.Repeat("9", n) },
			lengths: []int{20, 100, 1_000, 100_000, 1_000_000},
		},
		// Digits then one non-digit: ParseInt fails at the LAST byte, so the
		// scan cannot short-circuit early.
		"digits_then_garbage": {
			build:   func(n int) string { return strings.Repeat("9", n) + "x" },
			lengths: []int{1, 10, 1_000, 100_000, 1_000_000},
		},
		// Whitespace-padded garbage: TrimSpace runs first, and a rewrite that
		// used strings.Trim on a copy would show up here and nowhere else.
		"padded_garbage": {
			build:   func(n int) string { return "  " + strings.Repeat("x", n) + "  " },
			lengths: []int{1, 10, 1_000, 100_000, 1_000_000},
		},
	}

	for name, class := range classes {
		t.Run(name, func(t *testing.T) {
			var baseline float64
			for i, n := range class.lengths {
				h := class.build(n)
				if d := ParseRetryAfter(h); d != 0 {
					t.Fatalf("ParseRetryAfter(%s) = %v, want 0: the fixture must be "+
						"unparseable for this class to measure the deep path",
						abbrev(h), d)
				}
				got := testing.AllocsPerRun(50, func() {
					durSink = ParseRetryAfter(h)
				})
				if i == 0 {
					baseline = got
					continue
				}
				// Not-greater rather than equal. A long value now short-circuits
				// before either parser touches it, so its count DROPS to zero
				// while the shortest fixture still runs the date parse, and an
				// equality would report that improvement as a failure. Growth is
				// the regression; a fall is the fix.
				if got > baseline {
					t.Errorf("ParseRetryAfter(%s) allocated %v times per run at "+
						"length %d but only %v at length %d, want no more than the "+
						"short input: parsing cost must not grow with a header the "+
						"upstream sizes",
						abbrev(h), got, n, baseline, class.lengths[0])
				}
				if got > maxParseAllocs {
					t.Errorf("ParseRetryAfter(%s) allocated %v times per run at "+
						"length %d, want at most %d", abbrev(h), got, n, maxParseAllocs)
				}
			}
			// BYTES are the axis that carried the defect, and the count above was
			// blind to it. strconv.ParseInt and each of http.ParseTime's three
			// failed layout attempts CLONE the input into the error they return,
			// so an unparseable header used to cost about 7 bytes of allocation
			// per header byte while the count stayed flat: a 1 MB Retry-After
			// bought roughly 7 MB of garbage, upstream-controlled, from a sender
			// that is already failing. Fixed by pre-filtering the numeric shape
			// (so ParseInt never sees a non-numeric string) and bounding what
			// reaches ParseTime by the longest date format it accepts.
			//
			// The assertion is an absolute CEILING, not a ratio and not equality
			// against the shortest fixture. Below the date bound the parse still
			// runs and its byte cost varies a little with the input, so the
			// shortest measurement is neither the minimum nor a stable divisor;
			// above the bound the cost is zero. What holds at every length is
			// that the total is bounded by a constant, and that is exactly what
			// a proportional parse cannot satisfy: the old behaviour reached
			// about 7 MB on a 1 MB header, four orders of magnitude past this.
			var maxBytes uint64
			for _, n := range class.lengths {
				h := class.build(n)
				got := allocBytesPerRun(50, func() {
					durSink = ParseRetryAfter(h)
				})
				maxBytes = max(maxBytes, got)
				if got > maxParseBytes {
					t.Errorf("ParseRetryAfter(%s) allocated %d bytes per run at "+
						"length %d, want at most %d at any length: a parse that "+
						"copies the header charges the process for a value the "+
						"upstream chose the size of",
						abbrev(h), got, n, maxParseBytes)
				}
			}

			// Logged only on the way to green: on failure the numbers above are
			// the story, and a "constant N" summary derived from the smallest
			// input would contradict them.
			if !t.Failed() {
				t.Logf("%s: at most %v allocations and %d bytes across %d to %d "+
					"header bytes", name, baseline, maxBytes,
					class.lengths[0], class.lengths[len(class.lengths)-1])
			}
		})
	}
}

// TestParseRetryAfterResponseAllocations extends the same contract to the
// response-shaped door, which is the one CheckStatus calls on every 429. The
// header lookup itself must not allocate, so a canonical-key hit stays free and
// an oversized value stays bounded.
func TestParseRetryAfterResponseAllocations(t *testing.T) {
	free := map[string]*http.Response{
		"delta_seconds": {Header: http.Header{"Retry-After": []string{"30"}}},
		"absent":        {Header: http.Header{}},
		"nil_header":    {},
	}
	for name, resp := range free {
		t.Run(name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				durSink = ParseRetryAfterResponse(resp)
			}); got != 0 {
				t.Errorf("ParseRetryAfterResponse(%s) allocated %v times per run, "+
					"want 0: reading and parsing the header of a rate-limited "+
					"response must not cost anything on the common forms", name, got)
			}
		})
	}

	t.Run("oversized_garbage", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{
			"Retry-After": []string{strings.Repeat("x", 1_000_000)},
		}}
		if got := testing.AllocsPerRun(50, func() {
			durSink = ParseRetryAfterResponse(resp)
		}); got > maxParseAllocs {
			t.Errorf("ParseRetryAfterResponse(1 MB of garbage) allocated %v times "+
				"per run, want at most %d: an upstream must not be able to size "+
				"the work its own header costs us", got, maxParseAllocs)
		}
	})
}

// The redaction corpus. RedactSecretString is a byte-exact strings.ReplaceAll,
// so the four cases that matter are the secret appearing many times, at the very
// start, at the very end, and not at all — the last being both the common case
// on a log path and the only one that can be free.
const (
	// redactSecret is the needle. It is a placeholder, not a credential shape
	// worth imitating.
	redactSecret = "supersecret-token"

	// redactFiller is the surrounding text a captured body or error message
	// would carry, long enough that a per-byte cost separates from a per-match
	// one.
	redactFillerLen = 4096
)

var (
	redactFiller = strings.Repeat("a", redactFillerLen)

	// redactAbsent holds no occurrence at all: the case where redaction must
	// return its input untouched, and the case a log line usually is.
	redactAbsent = redactFiller

	// redactAtStart, redactAtEnd and redactInMiddle place one occurrence at each
	// position. Position is worth enumerating because a scan that copied
	// everything before the first match would price these three differently.
	redactAtStart  = redactSecret + redactFiller
	redactAtEnd    = redactFiller + redactSecret
	redactInMiddle = redactFiller[:redactFillerLen/2] + redactSecret + redactFiller[redactFillerLen/2:]
)

// TestRedactSecretStringAllocations pins redaction's cost and, in the same pass,
// the security property the cost is only interesting because of.
//
// Redaction runs on an error path that is about to be logged, which is a worse
// place to allocate than it first appears: the strings involved are
// upstream-authored, the path is already the failure path, and a caller under a
// credential-leak-shaped incident is likely running at Debug with the volume
// turned up.
//
// The measured shape is a clean split. An absent secret is FREE at any haystack
// length, because strings.ReplaceAll returns its input when there is nothing to
// replace. A present secret costs exactly ONE allocation regardless of how many
// times it occurs, because Replace sizes one buffer from the match count up
// front. Both halves are asserted: zero for absence, and a constant that does
// not track occurrence count for presence.
func TestRedactSecretStringAllocations(t *testing.T) {
	t.Run("absent_secret_is_free", func(t *testing.T) {
		haystacks := map[string]string{
			"short":        "connection refused",
			"filler":       redactAbsent,
			"long":         strings.Repeat("b", 1_000_000),
			"empty_string": "",
		}
		for name, s := range haystacks {
			t.Run(name, func(t *testing.T) {
				if got := testing.AllocsPerRun(50, func() {
					strSink = RedactSecretString(s, redactSecret)
				}); got != 0 {
					t.Errorf("RedactSecretString(%s, secret) allocated %v times per "+
						"run, want 0: scanning a log line that does not carry the "+
						"secret is the common case, and it must cost nothing",
						abbrev(s), got)
				}
			})
		}
	})

	t.Run("empty_secret_is_free", func(t *testing.T) {
		// An empty Secret disables redaction by documented contract. That path
		// must not copy the haystack on the way to doing nothing.
		if got := testing.AllocsPerRun(200, func() {
			strSink = RedactSecretString(redactAtStart, "")
		}); got != 0 {
			t.Errorf("RedactSecretString(%s, \"\") allocated %v times per run, want "+
				"0: the empty secret is a documented no-op and must not copy",
				abbrev(redactAtStart), got)
		}
	})

	t.Run("position_does_not_change_the_cost", func(t *testing.T) {
		positions := map[string]string{
			"at_start":  redactAtStart,
			"at_end":    redactAtEnd,
			"in_middle": redactInMiddle,
		}
		var baseline float64
		var baselineName string
		for name, s := range positions {
			got := testing.AllocsPerRun(200, func() {
				strSink = RedactSecretString(s, redactSecret)
			})
			out := RedactSecretString(s, redactSecret)
			if strings.Contains(out, redactSecret) {
				t.Errorf("RedactSecretString(%s, secret) left the secret in its "+
					"output: redaction at position %s did not replace the value",
					abbrev(s), name)
			}
			if !strings.Contains(out, "REDACTED") {
				t.Errorf("RedactSecretString(%s, secret) produced no REDACTED "+
					"marker at position %s, so the fixture did not exercise a "+
					"replacement", abbrev(s), name)
			}
			if baselineName == "" {
				baseline, baselineName = got, name
				continue
			}
			if got != baseline {
				t.Errorf("RedactSecretString(%s, secret) allocated %v times per run "+
					"with the secret %s but %v with it %s, want equal: where the "+
					"secret sits in the text must not change what redacting it costs",
					abbrev(s), got, name, baseline, baselineName)
			}
		}
	})

	t.Run("cost_does_not_scale_with_occurrence_count", func(t *testing.T) {
		counts := []int{1, 2, 16, 256, 4096}
		var baseline float64
		for i, n := range counts {
			s := strings.Repeat(redactSecret+"|", n)
			out := RedactSecretString(s, redactSecret)
			if strings.Contains(out, redactSecret) {
				t.Fatalf("RedactSecretString(%d occurrences, secret) left the secret "+
					"in its output, so this fixture cannot witness a cost contract", n)
			}
			got := testing.AllocsPerRun(50, func() {
				strSink = RedactSecretString(s, redactSecret)
			})
			if i == 0 {
				baseline = got
				continue
			}
			if got != baseline {
				t.Errorf("RedactSecretString(%d occurrences, secret) allocated %v "+
					"times per run but %v at %d occurrence, want equal: a message "+
					"repeating the secret is exactly what a leaking upstream sends, "+
					"and redacting it must not cost per occurrence",
					n, got, baseline, counts[0])
			}
		}
		if !t.Failed() {
			t.Logf("redaction is a constant %v allocation(s) from %d to %d occurrences",
				baseline, counts[0], counts[len(counts)-1])
		}
	})
}

// TestRedactTransportErrorAllocations pins the error-shaped helpers the retry
// doors actually call.
//
// The split mirrors the string helper, with one qualification the measurement
// forced: an error whose message does not contain the secret is returned
// untouched and for free ONLY when rendering that message is itself free. The
// helper cannot know whether the secret is present without calling Error(), so
// an error type that builds its message pays for the build either way — see the
// *StatusError subtest. When the secret IS present the cost is a small constant
// that does not track how many times it appears.
func TestRedactTransportErrorAllocations(t *testing.T) {
	absentPlain := errors.New("dial tcp 10.0.0.1:443: connect: connection refused")
	absentURL := &url.Error{
		Op:  "Get",
		URL: "https://api.example/v1/items",
		Err: io.ErrUnexpectedEOF,
	}
	presentPlain := errors.New("upstream rejected token=" + redactSecret)
	presentURL := &url.Error{
		Op:  "Get",
		URL: "https://api.example/v1/items?apikey=" + redactSecret,
		Err: errors.New("dial tcp: bad token " + redactSecret),
	}

	t.Run("absent_secret_is_free", func(t *testing.T) {
		// Every fixture here renders its message from a stored string, so
		// materializing it to search for the secret costs nothing. That is the
		// precondition the free path depends on, and the subtest below is the
		// case where it does not hold.
		cases := map[string]error{
			"plain_error":       absentPlain,
			"url_error":         absentURL,
			"nil_error":         nil,
			"wrapped_at_depth8": wrapChain(absentPlain, 8),
		}
		for name, err := range cases {
			t.Run(name, func(t *testing.T) {
				if got := testing.AllocsPerRun(200, func() {
					errSink = RedactTransportError(err, "", redactSecret)
				}); got != 0 {
					t.Errorf("RedactTransportError(%s, \"\", secret) allocated %v "+
						"times per run, want 0: an error that does not carry the "+
						"secret is the usual case on a logged path and must pass "+
						"through untouched", name, got)
				}
			})
		}
	})

	// This is the property that did NOT hold as expected, recorded rather than
	// asserted away. "Free when the secret is absent" is a property of the
	// ERROR, not of the helper: to decide whether the secret is present at all,
	// RedactTransportError must call Error() and search the result, so an error
	// whose Error() BUILDS its message pays for that message even when nothing
	// is replaced. *StatusError is exactly that shape — its Error() runs the URL
	// through redactURL, which parses and re-serializes — so a caller redacting
	// a known secret out of one of this package's own status errors pays 5
	// allocations on the no-op path.
	//
	// The direction is asserted so a future change makes itself visible: if this
	// ever becomes free (a cached message, a cheaper rendering), the test names
	// what to do about it.
	t.Run("rendering_error_is_not_free_when_the_secret_is_absent", func(t *testing.T) {
		statusErr := &StatusError{Code: 429, rawURL: "https://api.example/v1/items?cmd=list"}
		got := testing.AllocsPerRun(200, func() {
			errSink = RedactTransportError(statusErr, "", redactSecret)
		})
		if got == 0 {
			t.Errorf("RedactTransportError(*StatusError, \"\", secret) allocated %v "+
				"times per run, want more than 0: if rendering a StatusError's "+
				"message is now free, the free-when-absent contract covers every "+
				"error type and this test should be replaced by a zero assertion "+
				"in the table above", got)
		}
		if got > maxRedactAllocs {
			t.Errorf("RedactTransportError(*StatusError, \"\", secret) allocated %v "+
				"times per run, want at most %d: the no-op path may cost the "+
				"error's own rendering, but that rendering must stay a small "+
				"constant", got, maxRedactAllocs)
		}
	})

	t.Run("empty_secret_is_free", func(t *testing.T) {
		if got := testing.AllocsPerRun(200, func() {
			errSink = RedactTransportError(presentPlain, "", "")
		}); got != 0 {
			t.Errorf("RedactTransportError(secret-bearing error, \"\", \"\") "+
				"allocated %v times per run, want 0: an empty secret disables "+
				"redaction and must not rebuild the error to do nothing", got)
		}
	})

	t.Run("present_secret_costs_a_bounded_constant", func(t *testing.T) {
		cases := map[string]struct {
			err    error
			prefix string
		}{
			"plain_error":        {presentPlain, ""},
			"plain_with_prefix":  {presentPlain, "fetch items"},
			"url_error":          {presentURL, ""},
			"url_error_with_pfx": {presentURL, "fetch items"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				out := RedactTransportError(tc.err, tc.prefix, redactSecret)
				if strings.Contains(out.Error(), redactSecret) {
					t.Errorf("RedactTransportError(%s) returned %q, which still "+
						"contains the secret: the helper's whole job is that it "+
						"does not", name, out)
				}
				if got := testing.AllocsPerRun(200, func() {
					errSink = RedactTransportError(tc.err, tc.prefix, redactSecret)
				}); got > maxRedactAllocs {
					t.Errorf("RedactTransportError(%s) allocated %v times per run, "+
						"want at most %d: rebuilding one redacted error must be a "+
						"small constant, not a function of the message", name, got,
						maxRedactAllocs)
				}
			})
		}
	})

	t.Run("cost_does_not_scale_with_occurrence_count", func(t *testing.T) {
		counts := []int{1, 16, 256}
		var baseline float64
		for i, n := range counts {
			err := errors.New(strings.Repeat(redactSecret+"|", n))
			got := testing.AllocsPerRun(100, func() {
				errSink = RedactSecret(err, redactSecret)
			})
			if i == 0 {
				baseline = got
				continue
			}
			if got != baseline {
				t.Errorf("RedactSecret(error with %d occurrences, secret) allocated "+
					"%v times per run but %v at %d occurrence, want equal: an error "+
					"text that repeats the credential must not cost per copy",
					n, got, baseline, counts[0])
			}
		}
	})
}

// TestLogSafeErrorAllocations pins the reduction httpclient applies to EVERY
// transport error it logs or wraps, and which callers apply to their own.
//
// It is a type test and a field read, so zero is the whole implementation: an
// *url.Error is reduced to its cause, everything else is returned as it came.
// A rewrite that rebuilt the error to strip the URL — the obvious alternative
// implementation — would allocate here, and would also break the errors.Is/As
// chains the current one preserves.
func TestLogSafeErrorAllocations(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	cases := map[string]error{
		"nil":                nil,
		"plain_error":        errors.New("connection refused"),
		"url_error":          &url.Error{Op: "Get", URL: "https://u:p@api.example/x?k=v", Err: cause},
		"url_error_no_cause": &url.Error{Op: "Get", URL: "https://api.example/x"},
		"status_error":       &StatusError{Code: 503, rawURL: "https://api.example/x?apikey=secret"},
		"wrapped_url_error":  fmt.Errorf("fetch: %w", &url.Error{Op: "Get", URL: "https://api.example/x", Err: cause}),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, func() {
				errSink = LogSafeError(err)
			}); got != 0 {
				t.Errorf("LogSafeError(%s) allocated %v times per run, want 0: this "+
					"reduction runs on every transport error the package logs, so "+
					"it is paid once per failed attempt", name, got)
			}
		})
	}
}

// allocBytesPerRun is the bytes twin of [testing.AllocsPerRun]. It exists
// because a parse that clones its input moves BYTES while leaving the
// allocation count untouched, so a contract written only on the count cannot
// see that class at all. Same shape as AllocsPerRun: one settling run, then the
// delta over n runs measured from a known GC point.
func allocBytesPerRun(n int, f func()) uint64 {
	f()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range n {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(n)
}

// TestParseRetryAfterRefusesAPaddedZoneOffset pins the one input where
// maxRetryAfterDateLen changes behaviour, so the change stays a decision on the
// record rather than a surprise a future reader has to rediscover.
//
// time.RFC850's zone element accepts GMT followed by a sign and an unbounded run
// of digits, so a date can be made arbitrarily long while still parsing. Padding
// carries no semantic value: the offset below means the same as its unpadded
// spelling. The bound refuses it, and the amplification the bound removes is why
// (see maxRetryAfterDateLen). Both directions are asserted: the padded form is
// refused, and the SAME date unpadded is still accepted, so a future change that
// widened the refusal to real dates would fail here.
func TestParseRetryAfterRefusesAPaddedZoneOffset(t *testing.T) {
	const unpadded = "Monday, 02-Jan-38 15:04:05 GMT+0023"
	padded := "Monday, 02-Jan-38 15:04:05 GMT+" + strings.Repeat("0", 40) + "23"

	if len(unpadded) > maxRetryAfterDateLen {
		t.Fatalf("the unpadded fixture is %d bytes, over the %d-byte bound: it must be "+
			"inside the bound or the accept half of this test is vacuous",
			len(unpadded), maxRetryAfterDateLen)
	}
	if _, err := http.ParseTime(padded); err != nil {
		t.Fatalf("http.ParseTime rejected %q: the fixture must PARSE upstream or this "+
			"test is not pinning a divergence at all (%v)", abbrev(padded), err)
	}

	if got := ParseRetryAfter(unpadded); got == 0 {
		t.Errorf("ParseRetryAfter(%q) = 0, want non-zero: a real date inside the bound "+
			"must still be accepted", unpadded)
	}
	if got := ParseRetryAfter(padded); got != 0 {
		t.Errorf("ParseRetryAfter(a %d-byte date with a zero-padded zone offset) = %v, "+
			"want 0: the bound refuses it deliberately, and http.ParseTime accepting it "+
			"is exactly why that is a behaviour change and not an equivalence",
			len(padded), got)
	}
}
