package httpwire

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

// defaultRowDeadline ends a Keyed handler's context when neither the route
// nor the server sets a deadline.
const defaultRowDeadline = 2 * time.Minute

// keyedRuns holds every Keyed row's runs in flight, which a request with
// the same slot joins, beside the store of settled answers. The join is in
// memory whatever the store.
type keyedRuns struct {
	claims map[IdempotencyKey]*claim
	byCred map[string]int
	store  IdempotencyStore
	// mem is the store when it is the default one, else the fallback that
	// keeps an answer the store failed to save.
	mem      *memStore
	observe  func(IdempotencyEvent)
	logged   map[string]bool // rows whose oversize answer with no Result was logged
	lim      idemLimits
	mu       sync.Mutex
	fallback bool
}

// claim is one run in flight. kept is set before done closes, and stays
// nil when the run kept nothing, so its waiters claim the slot again.
type claim struct {
	done chan struct{}
	kept *KeptAnswer
	fp   [32]byte
}

type claimState uint8

const (
	claimMine claimState = iota + 1
	claimJoin
	claimKept
	claimReused
	claimFull
)

func newKeyedRuns(o IdempotencyOptions, l idemLimits, t *Table) *keyedRuns {
	maxKept := map[string]int{}
	for i := range t.Endpoints {
		if ep := &t.Endpoints[i]; ep.MaxKept > 0 {
			maxKept[ep.Name] = ep.MaxKept
		}
	}
	s := &keyedRuns{claims: map[IdempotencyKey]*claim{}, byCred: map[string]int{}, store: o.Store, observe: o.Observer, logged: map[string]bool{}, lim: l}
	s.mem = newMemStore(l.maxBytes, maxKept)
	s.fallback = s.store != nil
	if !s.fallback {
		s.store = s.mem
	}
	return s
}

// claim answers what a request on slot k with fingerprint fp meets: a run
// in flight to join or refuse by fingerprint, an answer held in memory,
// the memory's bounds, or a new run of its own. The fallback's bounds
// count only the answers it holds, so a store that saves caps no run.
func (s *keyedRuns) claim(k IdempotencyKey, fp [32]byte) (*claim, *KeptAnswer, claimState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.claims[k]; c != nil {
		if c.fp != fp {
			return nil, nil, claimReused
		}
		return c, nil, claimJoin
	}
	if a, ok, _ := s.mem.Load(context.Background(), k); ok {
		return nil, &a, claimKept
	}
	claims, all := s.byCred, len(s.claims)
	if s.fallback {
		claims, all = nil, 0
	}
	if !s.mem.admit(k.Credential, claims, all, &s.lim) {
		return nil, nil, claimFull
	}
	c := &claim{fp: fp, done: make(chan struct{})}
	s.claims[k] = c
	s.byCred[k.Credential]++
	return c, nil, claimMine
}

func (s *keyedRuns) release(k IdempotencyKey, c *claim, kept *KeptAnswer) {
	s.mu.Lock()
	c.kept = kept
	delete(s.claims, k)
	s.byCred[k.Credential]--
	if s.byCred[k.Credential] == 0 {
		delete(s.byCred, k.Credential)
	}
	s.mu.Unlock()
	close(c.done)
}

func (s *keyedRuns) event(row string, o IdempotencyOutcome) {
	if s.observe != nil {
		s.observe(IdempotencyEvent{Row: row, Outcome: o})
	}
}

// keyed runs a Keyed row's request at most once per slot: a request whose
// slot holds an answer or a run in flight is answered with it, by
// fingerprint first, and run answers a new one.
func (a *API) keyed(w http.ResponseWriter, r *http.Request, ep *Endpoint, d *decoded, run http.HandlerFunc) {
	fp, err := fingerprint(ep.Name, d)
	if err != nil {
		a.log().ErrorContext(r.Context(), "httpwire: a Keyed request does not marshal for its fingerprint", "row", ep.Name, "error", err)
		a.table.Catalog.WriteError(w, r, &Error{Code: codeInternal})
		return
	}
	s := a.idem
	k := IdempotencyKey{Credential: CredentialOf(r.Context()), Row: ep.Name, Key: d.key}
	for {
		c, kept, state := s.claim(k, fp)
		switch state {
		case claimReused:
			a.reused(w, r, ep)
			return
		case claimFull:
			a.table.Catalog.WriteError(w, r, &Error{Code: codeStoreFull, Message: "the server keeps no more answers for this credential now", RetryAfter: time.Minute})
			return
		case claimKept:
			a.replay(w, r, ep, fp, kept, false)
			return
		case claimJoin:
			if a.join(w, r, ep, fp, c) {
				return
			}
			continue
		case claimMine:
		}
		if !s.fallback || !a.loaded(w, r, ep, k, c) {
			a.runClaim(w, r, ep, k, c, run)
		}
		return
	}
}

// loaded answers slot k from a store other than the default one, which
// claim already read, reporting whether it answered. Its answer is handed
// to the claim's waiters too.
func (a *API) loaded(w http.ResponseWriter, r *http.Request, ep *Endpoint, k IdempotencyKey, c *claim) bool {
	kept, found, err := a.idem.store.Load(r.Context(), k)
	switch {
	case err != nil:
		a.idem.release(k, c, nil)
		a.log().ErrorContext(r.Context(), "httpwire: the idempotency store could not load a key", "row", ep.Name, "error", err)
		a.table.Catalog.WriteError(w, r, &Error{Code: codeInternal})
		return true
	case found && time.Now().Before(kept.Expires):
		a.idem.release(k, c, &kept)
		a.replay(w, r, ep, c.fp, &kept, false)
		return true
	}
	return false
}

// join waits, through the hold, for the run in flight on c. It answers
// false when that run kept nothing, so the caller claims the slot again.
func (a *API) join(w http.ResponseWriter, r *http.Request, ep *Endpoint, fp [32]byte, c *claim) bool {
	done := true
	a.o.hold(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := rowDeadline(r)
		defer cancel()
		select {
		case <-c.done:
			if c.kept == nil {
				done = false
				return
			}
			a.replay(w, r, ep, fp, c.kept, true)
		case <-ctx.Done():
			if r.Context().Err() == nil {
				a.table.Catalog.WriteError(w, r, &Error{Code: codeInFlight, Message: "the first request with this key is still running", RetryAfter: 5 * time.Second})
			}
		}
	})).ServeHTTP(w, r)
	return done
}

// runClaim runs the handler for slot k under the row's deadline and keeps
// its answer by the kept rule, in the fallback when the store fails to
// save it. The claim is released when the handler returns, a panic
// included, which keeps nothing.
func (a *API) runClaim(w http.ResponseWriter, r *http.Request, ep *Endpoint, k IdempotencyKey, c *claim, run http.HandlerFunc) {
	var kept *KeptAnswer
	defer func() { a.idem.release(k, c, kept) }()
	ctx, cancel := rowDeadline(r)
	defer cancel()
	rec := &keepWriter{ResponseWriter: w, limit: a.idem.lim.maxAnswer}
	run(rec, r.WithContext(context.WithValue(ctx, idemCallKey{}, &idemCall{key: k, fp: c.fp})))
	ans, keep := a.keptAnswer(ep, rec, ctx.Err() != nil)
	if !keep {
		return
	}
	ans.Fingerprint = c.fp
	ans.Expires = time.Now().Add(ep.Window)
	if ans.Oversize && ep.Result == "" {
		a.logOversize(r, ep)
	}
	if err := a.idem.store.Save(context.WithoutCancel(r.Context()), k, ans); err != nil {
		a.log().ErrorContext(r.Context(), "httpwire: the idempotency store could not save an answer, which is kept in memory until it expires", "row", ep.Name, "error", err)
		_ = a.idem.mem.Save(r.Context(), k, ans)
	}
	kept = &ans
}

func (a *API) logOversize(r *http.Request, ep *Endpoint) {
	s := a.idem
	s.mu.Lock()
	first := !s.logged[ep.Name]
	s.logged[ep.Name] = true
	s.mu.Unlock()
	if first {
		a.log().ErrorContext(r.Context(), "httpwire: a Keyed row with no Result answered past MaxAnswer, so its resend cannot replay the result", "row", ep.Name)
	}
}

// keptAnswer applies the kept rule to a run's answer: a success, and a
// refusal whose code has NoEffect false. Once the handler's context had
// ended, a refusal the binder wrote for it (internal_error, or a code
// outside the row's union) may be a rollback the resend must run again, so
// only a code the handler chose from its union is kept.
func (a *API) keptAnswer(ep *Endpoint, rec *keepWriter, ended bool) (KeptAnswer, bool) {
	status := cmpStatus(rec.status)
	ans := KeptAnswer{Status: status, ContentType: rec.contentType, Body: rec.body.Bytes(), Oversize: rec.over}
	if rec.over {
		ans.Body = nil
	}
	if status < http.StatusBadRequest {
		return ans, true
	}
	code := rec.code
	if code == "" {
		if env, err := DecodeEnvelope(ans.Body); err == nil {
			code = env.Code
		}
	}
	if ended && (code == codeInternal || !slices.Contains(ep.Errors, code)) {
		return KeptAnswer{}, false
	}
	if spec, ok := a.table.Catalog.Lookup(code); ok && spec.NoEffect {
		return KeptAnswer{}, false
	}
	ans.RetryAfter = rec.retryAfter
	return ans, true
}

func cmpStatus(s int) int {
	if s == 0 {
		return http.StatusOK
	}
	return s
}

// replay answers a kept answer: idempotency_key_reused under another
// fingerprint, idempotency_answer_too_large when it was kept with no body,
// else its status, Content-Type, Retry-After and body. Wire-Version and
// X-Request-Id are this request's, set before.
func (a *API) replay(w http.ResponseWriter, r *http.Request, ep *Endpoint, fp [32]byte, ans *KeptAnswer, joined bool) {
	switch {
	case ans.Fingerprint != fp:
		a.reused(w, r, ep)
		return
	case ans.Oversize:
		a.idem.event(ep.Name, OutcomeOversize)
		e := &Error{Code: codeTooLarge, Message: "the request completed, and its answer is too large to replay"}
		if ep.Result != "" {
			e.remediation = "The request completed; read its result with " + ep.Result + "."
		}
		a.table.Catalog.WriteError(w, r, e)
		return
	case joined:
		a.idem.event(ep.Name, OutcomeJoined)
	default:
		a.idem.event(ep.Name, OutcomeReplayed)
	}
	h := w.Header()
	if ans.ContentType != "" {
		h.Set("Content-Type", ans.ContentType)
		h.Set("X-Content-Type-Options", "nosniff")
	}
	if ans.RetryAfter > 0 {
		h.Set("Retry-After", strconv.FormatInt(int64((ans.RetryAfter+time.Second-1)/time.Second), 10))
	}
	w.WriteHeader(ans.Status)
	_, _ = w.Write(ans.Body)
}

func (a *API) reused(w http.ResponseWriter, r *http.Request, ep *Endpoint) {
	a.idem.event(ep.Name, OutcomeReused)
	a.table.Catalog.WriteError(w, r, &Error{Code: codeKeyReused, Message: "this Idempotency-Key was sent with a different request"})
}

// rowDeadline is r's context ending at the row's deadline: the route's,
// else the server's WriteTimeout, else defaultRowDeadline.
func rowDeadline(r *http.Request) (context.Context, context.CancelFunc) {
	ctx := r.Context()
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	d := defaultRowDeadline
	if srv, ok := ctx.Value(http.ServerContextKey).(*http.Server); ok && srv.WriteTimeout > 0 {
		d = srv.WriteTimeout
	}
	return context.WithTimeout(ctx, d)
}

// keepWriter passes a run's answer through and keeps a copy of its status,
// Content-Type, Retry-After and body up to limit bytes, and the code of a
// refusal the catalog wrote, which the kept rule reads in place of a body
// it may not have kept.
type keepWriter struct {
	http.ResponseWriter
	code        httpserver.ErrorCode
	contentType string
	body        bytes.Buffer
	retryAfter  time.Duration
	status      int
	limit       int
	over        bool
}

func (w *keepWriter) WriteHeader(code int) {
	if w.status == 0 && code >= http.StatusOK {
		h := w.Header()
		w.status, w.contentType = code, h.Get("Content-Type")
		if s, err := strconv.ParseInt(h.Get("Retry-After"), 10, 32); err == nil && s > 0 {
			w.retryAfter = time.Duration(s) * time.Second
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *keepWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if !w.over {
		if w.body.Len()+len(p) > w.limit {
			w.over = true
			w.body.Reset()
		} else {
			w.body.Write(p)
		}
	}
	return w.ResponseWriter.Write(p)
}

func (w *keepWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// noteCode tells the keepWriter under w, found through Unwrap, the code of
// the refusal about to be written, unless the answer has already started.
func noteCode(w http.ResponseWriter, code httpserver.ErrorCode) {
	for w != nil {
		if k, ok := w.(*keepWriter); ok {
			if k.status == 0 {
				k.code = code
			}
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}
