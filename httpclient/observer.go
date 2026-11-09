package httpclient

import (
	"errors"
	"net/http"
	"time"
)

// Attempt is one try a door made: one call of Do's function, one GetBytes
// request, or one RetryRoundTripper round trip.
type Attempt struct {
	// Start is when the try began: for Do, the function's call; for GetBytes,
	// the send, after the request build and WithHeaders; for the transport,
	// the send, after any body rewind and PrepareRetry, or the try's start if
	// either failed.
	Start time.Time
	// Err is the try's error as LogSafeError renders it, so no *url.Error's
	// raw URL reaches the observer: Do's function error (marked with
	// AttemptTimeout when WithAttemptTimeout's bound expired), GetBytes's
	// request or read error, whose *StatusError names the request URL
	// redacted, or the transport's base round-trip error. LogSafeError keeps
	// this package's marks (Permanent, MarkTransient, AttemptTimeout), the
	// transport's retry steps and every errors.Join branch, so IsTransient and
	// IsPermanent read them. Any other wrapper above a *url.Error is dropped
	// with its type and text, a Do caller's own included: reduce a transport
	// error with LogSafeError before wrapping it if the observer must see the
	// wrapper. It is nil when the try succeeded; for the transport, a response
	// of any status is a success and Status carries it.
	Err error
	// Duration runs from the send to the answer or the error. GetBytes
	// includes the body read; the transport stops at the response headers.
	Duration time.Duration
	// RetryAfter is the response's Retry-After, capped at RetryAfterCap, zero
	// when there is none. Do reads it from the function's error before the
	// redaction: a RetryAfterHint, else a *StatusError's RetryAfter.
	RetryAfter time.Duration
	// Wait is the sleep the door starts before the next try: the backoff, a
	// Retry-After or a rate-limit wait. It is zero when the call ends after
	// this try. A context that ends during the sleep stops the call, so that
	// try keeps its Wait with no try after it, and the Outcome carries the
	// context's error.
	Wait time.Duration
	// Number is the try's place in the call, 1 for the first.
	Number int
	// Status is the HTTP status, zero when no response arrived. Do reads it
	// from a *StatusError in Err.
	Status int
	// Cause is CauseOf(Err).
	Cause Cause
}

// Outcome is a whole call: what the door returned, and the tries it took.
type Outcome struct {
	// Err is the error the door returned, as LogSafeError renders it; nil on
	// success. A transport call that returned a response, of any status, has
	// a nil Err.
	Err error
	// Duration runs from the call's start to its return, every try and wait
	// included.
	Duration time.Duration
	// Attempts is the number of tries, which is the number of Attempt calls.
	Attempts int
}

// Observer receives each Attempt of a call and then its Outcome, on the
// calling goroutine and outside any lock. Concurrent calls on one door, or on
// one shared RetryRoundTripper, call the observer concurrently, so funcs that
// share state must synchronize. A func must return quickly and must not call
// back into the door. A nil func is skipped, so a caller that only counts
// calls sets Outcome alone, and an Observer with neither func is the same as
// none: the door records nothing.
//
// The records carry no URL or host field, and their errors pass through
// LogSafeError: one observer per client names the upstream. An error's text
// can still name the address a dial or a DNS lookup failed on. The records
// carry no failure class either; derive one from Err with IsTransient,
// IsPermanent, a *StatusError's Code or Cause.
type Observer struct {
	Attempt func(Attempt)
	Outcome func(Outcome)
}

// observerOption implements Option for WithObserver.
type observerOption Observer

func (o observerOption) applyDo(c *loopConfig) { c.observer = Observer(o) }
func (o observerOption) applyGet(c *getConfig) { o.applyDo(&c.loopConfig) }

// WithObserver sets the Observer that Do or GetBytes reports each attempt
// and the call's outcome to. Default: none. The transport takes one through
// TransportConfig.Observer.
func WithObserver(o Observer) Option { return observerOption(o) }

// observation is one call's state on its way to an Observer: the attempt in
// progress and the call's start. With neither func set it is off, and every
// method returns at once.
type observation struct {
	obs     Observer
	start   time.Time
	attempt Attempt
	on      bool
}

// newObservation returns the observation by value, so a door's local copy
// stays on its stack.
func newObservation(obs Observer) observation {
	o := observation{obs: obs, on: obs.Attempt != nil || obs.Outcome != nil}
	if o.on {
		o.start = time.Now()
	}
	return o
}

// begin opens attempt n, 0-based as schedule.run counts.
func (o *observation) begin(n int) {
	if o.on {
		o.attempt = Attempt{Number: n + 1, Start: time.Now()}
	}
}

// sending restamps the open attempt's start at its send, so GetBytes's request
// build and WithHeaders, and the transport's rewind and PrepareRetry, do not
// count as the upstream's time.
func (o *observation) sending() {
	if o.on {
		o.attempt.Start = time.Now()
	}
}

// end closes the open attempt with its error, redacted, its status and its
// Retry-After. Its callers check on first, so they skip their own parsing.
func (o *observation) end(err error, status int, retryAfter time.Duration) {
	a := &o.attempt
	a.Duration = time.Since(a.Start)
	a.Err = LogSafeError(err)
	a.Status, a.RetryAfter, a.Cause = status, retryAfter, CauseOf(a.Err)
}

// endErr closes the open attempt from its error alone, as Do sees a try.
func (o *observation) endErr(err error) {
	if o.on {
		status, retryAfter := errStatus(err)
		o.end(err, status, retryAfter)
	}
}

// endResponse closes the open attempt from a round trip's result: resp's
// status and capped Retry-After when one arrived, with or without err.
func (o *observation) endResponse(resp *http.Response, err error) {
	if resp == nil {
		o.endHeader(err, 0, nil)
		return
	}
	o.endHeader(err, resp.StatusCode, resp.Header)
}

// endHeader closes the open attempt from a status and the header it came
// with, zero and nil when no response arrived, reading the header's
// Retry-After capped.
func (o *observation) endHeader(err error, status int, header http.Header) {
	if o.on {
		o.end(err, status, ParseRetryAfter(header.Get("Retry-After")))
	}
}

// settle reports the open attempt with the wait that follows it; schedule.run
// calls it once per attempt.
func (o *observation) settle(wait time.Duration) {
	if o.on && o.obs.Attempt != nil {
		o.attempt.Wait = wait
		o.obs.Attempt(o.attempt)
	}
}

// finish reports the call's outcome with err redacted and returns err
// unchanged, so a door can end with return o.finish(err).
func (o *observation) finish(err error) error { return o.finishAs(err, err) }

// finishAs reports rec, redacted, as the outcome and returns ret: a door whose
// returned error wraps a cause reduces the cause first and reports that.
func (o *observation) finishAs(ret, rec error) error {
	if o.on && o.obs.Outcome != nil {
		o.obs.Outcome(Outcome{Err: LogSafeError(rec), Duration: time.Since(o.start), Attempts: o.attempt.Number})
	}
	return ret
}

// errStatus reads the status and Retry-After a Do error carries: a
// *StatusError's code, and a RetryAfterHint or the *StatusError's
// Retry-After, capped at RetryAfterCap.
func errStatus(err error) (status int, retryAfter time.Duration) {
	retryAfter = retryAfterHintWait(err)
	if se, ok := errors.AsType[*StatusError](err); ok {
		status = se.Code
		if retryAfter <= 0 {
			retryAfter = se.RetryAfter
		}
	}
	return status, min(max(retryAfter, 0), RetryAfterCap)
}
