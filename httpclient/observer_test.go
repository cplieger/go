package httpclient_test

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// mustOutcome returns the one Outcome the observer got.
func (r *record) mustOutcome(t *testing.T) httpclient.Outcome {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.outcomes) != 1 {
		t.Fatalf("observer got %d outcomes, want 1", len(r.outcomes))
	}
	return r.outcomes[0]
}

// errs lists the attempts' errors.
func (r *record) errs() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]error, 0, len(r.attempts))
	for _, a := range r.attempts {
		out = append(out, a.Err)
	}
	return out
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
		time.Sleep(2 * time.Millisecond)
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
	var spent time.Duration
	for _, a := range rec.attempts {
		if a.Duration < 2*time.Millisecond {
			t.Errorf("attempt %d Duration = %v, shorter than the try's 2ms", a.Number, a.Duration)
		}
		spent += a.Duration + a.Wait
	}
	if o.Duration < spent {
		t.Errorf("Outcome.Duration = %v, shorter than the tries and waits %v", o.Duration, spent)
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
	srv := statusServer(t, http.Header{"Retry-After": {"7200"}}, http.StatusNotFound)
	_, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), srv.URL,
		httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if se, ok := errors.AsType[*httpclient.StatusError](err); !ok || se.Code != http.StatusNotFound {
		t.Fatalf("GetBytes err = %v, want the 404", err)
	}
	o := rec.check(t, 1)
	if a := rec.attempts[0]; a.Status != http.StatusNotFound || a.RetryAfter != httpclient.RetryAfterCap {
		t.Errorf("attempt Status, RetryAfter = %d, %v, want 404, RetryAfterCap", a.Status, a.RetryAfter)
	}
	if o.Err == nil || o.Err.Error() != err.Error() {
		t.Errorf("Outcome.Err = %v, want what GetBytes returned, %v", o.Err, err)
	}
}

func TestWithObserver_GetBytes_too_large_body(t *testing.T) {
	var rec record
	srv := statusServer(t, http.Header{"Retry-After": {"5"}}, http.StatusOK)
	_, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), srv.URL,
		httpclient.WithMaxBodyBytes(1), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if _, ok := errors.AsType[*httpclient.ResponseTooLargeError](err); !ok {
		t.Fatalf("GetBytes err = %v, want *ResponseTooLargeError", err)
	}
	rec.check(t, 1)
	if a := rec.attempts[0]; a.Status != http.StatusOK || a.Cause != httpclient.CauseTooLarge || a.RetryAfter != 5*time.Second {
		t.Errorf("attempt Status, Cause, RetryAfter = %d, %v, %v, want 200, too large, 5s", a.Status, a.Cause, a.RetryAfter)
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
	srv := statusServer(t, http.Header{"Retry-After": {"7200"}}, http.StatusServiceUnavailable)
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
	if a := rec.attempts[0]; a.RetryAfter != httpclient.RetryAfterCap {
		t.Errorf("attempt RetryAfter = %v, want RetryAfterCap", a.RetryAfter)
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

// A redirect the client refuses still delivered its response: the attempt
// keeps that status and Retry-After, and the refusal reaches the observer
// redacted, while GetBytes returns what it returns with no observer.
func TestWithObserver_GetBytes_redirect_refusal_keeps_status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7200")
		w.Header().Set("Location", "http://user:s3cr3t@elsewhere.example/x?token=s3cr3t")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	get := func(opts ...httpclient.GetOption) error {
		_, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), srv.URL,
			append([]httpclient.GetOption{httpclient.WithMaxAttempts(1), httpclient.WithLogger(discardLogger())}, opts...)...)
		return err
	}
	plain := get()
	var rec record
	err := get(httpclient.WithObserver(rec.observer()))
	if _, ok := errors.AsType[*httpclient.RedirectRefusedError](err); !ok || err.Error() != plain.Error() {
		t.Fatalf("GetBytes err = %v, want the refusal it returns unobserved, %v", err, plain)
	}
	o := rec.check(t, 1)
	a := rec.attempts[0]
	if a.Status != http.StatusFound || a.RetryAfter != httpclient.RetryAfterCap {
		t.Errorf("attempt Status, RetryAfter = %d, %v, want 302, RetryAfterCap", a.Status, a.RetryAfter)
	}
	for _, e := range []error{a.Err, o.Err} {
		if _, ok := errors.AsType[*httpclient.RedirectRefusedError](e); !ok || strings.Contains(e.Error(), "s3cr3t") {
			t.Errorf("observed error %v, want the redacted refusal", e)
		}
	}
}

// urlErrorRT fails every round trip with the *url.Error an http.Client would
// build, URL and all, as a base wrapping another client does.
type urlErrorRT struct{ cause error }

func (u urlErrorRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: u.cause}
}

// Every door hands the observer its errors through LogSafeError, so a URL's
// userinfo and query never cross the hook, while the door still returns its
// own error.
func TestObserver_errors_are_redacted_at_every_door(t *testing.T) {
	const target = "http://user:s3cr3t@example.test/feed?token=s3cr3t"
	cause := httpclient.MarkTransient(errors.New("boom"))
	for _, door := range []struct {
		name string
		call func(httpclient.Observer) error
	}{
		{"Do", func(obs httpclient.Observer) error {
			_, err := httpclient.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
				_, err := urlErrorRT{cause: cause}.RoundTrip(req) //nolint:bodyclose // error path, no body
				return struct{}{}, err
			}, httpclient.WithMaxAttempts(1), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(obs))
			return err
		}},
		{"transport", func(obs httpclient.Observer) error {
			rt := httpclient.NewRetryRoundTripper(urlErrorRT{cause: cause}, httpclient.TransportConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, Observer: obs})
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
			_, err := rt.RoundTrip(req) //nolint:bodyclose // error path, no body
			return err
		}},
	} {
		t.Run(door.name, func(t *testing.T) {
			var rec record
			err := door.call(rec.observer())
			if !strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("the door returned %q, want its own unredacted error", err)
			}
			o := rec.mustOutcome(t)
			for _, e := range append([]error{o.Err}, rec.errs()...) {
				if _, isURL := errors.AsType[*url.Error](e); isURL || strings.Contains(e.Error(), "s3cr3t") || !errors.Is(e, cause) || !httpclient.IsTransient(e) {
					t.Errorf("observed error %q, want the redacted cause with its mark", e)
				}
			}
		})
	}
}

// A context that ends during a GetBytes or transport wait stops the call:
// the attempt keeps its wait, none follows, and the outcome is the context's.
func TestObserver_context_end_during_the_wait(t *testing.T) {
	srv := statusServer(t, nil, http.StatusServiceUnavailable)
	for _, door := range []struct {
		name string
		call func(context.Context, httpclient.Observer) error
	}{
		{"GetBytes", func(ctx context.Context, obs httpclient.Observer) error {
			_, err := httpclient.GetBytes(ctx, httpclient.New(5*time.Second), srv.URL,
				httpclient.WithBaseDelay(time.Hour), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(obs))
			return err
		}},
		{"transport", func(ctx context.Context, obs httpclient.Observer) error {
			client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{BaseDelay: time.Hour, Observer: obs})
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
			_, err := client.Do(req) //nolint:bodyclose // error path, no body
			return err
		}},
	} {
		t.Run(door.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			var attempts []httpclient.Attempt
			var outcome httpclient.Outcome
			err := door.call(ctx, httpclient.Observer{
				Attempt: func(a httpclient.Attempt) { attempts = append(attempts, a); cancel() },
				Outcome: func(o httpclient.Outcome) { outcome = o },
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if len(attempts) != 1 || attempts[0].Wait < 30*time.Minute || attempts[0].Status != http.StatusServiceUnavailable {
				t.Fatalf("attempts = %+v, want one 503 with the hour's jittered wait", attempts)
			}
			if outcome.Attempts != 1 || !errors.Is(outcome.Err, context.Canceled) {
				t.Errorf("Outcome = %+v, want 1 attempt and context.Canceled", outcome)
			}
		})
	}
}

// cancelRT answers a retryable 503 after ending the request's context, so the
// transport stops on the context once the try is judged.
type cancelRT struct{ cancel context.CancelFunc }

func (c cancelRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.cancel()
	return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: http.NoBody, Request: req}, nil
}

// A context that ends during a transport try leaves the try its own 503 and
// gives the outcome the context's error.
func TestTransportObserver_context_end_after_a_try(t *testing.T) {
	var rec record
	ctx, cancel := context.WithCancel(t.Context())
	rt := httpclient.NewRetryRoundTripper(cancelRT{cancel: cancel}, httpclient.TransportConfig{Observer: rec.observer()})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.test/", http.NoBody)
	if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) { //nolint:bodyclose // error path, no body
		t.Fatalf("RoundTrip err = %v, want context.Canceled", err)
	}
	o := rec.check(t, 1)
	if a := rec.attempts[0]; a.Status != http.StatusServiceUnavailable || a.Err != nil || !errors.Is(o.Err, context.Canceled) {
		t.Errorf("attempt Status, Err = %d, %v and Outcome.Err = %v, want 503, nil and context.Canceled", a.Status, a.Err, o.Err)
	}
}

// stamp records the instant a step before the send ended.
type stamp struct {
	mu sync.Mutex
	at time.Time
}

func (s *stamp) mark() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at = time.Now()
}

func (s *stamp) get() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at
}

// The transport times a try from its send: a PrepareRetry is not the
// upstream's time, so the try starts after it returned.
func TestTransportObserver_start_follows_prepare_retry(t *testing.T) {
	var rec record
	var prepared stamp
	srv := statusServer(t, nil, http.StatusServiceUnavailable)
	client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{
		MaxAttempts: 2, BaseDelay: time.Millisecond, Observer: rec.observer(),
		PrepareRetry: func(*http.Request) error { time.Sleep(time.Millisecond); prepared.mark(); return nil },
	})
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	rec.check(t, 2)
	if a := rec.attempts[1]; a.Start.Before(prepared.get()) {
		t.Errorf("second attempt started %v before PrepareRetry returned", prepared.get().Sub(a.Start))
	}
}

// GetBytes times a try from its send: WithHeaders is not the upstream's time,
// so the try starts after it returned.
func TestWithObserver_GetBytes_start_follows_headers(t *testing.T) {
	var rec record
	var headed stamp
	srv := statusServer(t, nil, http.StatusOK)
	_, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), srv.URL,
		httpclient.WithHeaders(func(*http.Request) { time.Sleep(time.Millisecond); headed.mark() }),
		httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if err != nil {
		t.Fatal(err)
	}
	rec.check(t, 1)
	if a := rec.attempts[0]; a.Start.Before(headed.get()) {
		t.Errorf("attempt started %v before WithHeaders returned", headed.get().Sub(a.Start))
	}
}

// A PrepareRetry failure is observed as that step's, redacted, not as the
// upstream's: the step survives LogSafeError and the URL does not.
func TestTransportObserver_prepare_retry_failure_keeps_its_step(t *testing.T) {
	var rec record
	srv := statusServer(t, nil, http.StatusServiceUnavailable)
	refresh := &url.Error{Op: "Post", URL: "http://user:s3cr3t@token.example/refresh?token=s3cr3t", Err: errors.New("connection refused")}
	rt := httpclient.NewRetryRoundTripper(nil, httpclient.TransportConfig{
		MaxAttempts: 2, BaseDelay: time.Millisecond, Observer: rec.observer(),
		PrepareRetry: func(*http.Request) error { return refresh },
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	_, err := rt.RoundTrip(req) //nolint:bodyclose // error path, no body
	if !errors.Is(err, refresh) || !strings.HasPrefix(err.Error(), "prepare retry: ") {
		t.Fatalf("RoundTrip err = %v, want the prepare retry step over the refresh error", err)
	}
	o := rec.check(t, 2)
	for _, e := range []error{rec.attempts[1].Err, o.Err} {
		if e.Error() != "prepare retry: connection refused" {
			t.Errorf("observed error %q, want the step over the redacted cause", e)
		}
	}
}

// A body that cannot be rewound for a retry is observed as that step's
// failure, redacted, like a PrepareRetry failure.
func TestTransportObserver_rewind_failure_keeps_its_step(t *testing.T) {
	var rec record
	srv := statusServer(t, nil, http.StatusServiceUnavailable)
	refresh := &url.Error{Op: "Get", URL: "http://user:s3cr3t@body.example/x?token=s3cr3t", Err: errors.New("connection refused")}
	rt := httpclient.NewRetryRoundTripper(nil, httpclient.TransportConfig{
		MaxAttempts: 2, BaseDelay: time.Millisecond, Observer: rec.observer(),
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL, strings.NewReader("x"))
	req.Header.Set("Idempotency-Key", "k")
	req.GetBody = func() (io.ReadCloser, error) { return nil, refresh }
	_, err := rt.RoundTrip(req) //nolint:bodyclose // error path, no body
	if !errors.Is(err, refresh) || !strings.HasPrefix(err.Error(), "rewind request body: ") {
		t.Fatalf("RoundTrip err = %v, want the rewind step over the body error", err)
	}
	o := rec.check(t, 2)
	for _, e := range []error{rec.attempts[1].Err, o.Err} {
		if e.Error() != "rewind request body: connection refused" {
			t.Errorf("observed error %q, want the step over the redacted cause", e)
		}
	}
}

// statusOverURL reports a *StatusError through an As method while it wraps a
// *url.Error, so the redaction drops it with the wrapper.
type statusOverURL struct {
	se  *httpclient.StatusError
	err error
}

func (e statusOverURL) Error() string { return "status: " + e.err.Error() }
func (e statusOverURL) Unwrap() error { return e.err }
func (e statusOverURL) As(target any) bool {
	if p, ok := target.(**httpclient.StatusError); ok {
		*p = e.se
		return true
	}
	return false
}

// Do reads Status and RetryAfter from the function's error before the
// redaction, so they survive a wrapper the redaction drops.
func TestWithObserver_Do_status_read_before_the_redaction(t *testing.T) {
	var rec record
	wrapped := statusOverURL{
		se:  &httpclient.StatusError{Code: http.StatusServiceUnavailable, RetryAfter: 4 * time.Second},
		err: &url.Error{Op: "Get", URL: "http://example.test/?token=s3cr3t", Err: errors.New("boom")},
	}
	_, _ = httpclient.Do(t.Context(), func(context.Context) (struct{}, error) {
		return struct{}{}, wrapped
	}, httpclient.WithMaxAttempts(1), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	o := rec.check(t, 1)
	a := rec.attempts[0]
	if a.Status != http.StatusServiceUnavailable || a.RetryAfter != 4*time.Second {
		t.Errorf("attempt Status, RetryAfter = %d, %v, want 503, 4s", a.Status, a.RetryAfter)
	}
	for _, e := range []error{a.Err, o.Err} {
		if _, ok := errors.AsType[*httpclient.StatusError](e); ok {
			t.Errorf("observed error %v still reaches the *StatusError the redaction dropped", e)
		}
	}
}

// A GetBytes request that cannot be built is a try with no send: it keeps
// the try's start and the build error.
func TestWithObserver_GetBytes_unbuildable_request(t *testing.T) {
	var rec record
	before := time.Now()
	_, err := httpclient.GetBytes(t.Context(), httpclient.New(5*time.Second), "http://h/\x7f",
		httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if err == nil {
		t.Fatal("GetBytes built a request from a URL with a control character")
	}
	rec.check(t, 1)
	if a := rec.attempts[0]; a.Err == nil || a.Start.Before(before) || a.Status != 0 {
		t.Errorf("attempt Start, Err, Status = %v, %v, %d, want the try's start, the build error and no status", a.Start, a.Err, a.Status)
	}
}

// callerTransient is a Do caller's own wrapper over a transport error, with a
// class and a Retry-After hint of its own.
type callerTransient struct{ err error }

func (c callerTransient) Error() string               { return "fetch: " + c.err.Error() }
func (c callerTransient) Unwrap() error               { return c.err }
func (callerTransient) IsTransient() bool             { return true }
func (callerTransient) RetryAfterHint() time.Duration { return 3 * time.Second }

// A caller's own wrapper above a *url.Error is dropped from the record with
// its URL, as LogSafeError documents, while Do acts on it and returns it, and
// the record keeps the hint Do read before the redaction.
func TestWithObserver_Do_caller_wrapper_is_dropped(t *testing.T) {
	var rec record
	wrapped := callerTransient{&url.Error{Op: "Get", URL: "http://example.test/feed?token=s3cr3t", Err: errors.New("boom")}}
	_, err := httpclient.Do(t.Context(), func(context.Context) (struct{}, error) {
		return struct{}{}, wrapped
	}, httpclient.WithMaxAttempts(1), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	if !httpclient.IsTransient(err) || !strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("Do err = %v, want the caller's wrapper unchanged", err)
	}
	rec.check(t, 1)
	a := rec.attempts[0]
	if _, kept := errors.AsType[callerTransient](a.Err); kept || httpclient.IsTransient(a.Err) || a.Err.Error() != "boom" {
		t.Errorf("attempt Err = %v, want the bare cause with the wrapper dropped", a.Err)
	}
	if a.RetryAfter != 3*time.Second {
		t.Errorf("attempt RetryAfter = %v, want the hint read before the redaction", a.RetryAfter)
	}
}

// A MaxElapsedTime stop over a *url.Error is observed with the ceiling and
// the format the door returns, the URL reduced inside the wrap.
func TestTransportObserver_max_elapsed_over_a_url_error(t *testing.T) {
	var rec record
	cause := httpclient.MarkTransient(errors.New("boom"))
	rt := httpclient.NewRetryRoundTripper(urlErrorRT{cause: cause}, httpclient.TransportConfig{
		BaseDelay: time.Hour, MaxElapsedTime: time.Second, Observer: rec.observer(),
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.test/x?token=s3cr3t", http.NoBody)
	_, err := rt.RoundTrip(req) //nolint:bodyclose // error path, no body
	if !errors.Is(err, httpclient.ErrMaxElapsed) {
		t.Fatalf("RoundTrip err = %v, want ErrMaxElapsed", err)
	}
	o := rec.check(t, 1)
	if got := o.Err.Error(); got != "max elapsed time exceeded (1s): boom" || !errors.Is(o.Err, httpclient.ErrMaxElapsed) || !errors.Is(o.Err, cause) {
		t.Errorf("Outcome.Err = %q, want the ceiling over the reduced cause", got)
	}
}

// Under WithAttemptTimeout, the try whose own bound expired is reported with
// the mark Do classifies it by.
func TestWithObserver_Do_attempt_timeout(t *testing.T) {
	var rec record
	_, _ = httpclient.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	}, httpclient.WithAttemptTimeout(time.Millisecond), httpclient.WithMaxAttempts(1),
		httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
	rec.check(t, 1)
	if a := rec.attempts[0]; !httpclient.IsAttemptTimeout(a.Err) || a.Cause != httpclient.CauseTimeout {
		t.Errorf("attempt Err, Cause = %v, %v, want the AttemptTimeout mark and timeout", a.Err, a.Cause)
	}
}

// hintedStatus carries both a Retry-After hint and a *StatusError whose own
// Retry-After differs.
type hintedStatus struct{ *httpclient.StatusError }

func (hintedStatus) RetryAfterHint() time.Duration { return 2 * time.Second }
func (h hintedStatus) Unwrap() error               { return h.StatusError }

// Do reads Retry-After from a RetryAfterHint first, else a *StatusError, and
// floors a negative value at zero.
func TestWithObserver_Do_retry_after_sources(t *testing.T) {
	srv := statusServer(t, http.Header{"Retry-After": {"4"}}, http.StatusServiceUnavailable)
	for _, tc := range []struct {
		name   string
		err    func(context.Context) error
		status int
		want   time.Duration
	}{
		{"GetBytes's exhaustion hint", func(ctx context.Context) error {
			_, err := httpclient.GetBytes(ctx, httpclient.New(5*time.Second), srv.URL, httpclient.WithMaxAttempts(1), httpclient.WithLogger(discardLogger()))
			return err
		}, http.StatusServiceUnavailable, 4 * time.Second},
		{"a hint over the status error's own", func(context.Context) error {
			return hintedStatus{&httpclient.StatusError{Code: http.StatusServiceUnavailable, RetryAfter: 9 * time.Second}}
		}, http.StatusServiceUnavailable, 2 * time.Second},
		{"a negative value", func(context.Context) error {
			return &httpclient.StatusError{Code: http.StatusServiceUnavailable, RetryAfter: -time.Second}
		}, http.StatusServiceUnavailable, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec record
			_, _ = httpclient.Do(t.Context(), func(ctx context.Context) (struct{}, error) {
				return struct{}{}, tc.err(ctx)
			}, httpclient.WithMaxAttempts(1), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(rec.observer()))
			rec.check(t, 1)
			if a := rec.attempts[0]; a.Status != tc.status || a.RetryAfter != tc.want {
				t.Errorf("attempt Status, RetryAfter = %d, %v, want %d, %v", a.Status, a.RetryAfter, tc.status, tc.want)
			}
		})
	}
}

// Under WithRateLimitOnly a 429's wait is the rate-limit wait, and a context
// that ends during it is the outcome.
func TestWithObserver_Do_rate_limit_only_wait(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var attempts []httpclient.Attempt
	var outcome httpclient.Outcome
	_, err := httpclient.Do(ctx, func(context.Context) (struct{}, error) {
		return struct{}{}, &httpclient.StatusError{Code: http.StatusTooManyRequests}
	}, httpclient.WithRateLimitOnly(time.Hour), httpclient.WithLogger(discardLogger()), httpclient.WithObserver(httpclient.Observer{
		Attempt: func(a httpclient.Attempt) { attempts = append(attempts, a); cancel() },
		Outcome: func(o httpclient.Outcome) { outcome = o },
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do err = %v, want context.Canceled", err)
	}
	if len(attempts) != 1 || attempts[0].Status != http.StatusTooManyRequests || attempts[0].Wait != time.Hour {
		t.Fatalf("attempts = %+v, want one 429 with the hour's wait", attempts)
	}
	if !errors.Is(outcome.Err, context.Canceled) {
		t.Errorf("Outcome.Err = %v, want context.Canceled", outcome.Err)
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
