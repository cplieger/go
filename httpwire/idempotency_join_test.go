package httpwire_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpwire"
)

const quickOne = "{\"id\":\"one\",\"pad\":\"\"}\n"

// blockedClaimant starts a jobs.quick request under key "j" whose run
// waits for release, and answers once it is in flight.
func blockedClaimant(t *testing.T, kf *keyedFixture) (release chan struct{}, answer func() *httptest.ResponseRecorder) {
	t.Helper()
	release = make(chan struct{})
	kf.quick = func(context.Context, httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
		<-release
		return jobAnswer{ID: "one"}, nil
	}
	var rec *httptest.ResponseRecorder
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec = kf.quickJob(`"j"`)
	}()
	synctest.Wait()
	if kf.runsOf("jobs.quick") != 1 {
		t.Fatalf("%d runs in flight, want 1", kf.runsOf("jobs.quick"))
	}
	return release, func() *httptest.ResponseRecorder { <-done; return rec }
}

func TestJoinWaitsForTheRunInFlight_Concurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		release, first := blockedClaimant(t, kf)
		recs := make([]*httptest.ResponseRecorder, 5)
		var wg sync.WaitGroup
		for i := range recs {
			wg.Go(func() { recs[i] = kf.quickJob(`"j"`) })
		}
		synctest.Wait()
		if kf.runsOf("jobs.quick") != 1 || kf.holdCount() != 5 {
			t.Fatalf("%d runs and %d held waiters, want one run and every waiter held", kf.runsOf("jobs.quick"), kf.holdCount())
		}
		if rec := kf.do(http.MethodPost, "/api/jobs/quick", `{"kind":"b","size":1}`, `"j"`); codeOf(t, rec) != "idempotency_key_reused" {
			t.Errorf("another body under the key in flight answered %d %s, want idempotency_key_reused at once", rec.Code, rec.Body)
		}
		close(release)
		wg.Wait()
		for i, rec := range append(recs, first()) {
			if rec.Code != http.StatusOK || rec.Body.String() != quickOne {
				t.Errorf("request %d answered %d %q, want the run's answer", i, rec.Code, rec.Body)
			}
		}
		if kf.runsOf("jobs.quick") != 1 {
			t.Errorf("%d runs, want 1", kf.runsOf("jobs.quick"))
		}
		joined := 0
		for _, e := range kf.outcomes() {
			if e == (httpwire.IdempotencyEvent{Row: "jobs.quick", Outcome: httpwire.OutcomeJoined}) {
				joined++
			}
		}
		if joined != 5 {
			t.Errorf("observer saw %+v, want five joins", kf.outcomes())
		}
	})
}

func TestJoinWaiterPastItsDeadlineAnswersInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		release, first := blockedClaimant(t, kf)
		start := time.Now()
		rec := kf.quickJob(`"j"`)
		if rec.Code != http.StatusConflict || codeOf(t, rec) != "idempotency_in_flight" || rec.Header().Get("Retry-After") != "5" {
			t.Errorf("waiter answered %d %s Retry-After %q, want 409 idempotency_in_flight with Retry-After 5", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
		}
		if waited := time.Since(start); waited != 2*time.Minute {
			t.Errorf("the waiter waited %v, want the 2 minute row deadline", waited)
		}
		close(release)
		if got := first(); got.Code != http.StatusOK || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("claimant answered %d after %d runs", got.Code, kf.runsOf("jobs.quick"))
		}
	})
}

func TestJoinWaiterOverTheHoldCapAnswersHeldLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		release, first := blockedClaimant(t, kf)
		kf.mu.Lock()
		kf.refuse = true
		kf.mu.Unlock()
		rec := kf.quickJob(`"j"`)
		if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "held_limit" {
			t.Errorf("waiter answered %d %s, want held_limit at once", rec.Code, rec.Body)
		}
		close(release)
		if got := first(); got.Code != http.StatusOK || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("claimant answered %d after %d runs", got.Code, kf.runsOf("jobs.quick"))
		}
	})
}

func TestJoinPastTheHoldCapAnswersHeldLimitAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		kf.mu.Lock()
		kf.holdCap = 8
		kf.mu.Unlock()
		release, first := blockedClaimant(t, kf)
		recs := make([]*httptest.ResponseRecorder, 8)
		var wg sync.WaitGroup
		for i := range recs {
			wg.Go(func() { recs[i] = kf.quickJob(`"j"`) })
		}
		synctest.Wait()
		start := time.Now()
		rec := kf.quickJob(`"j"`)
		if rec.Code != http.StatusTooManyRequests || codeOf(t, rec) != "held_limit" || rec.Header().Get("Retry-After") != "5" {
			t.Errorf("the ninth waiter answered %d %s Retry-After %q, want 429 held_limit with Retry-After 5", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("the ninth waiter waited %v, want an answer at once", waited)
		}
		close(release)
		wg.Wait()
		for i, rec := range append(recs, first()) {
			if rec.Code != http.StatusOK || rec.Body.String() != quickOne {
				t.Errorf("request %d answered %d %q, want the run's answer", i, rec.Code, rec.Body)
			}
		}
		if kf.runsOf("jobs.quick") != 1 {
			t.Errorf("%d runs, want 1", kf.runsOf("jobs.quick"))
		}
	})
}

func TestJoinWaiterWhoseClientLeftAnswersNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		release, first := blockedClaimant(t, kf)
		ctx, cancel := context.WithCancel(t.Context())
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			kf.api.ServeHTTP(rec, keyedRequest(http.MethodPost, "/api/jobs/quick", `{"kind":"a","size":1}`, `"j"`).WithContext(ctx))
		}()
		synctest.Wait()
		cancel()
		<-done
		if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
			t.Errorf("a waiter whose client left wrote %q", rec.Body)
		}
		close(release)
		if got := first(); got.Code != http.StatusOK || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("claimant answered %d after %d runs", got.Code, kf.runsOf("jobs.quick"))
		}
	})
}

func TestJoinAfterADroppedClaimantRunsAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		kf.quick = func(ctx context.Context, _ httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
			if kf.runsOf("jobs.quick") == 1 {
				<-ctx.Done()
				return jobAnswer{}, ctx.Err()
			}
			return jobAnswer{ID: "one"}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		go kf.api.ServeHTTP(httptest.NewRecorder(), keyedRequest(http.MethodPost, "/api/jobs/quick", `{"kind":"a","size":1}`, `"j"`).WithContext(ctx))
		synctest.Wait()
		var waiter *httptest.ResponseRecorder
		done := make(chan struct{})
		go func() {
			defer close(done)
			waiter = kf.quickJob(`"j"`)
		}()
		synctest.Wait()
		if kf.runsOf("jobs.quick") != 1 || kf.holdCount() != 1 {
			t.Fatalf("%d runs and %d held, want the second request waiting on the first", kf.runsOf("jobs.quick"), kf.holdCount())
		}
		cancel()
		<-done
		if waiter.Code != http.StatusOK || waiter.Body.String() != quickOne || kf.runsOf("jobs.quick") != 2 {
			t.Errorf("waiter answered %d %q after %d runs, want a second run's answer", waiter.Code, waiter.Body, kf.runsOf("jobs.quick"))
		}
	})
}

func TestJoinAfterAPanickingClaimantRunsAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		release := make(chan struct{})
		kf.quick = func(context.Context, httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
			if kf.runsOf("jobs.quick") == 1 {
				<-release
				panic("boom")
			}
			return jobAnswer{ID: "one"}, nil
		}
		var first *httptest.ResponseRecorder
		claimant := make(chan struct{})
		go func() {
			defer close(claimant)
			first = kf.quickJob(`"j"`)
		}()
		synctest.Wait()
		var waiter *httptest.ResponseRecorder
		done := make(chan struct{})
		go func() {
			defer close(done)
			waiter = kf.quickJob(`"j"`)
		}()
		synctest.Wait()
		close(release)
		<-claimant
		<-done
		if first.Code != http.StatusInternalServerError || codeOf(t, first) != "internal_error" {
			t.Errorf("the panicking claimant answered %d %q, want 500 internal_error", first.Code, first.Body)
		}
		if waiter.Code != http.StatusOK || waiter.Body.String() != quickOne || kf.runsOf("jobs.quick") != 2 {
			t.Errorf("waiter answered %d %q after %d runs, want a second run's answer", waiter.Code, waiter.Body, kf.runsOf("jobs.quick"))
		}
		if rec := kf.quickJob(`"j"`); rec.Body.String() != quickOne || kf.runsOf("jobs.quick") != 2 {
			t.Errorf("a resend answered %q after %d runs, want the second run's answer replayed", rec.Body, kf.runsOf("jobs.quick"))
		}
	})
}

func TestKeyedHandlerContextEndsAtTheRowDeadline(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  func(context.Context) (context.Context, context.CancelFunc)
		want time.Duration
	}{
		"by default": {func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, func() {} }, 2 * time.Minute},
		"the server's WriteTimeout": {func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithValue(ctx, http.ServerContextKey, &http.Server{WriteTimeout: 30 * time.Second}), func() {}
		}, 30 * time.Second},
		"a route deadline": {func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 10*time.Second)
		}, 10 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
				var left time.Duration
				kf.quick = func(ctx context.Context, _ httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
					start := time.Now()
					<-ctx.Done()
					left = time.Since(start)
					return jobAnswer{ID: "late"}, nil
				}
				ctx, cancel := tc.ctx(t.Context())
				defer cancel()
				rec := httptest.NewRecorder()
				kf.api.ServeHTTP(rec, keyedRequest(http.MethodPost, "/api/jobs/quick", `{"kind":"a","size":1}`, `"d"`).WithContext(ctx))
				if left != tc.want {
					t.Errorf("the handler's context ended after %v, want %v", left, tc.want)
				}
			})
		})
	}
}

func TestCommitAfterTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		kf.quick = func(ctx context.Context, c httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
			<-ctx.Done()
			if c.Body.Kind == "rollback" {
				return jobAnswer{}, ctx.Err()
			}
			return jobAnswer{ID: "late"}, nil
		}
		job := func(kind string) *httptest.ResponseRecorder {
			return kf.do(http.MethodPost, "/api/jobs/quick", `{"kind":"`+kind+`","size":1}`, `"`+kind+`"`)
		}
		first := job("commit")
		if rec := job("commit"); first.Code != http.StatusOK || rec.Body.String() != first.Body.String() || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("a success: first %d, resend %d %q after %d runs, want the committed answer replayed", first.Code, rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
		job("rollback")
		job("rollback")
		if kf.runsOf("jobs.quick") != 3 {
			t.Errorf("an error after the context ended: %d runs, want the resend run again", kf.runsOf("jobs.quick"))
		}
	})
}

// A handler that names a code of its row's union after its client left has
// decided the answer itself, so the kept rule reads that code's NoEffect;
// an answer the binder wrote for a context error is never kept.
func TestUnknownOutcomeKeptAfterTheClientLeaves(t *testing.T) {
	cases := map[string]struct {
		answer   func(ctx context.Context) error
		wantCode string
		wantRuns int
	}{
		"a union code that may have acted": {func(context.Context) error {
			return &httpwire.Error{Code: "outcome_unknown", Message: "the write was sent"}
		}, "outcome_unknown", 1},
		"a union code with no effect": {func(context.Context) error {
			return &httpwire.Error{Code: "queue_full", Message: "the queue is full", RetryAfter: 5 * time.Second}
		}, "", 2},
		"the context's own error": {func(ctx context.Context) error { return ctx.Err() }, "", 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
				leave := make(chan struct{})
				kf.quick = func(ctx context.Context, c httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
					if c.Body.Kind != "leave" {
						return jobAnswer{ID: "again"}, nil
					}
					close(leave)
					<-ctx.Done()
					return jobAnswer{}, tc.answer(ctx)
				}
				ctx, cancel := context.WithCancel(t.Context())
				go func() {
					<-leave
					cancel()
				}()
				body := `{"kind":"leave","size":1}`
				kf.api.ServeHTTP(httptest.NewRecorder(), keyedRequest(http.MethodPost, "/api/jobs/quick", body, `"k"`).WithContext(ctx))
				kf.quick = func(context.Context, httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
					return jobAnswer{ID: "again"}, nil
				}
				rec := kf.do(http.MethodPost, "/api/jobs/quick", body, `"k"`)
				if got := codeOf(t, rec); got != tc.wantCode || kf.runsOf("jobs.quick") != tc.wantRuns {
					t.Errorf("resend answered %d %q after %d runs, want code %q after %d runs", rec.Code, got, kf.runsOf("jobs.quick"), tc.wantCode, tc.wantRuns)
				}
			})
		})
	}
}

// txStore is a store whose answers the handler commits with its work;
// Save writes nothing.
type txStore struct {
	answers map[httpwire.IdempotencyKey]httpwire.KeptAnswer
	mu      sync.Mutex
}

func (s *txStore) Load(_ context.Context, k httpwire.IdempotencyKey) (httpwire.KeptAnswer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.answers[k]
	return a, ok, nil
}

func (*txStore) Save(context.Context, httpwire.IdempotencyKey, httpwire.KeptAnswer) error { return nil }

func TestAnswerCommittedThroughIdempotencyIsReplayed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &txStore{answers: map[httpwire.IdempotencyKey]httpwire.KeptAnswer{}}
		kf := newKeyed(t, httpwire.IdempotencyOptions{Store: store}, nil)
		kf.quick = func(ctx context.Context, _ httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
			k, fp, ok := httpwire.Idempotency(ctx)
			if !ok {
				t.Error("Idempotency answered false inside a Keyed handler")
			}
			store.mu.Lock()
			store.answers[k] = httpwire.KeptAnswer{
				Fingerprint: fp, Status: http.StatusOK, ContentType: "application/json",
				Body: []byte("{\"id\":\"tx\",\"pad\":\"\"}\n"), Expires: time.Now().Add(time.Hour),
			}
			store.mu.Unlock()
			<-ctx.Done()
			return jobAnswer{}, ctx.Err()
		}
		kf.quickJob(`"tx"`)
		rec := kf.quickJob(`"tx"`)
		if rec.Code != http.StatusOK || rec.Body.String() != "{\"id\":\"tx\",\"pad\":\"\"}\n" || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("resend answered %d %q after %d runs, want the committed answer and one run", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
		if _, _, ok := httpwire.Idempotency(t.Context()); ok {
			t.Error("Idempotency answered true outside a handler")
		}
	})
}
