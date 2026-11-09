package httpclient

import (
	"errors"
	"time"
)

// Attempt is one try a door made: one call of Do's function, one GetBytes
// request, or one RetryRoundTripper round trip.
type Attempt struct {
	// Start is when the try began.
	Start time.Time
	// Err is the try's error as the door returns errors: Do's function error
	// unchanged, GetBytes's redacted by LogSafeError, the transport's as the
	// base transport returned it. It is nil when the try succeeded; for the
	// transport, a response of any status is a success and Status carries it.
	Err error
	// Duration runs from the send to the answer or the error. GetBytes
	// includes the body read; the transport stops at the response headers.
	Duration time.Duration
	// RetryAfter is the server's Retry-After, capped at RetryAfterCap, zero
	// when there is none. Do reads it from a RetryAfterHint or a *StatusError
	// in Err.
	RetryAfter time.Duration
	// Wait is the sleep the door starts before the next try, zero when no try
	// follows. A context that ends during the sleep stops the call, and the
	// Outcome reports it.
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
	// Err is the error the door returned, nil on success. A transport call
	// that returned a response, of any status, has a nil Err.
	Err error
	// Duration runs from the first send to the return, waits included.
	Duration time.Duration
	// Attempts is the number of tries, which is the number of Attempt calls.
	Attempts int
}

// Observer receives each Attempt of a call and then its Outcome, on the
// calling goroutine and outside any lock. Concurrent calls on one door, or on
// one shared RetryRoundTripper, call the observer concurrently, so funcs that
// share state must synchronize. A func must return quickly and must not call
// back into the door. A nil func is skipped, so a caller that only counts
// calls sets Outcome alone.
//
// The structs carry no URL or host: one observer per client names the
// upstream, and nothing redacted needs to cross the hook. They carry no
// failure class either; derive one from Err with IsTransient, IsPermanent,
// a *StatusError's Code or Cause.
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
// progress and the call's start.
type observation struct {
	obs     Observer
	start   time.Time
	attempt Attempt
}

func newObservation(obs Observer) *observation {
	return &observation{obs: obs, start: time.Now()}
}

// begin opens attempt n, 0-based as schedule.run counts.
func (o *observation) begin(n int) {
	o.attempt = Attempt{Start: time.Now(), Number: n + 1}
}

// end closes the open attempt with its error, status and Retry-After.
func (o *observation) end(err error, status int, retryAfter time.Duration) {
	a := &o.attempt
	a.Duration = time.Since(a.Start)
	a.Err, a.Status, a.RetryAfter, a.Cause = err, status, retryAfter, CauseOf(err)
}

// settle reports the open attempt with the wait that follows it; schedule.run
// calls it once per attempt.
func (o *observation) settle(wait time.Duration) {
	o.attempt.Wait = wait
	if o.obs.Attempt != nil {
		o.obs.Attempt(o.attempt)
	}
}

// finish reports the call's outcome and returns err, so a door can end with
// return o.finish(err).
func (o *observation) finish(err error) error {
	if o.obs.Outcome != nil {
		o.obs.Outcome(Outcome{Err: err, Duration: time.Since(o.start), Attempts: o.attempt.Number})
	}
	return err
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
