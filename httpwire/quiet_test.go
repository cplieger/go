package httpwire_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpwire"
)

// gate holds a handler: each request sends its context on entered, then
// returns once release closes or its context ends.
type gate struct {
	entered chan context.Context
	release chan struct{}
}

func newGate() *gate {
	return &gate{entered: make(chan context.Context, 8), release: make(chan struct{})}
}

func (g *gate) hold(ctx context.Context) error {
	g.entered <- ctx
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// quietCase is the fixture table under fixtureGuard, with settings.save,
// jobs.list, events.wait, live.stream and archive.export held by gates.
type quietCase struct {
	*apiCase
	api                                 *httpwire.API
	write, read, held, stream, rawWrite *gate
}

func newQuietCase(t *testing.T) *quietCase {
	t.Helper()
	q := &quietCase{apiCase: newCase(t), write: newGate(), read: newGate(), held: newGate(), stream: newGate(), rawWrite: newGate()}
	q.binds["settings.save"] = httpwire.Bind("settings.save", func(ctx context.Context, _ httpwire.Call[noP, noQ, otherBody]) (httpwire.None, error) {
		return httpwire.None{}, q.write.hold(ctx)
	})
	q.binds["jobs.list"] = httpwire.Bind("jobs.list", func(ctx context.Context, _ httpwire.Call[noP, jobQuery, noB]) ([]job, error) {
		return []job{{ID: "a"}}, q.read.hold(ctx)
	})
	q.binds["events.wait"] = httpwire.Bind("events.wait", func(ctx context.Context, _ httpwire.Call[noP, noQ, noB]) (httpwire.Status[nudge], error) {
		return httpwire.Answer(http.StatusOK, nudge{At: 1}), q.held.hold(ctx)
	})
	q.binds["live.stream"] = httpwire.BindStream("live.stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q.stream.hold(r.Context()) == nil {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: changed\ndata: {}\n\n")
		}
	}))
	q.binds["archive.export"] = httpwire.BindRaw("archive.export", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q.rawWrite.hold(r.Context()) == nil {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK"))
		}
	}))
	return q
}

func (q *quietCase) build(t *testing.T) *httpwire.API {
	t.Helper()
	q.api = q.mustBuild(t)
	return q.api
}

func writeReq() *http.Request {
	return request(http.MethodPatch, "/api/settings", `{"name":"x"}`, "If-Match", fixtureIfMatch)
}

func readReq() *http.Request { return request(http.MethodGet, "/api/jobs", "") }

// start serves r in its own goroutine; the channel carries its answer.
func start(h http.Handler, r *http.Request) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() { out <- answer(h, r) }()
	return out
}

type quiesced struct {
	resume func()
	err    error
}

func startQuiesce(ctx context.Context, qr httpwire.Quiescer, q httpwire.Quiet) <-chan quiesced {
	out := make(chan quiesced, 1)
	go func() {
		resume, err := qr.Quiesce(ctx, q)
		out <- quiesced{resume, err}
	}()
	return out
}

// ready answers ch's value when one is waiting, after every goroutine of
// the bubble has blocked.
func ready[T any](ch <-chan T) (T, bool) {
	synctest.Wait()
	select {
	case v := <-ch:
		return v, true
	default:
		var zero T
		return zero, false
	}
}

func mustQuiesce(t *testing.T, api *httpwire.API, q httpwire.Quiet) func() {
	t.Helper()
	resume, err := api.Quiesce(t.Context(), q)
	if err != nil || resume == nil {
		t.Fatalf("Quiesce(%d): resume %v, error %v", q, resume != nil, err)
	}
	return resume
}

func isMaintenance(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	return rec.Code == http.StatusServiceUnavailable && codeOf(t, rec) == "maintenance" && rec.Header().Get("Retry-After") == "5"
}

var quietLevels = map[string]httpwire.Quiet{"QuietWrites": httpwire.QuietWrites, "QuietAll": httpwire.QuietAll}

func TestQuietWritesRefusesBeforeTheGuard(t *testing.T) {
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/jobs", `{"kind":"a"}`},
		{http.MethodPatch, "/api/settings", `{"name":"x"}`},
		{http.MethodPost, "/api/archive", "PK"},
		{http.MethodPost, "/api/auth/keys", `{"name":"x"}`},
		{http.MethodGet, "/api/stream", ""},
	}
	t.Run("writes, mount writes and the stream answer maintenance", func(t *testing.T) {
		o := newOrderCase(t)
		mounted := mountReached(o.apiCase)
		api := o.mustBuild(t)
		mustQuiesce(t, api, httpwire.QuietWrites)
		for _, w := range writes {
			if rec := answer(api, request(w.method, w.path, w.body, "If-Match", fixtureIfMatch)); !isMaintenance(t, rec) {
				t.Errorf("%s %s: answer %d %s Retry-After %q, want 503 maintenance with Retry-After 5", w.method, w.path, rec.Code, rec.Body, rec.Header().Get("Retry-After"))
			}
		}
		if len(o.guarded) != 0 || len(o.handled) != 0 || *mounted {
			t.Errorf("guarded %v, handled %v, mount reached %v; want nothing run", o.guarded, o.handled, *mounted)
		}
		if rec := answer(api, readReq()); rec.Code != http.StatusOK {
			t.Errorf("a read answered %d %s, want 200", rec.Code, rec.Body)
		}
		if answer(api, request(http.MethodGet, "/api/auth/keys", "")); !*mounted {
			t.Error("a mount read was not served")
		}
	})
	t.Run("resume serves writes again, twice called", func(t *testing.T) {
		o := newOrderCase(t)
		api := o.mustBuild(t)
		resume := mustQuiesce(t, api, httpwire.QuietWrites)
		resume()
		resume()
		if rec := answer(api, writeReq()); rec.Code != http.StatusNoContent {
			t.Errorf("a write after resume answered %d %s, want 204", rec.Code, rec.Body)
		}
	})
	t.Run("the client floor and the cross-origin check come first", func(t *testing.T) {
		o := newOrderCase(t)
		o.opts["floor"] = floorOption()
		api := o.mustBuild(t)
		mustQuiesce(t, api, httpwire.QuietWrites)
		if rec := answer(api, request(http.MethodPatch, "/api/settings", `{"name":"x"}`, "Fixture-Version", "1")); codeOf(t, rec) != "upgrade_required" {
			t.Errorf("a client below the floor answered %d %s, want upgrade_required", rec.Code, rec.Body)
		}
		if rec := answer(api, request(http.MethodPost, "/api/auth/keys", `{"name":"x"}`, "Fixture-Version", "1")); codeOf(t, rec) != "upgrade_required" {
			t.Errorf("a mount write below the floor answered %d %s, want upgrade_required", rec.Code, rec.Body)
		}
		if rec := answer(api, request(http.MethodPatch, "/api/settings", `{"name":"x"}`, "Sec-Fetch-Site", "cross-site")); codeOf(t, rec) != "cross_origin_refused" {
			t.Errorf("a cross-site write answered %d %s, want cross_origin_refused", rec.Code, rec.Body)
		}
	})
	t.Run("maintenance comes before an outage", func(t *testing.T) {
		file := "store"
		o, _, _ := outageCase(t, &file)
		api := o.mustBuild(t)
		mustQuiesce(t, api, httpwire.QuietWrites)
		if rec := answer(api, writeReq()); !isMaintenance(t, rec) {
			t.Errorf("a write during an outage answered %d %s, want maintenance", rec.Code, rec.Body)
		}
	})
}

func TestQuiesceRefusesAnUnknownQuiet(t *testing.T) {
	o := newOrderCase(t)
	api := o.mustBuild(t)
	for _, q := range []httpwire.Quiet{0, 3, -1} {
		if resume, err := api.Quiesce(t.Context(), q); err == nil || resume != nil {
			t.Errorf("Quiesce(%d): resume %v, error %v; want an error and no resume", q, resume != nil, err)
		}
	}
	if rec := answer(api, writeReq()); rec.Code != http.StatusNoContent {
		t.Errorf("a write after the refusals answered %d %s, want 204", rec.Code, rec.Body)
	}
}

func TestQuietWritesWaitsForWritesInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newQuietCase(t)
		api := q.build(t)
		write, raw, read, stream := start(api, writeReq()), start(api, request(http.MethodPost, "/api/archive", "PK")), start(api, readReq()), start(api, request(http.MethodGet, "/api/stream", ""))
		writeCtx, readCtx, streamCtx := <-q.write.entered, <-q.read.entered, <-q.stream.entered
		<-q.rawWrite.entered
		done := startQuiesce(t.Context(), api, httpwire.QuietWrites)
		if _, ok := ready(done); ok {
			t.Fatal("Quiesce returned with two writes in flight")
		}
		close(q.rawWrite.release)
		if rec, _ := ready(raw); rec == nil || rec.Code != http.StatusOK {
			t.Fatalf("the Raw write in flight answered %v, want its own 200", rec)
		}
		time.Sleep(time.Minute)
		if _, ok := ready(done); ok {
			t.Fatal("Quiesce returned with a write in flight")
		}
		if writeCtx.Err() != nil {
			t.Fatal("QuietWrites cancelled a write in flight")
		}
		close(q.write.release)
		got, ok := ready(done)
		if !ok || got.err != nil {
			t.Fatalf("Quiesce after the last write returned: %v, error %v", ok, got.err)
		}
		if rec := <-write; rec.Code != http.StatusNoContent {
			t.Errorf("the write in flight answered %d %s, want its own 204", rec.Code, rec.Body)
		}
		if _, ok := ready(read); ok || readCtx.Err() != nil || streamCtx.Err() != nil {
			t.Errorf("read answered %v, read cancelled %v, stream cancelled %v; want both still running", ok, readCtx.Err(), streamCtx.Err())
		}
		if _, ok := ready(stream); ok {
			t.Error("the open stream ended")
		}
		close(q.read.release)
		close(q.stream.release)
		<-read
		<-stream
		got.resume()
	})
}

func TestQuiesceSkipsDetachedCallerRequest(t *testing.T) {
	for name, derive := range map[string]func(context.Context) (context.Context, context.CancelFunc){
		"the request's own context": func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, func() {} },
		"a detached context": func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithoutCancel(ctx), func() {}
		},
		"a detached context with a timeout": func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.WithoutCancel(ctx), time.Hour)
		},
	} {
		for qname, quiet := range quietLevels {
			t.Run(name+" under "+qname, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					q := newQuietCase(t)
					type outcome struct {
						resume       func()
						err          error
						waited       time.Duration
						callerCancel error
					}
					outcomes := make(chan outcome, 1)
					q.binds["settings.save"] = httpwire.Bind("settings.save", func(ctx context.Context, _ httpwire.Call[noP, noQ, otherBody]) (httpwire.None, error) {
						qctx, cancel := derive(ctx)
						defer cancel()
						began := time.Now()
						resume, err := q.api.Quiesce(qctx, quiet)
						waited := time.Since(began)
						time.Sleep(15 * time.Second)
						outcomes <- outcome{resume, err, waited, ctx.Err()}
						return httpwire.None{}, nil
					})
					api := q.build(t)
					other := start(api, request(http.MethodPost, "/api/archive", "PK"))
					<-q.rawWrite.entered
					caller := start(api, writeReq())
					time.Sleep(3 * time.Second)
					close(q.rawWrite.release)
					got := <-outcomes
					if got.err != nil || got.resume == nil {
						t.Fatalf("the caller's Quiesce: resume %v, error %v", got.resume != nil, got.err)
					}
					if got.waited != 3*time.Second {
						t.Errorf("the caller's Quiesce waited %v, want the 3 s until the other write returned", got.waited)
					}
					if got.callerCancel != nil {
						t.Errorf("the caller's own context ended: %v", got.callerCancel)
					}
					if rec := <-caller; rec.Code != http.StatusNoContent {
						t.Errorf("the caller answered %d %s, want 204", rec.Code, rec.Body)
					}
					if rec := <-other; rec.Code != http.StatusOK {
						t.Errorf("the other write answered %d %s, want its own 200", rec.Code, rec.Body)
					}
					got.resume()
				})
			})
		}
	}
}

func TestQuietAllHoldsReadsUntilResume(t *testing.T) {
	t.Run("a read waits and is answered after resume", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			q := newQuietCase(t)
			close(q.read.release)
			api := q.build(t)
			resume := mustQuiesce(t, api, httpwire.QuietAll)
			read := start(api, readReq())
			mount := start(api, request(http.MethodGet, "/api/auth/keys", ""))
			began := time.Now()
			time.Sleep(2 * time.Second)
			if _, ok := ready(read); ok {
				t.Fatal("a read was answered under QuietAll")
			}
			if len(q.read.entered) != 0 {
				t.Fatal("a held read reached its handler")
			}
			resume()
			rec := <-read
			if rec.Code != http.StatusOK || time.Since(began) != 2*time.Second {
				t.Errorf("the held read answered %d %s after %v, want 200 at resume, 2 s in", rec.Code, rec.Body, time.Since(began))
			}
			if rec := <-mount; rec.Code != http.StatusNotFound {
				t.Errorf("the held mount read answered %d, want the mount's own 404 after resume", rec.Code)
			}
		})
	})
	t.Run("a read waiting past 5 s answers maintenance", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			q := newQuietCase(t)
			api := q.build(t)
			resume := mustQuiesce(t, api, httpwire.QuietAll)
			began := time.Now()
			rec := answer(api, readReq())
			if !isMaintenance(t, rec) || time.Since(began) != 5*time.Second {
				t.Errorf("the read answered %d %s after %v, want maintenance 5 s in", rec.Code, rec.Body, time.Since(began))
			}
			if len(q.read.entered) != 0 {
				t.Error("the refused read reached its handler")
			}
			resume()
		})
	})
	t.Run("a write and the stream answer maintenance at once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			q := newQuietCase(t)
			api := q.build(t)
			resume := mustQuiesce(t, api, httpwire.QuietAll)
			began := time.Now()
			for _, r := range []*http.Request{writeReq(), request(http.MethodGet, "/api/stream", "")} {
				if rec := answer(api, r); !isMaintenance(t, rec) || time.Since(began) != 0 {
					t.Errorf("%s %s answered %d %s after %v, want maintenance at once", r.Method, r.URL.Path, rec.Code, rec.Body, time.Since(began))
				}
			}
			resume()
		})
	})
	t.Run("a held read whose client left is never served", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			q := newQuietCase(t)
			api := q.build(t)
			resume := mustQuiesce(t, api, httpwire.QuietAll)
			ctx, leave := context.WithCancel(t.Context())
			read := start(api, readReq().WithContext(ctx))
			time.Sleep(time.Second)
			leave()
			if _, ok := ready(read); !ok {
				t.Fatal("the read whose client left is still held")
			}
			resume()
			synctest.Wait()
			if len(q.read.entered) != 0 {
				t.Error("the read whose client left reached its handler")
			}
		})
	})
	t.Run("QuietAll under QuietWrites returns to it at its own resume", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			q := newQuietCase(t)
			close(q.read.release)
			close(q.write.release)
			api := q.build(t)
			writes := mustQuiesce(t, api, httpwire.QuietWrites)
			all := mustQuiesce(t, api, httpwire.QuietAll)
			read := start(api, readReq())
			began := time.Now()
			time.Sleep(time.Second)
			all()
			if rec := <-read; rec.Code != http.StatusOK || time.Since(began) != time.Second {
				t.Errorf("a held read answered %d after %v, want 200 at QuietAll's resume, 1 s in", rec.Code, time.Since(began))
			}
			if rec := answer(api, writeReq()); !isMaintenance(t, rec) {
				t.Errorf("a write under the remaining QuietWrites answered %d %s, want maintenance", rec.Code, rec.Body)
			}
			writes()
			if rec := answer(api, writeReq()); rec.Code != http.StatusNoContent {
				t.Errorf("a write after both resumes answered %d %s, want 204", rec.Code, rec.Body)
			}
		})
	})
}

func TestResumeIsIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newQuietCase(t)
		close(q.write.release)
		api := q.build(t)
		first, second := mustQuiesce(t, api, httpwire.QuietWrites), mustQuiesce(t, api, httpwire.QuietWrites)
		first()
		first()
		if rec := answer(api, writeReq()); !isMaintenance(t, rec) {
			t.Errorf("a write under the second QuietWrites answered %d %s, want maintenance", rec.Code, rec.Body)
		}
		second()
		allFirst, allSecond := mustQuiesce(t, api, httpwire.QuietAll), mustQuiesce(t, api, httpwire.QuietAll)
		allFirst()
		allFirst()
		if rec := answer(api, readReq()); !isMaintenance(t, rec) {
			t.Errorf("a read under the second QuietAll answered %d %s, want maintenance", rec.Code, rec.Body)
		}
		allSecond()
		if rec := answer(api, writeReq()); rec.Code != http.StatusNoContent {
			t.Errorf("a write after every resume answered %d %s, want 204", rec.Code, rec.Body)
		}
	})
}

func TestQuietAllCancelsRequestsRunningTenSecondsIn(t *testing.T) {
	t.Run("a long wait and a write still running are cancelled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			q := newQuietCase(t)
			api := q.build(t)
			held, write := start(api, request(http.MethodGet, "/api/events/wait", "")), start(api, writeReq())
			heldCtx, writeCtx := <-q.held.entered, <-q.write.entered
			began := time.Now()
			done := startQuiesce(t.Context(), api, httpwire.QuietAll)
			time.Sleep(10*time.Second - time.Nanosecond)
			if _, ok := ready(done); ok || heldCtx.Err() != nil || writeCtx.Err() != nil {
				t.Fatalf("before 10 s: returned %v, long wait cancelled %v, write cancelled %v", ok, heldCtx.Err(), writeCtx.Err())
			}
			got := <-done
			if got.err != nil || time.Since(began) != 10*time.Second {
				t.Fatalf("Quiesce returned after %v with error %v, want nil 10 s in", time.Since(began), got.err)
			}
			if !errors.Is(heldCtx.Err(), context.Canceled) || !errors.Is(writeCtx.Err(), context.Canceled) {
				t.Errorf("long wait %v, write %v; want both cancelled", heldCtx.Err(), writeCtx.Err())
			}
			for name, ch := range map[string]<-chan *httptest.ResponseRecorder{"long wait": held, "write": write} {
				if rec := <-ch; !isMaintenance(t, rec) {
					t.Errorf("the cancelled %s answered %d %s, want maintenance", name, rec.Code, rec.Body)
				}
			}
			got.resume()
		})
	})
	t.Run("a request returning inside 10 s is not cancelled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			q := newQuietCase(t)
			api := q.build(t)
			held := start(api, request(http.MethodGet, "/api/events/wait", ""))
			<-q.held.entered
			began := time.Now()
			done := startQuiesce(t.Context(), api, httpwire.QuietAll)
			time.Sleep(3 * time.Second)
			close(q.held.release)
			got := <-done
			if got.err != nil || time.Since(began) != 3*time.Second {
				t.Errorf("Quiesce returned after %v with error %v, want nil at the long wait's return, 3 s in", time.Since(began), got.err)
			}
			if rec := <-held; rec.Code != http.StatusOK {
				t.Errorf("the long wait answered %d %s, want its own 200", rec.Code, rec.Body)
			}
			got.resume()
		})
	})
	t.Run("an open stream is neither waited for nor cancelled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			q := newQuietCase(t)
			api := q.build(t)
			stream := start(api, request(http.MethodGet, "/api/stream", ""))
			streamCtx := <-q.stream.entered
			got, ok := ready(startQuiesce(t.Context(), api, httpwire.QuietAll))
			if !ok || got.err != nil {
				t.Fatalf("Quiesce with only a stream open: returned %v, error %v", ok, got.err)
			}
			time.Sleep(time.Minute)
			if streamCtx.Err() != nil {
				t.Error("QuietAll cancelled the open stream")
			}
			close(q.stream.release)
			<-stream
			got.resume()
		})
	})
}

func TestQuiesceEndedContextHoldsNothing(t *testing.T) {
	for name, quiet := range quietLevels {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := newQuietCase(t)
				api := q.build(t)
				raw := start(api, request(http.MethodPost, "/api/archive", "PK"))
				rawCtx := <-q.rawWrite.entered
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				resume, err := api.Quiesce(ctx, quiet)
				if !errors.Is(err, context.DeadlineExceeded) || resume != nil {
					t.Fatalf("Quiesce under an ended context: resume %v, error %v; want DeadlineExceeded and no resume", resume != nil, err)
				}
				close(q.write.release)
				close(q.read.release)
				began := time.Now()
				if rec := answer(api, writeReq()); rec.Code != http.StatusNoContent {
					t.Errorf("a write after the failed Quiesce answered %d %s, want 204", rec.Code, rec.Body)
				}
				if rec := answer(api, readReq()); rec.Code != http.StatusOK || time.Since(began) != 0 {
					t.Errorf("a read after the failed Quiesce answered %d after %v, want 200 at once", rec.Code, time.Since(began))
				}
				time.Sleep(time.Minute)
				if rawCtx.Err() != nil {
					t.Error("the failed Quiesce cancelled the write in flight")
				}
				close(q.rawWrite.release)
				<-raw
			})
		})
	}
}

// jobRunner is an app's runner of jobs that outlive the request starting
// them. It meets the Quiescer contract by cancelling its jobs at QuietAll
// and returning once each has returned.
type jobRunner struct {
	cancels []context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
}

func (j *jobRunner) start(ctx context.Context, job func(context.Context)) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	j.mu.Lock()
	j.cancels = append(j.cancels, cancel)
	j.mu.Unlock()
	j.wg.Go(func() {
		defer cancel()
		job(ctx)
	})
}

func (j *jobRunner) Quiesce(ctx context.Context, q httpwire.Quiet) (func(), error) {
	if q != httpwire.QuietAll {
		return func() {}, nil
	}
	j.mu.Lock()
	for _, cancel := range j.cancels {
		cancel()
	}
	j.mu.Unlock()
	done := make(chan struct{})
	go func() { j.wg.Wait(); close(done) }()
	select {
	case <-done:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestQuietAllWithAJobRunnerAfterTheAPI(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newQuietCase(t)
		runner := &jobRunner{}
		jobEnded := make(chan time.Time, 1)
		q.binds["settings.save"] = httpwire.Bind("settings.save", func(ctx context.Context, _ httpwire.Call[noP, noQ, otherBody]) (httpwire.None, error) {
			runner.start(ctx, func(ctx context.Context) {
				<-ctx.Done()
				time.Sleep(2 * time.Second)
				jobEnded <- time.Now()
			})
			return httpwire.None{}, nil
		})
		api := q.build(t)
		if rec := answer(api, writeReq()); rec.Code != http.StatusNoContent {
			t.Fatalf("the job start answered %d %s", rec.Code, rec.Body)
		}
		began := time.Now()
		for _, qr := range []httpwire.Quiescer{api, runner} {
			resume, err := qr.Quiesce(t.Context(), httpwire.QuietAll)
			if err != nil {
				t.Fatalf("Quiesce: %v", err)
			}
			defer resume()
			if qr == httpwire.Quiescer(api) && time.Since(began) != 0 {
				t.Errorf("the API waited %v for a job that outlived its request", time.Since(began))
			}
		}
		if _, ok := ready((<-chan time.Time)(jobEnded)); !ok || time.Since(began) != 2*time.Second {
			t.Errorf("job ended %v, the list returned %v in; want the runner to return when its job did, 2 s in", ok, time.Since(began))
		}
	})
}
