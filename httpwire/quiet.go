package httpwire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Quiet is how far Quiesce quiets an API.
type Quiet int

const (
	// QuietWrites answers maintenance on every write row, a mount's
	// included, and every Stream row.
	QuietWrites Quiet = iota + 1
	// QuietAll also holds every other row: a request waits for resume, at
	// most 5 s, then answers maintenance.
	QuietAll
)

// Quiescer is quieted while a database file it reads is replaced. It
// returns from Quiesce(ctx, QuietAll) only once it will commit nothing
// derived from the old file after the reopen: its requests and its own
// writers held, and a job runner's running jobs cancelled or finished,
// each job writing only through the pin it took at its start. resume is
// idempotent.
type Quiescer interface {
	Quiesce(ctx context.Context, q Quiet) (resume func(), err error)
}

var _ Quiescer = (*API)(nil)

const (
	quietHold   = 5 * time.Second
	quietCancel = 10 * time.Second
)

// errQuiesced is the cause of a request context QuietAll cancelled.
var errQuiesced = errors.New("httpwire: cancelled for maintenance")

// quietState counts the holders of each level and the requests in flight.
// The zero value is open.
type quietState struct {
	running map[*inflight]struct{}
	// resumed is closed when the last QuietAll holder resumes; it exists
	// while all > 0.
	resumed chan struct{}
	writes  int
	all     int
	mu      sync.Mutex
}

// inflight is one request past the maintenance check, and the mark its
// context carries.
type inflight struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
	write  bool
}

type quietMark struct{}

// Quiesce starts refusing or holding at once, then returns when every
// request of that level already in flight (writes for QuietWrites, every
// request but an open stream for QuietAll) has returned; one still running
// 10 s into QuietAll has its context cancelled. It returns ctx's error,
// holding nothing, when ctx ends first. The request ctx descends from,
// detached or not, is never waited for. A QuietAll taken under a
// QuietWrites returns to it at its own resume.
func (a *API) Quiesce(ctx context.Context, q Quiet) (resume func(), err error) {
	if q != QuietWrites && q != QuietAll {
		return nil, fmt.Errorf("httpwire: Quiesce of Quiet %d, not QuietWrites or QuietAll", q)
	}
	self, _ := ctx.Value(quietMark{}).(*inflight)
	wait := a.quiet.take(q, self)
	resume = sync.OnceFunc(func() { a.quiet.release(q) })
	if err := drain(ctx, q, wait); err != nil {
		resume()
		return nil, err
	}
	return resume, nil
}

// take raises q and answers the requests in flight it waits for.
func (s *quietState) take(q Quiet, self *inflight) []*inflight {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q == QuietAll {
		if s.all == 0 {
			s.resumed = make(chan struct{})
		}
		s.all++
	} else {
		s.writes++
	}
	var wait []*inflight
	for in := range s.running {
		if in != self && (q == QuietAll || in.write) {
			wait = append(wait, in)
		}
	}
	return wait
}

func (s *quietState) release(q Quiet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q == QuietWrites {
		s.writes--
		return
	}
	s.all--
	if s.all == 0 {
		close(s.resumed)
	}
}

// drain waits for each request in wait, cancelling those left 10 s into
// QuietAll.
func drain(ctx context.Context, q Quiet, wait []*inflight) error {
	var cancelAt <-chan time.Time
	if q == QuietAll && len(wait) > 0 {
		t := time.NewTimer(quietCancel)
		defer t.Stop()
		cancelAt = t.C
	}
	for _, in := range wait {
		for running := true; running; {
			select {
			case <-in.done:
				running = false
			case <-ctx.Done():
				return ctx.Err()
			case <-cancelAt:
				cancelAt = nil
				for _, in := range wait {
					in.cancel(errQuiesced)
				}
			}
		}
	}
	return nil
}

// enter admits a request unless maintenance refuses it: a refused one
// (a write or a stream) under either level, any other after waiting out
// QuietAll for 5 s. The admitted request's context is cancellable by
// QuietAll; a stream is never waited for, so it is not counted.
func (s *quietState) enter(ctx context.Context, refused, stream bool) (*inflight, context.Context, bool) {
	deadline := time.Now().Add(quietHold)
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.all > 0 || (refused && s.writes > 0) {
		if refused || !s.await(ctx, deadline) {
			return nil, nil, false
		}
	}
	in := &inflight{write: refused, done: make(chan struct{})}
	ctx, in.cancel = context.WithCancelCause(ctx)
	if !stream {
		if s.running == nil {
			s.running = map[*inflight]struct{}{}
		}
		s.running[in] = struct{}{}
	}
	return in, context.WithValue(ctx, quietMark{}, in), true
}

// await waits, with s.mu released, for the last QuietAll holder to resume,
// until deadline or ctx's end.
func (s *quietState) await(ctx context.Context, deadline time.Time) bool {
	resumed := s.resumed
	s.mu.Unlock()
	defer s.mu.Lock()
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-resumed:
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	return false
}

func (s *quietState) leave(in *inflight) {
	s.mu.Lock()
	delete(s.running, in)
	s.mu.Unlock()
	close(in.done)
	in.cancel(nil)
}

// quieted is ep's maintenance check, after the client floor and before
// the outage and the guard, which is safe because the refusal names
// nothing about the principal. Each admitted request's context carries
// its mark, which context.WithoutCancel keeps.
func (a *API) quieted(ep *Endpoint, next http.Handler) http.Handler {
	stream := ep.Kind == Stream
	refused := isWrite(ep.Method) || stream
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in, ctx, ok := a.quiet.enter(r.Context(), refused, stream)
		if !ok {
			a.table.Catalog.WriteError(w, r, maintenance())
			return
		}
		defer a.quiet.leave(in)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func maintenance() *Error {
	return &Error{Code: codeMaintenance, Message: "the server is in maintenance", RetryAfter: quietHold}
}
