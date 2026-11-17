package httpwire_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

func TestJSONRowAnswersItsPrimaryStatus(t *testing.T) {
	api := newCase(t).mustBuild(t)
	for name, tc := range map[string]struct {
		method, path, body string
		status             int
		want               string
	}{
		"a 200 read":                  {http.MethodGet, "/api/jobs", "", http.StatusOK, "[{\"id\":\"a\"}]\n"},
		"a declared 202":              {http.MethodPost, "/api/jobs", `{"kind":"x"}`, http.StatusAccepted, "{\"id\":\"new\"}\n"},
		"a row with no Response, 204": {http.MethodPatch, "/api/settings", `{"name":"x"}`, http.StatusNoContent, ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := serve(api, tc.method, tc.path, tc.body)
			if rec.Code != tc.status || rec.Body.String() != tc.want {
				t.Errorf("answer = %d %q, want %d %q", rec.Code, rec.Body.String(), tc.status, tc.want)
			}
			if tc.want != "" && rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// statusAPI binds events.wait (200 with a nudge, or 204) to answer s.
func statusAPI(t *testing.T, s httpwire.Status[nudge]) (*httpwire.API, *logSink) {
	t.Helper()
	c := newCase(t)
	sink := &logSink{}
	c.opts["logger"] = httpwire.WithLogger(slog.New(sink))
	c.binds["events.wait"] = httpwire.Bind("events.wait", func(context.Context, httpwire.Call[noP, noQ, noB]) (httpwire.Status[nudge], error) {
		return s, nil
	})
	return c.mustBuild(t), sink
}

func TestStatusAnswersADeclaredStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		answer httpwire.Status[nudge]
		status int
		want   string
	}{
		"the primary with its body":    {httpwire.Answer(http.StatusOK, nudge{At: 7}), http.StatusOK, "{\"at\":7}\n"},
		"a zero Code is the primary":   {httpwire.Status[nudge]{Body: nudge{At: 8}}, http.StatusOK, "{\"at\":8}\n"},
		"a bodiless extra writes none": {httpwire.Answer(http.StatusNoContent, nudge{At: 9}), http.StatusNoContent, ""},
	} {
		t.Run(name, func(t *testing.T) {
			api, sink := statusAPI(t, tc.answer)
			rec := serve(api, http.MethodGet, "/api/events/wait", "")
			if rec.Code != tc.status || rec.Body.String() != tc.want {
				t.Errorf("answer = %d %q, want %d %q", rec.Code, rec.Body.String(), tc.status, tc.want)
			}
			if lines := sink.all(); len(lines) != 0 {
				t.Errorf("a declared status logged %v", lines)
			}
		})
	}
}

func TestUndeclaredSuccessStatusIsInternalError(t *testing.T) {
	for _, code := range []int{http.StatusCreated, http.StatusAccepted, http.StatusNotFound, 99} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			api, sink := statusAPI(t, httpwire.Answer(code, nudge{At: 7}))
			rec := serve(api, http.MethodGet, "/api/events/wait", "")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if env := envelopeOf(t, rec); env.Code != "internal_error" || env.Message != "internal error" {
				t.Errorf("envelope = %+v, want internal_error", env)
			}
			if strings.Contains(rec.Body.String(), `"at"`) {
				t.Errorf("the body reached the wire: %q", rec.Body.String())
			}
			if !sink.has("ERROR", "row=events.wait", fmt.Sprintf("status=%d", code)) {
				t.Errorf("no ERROR line naming the row and the status: %v", sink.all())
			}
		})
	}
}

var (
	errJobsDown = errors.New("jobs down")
	errUnmapped = errors.New("disk read /var/lib/app/secret.db failed")
)

// errorAPI binds jobs.get to answer err, with a second owner whose code is
// a 503.
func errorAPI(t *testing.T, err error) (*httpwire.API, *logSink) {
	t.Helper()
	c := newCase(t)
	if e := c.tb.Catalog.Add("jobs_down", httpserver.CodeSpec{Status: http.StatusServiceUnavailable, Remediation: "Retry later."}); e != nil {
		t.Fatal(e)
	}
	down := &httpwire.Owner{
		Name:      "runner",
		Codes:     []httpwire.OwnerCode{{Err: errJobsDown, Code: "jobs_down"}},
		Sentinels: map[string]error{"ErrJobsDown": errJobsDown},
	}
	ep := row(c.tb, "jobs.get")
	ep.Owners = append(ep.Owners, down)
	sink := &logSink{}
	c.opts["logger"] = httpwire.WithLogger(slog.New(sink))
	c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB]) (job, error) {
		return job{ID: "x"}, err
	})
	return c.mustBuild(t), sink
}

func TestMapErrorAnswersHandlerErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err           error
		status        int
		code, message string
	}{
		"an Error in the union":         {&httpwire.Error{Code: "job_missing", Message: "no job abc"}, http.StatusNotFound, "job_missing", "no job abc"},
		"a wrapped Error":               {fmt.Errorf("load: %w", &httpwire.Error{Code: "job_missing", Message: "gone"}), http.StatusNotFound, "job_missing", "gone"},
		"a sentinel, with its own text": {fmt.Errorf("read /var/lib/app/jobs/abc: %w", errJobMissing), http.StatusNotFound, "job_missing", "job missing"},
		"a second sentinel on one code": {errRunMissing, http.StatusNotFound, "job_missing", "run missing"},
		"a later owner's sentinel":      {fmt.Errorf("run: %w", errJobsDown), http.StatusServiceUnavailable, "jobs_down", "jobs down"},
	} {
		t.Run(name, func(t *testing.T) {
			api, _ := errorAPI(t, tc.err)
			rec := serve(api, http.MethodGet, "/api/jobs/abc", "")
			env := envelopeOf(t, rec)
			if rec.Code != tc.status || env.Code != tc.code || env.Message != tc.message {
				t.Errorf("answer = %d %s %q, want %d %s %q", rec.Code, env.Code, env.Message, tc.status, tc.code, tc.message)
			}
			if strings.Contains(rec.Body.String(), "/var/lib") {
				t.Errorf("the wrapped text reached the wire: %q", rec.Body.String())
			}
		})
	}
}

func TestMapErrorLogsA5xxOwnerCodeAtWarn(t *testing.T) {
	api, sink := errorAPI(t, errJobsDown)
	serve(api, http.MethodGet, "/api/jobs/abc", "")
	if !sink.has("WARN", "row=jobs.get", "code=jobs_down") {
		t.Errorf("no WARN line naming the row and the code: %v", sink.all())
	}
	api, sink = errorAPI(t, errJobMissing)
	serve(api, http.MethodGet, "/api/jobs/abc", "")
	if lines := sink.all(); len(lines) != 0 {
		t.Errorf("a 4xx owner code logged %v", lines)
	}
}

func TestMapErrorAnswersInternalErrorForAWiringBug(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want []string
	}{
		"an Error outside the union":    {&httpwire.Error{Code: "job_busy", Message: "busy"}, []string{"row=jobs.get", "code=job_busy"}},
		"an Error with an empty code":   {&httpwire.Error{Message: "what"}, []string{"row=jobs.get"}},
		"an error matching no owner":    {errUnmapped, []string{"row=jobs.get", "secret.db"}},
		"a nil Error pointer":           {(*httpwire.Error)(nil), []string{"row=jobs.get"}},
		"an owner sentinel kept inside": {errLocked, []string{"row=jobs.get"}},
	} {
		t.Run(name, func(t *testing.T) {
			api, sink := errorAPI(t, tc.err)
			rec := serve(api, http.MethodGet, "/api/jobs/abc", "")
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusInternalServerError || env.Code != "internal_error" || env.Message != "internal error" {
				t.Errorf("answer = %d %s %q, want 500 internal_error", rec.Code, env.Code, env.Message)
			}
			if strings.Contains(rec.Body.String(), "secret.db") || strings.Contains(rec.Body.String(), "busy") {
				t.Errorf("the error's text reached the wire: %q", rec.Body.String())
			}
			if !sink.has("ERROR", tc.want...) {
				t.Errorf("no ERROR line holding %v: %v", tc.want, sink.all())
			}
		})
	}
}

func TestMapErrorLogsALeftClientAtDebug(t *testing.T) {
	api, sink := errorAPI(t, context.Canceled)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/jobs/abc", nil))
	if !sink.has("DEBUG", "row=jobs.get") {
		t.Errorf("no DEBUG line for a client that left: %v", sink.all())
	}
	if sink.has("ERROR") || sink.has("WARN") {
		t.Errorf("a client that left logged above DEBUG: %v", sink.all())
	}
}

func TestBinderLinesGoToTheDefaultLoggerWhenWritten(t *testing.T) {
	c := newCase(t)
	c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB]) (job, error) {
		return job{}, errUnmapped
	})
	api := c.mustBuild(t)
	sink := &logSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(prev) })
	serve(api, http.MethodGet, "/api/jobs/abc", "")
	if !sink.has("ERROR", "row=jobs.get") {
		t.Errorf("a default logger set after New heard nothing: %v", sink.all())
	}
}

// wrappedStatus embeds a Status: it is a plain body, never a status pick.
type wrappedStatus struct {
	httpwire.Status[nudge]
}

func TestATypeEmbeddingStatusIsAPlainBody(t *testing.T) {
	c := newCase(t)
	row(c.tb, "jobs.get").Response = reflect.TypeFor[wrappedStatus]()
	c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB]) (wrappedStatus, error) {
		return wrappedStatus{httpwire.Answer(http.StatusCreated, nudge{At: 3})}, nil
	})
	rec := serve(c.mustBuild(t), http.MethodGet, "/api/jobs/abc", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"Body\":{\"at\":3},\"Code\":201}\n" {
		t.Errorf("answer = %d %q, want the primary 200 with the whole value", rec.Code, rec.Body.String())
	}
}
