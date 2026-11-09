package httpclient_test

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// record collects what an Observer receives, in order, safe for the
// concurrent calls the Observer doc allows.
type record struct {
	mu       sync.Mutex
	attempts []httpclient.Attempt
	outcomes []httpclient.Outcome
	order    []string
}

func (r *record) observer() httpclient.Observer {
	return httpclient.Observer{
		Attempt: func(a httpclient.Attempt) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.attempts = append(r.attempts, a)
			r.order = append(r.order, "attempt")
		},
		Outcome: func(o httpclient.Outcome) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.outcomes = append(r.outcomes, o)
			r.order = append(r.order, "outcome")
		},
	}
}

// check asserts the invariants every door keeps: the attempts arrive
// numbered from 1, one Outcome comes last, it counts the attempts, and only
// the last attempt has no wait.
func (r *record) check(t *testing.T, wantAttempts int) httpclient.Outcome {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempts) != wantAttempts || len(r.outcomes) != 1 {
		t.Fatalf("observer got %d attempts and %d outcomes, want %d and 1", len(r.attempts), len(r.outcomes), wantAttempts)
	}
	if r.order[len(r.order)-1] != "outcome" {
		t.Errorf("observer order %v, want the outcome last", r.order)
	}
	for i, a := range r.attempts {
		if a.Number != i+1 {
			t.Errorf("attempt %d Number = %d", i, a.Number)
		}
		if a.Start.IsZero() || a.Duration < 0 {
			t.Errorf("attempt %d Start = %v, Duration = %v", a.Number, a.Start, a.Duration)
		}
		if last := i == len(r.attempts)-1; last != (a.Wait == 0) {
			t.Errorf("attempt %d Wait = %v, want a wait on every attempt but the last", a.Number, a.Wait)
		}
		if a.Cause != httpclient.CauseOf(a.Err) {
			t.Errorf("attempt %d Cause = %v, want CauseOf(Err) = %v", a.Number, a.Cause, httpclient.CauseOf(a.Err))
		}
	}
	o := r.outcomes[0]
	if o.Attempts != wantAttempts {
		t.Errorf("Outcome.Attempts = %d, want %d", o.Attempts, wantAttempts)
	}
	var waits time.Duration
	for _, a := range r.attempts {
		waits += a.Wait
	}
	if o.Duration < waits {
		t.Errorf("Outcome.Duration = %v, shorter than the waits %v", o.Duration, waits)
	}
	return o
}

// statusServer answers each request with the next status in codes, the last
// one repeating, with a body of "ok" on a 2xx.
func statusServer(t *testing.T, header http.Header, codes ...int) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := min(int(n.Add(1))-1, len(codes)-1)
		maps.Copy(w.Header(), header)
		w.WriteHeader(codes[i])
		if codes[i] < 300 {
			_, _ = w.Write([]byte("ok"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

var errFlaky = httpclient.MarkTransient(errors.New("flaky"))

func TestWithObserver_Do_reports_each_attempt_then_the_outcome(t *testing.T) {
	var rec record
	calls := 0
	got, err := httpclient.Do(t.Context(), func(context.Context) (int, error) {
		calls++
		if calls < 3 {
			return 0, errFlaky
		}
		return 7, nil
	}, httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if got != 7 || err != nil {
		t.Fatalf("Do = %d, %v, want 7, nil", got, err)
	}
	o := rec.check(t, 3)
	if o.Err != nil {
		t.Errorf("Outcome.Err = %v, want nil", o.Err)
	}
	for i, a := range rec.attempts {
		if wantErr := i < 2; (a.Err != nil) != wantErr || (wantErr && !errors.Is(a.Err, errFlaky)) {
			t.Errorf("attempt %d Err = %v", a.Number, a.Err)
		}
		if a.Status != 0 || a.RetryAfter != 0 {
			t.Errorf("attempt %d Status, RetryAfter = %d, %v, want 0, 0 for an error with no status", a.Number, a.Status, a.RetryAfter)
		}
	}
}

func TestWithObserver_Do_reads_status_and_retry_after_from_the_error(t *testing.T) {
	var rec record
	limited := &httpclient.StatusError{Code: http.StatusTooManyRequests, RetryAfter: 2 * time.Second}
	_, err := httpclient.Do(t.Context(), func(context.Context) (struct{}, error) {
		return struct{}{}, limited
	}, httpclient.WithMaxAttempts(2), httpclient.WithRateLimitRetry(5*time.Millisecond),
		httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if !errors.Is(err, limited) {
		t.Fatalf("Do err = %v, want the 429", err)
	}
	o := rec.check(t, 2)
	if !errors.Is(o.Err, limited) {
		t.Errorf("Outcome.Err = %v, want the 429", o.Err)
	}
	first := rec.attempts[0]
	if first.Status != http.StatusTooManyRequests || first.RetryAfter != 2*time.Second || first.Wait != 5*time.Millisecond {
		t.Errorf("first attempt Status, RetryAfter, Wait = %d, %v, %v, want 429, 2s, 5ms", first.Status, first.RetryAfter, first.Wait)
	}
}

// A Retry-After past RetryAfterCap reaches the observer capped, as every door
// reads the header.
func TestWithObserver_Do_caps_retry_after(t *testing.T) {
	var rec record
	_, _ = httpclient.Do(t.Context(), func(context.Context) (struct{}, error) {
		return struct{}{}, &httpclient.StatusError{Code: http.StatusServiceUnavailable, RetryAfter: time.Hour}
	}, httpclient.WithMaxAttempts(1), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	rec.check(t, 1)
	if got := rec.attempts[0].RetryAfter; got != httpclient.RetryAfterCap {
		t.Errorf("RetryAfter = %v, want RetryAfterCap", got)
	}
}

// When the context ends after a failed attempt, the attempt keeps its own
// error and the outcome carries what Do returns.
func TestWithObserver_Do_context_end_is_the_outcome(t *testing.T) {
	var rec record
	ctx, cancel := context.WithCancel(t.Context())
	_, err := httpclient.Do(ctx, func(context.Context) (struct{}, error) {
		cancel()
		return struct{}{}, errFlaky
	}, httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do err = %v, want context.Canceled", err)
	}
	o := rec.check(t, 1)
	if !errors.Is(rec.attempts[0].Err, errFlaky) || !errors.Is(o.Err, context.Canceled) {
		t.Errorf("attempt Err = %v, Outcome.Err = %v, want flaky and context.Canceled", rec.attempts[0].Err, o.Err)
	}
}

// The attempt is reported before its wait starts, so a context that ends
// during the wait leaves an attempt with a wait and no try after it, and the
// outcome says why.
func TestWithObserver_Do_context_end_during_the_wait(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var attempts []httpclient.Attempt
	var outcome httpclient.Outcome
	_, err := httpclient.Do(ctx, func(context.Context) (struct{}, error) {
		return struct{}{}, errFlaky
	}, httpclient.WithBaseDelay(time.Hour), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(httpclient.Observer{
		Attempt: func(a httpclient.Attempt) { attempts = append(attempts, a); cancel() },
		Outcome: func(o httpclient.Outcome) { outcome = o },
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do err = %v, want context.Canceled", err)
	}
	if len(attempts) != 1 || attempts[0].Wait < 30*time.Minute {
		t.Fatalf("attempts = %+v, want one with the hour's jittered wait", attempts)
	}
	if outcome.Attempts != 1 || !errors.Is(outcome.Err, context.Canceled) || outcome.Duration >= time.Minute {
		t.Errorf("Outcome = %+v, want 1 attempt, context.Canceled, no full wait", outcome)
	}
}

func TestWithObserver_GetBytes_reports_status_and_the_body_read(t *testing.T) {
	var rec record
	srv := statusServer(t, nil, http.StatusServiceUnavailable, http.StatusOK)
	body, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), srv.URL,
		httpclient.WithBaseDelay(time.Millisecond), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if string(body) != "ok" || err != nil {
		t.Fatalf("GetBytes = %q, %v, want ok, nil", body, err)
	}
	o := rec.check(t, 2)
	if o.Err != nil {
		t.Errorf("Outcome.Err = %v, want nil", o.Err)
	}
	first, second := rec.attempts[0], rec.attempts[1]
	if _, ok := errors.AsType[*httpclient.StatusError](first.Err); !ok || first.Status != http.StatusServiceUnavailable {
		t.Errorf("first attempt Status, Err = %d, %v, want 503 and its *StatusError", first.Status, first.Err)
	}
	if second.Status != http.StatusOK || second.Err != nil {
		t.Errorf("second attempt Status, Err = %d, %v, want 200, nil", second.Status, second.Err)
	}
}

func TestWithObserver_GetBytes_reports_retry_after_and_a_settled_status(t *testing.T) {
	var rec record
	srv := statusServer(t, http.Header{"Retry-After": {"30"}}, http.StatusNotFound)
	_, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), srv.URL,
		httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if se, ok := errors.AsType[*httpclient.StatusError](err); !ok || se.Code != http.StatusNotFound {
		t.Fatalf("GetBytes err = %v, want the 404", err)
	}
	o := rec.check(t, 1)
	if a := rec.attempts[0]; a.Status != http.StatusNotFound || a.RetryAfter != 30*time.Second {
		t.Errorf("attempt Status, RetryAfter = %d, %v, want 404, 30s", a.Status, a.RetryAfter)
	}
	if o.Err == nil || o.Err.Error() != err.Error() {
		t.Errorf("Outcome.Err = %v, want what GetBytes returned, %v", o.Err, err)
	}
}

func TestWithObserver_GetBytes_too_large_body(t *testing.T) {
	var rec record
	srv := statusServer(t, nil, http.StatusOK)
	_, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), srv.URL,
		httpclient.WithMaxBodyBytes(1), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if _, ok := errors.AsType[*httpclient.ResponseTooLargeError](err); !ok {
		t.Fatalf("GetBytes err = %v, want *ResponseTooLargeError", err)
	}
	rec.check(t, 1)
	if a := rec.attempts[0]; a.Status != http.StatusOK || a.Cause != httpclient.CauseTooLarge {
		t.Errorf("attempt Status, Cause = %d, %v, want 200, too large", a.Status, a.Cause)
	}
}

// GetBytes hands the observer its errors as it returns them, redacted, so a
// secret in the URL never crosses the hook.
func TestWithObserver_GetBytes_errors_are_redacted(t *testing.T) {
	var rec record
	url := "http://" + closedPort(t) + "/feed?token=s3cr3t"
	_, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), url,
		httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Millisecond),
		httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if err == nil {
		t.Fatal("GetBytes to a closed port succeeded")
	}
	o := rec.check(t, 2)
	for _, a := range rec.attempts {
		if a.Err == nil || strings.Contains(a.Err.Error(), "s3cr3t") || a.Cause != httpclient.CauseRefused || a.Status != 0 {
			t.Errorf("attempt %d Err, Cause, Status = %v, %v, %d, want a redacted refusal with no status", a.Number, a.Err, a.Cause, a.Status)
		}
	}
	if strings.Contains(o.Err.Error(), "s3cr3t") {
		t.Errorf("Outcome.Err %q holds the secret", o.Err)
	}
}

func TestTransportObserver_reports_each_round_trip(t *testing.T) {
	var rec record
	srv := statusServer(t, http.Header{"Retry-After": {"0"}}, http.StatusBadGateway, http.StatusOK)
	client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{
		BaseDelay: time.Millisecond, Observer: rec.observer(),
	})
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	o := rec.check(t, 2)
	if o.Err != nil {
		t.Errorf("Outcome.Err = %v, want nil", o.Err)
	}
	if first, second := rec.attempts[0], rec.attempts[1]; first.Status != http.StatusBadGateway || first.Err != nil || second.Status != http.StatusOK {
		t.Errorf("attempts Status, Err = %d, %v and %d, want 502, nil and 200", first.Status, first.Err, second.Status)
	}
}

// A request the transport may not replay is one try, observed like any other.
func TestTransportObserver_single_try_request(t *testing.T) {
	var rec record
	srv := statusServer(t, nil, http.StatusServiceUnavailable)
	client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{Observer: rec.observer()})
	resp, err := client.Post(srv.URL, "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	rec.check(t, 1)
	if a := rec.attempts[0]; a.Status != http.StatusServiceUnavailable {
		t.Errorf("attempt Status = %d, want 503", a.Status)
	}
}

// A MaxElapsedTime stop starts no wait, so the last attempt reports none,
// and the outcome carries ErrMaxElapsed.
func TestTransportObserver_max_elapsed_stop(t *testing.T) {
	var rec record
	srv := statusServer(t, http.Header{"Retry-After": {"30"}}, http.StatusServiceUnavailable)
	client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{
		MaxElapsedTime: time.Second, Observer: rec.observer(),
	})
	_, err := client.Get(srv.URL) //nolint:bodyclose // error path, no body
	if !errors.Is(err, httpclient.ErrMaxElapsed) {
		t.Fatalf("Get err = %v, want ErrMaxElapsed", err)
	}
	o := rec.check(t, 1)
	if !errors.Is(o.Err, httpclient.ErrMaxElapsed) {
		t.Errorf("Outcome.Err = %v, want ErrMaxElapsed", o.Err)
	}
	if a := rec.attempts[0]; a.RetryAfter != 30*time.Second {
		t.Errorf("attempt RetryAfter = %v, want 30s", a.RetryAfter)
	}
}

// A CheckRetry error ends the call: the attempt keeps the round trip's own
// result and the outcome carries the policy's error.
func TestTransportObserver_check_retry_error(t *testing.T) {
	var rec record
	stop := errors.New("stop")
	srv := statusServer(t, nil, http.StatusTeapot)
	client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{
		CheckRetry: func(context.Context, *http.Response, error) (bool, error) { return false, stop },
		Observer:   rec.observer(),
	})
	_, err := client.Get(srv.URL) //nolint:bodyclose // error path, no body
	if !errors.Is(err, stop) {
		t.Fatalf("Get err = %v, want the policy's error", err)
	}
	o := rec.check(t, 1)
	if a := rec.attempts[0]; a.Status != http.StatusTeapot || a.Err != nil || !errors.Is(o.Err, stop) {
		t.Errorf("attempt Status, Err = %d, %v and Outcome.Err = %v, want 418, nil and stop", a.Status, a.Err, o.Err)
	}
}

// A retry whose body cannot be rewound is a try that sent nothing: it is
// reported with its error and no status, and it ends the call.
func TestTransportObserver_rewind_failure(t *testing.T) {
	var rec record
	srv := statusServer(t, nil, http.StatusServiceUnavailable)
	client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{
		BaseDelay: time.Millisecond, Observer: rec.observer(),
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "k")
	rewind := errors.New("no rewind")
	req.GetBody = func() (io.ReadCloser, error) { return nil, rewind }
	if _, err := client.Do(req); !errors.Is(err, rewind) { //nolint:bodyclose // error path, no body
		t.Fatalf("Do err = %v, want the rewind failure", err)
	}
	o := rec.check(t, 2)
	if a := rec.attempts[1]; !errors.Is(a.Err, rewind) || a.Status != 0 || !errors.Is(o.Err, rewind) {
		t.Errorf("second attempt Err, Status = %v, %d and Outcome.Err = %v, want the rewind failure, 0 and it again", a.Err, a.Status, o.Err)
	}
}

// A nil func in Observer is skipped, whichever half is set.
func TestObserver_nil_funcs_are_skipped(t *testing.T) {
	srv := statusServer(t, nil, http.StatusOK)
	var outcomes, attempts atomic.Int32
	for _, obs := range []httpclient.Observer{
		{},
		{Outcome: func(httpclient.Outcome) { outcomes.Add(1) }},
		{Attempt: func(httpclient.Attempt) { attempts.Add(1) }},
	} {
		if _, err := httpclient.Do(t.Context(), func(context.Context) (int, error) { return 1, nil }, httpclient.WithObserver(obs)); err != nil {
			t.Fatal(err)
		}
		if _, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), srv.URL, httpclient.WithObserver(obs)); err != nil {
			t.Fatal(err)
		}
		resp, err := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{Observer: obs}).Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if outcomes.Load() != 3 || attempts.Load() != 3 {
		t.Errorf("outcomes, attempts = %d, %d, want 3, 3", outcomes.Load(), attempts.Load())
	}
}

// One RetryRoundTripper shared between goroutines calls its observer from
// each of them; the race detector checks the door adds no shared state.
func TestTransportObserver_concurrent_round_trips(t *testing.T) {
	var rec record
	srv := statusServer(t, nil, http.StatusOK)
	client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{Observer: rec.observer()})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			resp, err := client.Get(srv.URL)
			if err != nil {
				t.Error(err)
				return
			}
			_ = resp.Body.Close()
		})
	}
	wg.Wait()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.attempts) != 8 || len(rec.outcomes) != 8 {
		t.Errorf("attempts, outcomes = %d, %d, want 8, 8", len(rec.attempts), len(rec.outcomes))
	}
}
