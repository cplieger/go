package httpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/cplieger/go/httpclient"
)

func TestTheValidatorTheServerSentIsReplayed(t *testing.T) {
	for _, test := range []struct {
		name, etag, modified, header, want string
	}{
		{name: "an_etag", etag: `W/"v1"`, header: "If-None-Match", want: `W/"v1"`},
		{name: "a_last_modified_alone", modified: "Wed, 25 Oct 2023 19:17:59 GMT", header: "If-Modified-Since", want: "Wed, 25 Oct 2023 19:17:59 GMT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo","ignored":"x"}`, test.etag)
			o.modified = test.modified
			c := newOwner(serveOrigin(t, o), ownerConfig{})
			first, _, err := c.get(t, "/user")
			if err != nil {
				t.Fatalf("the first read = %v, want nil", err)
			}
			second, answer, err := c.get(t, "/user")
			if err != nil {
				t.Fatalf("the revalidated read = %v, want nil", err)
			}
			if got := o.last().Get(test.header); got != test.want {
				t.Errorf("the second read sent %s %q, want %q: RFC 9110 section 13.1 replays the validator the server sent", test.header, got, test.want)
			}
			if second != first || !answer.served || answer.status != http.StatusOK {
				t.Errorf("the revalidated read = %+v, served %v, status %d, want %+v served as a 200", second, answer.served, answer.status, first)
			}
		})
	}
}

func TestAReadNotStoredCarriesNoValidator(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	if _, _, err := c.get(t, "/user"); err != nil {
		t.Fatalf("Setup: the stored read = %v", err)
	}
	for i := range 2 {
		answer, err := c.do(t.Context(), ownerRead{path: "/user", unstored: true})
		if err != nil {
			t.Fatalf("unstored read %d = %v", i, err)
		}
		if got := o.last().Get("If-None-Match"); got != "" || answer.answered {
			t.Errorf("unstored read %d carried If-None-Match %q, answered %v, want no validator and nothing counted", i, got, answer.answered)
		}
	}
	if got := c.cache.State(); got.Hits != 0 || got.Misses != 1 || got.Bytes == 0 {
		t.Errorf("State = %+v, want only the stored read counted and its answer still held", got)
	}
}

func TestEveryHeaderThatVariesTheAnswerIsKeyed(t *testing.T) {
	t.Run("a_rotated_token", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		var token atomic.Value
		token.Store("first-token")
		c := newOwner(serveOrigin(t, o), ownerConfig{token: func(context.Context) (string, error) { return token.Load().(string), nil }})
		if _, _, err := c.get(t, "/user"); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		token.Store("second-token")
		if _, _, err := c.get(t, "/user"); err != nil {
			t.Fatalf("the read under the new token = %v", err)
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("the read under another token carried If-None-Match %q, want none: an answer is never revalidated under another credential", got)
		}
	})
	t.Run("two_caches", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		url := serveOrigin(t, o)
		if _, _, err := newOwner(url, ownerConfig{}).get(t, "/user"); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if _, _, err := newOwner(url, ownerConfig{}).get(t, "/user"); err != nil {
			t.Fatalf("the second cache's read = %v", err)
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("another cache's read carried If-None-Match %q, want none: two caches never share entries", got)
		}
	})
	for _, header := range []string{"Accept", "X-Api-Version"} {
		t.Run("another_"+strings.ToLower(header), func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			c := newOwner(serveOrigin(t, o), ownerConfig{})
			read := func(value string) {
				t.Helper()
				if _, err := c.do(t.Context(), ownerRead{path: "/user", header: http.Header{header: {value}}}); err != nil {
					t.Fatalf("the read with %s %q = %v", header, value, err)
				}
			}
			read("one")
			read("two")
			if got := o.last().Get("If-None-Match"); got != "" {
				t.Errorf("a read under another %s carried If-None-Match %q, want none: the header varies the representation", header, got)
			}
			read("two")
			if got := o.last().Get("If-None-Match"); got != `"v1"` {
				t.Errorf("a repeat under the same %s carried If-None-Match %q, want the held validator", header, got)
			}
		})
	}
}

// profile is a second Go type the same body decodes into.
type profile account

func TestReadsOfAnotherVariantNeverServeEachOther(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	if _, err := c.do(t.Context(), ownerRead{path: "/user", into: &account{}}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, err := c.do(t.Context(), ownerRead{path: "/user", into: &profile{}}); err != nil {
		t.Fatalf("the read of another variant = %v", err)
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("a read of another variant carried If-None-Match %q, want none: what one variant kept never serves another", got)
	}
	answer, err := c.do(t.Context(), ownerRead{path: "/user", into: &profile{}})
	if err != nil || !answer.served {
		t.Errorf("a repeat of the same variant = served %v, %v, want it served from its own entry", answer.served, err)
	}
}

func TestOnlyAWellFormedValidatorIsReplayed(t *testing.T) {
	const date = "Wed, 25 Oct 2023 19:17:59 GMT"
	for _, test := range []struct {
		name, etag, modified string
		wantTag, wantDate    string
	}{
		{name: "a_strong_etag", etag: `"v1"`, wantTag: `"v1"`},
		{name: "a_weak_etag", etag: `W/"v1"`, wantTag: `W/"v1"`},
		{name: "an_empty_etag", etag: `""`, wantTag: `""`},
		{name: "a_wildcard", etag: `*`},
		{name: "a_list", etag: `"v1", "v2"`},
		{name: "an_unquoted_tag", etag: `v1`},
		{name: "trailing_data", etag: `"v1"x`},
		{name: "a_lowercase_weak_prefix", etag: `w/"v1"`},
		{name: "a_bad_etag_beside_a_date", etag: `*`, modified: date, wantDate: date},
		{name: "a_date", modified: date, wantDate: date},
		{name: "not_a_date", modified: "yesterday"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, test.etag)
			o.modified = test.modified
			c := newOwner(serveOrigin(t, o), ownerConfig{})
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatalf("Setup: the first read = %v", err)
			}
			o.set(func(o *origin) { o.body = `{"login":"new"}` })
			got, _, err := c.get(t, "/user")
			if err != nil {
				t.Fatal(err)
			}
			sent := o.last()
			if tag := sent.Get("If-None-Match"); tag != test.wantTag {
				t.Errorf("the second read sent If-None-Match %q, want %q", tag, test.wantTag)
			}
			if date := sent.Get("If-Modified-Since"); date != test.wantDate {
				t.Errorf("the second read sent If-Modified-Since %q, want %q", date, test.wantDate)
			}
			if test.wantTag == "" && test.wantDate == "" && got.Login != "new" {
				t.Errorf("the second read = %+v, want the server's new answer: nothing valid was held to confirm", got)
			}
		})
	}
}

func TestAnOverlongValidatorIsNotReplayed(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"`+strings.Repeat("v", 2<<10)+`"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	for range 2 {
		if _, _, err := c.get(t, "/user"); err != nil {
			t.Fatal(err)
		}
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("a 2 KiB validator was replayed, want it refused")
	}
}

func TestAnAnswerWithNoValidatorIsNotKept(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, "")
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	for range 2 {
		if _, _, err := c.get(t, "/user"); err != nil {
			t.Fatalf("a read = %v, want nil", err)
		}
	}
	if got := o.last().Get("If-None-Match") + o.last().Get("If-Modified-Since"); got != "" {
		t.Errorf("the second read carried validator %q, want none: the server sent none to replay", got)
	}
	if got := c.cache.State().Bytes; got != 0 {
		t.Errorf("State.Bytes = %d, want 0: nothing revalidatable was answered", got)
	}
}

// RFC 9111 section 5.2.2.5: no-store on a 304 withdraws permission to keep the
// response, so the held answer serves this read and is then forgotten.
func TestANotModifiedForbiddingStorageForgetsTheHeldAnswer(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	if _, _, err := c.get(t, "/user"); err != nil {
		t.Fatal(err)
	}
	o.set(func(o *origin) { o.header.Set("Cache-Control", "no-store") })
	got, answer, err := c.get(t, "/user")
	if err != nil || got.Login != "octo" || !answer.served {
		t.Fatalf("the not-modified read = %+v, served %v, %v, want the held answer served", got, answer.served, err)
	}
	if held := c.cache.State().Bytes; held != 0 {
		t.Errorf("after a no-store 304 the cache holds %d bytes, want none", held)
	}
	if _, _, err := c.get(t, "/user"); err != nil {
		t.Fatal(err)
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("the read after a no-store 304 carried If-None-Match %q, want none", got)
	}
}

// TestEveryOwnerReadEndsInOneOutcome holds every way a stored read can end to one
// of two final states: the answer the read vouched for is kept, or the key is
// forgotten. Each case starts from an answer the cache holds, then changes the
// server, or switches on an observer, a cut connection or a failing resend,
// before the second read.
func TestEveryOwnerReadEndsInOneOutcome(t *testing.T) {
	type setup struct {
		origin        *origin
		third         func(context.Context) (string, error)
		req           ownerRead
		abort         atomic.Bool
		changeRequest atomic.Bool
		changeAnswer  atomic.Bool
	}
	// resend makes the second read's 304 name another representation, so the read
	// is sent once more.
	resend := func(s *setup) { s.origin.etag, s.origin.notModified = `"v2"`, 1 }
	for _, test := range []struct {
		change  func(*setup)
		name    string
		timeout time.Duration
		kept    bool
	}{
		{name: "a_200_with_a_validator", kept: true, change: func(s *setup) {
			s.origin.body, s.origin.etag = `{"login":"new"}`, `"v2"`
		}},
		{name: "a_200_without_a_validator", change: func(s *setup) {
			s.origin.body, s.origin.etag = `{"login":"new"}`, ""
		}},
		{name: "a_200_forbidding_storage", change: func(s *setup) {
			s.origin.body, s.origin.etag = `{"login":"new"}`, `"v2"`
			s.origin.header.Set("Cache-Control", "no-store")
		}},
		{name: "a_304_to_the_held_answer", kept: true, change: func(*setup) {}},
		{name: "a_304_naming_a_new_validator", kept: true, change: func(s *setup) {
			s.origin.etag, s.origin.notModified = `"v2"`, 1
		}},
		{name: "a_304_forbidding_storage", change: func(s *setup) {
			s.origin.header.Set("Cache-Control", "no-store")
		}},
		{name: "a_404", change: func(s *setup) { s.origin.status = http.StatusNotFound }},
		{name: "a_500", change: func(s *setup) { s.origin.status = http.StatusInternalServerError }},
		{name: "an_unreadable_body", change: func(s *setup) {
			s.origin.body, s.origin.etag = `{"login":"`+strings.Repeat("x", 4096)+`"}`, `"v2"`
			s.req.maxBytes = 64
		}},
		{name: "an_undecodable_body", change: func(s *setup) {
			s.origin.body, s.origin.etag = `not json`, `"v2"`
		}},
		{name: "an_answer_an_observer_changed", change: func(s *setup) { s.changeAnswer.Store(true) }},
		{name: "a_request_an_observer_changed", change: func(s *setup) { s.changeRequest.Store(true) }},
		{name: "a_transport_failure", change: func(s *setup) { s.abort.Store(true) }},
		{name: "a_resend_answered_304_again", change: func(s *setup) {
			s.origin.etag, s.origin.notModified = `"v2"`, 2
		}},
		{name: "a_resend_whose_credential_fails", change: func(s *setup) {
			resend(s)
			s.third = func(context.Context) (string, error) { return "", errors.New("refresh failed") }
		}},
		{name: "a_resend_past_the_deadline", timeout: 200 * time.Millisecond, change: func(s *setup) {
			resend(s)
			s.third = func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			}
		}},
		{name: "a_resend_under_a_rotated_token", change: func(s *setup) {
			resend(s)
			s.third = func(context.Context) (string, error) { return "rotated-token", nil }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &setup{origin: newOrigin(`{"login":"octo"}`, `"v1"`), req: ownerRead{path: "/user"}}
			url := serveOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if s.abort.Load() {
					panic(http.ErrAbortHandler)
				}
				s.origin.ServeHTTP(w, r)
			}))
			var calls atomic.Int32
			token := func(ctx context.Context) (string, error) {
				if calls.Add(1) == 3 && s.third != nil {
					return s.third(ctx)
				}
				return "test-token", nil
			}
			c := newOwner(url, ownerConfig{
				token: token,
				request: requestRewrite(func(r *http.Request) {
					if s.changeRequest.Load() {
						r.Header.Set("X-Trace", "1")
					}
				}),
				attempt: answerRewrite(func(resp *http.Response) {
					if s.changeAnswer.Load() {
						resp.Header.Set("X-Forged", "1")
					}
				}),
			})
			if _, _, err := c.get(t, "/user"); err != nil || c.cache.State().Bytes == 0 {
				t.Fatalf("Setup: the first read = %v, holding %d bytes, want its answer kept", err, c.cache.State().Bytes)
			}
			s.origin.set(func(*origin) { test.change(s) })
			ctx := t.Context()
			if test.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.timeout)
				defer cancel()
			}
			_, _ = c.do(ctx, s.req)
			held := c.cache.State().Bytes
			switch {
			case test.kept && held == 0:
				t.Errorf("the cache holds nothing after the second read, want the vouched answer kept")
			case !test.kept && held != 0:
				t.Errorf("the cache holds %d bytes after the second read, want the key forgotten", held)
			}
		})
	}
}

// RFC 9111 section 4.3.4: a 304 whose validator names another representation must
// not update or serve the held one. It is read again in full, and that answer is
// what later 304s confirm.
func TestAnOwnerReadOfAnotherRepresentationIsReadInFull(t *testing.T) {
	const (
		dayOne = "Wed, 25 Oct 2023 19:17:59 GMT"
		dayTwo = "Thu, 26 Oct 2023 08:00:00 GMT"
	)
	for _, test := range []struct {
		change func(*origin)
		name   string
		header string
		want   string
	}{
		{name: "another_etag", header: "If-None-Match", want: `"v2"`, change: func(o *origin) { o.etag = `"v2"` }},
		{name: "another_last_modified", header: "If-Modified-Since", want: dayTwo, change: func(o *origin) { o.modified = dayTwo }},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, "")
			if test.header == "If-None-Match" {
				o.etag = `"v1"`
			} else {
				o.modified = dayOne
			}
			c := newOwner(serveOrigin(t, o), ownerConfig{})
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatalf("Setup: the first read = %v", err)
			}
			o.set(func(o *origin) {
				o.body, o.notModified = `{"login":"new"}`, 1
				test.change(o)
			})
			if got, _, err := c.get(t, "/user"); err != nil || got.Login != "new" {
				t.Errorf("a read answered 304 naming another representation = %+v, %v, want the server's new answer", got, err)
			}
			got, _, err := c.get(t, "/user")
			if err != nil || got.Login != "new" {
				t.Errorf("the next read, confirmed by a 304 = %+v, %v, want the new answer, not the one held before", got, err)
			}
			if sent := o.last().Get(test.header); sent != test.want {
				t.Errorf("the next read sent %s %q, want %q", test.header, sent, test.want)
			}
		})
	}
}

// A 304 to a read that carried no validator is sent once more without one,
// whether or not the read is stored, and an answer the cache may not hold is not
// kept.
func TestANotModifiedToAnUnconditionalOwnerReadIsSentOnceMore(t *testing.T) {
	for _, test := range []struct {
		name  string
		req   ownerRead
		cache httpclient.CacheConfig
		kept  bool
	}{
		{name: "a_stored_read", kept: true},
		{name: "a_read_forbidding_storage", req: ownerRead{header: http.Header{"Cache-Control": {"no-store"}}}},
		{name: "the_cache_switched_off", cache: httpclient.CacheConfig{MaxBytes: -1}},
		{name: "a_read_not_stored", req: ownerRead{unstored: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			o.notModified = 1
			c := newOwner(serveOrigin(t, o), ownerConfig{cache: test.cache})
			var got account
			req := test.req
			req.path, req.into = "/user", &got
			if _, err := c.do(t.Context(), req); err != nil {
				t.Fatalf("a read answered 304 then 200 = %v, want nil", err)
			}
			if got.Login != "octo" {
				t.Errorf("the read = %+v, want the body the resend answered", got)
			}
			if n := len(o.requests()); n != 2 {
				t.Errorf("the server saw %d request(s), want 2: a 304 to a read with no validator is resent once without one", n)
			}
			if held := c.cache.State().Bytes; test.kept != (held > 0) {
				t.Errorf("the cache holds %d bytes after the resend, want kept=%v", held, test.kept)
			}
		})
	}
	t.Run("the_resend_answers_304_again", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, "")
		o.notModified = 5
		c := newOwner(serveOrigin(t, o), ownerConfig{})
		_, answer, err := c.get(t, "/user")
		if !errors.Is(err, errStatus) || answer.status != http.StatusNotModified {
			t.Errorf("a read answered 304 twice = status %d, %v, want the 304 itself reaching the owner", answer.status, err)
		}
		if n := len(o.requests()); n != 2 {
			t.Errorf("the server saw %d request(s), want exactly 2", n)
		}
	})
}

func TestAReadThatMayNotResendGetsItsNotModified(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, "")
	o.notModified = 1
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	answer, err := c.do(t.Context(), ownerRead{path: "/user", noResend: true})
	if !errors.Is(err, errStatus) || answer.status != http.StatusNotModified || answer.served {
		t.Errorf("a read that may not resend, answered 304 = status %d, served %v, %v, want the 304 itself", answer.status, answer.served, err)
	}
	if n := len(o.requests()); n != 1 {
		t.Errorf("the server saw %d request(s), want 1: a read that may not resend is sent once", n)
	}
}

// unreadable304 is an observer that makes every 304's body fail to read.
var unreadable304 = answerRewrite(func(resp *http.Response) {
	if resp.StatusCode == http.StatusNotModified {
		resp.Body = io.NopCloser(iotest.ErrReader(errors.New("connection reset")))
	}
})

func TestANotModifiedWhoseBodyFailsEndsTheRead(t *testing.T) {
	var sent atomic.Int32
	url := serveOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		w.Header().Set("X-Remaining", "17")
		w.WriteHeader(http.StatusNotModified)
	}))
	c := newOwner(url, ownerConfig{attempt: unreadable304})
	_, err := c.do(t.Context(), ownerRead{path: "/user", noResend: true})
	if err == nil || errors.Is(err, errStatus) {
		t.Errorf("a read whose 304 body fails to read = %v, want the read's failure, not the 304", err)
	}
	if n := sent.Load(); n != 1 {
		t.Errorf("the server saw %d request(s), want 1", n)
	}
	if got := c.dropped(); len(got) != 1 || got[0].Get("X-Remaining") != "17" {
		t.Errorf("Discarded took %v, want the failed 304's header", got)
	}
}

func TestADroppedNotModifiedReachesDiscarded(t *testing.T) {
	serve := func(t *testing.T, resendFails bool) string {
		var sent atomic.Int32
		return serveOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if sent.Add(1) == 1 {
				w.Header().Set("X-Remaining", "41")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if resendFails {
				panic(http.ErrAbortHandler)
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = io.WriteString(w, `{"login":"octo"}`)
		}))
	}
	for _, test := range []struct {
		name        string
		req         ownerRead
		resendFails bool
	}{
		{name: "a_stored_read"},
		{name: "a_read_not_stored_whose_resend_fails", req: ownerRead{unstored: true}, resendFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newOwner(serve(t, test.resendFails), ownerConfig{})
			req := test.req
			req.path = "/user"
			if _, err := c.do(t.Context(), req); (err != nil) != test.resendFails {
				t.Fatalf("the read = %v, want failed=%v", err, test.resendFails)
			}
			if got := c.dropped(); len(got) != 1 || got[0].Get("X-Remaining") != "41" {
				t.Errorf("Discarded took %v, want the resent 304's header", got)
			}
		})
	}
	t.Run("a_stored_read_whose_forged_304_resend_fails", func(t *testing.T) {
		forged := answerRewrite(func(resp *http.Response) {
			if resp.StatusCode == http.StatusNotModified {
				resp.Header.Set("X-Remaining", "7")
			}
		})
		c := newOwner(serve(t, true), ownerConfig{attempt: forged})
		if _, err := c.do(t.Context(), ownerRead{path: "/user"}); err == nil {
			t.Fatal("the read whose resend fails = nil, want the resend's failure")
		}
		if got := c.dropped(); len(got) != 0 {
			t.Errorf("Discarded took %v, the header an observer wrote over the 304: a stored read reports only a header the wire vouches for", got)
		}
	})
}

// A required held field the 304 omits leaves the held copy unconfirmed, so the
// read is sent once more without a validator; an empty one holds nothing to
// require.
func TestARequiredFieldTheNotModifiedOmitsSendsTheReadOnceMore(t *testing.T) {
	for _, test := range []struct {
		first, then http.Header
		name        string
		requests    int
		served      bool
	}{
		{name: "a_required_field_the_304_omits", first: http.Header{"X-Total-Count": {"7"}}, then: http.Header{}, requests: 3},
		{name: "a_required_field_the_304_carries", first: http.Header{"X-Total-Count": {"7"}}, then: http.Header{"X-Total-Count": {"8"}}, requests: 2, served: true},
		{name: "an_empty_required_field_the_304_omits", first: http.Header{"X-Total-Count": {""}}, then: http.Header{}, requests: 2, served: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			o.header = test.first.Clone()
			c := newOwner(serveOrigin(t, o), ownerConfig{cache: httpclient.CacheConfig{Held: []httpclient.HeldField{{Name: "X-Total-Count", Required: true}}}})
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatal(err)
			}
			o.set(func(o *origin) { o.header = test.then.Clone() })
			got, answer, err := c.get(t, "/user")
			if err != nil || got.Login != "octo" {
				t.Fatalf("the second read = %+v, %v, want the account", got, err)
			}
			if answer.served != test.served {
				t.Errorf("the second read served=%v, want %v", answer.served, test.served)
			}
			if n := len(o.requests()); n != test.requests {
				t.Errorf("the server saw %d request(s), want %d", n, test.requests)
			}
		})
	}
}

func TestAHeldFieldNamedTwiceIsHeldOnce(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	o.header = http.Header{"Link": {"<page2>"}}
	held := []httpclient.HeldField{{Name: "link"}, {Name: "Link", Required: true}}
	c := newOwner(serveOrigin(t, o), ownerConfig{cache: httpclient.CacheConfig{Held: held}})
	if _, _, err := c.get(t, "/user"); err != nil {
		t.Fatal(err)
	}
	o.set(func(o *origin) { o.header = http.Header{} })
	_, answer, err := c.get(t, "/user")
	if err != nil {
		t.Fatal(err)
	}
	if answer.served || len(o.requests()) != 3 {
		t.Errorf("a 304 omitting a field named twice, once required = served %v over %d request(s), want it required and the read sent once more", answer.served, len(o.requests()))
	}
	if got := answer.header.Values("Link"); len(got) != 0 {
		t.Errorf("the resend's Link = %q, want none: the field is held once, never twice", got)
	}
}

// Hits and Misses count each stored read the server answered in full once: a hit
// where the held answer served it, a miss on every other outcome, an error status
// included. A read the wire never answered counts neither.
func TestEveryAnsweredReadCountsOnce(t *testing.T) {
	errCut := errors.New("connection cut")
	for _, test := range []struct {
		change func(*origin)
		name   string
		want   httpclient.CacheState
		abort  bool
	}{
		{name: "a_404", want: httpclient.CacheState{Misses: 2}, change: func(o *origin) { o.status = http.StatusNotFound }},
		{name: "a_500", want: httpclient.CacheState{Misses: 2}, change: func(o *origin) { o.status = http.StatusInternalServerError }},
		{name: "a_304_served", want: httpclient.CacheState{Hits: 1, Misses: 1}, change: func(*origin) {}},
		{name: "a_transport_failure", want: httpclient.CacheState{Misses: 1}, abort: true, change: func(*origin) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			var abort atomic.Bool
			cut := func(next http.RoundTripper) http.RoundTripper {
				return cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
					if abort.Load() {
						return nil, errCut
					}
					return next.RoundTrip(r)
				})
			}
			c := newOwner(serveOrigin(t, o), ownerConfig{attempt: cut})
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatalf("Setup: the first read = %v", err)
			}
			o.set(test.change)
			abort.Store(test.abort)
			_, _, _ = c.get(t, "/user")
			got := c.cache.State()
			got.Bytes = 0
			if got != test.want {
				t.Errorf("State = %+v, want %+v", got, test.want)
			}
		})
	}
}

// slowAccount is an account whose decode takes a while once slow is set.
type slowAccount struct {
	slow *atomic.Bool
	account
}

//deadset:ignore DS1103 -- encoding/json calls it through json.Unmarshaler when the read decodes into a *slowAccount.
func (a *slowAccount) UnmarshalJSON(b []byte) error {
	if a.slow.Load() {
		time.Sleep(300 * time.Millisecond)
	}
	return json.Unmarshal(b, &a.account)
}

// A held answer served on a 304 whose read then fails did not answer the read, so
// it counts as a miss, not a hit.
func TestAServedReadTheOwnerFailsIsNoHit(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	var slow atomic.Bool
	read := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		_, err := c.do(ctx, ownerRead{path: "/user", into: &slowAccount{slow: &slow}})
		return err
	}
	if err := read(); err != nil {
		t.Fatalf("Setup: the first read = %v", err)
	}
	slow.Store(true)
	if err := read(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Setup: a served read whose decode outlasts its bound = %v, want context.DeadlineExceeded", err)
	}
	if got := c.cache.State(); got.Hits != 0 || got.Misses != 2 || got.Bytes != 0 {
		t.Errorf("State = %+v, want Hits 0, Misses 2 and nothing held: the failed read was not answered from the cache", got)
	}
}

func TestAnsweredAndFinishReportWhatTheReadCounts(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	for i, want := range []struct{ answered, hit bool }{{answered: true}, {answered: true, hit: true}} {
		_, answer, err := c.get(t, "/user")
		if err != nil {
			t.Fatal(err)
		}
		if answer.answered != want.answered || answer.hit != want.hit {
			t.Errorf("read %d = answered %v, hit %v, want %v, %v", i, answer.answered, answer.hit, want.answered, want.hit)
		}
	}
	o.set(func(o *origin) { o.status = http.StatusNotFound })
	if _, answer, _ := c.get(t, "/user"); !answer.answered || answer.hit {
		t.Errorf("a 404 = answered %v, hit %v, want answered and no hit", answer.answered, answer.hit)
	}
	answer, err := c.do(t.Context(), ownerRead{path: "/user", unstored: true})
	if answer.answered || answer.hit {
		t.Errorf("a read not stored = answered %v, hit %v, %v, want neither", answer.answered, answer.hit, err)
	}
}

func TestTheCacheHoldsItsBound(t *testing.T) {
	const bound = 8 << 10
	big := `{"login":"` + strings.Repeat("a", 600) + `"}`
	bounded := ownerConfig{cache: httpclient.CacheConfig{MaxBytes: bound}}
	user := func(i int) string { return fmt.Sprintf("/user/%d", i) }
	t.Run("eviction_keeps_the_bound", func(t *testing.T) {
		o := newOrigin(big, `"v1"`)
		c := newOwner(serveOrigin(t, o), bounded)
		for i := range 40 {
			if _, _, err := c.get(t, user(i)); err != nil {
				t.Fatalf("read %d = %v", i, err)
			}
			if got := c.cache.State().Bytes; got > bound {
				t.Fatalf("after read %d, State.Bytes = %d, want at most %d", i, got, bound)
			}
		}
		if _, _, err := c.get(t, user(0)); err != nil {
			t.Fatal(err)
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("the least recently used entry was revalidated (%q), want it evicted", got)
		}
		if _, _, err := c.get(t, user(39)); err != nil {
			t.Fatal(err)
		}
		if got := o.last().Get("If-None-Match"); got != `"v1"` {
			t.Errorf("the most recent entry carried If-None-Match %q, want it held", got)
		}
	})
	t.Run("a_read_entry_outlives_one_left_unread", func(t *testing.T) {
		o := newOrigin(big, `"v1"`)
		c := newOwner(serveOrigin(t, o), bounded)
		if _, _, err := c.get(t, user(0)); err != nil {
			t.Fatal(err)
		}
		held := int(bound / c.cache.State().Bytes)
		for i := 1; i < held; i++ {
			if _, _, err := c.get(t, user(i)); err != nil {
				t.Fatalf("read %d = %v", i, err)
			}
		}
		for _, i := range []int{0, held, 0} {
			if _, _, err := c.get(t, user(i)); err != nil {
				t.Fatal(err)
			}
		}
		if got := o.last().Get("If-None-Match"); got != `"v1"` {
			t.Errorf("the entry read again before the eviction carried If-None-Match %q, want it held", got)
		}
		if _, _, err := c.get(t, user(1)); err != nil {
			t.Fatal(err)
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("the entry left unread longest was revalidated (%q), want it evicted", got)
		}
	})
	t.Run("an_oversized_entry_is_not_kept", func(t *testing.T) {
		o := newOrigin(`{"login":"`+strings.Repeat("a", 4<<10)+`"}`, `"v1"`)
		c := newOwner(serveOrigin(t, o), bounded)
		for range 2 {
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatal(err)
			}
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("an entry over a quarter of the bound was revalidated (%q), want it not kept", got)
		}
	})
	t.Run("a_negative_bound_switches_the_cache_off", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		c := newOwner(serveOrigin(t, o), ownerConfig{cache: httpclient.CacheConfig{MaxBytes: -1}})
		for range 2 {
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatal(err)
			}
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("MaxBytes -1 sent If-None-Match %q, want every read unconditional", got)
		}
		if got := c.cache.State(); got != (httpclient.CacheState{}) {
			t.Errorf("MaxBytes -1 reports %+v, want the zero state", got)
		}
	})
}

func TestClearEmptiesTheCache(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{})
	if _, _, err := c.get(t, "/user"); err != nil {
		t.Fatal(err)
	}
	c.cache.Clear()
	if got := c.cache.State().Bytes; got != 0 {
		t.Errorf("after Clear, State.Bytes = %d, want 0", got)
	}
	if _, _, err := c.get(t, "/user"); err != nil {
		t.Fatal(err)
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("the read after Clear carried If-None-Match %q, want none", got)
	}
}

// heldOrigin is an origin that, while hold is set, parks the next request it
// receives until release is closed, after signalling received.
type heldOrigin struct {
	*origin
	received chan struct{}
	release  chan struct{}
	hold     atomic.Bool
}

func newHeldOrigin(body, etag string) *heldOrigin {
	return &heldOrigin{origin: newOrigin(body, etag), received: make(chan struct{}, 1), release: make(chan struct{})}
}

func (h *heldOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.hold.CompareAndSwap(true, false) {
		h.received <- struct{}{}
		<-h.release
	}
	h.origin.ServeHTTP(w, r)
}

// readAcrossClear starts a read, clears the cache once the server has received
// it, then lets the server answer, and reports what the read returned.
func readAcrossClear(t *testing.T, c *owner, h *heldOrigin) (account, error) {
	t.Helper()
	h.hold.Store(true)
	type result struct {
		err error
		got account
	}
	done := make(chan result, 1)
	go func() {
		var got account
		_, err := c.do(t.Context(), ownerRead{path: "/user", into: &got})
		done <- result{got: got, err: err}
	}()
	<-h.received
	c.cache.Clear()
	close(h.release)
	r := <-done
	return r.got, r.err
}

func TestAReadInFlightAcrossClearKeepsNothing(t *testing.T) {
	h := newHeldOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, h), ownerConfig{})
	got, err := readAcrossClear(t, c, h)
	if err != nil || got.Login != "octo" {
		t.Fatalf("the read in flight across Clear = %+v, %v, want the server's answer", got, err)
	}
	if held := c.cache.State().Bytes; held != 0 {
		t.Errorf("after a read answered across Clear the cache holds %d bytes, want none", held)
	}
	if _, _, err := c.get(t, "/user"); err != nil {
		t.Fatal(err)
	}
	if c.cache.State().Bytes == 0 {
		t.Errorf("a read begun after Clear kept nothing, want its answer kept")
	}
}

// A read that carried the held answer's validator into Clear is not served from an
// entry Clear released: it is sent once more without a validator, and kept nowhere.
func TestANotModifiedAcrossClearIsReadInFull(t *testing.T) {
	h := newHeldOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, h), ownerConfig{})
	if _, _, err := c.get(t, "/user"); err != nil {
		t.Fatal(err)
	}
	got, err := readAcrossClear(t, c, h)
	if err != nil || got.Login != "octo" {
		t.Fatalf("the read revalidated across Clear = %+v, %v, want the server's answer", got, err)
	}
	sent := h.requests()
	if len(sent) != 3 || sent[1].Get("If-None-Match") != `"v1"` || sent[2].Get("If-None-Match") != "" {
		t.Errorf("the read across Clear sent %d request(s), want the conditional one then one with no validator", len(sent))
	}
	if held := c.cache.State().Bytes; held != 0 {
		t.Errorf("after a read revalidated across Clear the cache holds %d bytes, want none", held)
	}
}

// cancelingAccount ends its read's context as it decodes, after the answer was
// read in full and before the read finishes.
type cancelingAccount struct {
	cancel context.CancelFunc
	account
}

//deadset:ignore DS1103 -- encoding/json calls it through json.Unmarshaler when the read decodes into a *cancelingAccount.
func (a *cancelingAccount) UnmarshalJSON(b []byte) error {
	if a.cancel != nil {
		a.cancel()
	}
	return json.Unmarshal(b, &a.account)
}

func TestAReadWhoseContextEndsBeforeFinishKeepsNothing(t *testing.T) {
	for _, test := range []struct {
		name   string
		primed bool
	}{
		{name: "a_200"},
		{name: "a_served_304", primed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			c := newOwner(serveOrigin(t, o), ownerConfig{})
			if test.primed {
				if _, err := c.do(t.Context(), ownerRead{path: "/user", into: &cancelingAccount{}}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			answer, err := c.do(ctx, ownerRead{path: "/user", into: &cancelingAccount{cancel: cancel}})
			if !errors.Is(err, context.Canceled) || answer.hit || answer.served != test.primed {
				t.Errorf("a read whose context ended before Finish = served %v, hit %v, %v, want served=%v, no hit and context.Canceled", answer.served, answer.hit, err, test.primed)
			}
			if held := c.cache.State().Bytes; held != 0 {
				t.Errorf("the cache holds %d bytes, want the key forgotten", held)
			}
		})
	}
}

func TestASecondFinishChangesNothing(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	url := serveOrigin(t, o)
	cache := httpclient.NewCache(httpclient.CacheConfig{})
	client := &http.Client{Transport: httpclient.CacheGuard(nil)}
	read := cache.Begin(httpclient.CacheReadConfig{Store: true, Resend: true})
	resp, err := read.Send(func(prepare func(*http.Request) *http.Request) (*http.Response, error) {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		return client.Do(prepare(r))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := httpclient.ReadLimitedBody(resp.Body, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	read.Answered()
	read.Keep(resp, body, func() ([]byte, error) { return body, nil })
	if hit, err := read.Finish(t.Context(), true); hit || err != nil {
		t.Fatalf("Setup: the first Finish = %v, %v, want a miss", hit, err)
	}
	want := cache.State()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if hit, err := read.Finish(ctx, false); hit || err != nil {
		t.Errorf("a second Finish = %v, %v, want the first's result", hit, err)
	}
	if got := cache.State(); got != want || got.Bytes == 0 {
		t.Errorf("State after a second Finish = %+v, want %+v unchanged with the answer held", got, want)
	}
	again, err := read.Send(func(func(*http.Request) *http.Request) (*http.Response, error) {
		t.Error("a second Send sent a request, want none")
		return nil, errors.New("sent")
	})
	if again != nil {
		again.Body.Close()
	}
	if err == nil {
		t.Error("a second Send = nil error, want a refusal")
	}
}

// RFC 9110 section 8.8.1: a validator names a representation of one resource, so a
// 304 from wherever a hop leads cannot vouch for an answer another address gave.
func TestARedirectedOwnerReadIsNeitherServedNorKept(t *testing.T) {
	for _, test := range []struct {
		name  string
		first string
	}{
		{name: "a_hop_that_changes_destination", first: "/a"},
		{name: "a_resource_that_starts_redirecting", first: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			alpha, beta := newOrigin(`{"login":"alpha"}`, `"shared"`), newOrigin(`{"login":"beta"}`, `"shared"`)
			var hop atomic.Value
			hop.Store(test.first)
			mux := http.NewServeMux()
			mux.Handle("/a", alpha)
			mux.Handle("/b", beta)
			mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
				if to, _ := hop.Load().(string); to != "" {
					http.Redirect(w, r, to, http.StatusMovedPermanently)
					return
				}
				alpha.ServeHTTP(w, r)
			})
			c := newOwner(serveOrigin(t, mux), ownerConfig{})
			if got, _, err := c.get(t, "/old"); err != nil || got.Login != "alpha" {
				t.Fatalf("Setup: the first read = %+v, %v, want alpha", got, err)
			}
			hop.Store("/b")
			for i := range 2 {
				got, answer, err := c.get(t, "/old")
				if err != nil {
					t.Fatal(err)
				}
				if got.Login != "beta" || !answer.redirected || answer.served {
					t.Errorf("read %d through the new hop = %+v, redirected %v, served %v, want beta read from the hop", i+2, got, answer.redirected, answer.served)
				}
			}
			for _, h := range beta.requests() {
				if v := h.Get("If-None-Match"); v != "" {
					t.Errorf("a request the hop led to carried If-None-Match %q, want none", v)
				}
			}
			if got := c.cache.State().Bytes; got != 0 {
				t.Errorf("the cache holds %d bytes after reads that took a hop, want nothing kept", got)
			}
		})
	}
}

func TestAReadWithNoGuardIsNeverKept(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{noGuard: true})
	for range 2 {
		if _, _, err := c.get(t, "/user"); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.cache.State(); got.Bytes != 0 || got.Hits != 0 {
		t.Errorf("State = %+v, want nothing kept or served for a read that reached no guard", got)
	}
}

// An observer that rewrites a request reaches the wire with a request the key does
// not describe, so neither the credential nor any varying header can be crossed.
func TestAnObserverThatChangesARequestBypassesTheCache(t *testing.T) {
	echo := func(read func(*http.Request) string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"shared"`)
			if r.Header.Get("If-None-Match") == `"shared"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, `{"login":"`+read(r)+`"}`)
		}
	}
	for _, field := range []struct {
		read func(*http.Request) string
		set  func(*http.Request, string)
		name string
	}{
		{name: "authorization", read: func(r *http.Request) string { return r.Header.Get("Authorization") }, set: func(r *http.Request, v string) { r.Header.Set("Authorization", v) }},
		{name: "accept", read: func(r *http.Request) string { return r.Header.Get("Accept") }, set: func(r *http.Request, v string) { r.Header.Set("Accept", v) }},
		{name: "host", read: func(r *http.Request) string { return r.Host }, set: func(r *http.Request, v string) { r.Host = v }},
	} {
		for _, position := range observerPositions {
			t.Run(position.name+"_"+field.name, func(t *testing.T) {
				var value atomic.Value
				value.Store("one.example")
				c := newOwner(serveOrigin(t, echo(field.read)), position.config(requestRewrite(func(r *http.Request) {
					field.set(r, value.Load().(string))
				})))
				if got, _, err := c.get(t, "/user"); err != nil || got.Login != "one.example" {
					t.Fatalf("Setup: the first read = %+v, %v, want one.example", got, err)
				}
				value.Store("two.example")
				got, _, err := c.get(t, "/user")
				if err != nil {
					t.Fatal(err)
				}
				if got.Login != "two.example" {
					t.Errorf("the read sent with %s two.example = %+v, want the server's answer to it, not the one kept for one.example", field.name, got)
				}
			})
		}
	}
	t.Run("an_observer_that_only_reads", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		pass := func(next http.RoundTripper) http.RoundTripper { return next }
		c := newOwner(serveOrigin(t, o), ownerConfig{request: pass, attempt: pass})
		for range 2 {
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatal(err)
			}
		}
		if got := c.cache.State().Hits; got != 1 {
			t.Errorf("State.Hits behind observers that change nothing = %d, want 1", got)
		}
	})
}

// A request an observer changed leaves without its validators however their lines
// and names are written: an empty first line hides none (RFC 9110 section 5.6.1),
// and net/http sends a field under any name.
func TestAChangedRequestLeavesWithoutAnyValidatorLine(t *testing.T) {
	for _, test := range []struct {
		change func(h http.Header, lines []string)
		name   string
	}{
		{name: "an_empty_first_line", change: func(h http.Header, lines []string) { h["If-None-Match"] = append([]string{""}, lines...) }},
		{name: "a_lowercase_name", change: func(h http.Header, lines []string) {
			h.Del("If-None-Match")
			h["if-none-match"] = lines
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			change := requestRewrite(func(r *http.Request) {
				if lines := r.Header.Values("If-None-Match"); len(lines) > 0 {
					test.change(r.Header, lines)
				}
			})
			c := newOwner(serveOrigin(t, o), ownerConfig{attempt: change})
			for i := range 2 {
				if got, _, err := c.get(t, "/user"); err != nil || got.Login != "octo" {
					t.Fatalf("read %d = %+v, %v, want the server's answer", i, got, err)
				}
			}
			got := o.requests()
			if len(got) != 2 {
				t.Errorf("the server saw %d request(s), want 2: a changed read is one unconditional attempt", len(got))
			}
			for i, header := range got {
				for name, lines := range header {
					if strings.EqualFold(name, "If-None-Match") && slices.ContainsFunc(lines, func(line string) bool { return line != "" }) {
						t.Errorf("wire request %d carried %s %q, want no validator on a changed request", i, name, lines)
					}
				}
			}
		})
	}
}

// An answer is for the request it reports: a substitute wire that rewrites the
// keyed request, here its credential, is answered for the request it sent, whether
// its answers report that request or name none.
func TestAWireThatRewritesARequestBypassesTheCache(t *testing.T) {
	for _, test := range []struct {
		report func(*http.Response)
		name   string
	}{
		{name: "an_answer_reporting_its_request", report: func(*http.Response) {}},
		{name: "an_answer_naming_no_request", report: func(resp *http.Response) { resp.Request = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			url := identityCacheServer(t, func(r *http.Request) string { return `{"login":"` + r.Header.Get("Authorization") + `"}` })
			var token atomic.Value
			wire := cacheRoundTrip(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				r.Header.Set("Authorization", token.Load().(string))
				resp, err := http.DefaultTransport.RoundTrip(r)
				if err == nil {
					test.report(resp)
				}
				return resp, err
			})
			c := newOwner(url, ownerConfig{wire: wire})
			for _, value := range []string{"one", "two"} {
				token.Store(value)
				if got, _, err := c.get(t, "/user"); err != nil || got.Login != value {
					t.Errorf("the read the wire sent as %q = %+v, %v, want its own answer", value, got, err)
				}
			}
			if got := c.cache.State(); got.Hits != 0 || got.Bytes != 0 {
				t.Errorf("State = %+v, want no hit and nothing held for a request the wire rewrote", got)
			}
		})
	}
}

// A held answer is served only on the wire's own 304, and kept only from the
// wire's own 200: an observer that changes what the server answered is bypassed.
func TestAnObserverThatChangesAnAnswerBypassesTheCache(t *testing.T) {
	for _, position := range observerPositions {
		t.Run(position.name+"_a_200_made_a_304", func(t *testing.T) {
			o := newOrigin(`{"login":"old"}`, `"old"`)
			var forge atomic.Bool
			c := newOwner(serveOrigin(t, o), position.config(answerRewrite(func(resp *http.Response) {
				if forge.Load() {
					resp.StatusCode = http.StatusNotModified
				}
			})))
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			o.set(func(o *origin) { o.body, o.etag = `{"login":"new"}`, `"new"` })
			forge.Store(true)
			if got, answer, _ := c.get(t, "/user"); answer.served || got.Login == "old" {
				t.Errorf("a 200 an observer reported as 304 served %+v, want the held answer never served without the server's 304", got)
			}
			if n := len(o.requests()); n != 3 {
				t.Errorf("the server saw %d request(s), want 3: a 304 the wire did not give is sent once more, never refused at once", n)
			}
		})
		t.Run(position.name+"_a_404_made_a_200", func(t *testing.T) {
			o := newOrigin(`{"login":"gone"}`, `"v1"`)
			o.status = http.StatusNotFound
			c := newOwner(serveOrigin(t, o), position.config(answerRewrite(func(resp *http.Response) {
				resp.StatusCode = http.StatusOK
			})))
			if _, _, err := c.get(t, "/user"); err != nil {
				t.Fatalf("the read the observer reported as 200 = %v, want the observer's answer", err)
			}
			if got := c.cache.State().Bytes; got != 0 {
				t.Errorf("a 404 an observer reported as 200 left %d bytes held, want nothing kept", got)
			}
		})
		for _, forged := range []struct {
			change func(*http.Response)
			name   string
		}{
			{name: "a_304_given_a_body", change: func(resp *http.Response) {
				resp.Body = io.NopCloser(strings.NewReader(`{"login":"forged"}`))
			}},
			{name: "a_304_given_a_header", change: func(resp *http.Response) { resp.Header.Set("X-Forged", "1") }},
		} {
			t.Run(position.name+"_"+forged.name, func(t *testing.T) {
				o := newOrigin(`{"login":"octo"}`, `"v1"`)
				var forge atomic.Bool
				c := newOwner(serveOrigin(t, o), position.config(answerRewrite(func(resp *http.Response) {
					if forge.Load() && resp.StatusCode == http.StatusNotModified {
						forged.change(resp)
					}
				})))
				if _, _, err := c.get(t, "/user"); err != nil {
					t.Fatalf("Setup: %v", err)
				}
				forge.Store(true)
				got, answer, err := c.get(t, "/user")
				if err != nil || answer.served || got.Login != "octo" {
					t.Errorf("%s = %+v, served %v, %v, want the held answer never served for an answer the wire did not give, and the resend's own answer", forged.name, got, answer.served, err)
				}
				if n := len(o.requests()); n != 3 {
					t.Errorf("the server saw %d request(s), want 3: that 304 is sent once more without a validator", n)
				}
			})
		}
		t.Run(position.name+"_another_body", func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			var swap atomic.Bool
			swap.Store(true)
			c := newOwner(serveOrigin(t, o), position.config(answerRewrite(func(resp *http.Response) {
				if swap.Load() {
					resp.Body = io.NopCloser(strings.NewReader(`{"login":"forged"}`))
				}
			})))
			if got, _, err := c.get(t, "/user"); err != nil || got.Login != "forged" {
				t.Fatalf("Setup: the read = %+v, %v, want the observer's body", got, err)
			}
			swap.Store(false)
			got, _, err := c.get(t, "/user")
			if err != nil {
				t.Fatal(err)
			}
			if got.Login != "octo" || o.last().Get("If-None-Match") != "" {
				t.Errorf("the read after a swapped body = %+v with If-None-Match %q, want the server's answer read unconditionally", got, o.last().Get("If-None-Match"))
			}
		})
	}
}

// An owner meters requests with an attempt observer, so it sees every wire
// attempt, a 304 as the 304 the server sent, though the owner gets the held body.
func TestTheAttemptObserverSeesEveryWireAttemptAsItWasAnswered(t *testing.T) {
	for _, test := range []struct {
		name        string
		notModified int
		want        []int
	}{
		{name: "a_revalidated_read", want: []int{http.StatusOK, http.StatusNotModified}},
		{name: "a_validatorless_304_resent", notModified: 1, want: []int{http.StatusNotModified, http.StatusOK, http.StatusNotModified}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			o.notModified = test.notModified
			var got []int
			var mu sync.Mutex
			c := newOwner(serveOrigin(t, o), ownerConfig{attempt: answerRewrite(func(resp *http.Response) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, resp.StatusCode)
			})})
			for range 2 {
				if answer, _, err := c.get(t, "/user"); err != nil || answer.Login != "octo" {
					t.Fatalf("a read = %+v, %v, want the server's body", answer, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(got, test.want) || len(got) != len(o.requests()) {
				t.Errorf("the attempt observer saw %v over %d wire request(s), want %v, one per request", got, len(o.requests()), test.want)
			}
		})
	}
}

func TestANilNextSendsThroughTheDefaultTransport(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	url := serveOrigin(t, o)
	t.Run("a_cache_round_tripper", func(t *testing.T) {
		tr := httpclient.NewCacheRoundTripper(nil, httpclient.CacheConfig{})
		for range 2 {
			if status, _, body := cacheGet(t, tr, http.MethodGet, url, nil); status != http.StatusOK || body != `{"login":"octo"}` {
				t.Errorf("a read through a nil next = %d %q, want the server's answer", status, body)
			}
		}
		if got := tr.State().Hits; got != 1 {
			t.Errorf("the reads hit %d time(s), want the second served", got)
		}
	})
	t.Run("a_guard", func(t *testing.T) {
		resp, err := (&http.Client{Transport: httpclient.CacheGuard(nil)}).Get(url)
		if err != nil {
			t.Fatalf("a request through a guard over nil = %v, want the server's answer", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("a request through a guard over nil = %d, want 200", resp.StatusCode)
		}
	})
}

func TestClosingAGuardedClientsIdleConnectionsReachesNext(t *testing.T) {
	next := &idleCounter{RoundTripper: http.DefaultTransport}
	(&http.Client{Transport: httpclient.CacheGuard(next)}).CloseIdleConnections()
	if got := next.closed.Load(); got != 1 {
		t.Errorf("next's idle connections were closed %d time(s), want 1", got)
	}
}
