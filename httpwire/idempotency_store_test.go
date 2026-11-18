package httpwire_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpwire"
)

const (
	kib = 1 << 10
	mib = 1 << 20
)

// padded makes jobs.quick answer a body of about n bytes.
func padded(kf *keyedFixture, n int) {
	kf.quick = func(_ context.Context, c httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
		return jobAnswer{ID: c.Body.Kind, Pad: strings.Repeat("p", n)}, nil
	}
}

func asCredential(c string) []string { return []string{"Credential", c} }

func TestStoreFullAtTheCredentialsCount(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{MaxPerCredential: 2}, nil)
	a, b := asCredential("s:a"), asCredential("s:b")
	kf.quickJob(`"1"`, a...)
	kf.quickJob(`"2"`, a...)
	rec := kf.quickJob(`"3"`, a...)
	if rec.Code != http.StatusServiceUnavailable || codeOf(t, rec) != "idempotency_store_full" || rec.Header().Get("Retry-After") != "60" || kf.runsOf("jobs.quick") != 2 {
		t.Errorf("third key answered %d %s Retry-After %q after %d runs, want 503 idempotency_store_full, Retry-After 60, no run", rec.Code, rec.Body, rec.Header().Get("Retry-After"), kf.runsOf("jobs.quick"))
	}
	if rec := kf.quickJob(`"1"`, b...); rec.Code != http.StatusOK || kf.runsOf("jobs.quick") != 3 {
		t.Errorf("another credential answered %d, want its keyed write run", rec.Code)
	}
	if rec := kf.quickJob(`"1"`, a...); rec.Code != http.StatusOK || kf.runsOf("jobs.quick") != 3 {
		t.Errorf("a full credential's resend answered %d %s, want its kept answer replayed", rec.Code, rec.Body)
	}
}

func TestStoreFullAtTheCredentialsByteShare(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{MaxBytes: 8 * mib, MaxBytesPerCredential: mib, MaxAnswer: mib}, nil)
	padded(kf, 600*kib)
	a, b := asCredential("s:a"), asCredential("s:b")
	kf.quickJob(`"1"`, a...)
	kf.quickJob(`"2"`, a...)
	if rec := kf.quickJob(`"3"`, a...); codeOf(t, rec) != "idempotency_store_full" || rec.Header().Get("Retry-After") != "60" || kf.runsOf("jobs.quick") != 2 {
		t.Errorf("past its share, answered %d %s after %d runs, want idempotency_store_full and no run", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
	}
	first := kf.quickJob(`"1"`, b...)
	rec := kf.quickJob(`"1"`, b...)
	if rec.Code != http.StatusOK || rec.Body.Len() < 600*kib || rec.Body.String() != first.Body.String() || kf.runsOf("jobs.quick") != 3 {
		t.Errorf("another credential's resend answered %d with %d bytes after %d runs, want its answer replayed whole", rec.Code, rec.Body.Len(), kf.runsOf("jobs.quick"))
	}
}

func TestStoreBudgetKeepsAnAnswerOversize(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{MaxBytes: mib, MaxBytesPerCredential: mib, MaxAnswer: mib}, nil)
	padded(kf, 700*kib)
	a, b := asCredential("s:a"), asCredential("s:b")
	firstA := kf.quickJob(`"1"`, a...)
	firstB := kf.quickJob(`"1"`, b...)
	if firstB.Code != http.StatusOK || firstB.Body.Len() < 700*kib {
		t.Fatalf("the answer past the budget was %d with %d bytes, want it sent whole", firstB.Code, firstB.Body.Len())
	}
	if rec := kf.quickJob(`"1"`, b...); codeOf(t, rec) != "idempotency_answer_too_large" || kf.runsOf("jobs.quick") != 2 {
		t.Errorf("its resend answered %d %s after %d runs, want idempotency_answer_too_large and no run", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
	}
	if rec := kf.quickJob(`"1"`, a...); rec.Body.String() != firstA.Body.String() {
		t.Errorf("the answer kept within the budget replayed %d bytes, want it whole", rec.Body.Len())
	}
}

func TestStoreFullOfEntriesRefusesOnlyTheLargestHolder(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{MaxPerCredential: 10_000}, nil)
	a, b := asCredential("s:a"), asCredential("s:b")
	for i := range 10_000 {
		cred := a
		if i >= 6_000 {
			cred = b
		}
		if rec := kf.quickJob(`"`+strconv.Itoa(i)+`"`, cred...); rec.Code != http.StatusOK {
			t.Fatalf("key %d answered %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := kf.quickJob(`"more"`, a...); codeOf(t, rec) != "idempotency_store_full" || kf.runsOf("jobs.quick") != 10_000 {
		t.Errorf("the largest holder answered %d %s, want idempotency_store_full and no run", rec.Code, rec.Body)
	}
	for _, cred := range [][]string{b, asCredential("s:c")} {
		if rec := kf.quickJob(`"more"`, cred...); rec.Code != http.StatusOK {
			t.Errorf("credential %s answered %d %s, want its run", cred[1], rec.Code, rec.Body)
		}
	}
	if rec := kf.quickJob(`"0"`, a...); rec.Code != http.StatusOK || kf.runsOf("jobs.quick") != 10_002 {
		t.Errorf("the oldest live answer answered %d after %d runs, want it replayed, never evicted", rec.Code, kf.runsOf("jobs.quick"))
	}
}

// TestExpiredAnswersNeverFillTheStore keeps answers that expired after the
// store's last sweep, so only a sweep at the bound tells them apart.
func TestExpiredAnswersNeverFillTheStore(t *testing.T) {
	quick := func(tb *httpwire.Table) { row(tb, "jobs.quick").Window = time.Minute }
	t.Run("the credential's count", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			kf := newKeyed(t, httpwire.IdempotencyOptions{MaxPerCredential: 2}, quick)
			a := asCredential("s:a")
			kf.quickJob(`"first"`, asCredential("s:c")...)
			time.Sleep(30 * time.Second)
			kf.quickJob(`"1"`, a...)
			kf.quickJob(`"2"`, a...)
			time.Sleep(30 * time.Second)
			kf.quickJob(`"sweep"`, asCredential("s:c")...)
			time.Sleep(40 * time.Second)
			if rec := kf.quickJob(`"3"`, a...); rec.Code != http.StatusOK {
				t.Errorf("with its answers expired, answered %d %s, want its run", rec.Code, rec.Body)
			}
		})
	})
	t.Run("the store's entries", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			kf := newKeyed(t, httpwire.IdempotencyOptions{MaxPerCredential: 10_000}, quick)
			a, b := asCredential("s:a"), asCredential("s:b")
			kf.do(http.MethodPost, "/api/jobs", `{"kind":"a","size":1}`, `"first"`, asCredential("s:c")...)
			time.Sleep(30 * time.Second)
			for i := range 6_000 {
				kf.quickJob(`"`+strconv.Itoa(i)+`"`, a...)
			}
			time.Sleep(30 * time.Second)
			for i := range 3_999 {
				if rec := kf.do(http.MethodPost, "/api/jobs", `{"kind":"a","size":1}`, `"`+strconv.Itoa(i)+`"`, b...); rec.Code != http.StatusAccepted {
					t.Fatalf("key %d answered %d %s", i, rec.Code, rec.Body)
				}
			}
			if rec := kf.quickJob(`"full"`, a...); codeOf(t, rec) != "idempotency_store_full" {
				t.Fatalf("with the store full, the largest holder answered %d %s", rec.Code, rec.Body)
			}
			time.Sleep(40 * time.Second)
			if rec := kf.quickJob(`"more"`, a...); rec.Code != http.StatusOK {
				t.Errorf("with its answers expired, the largest holder answered %d %s, want its run", rec.Code, rec.Body)
			}
		})
	})
}

func TestMaxKeptEvictsTheCredentialsOldestOfTheRow(t *testing.T) {
	kf := newKeyed(t, httpwire.IdempotencyOptions{}, func(tb *httpwire.Table) { row(tb, "jobs.quick").MaxKept = 2 })
	a, b := asCredential("s:a"), asCredential("s:b")
	kf.quickJob(`"1"`, a...)
	kf.quickJob(`"1"`, b...)
	kf.quickJob(`"2"`, a...)
	kf.quickJob(`"3"`, a...)
	kf.do(http.MethodPost, "/api/jobs", `{"kind":"a","size":1}`, `"1"`, a...)
	runs := kf.runsOf("jobs.quick")
	for _, tc := range []struct {
		key  string
		cred []string
	}{{`"2"`, a}, {`"3"`, a}, {`"1"`, b}} {
		kf.quickJob(tc.key, tc.cred...)
	}
	if got := kf.runsOf("jobs.quick"); got != runs {
		t.Errorf("%d runs replaying the kept answers, want none", got-runs)
	}
	if rec := kf.do(http.MethodPost, "/api/jobs", `{"kind":"a","size":1}`, `"1"`, a...); rec.Code != http.StatusAccepted || kf.runsOf("jobs.start") != 1 {
		t.Errorf("another row's answer was evicted: %d after %d runs", rec.Code, kf.runsOf("jobs.start"))
	}
	if kf.quickJob(`"1"`, a...); kf.runsOf("jobs.quick") != runs+1 {
		t.Errorf("the evicted key ran %d times, want it run as a new request", kf.runsOf("jobs.quick")-runs)
	}
}

func TestWindowBoundsTheKeptAnswer(t *testing.T) {
	ownWindow := func(tb *httpwire.Table) { tb.Window = 5 * time.Minute; row(tb, "jobs.quick").Window = time.Minute }
	for name, tc := range map[string]struct {
		opts   httpwire.IdempotencyOptions
		edit   func(*httpwire.Table)
		target string
		window time.Duration
	}{
		"the row's own":     {httpwire.IdempotencyOptions{}, ownWindow, "/api/jobs/quick", time.Minute},
		"the table default": {httpwire.IdempotencyOptions{}, nil, "/api/jobs/quick", 10 * time.Minute},
		"the table's":       {httpwire.IdempotencyOptions{}, func(tb *httpwire.Table) { tb.Window = 5 * time.Minute }, "/api/jobs", 5 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				kf := newKeyed(t, tc.opts, tc.edit)
				job := func() { kf.do(http.MethodPost, tc.target, `{"kind":"a","size":1}`, `"w"`) }
				runs := func() int { return kf.runsOf("jobs.quick") + kf.runsOf("jobs.start") }
				job()
				time.Sleep(tc.window - time.Second)
				if job(); runs() != 1 {
					t.Errorf("a second before the window ends, %d runs, want the replay", runs())
				}
				time.Sleep(time.Second)
				if job(); runs() != 2 {
					t.Errorf("at the window's end, %d runs, want a new run", runs())
				}
			})
		})
	}
}

type failingStore struct{ load, save error }

func (s failingStore) Load(context.Context, httpwire.IdempotencyKey) (httpwire.KeptAnswer, bool, error) {
	return httpwire.KeptAnswer{}, false, s.load
}

func (s failingStore) Save(context.Context, httpwire.IdempotencyKey, httpwire.KeptAnswer) error {
	return s.save
}

// staleStore answers every key with an answer that has expired.
type staleStore struct{}

func (staleStore) Load(context.Context, httpwire.IdempotencyKey) (httpwire.KeptAnswer, bool, error) {
	return httpwire.KeptAnswer{Status: http.StatusTeapot, Expires: time.Now().Add(-time.Second)}, true, nil
}

func (staleStore) Save(context.Context, httpwire.IdempotencyKey, httpwire.KeptAnswer) error {
	return nil
}

func TestStoreErrors(t *testing.T) {
	t.Run("an expired answer", func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{Store: staleStore{}}, nil)
		if rec := kf.quickJob(`"e"`); rec.Code != http.StatusOK || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("answered %d %s after %d runs, want the expired answer ignored and the request run", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
	})
	t.Run("load", func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{Store: failingStore{load: errors.New("disk gone")}}, nil)
		rec := kf.quickJob(`"l"`)
		if codeOf(t, rec) != "internal_error" || kf.runsOf("jobs.quick") != 0 || !kf.log.has("ERROR", "row=jobs.quick", "disk gone") {
			t.Errorf("answered %d %s after %d runs, log %q; want internal_error, no run, an ERROR line", rec.Code, rec.Body, kf.runsOf("jobs.quick"), kf.log.all())
		}
	})
}

func TestFailedSaveKeepsTheAnswerInMemory(t *testing.T) {
	diskFull := httpwire.IdempotencyOptions{Store: failingStore{save: errors.New("disk full")}}
	t.Run("the resend replays", func(t *testing.T) {
		kf := newKeyed(t, diskFull, nil)
		first := kf.quickJob(`"s"`)
		if first.Code != http.StatusOK || !kf.log.has("ERROR", "row=jobs.quick", "disk full") {
			t.Errorf("answered %d %s, log %q; want the answer and an ERROR line", first.Code, first.Body, kf.log.all())
		}
		if rec := kf.quickJob(`"s"`); rec.Code != http.StatusOK || rec.Body.String() != first.Body.String() || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("the resend answered %d %s after %d runs, want the first answer replayed and one run", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
		if rec := kf.do(http.MethodPost, "/api/jobs/quick", `{"kind":"other","size":1}`, `"s"`); codeOf(t, rec) != "idempotency_key_reused" || kf.runsOf("jobs.quick") != 1 {
			t.Errorf("the key with another body answered %d %s after %d runs, want idempotency_key_reused and no run", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
	})
	t.Run("until it expires", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			kf := newKeyed(t, diskFull, func(tb *httpwire.Table) { row(tb, "jobs.quick").Window = time.Minute })
			kf.quickJob(`"s"`)
			time.Sleep(59 * time.Second)
			kf.quickJob(`"s"`)
			if runs := kf.runsOf("jobs.quick"); runs != 1 {
				t.Errorf("inside the window, %d runs, want 1", runs)
			}
			time.Sleep(time.Second)
			kf.quickJob(`"s"`)
			if runs := kf.runsOf("jobs.quick"); runs != 2 {
				t.Errorf("at the window's end, %d runs, want a new run", runs)
			}
		})
	})
	t.Run("under the default count", func(t *testing.T) {
		kf := newKeyed(t, diskFull, nil)
		a := asCredential("s:a")
		for i := range 1_000 {
			if rec := kf.quickJob(`"`+strconv.Itoa(i)+`"`, a...); rec.Code != http.StatusOK {
				t.Fatalf("key %d answered %d %s", i, rec.Code, rec.Body)
			}
		}
		if rec := kf.quickJob(`"more"`, a...); codeOf(t, rec) != "idempotency_store_full" || kf.runsOf("jobs.quick") != 1_000 {
			t.Errorf("past the default count, answered %d %s after %d runs, want idempotency_store_full and no run", rec.Code, rec.Body, kf.runsOf("jobs.quick"))
		}
		if rec := kf.quickJob(`"more"`, asCredential("s:b")...); rec.Code != http.StatusOK || kf.runsOf("jobs.quick") != 1_001 {
			t.Errorf("another credential answered %d %s, want its run", rec.Code, rec.Body)
		}
	})
	t.Run("a store that saved keeps nothing in memory", func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{Store: failingStore{}}, nil)
		kf.quickJob(`"s"`)
		kf.quickJob(`"s"`)
		if runs := kf.runsOf("jobs.quick"); runs != 2 {
			t.Errorf("with the answer saved and not loaded, %d runs, want the store's answer to decide, 2", runs)
		}
	})
	t.Run("a store that saves caps no run in flight", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// MaxAnswer 1 MiB makes the default byte share 8 MiB, eight runs in flight.
			kf := newKeyed(t, httpwire.IdempotencyOptions{Store: failingStore{}, MaxAnswer: mib}, nil)
			release := make(chan struct{})
			kf.quick = func(context.Context, httpwire.Call[noP, noQ, jobBody]) (jobAnswer, error) {
				<-release
				return jobAnswer{ID: "one"}, nil
			}
			codes := make([]int, 20)
			var wg sync.WaitGroup
			for i := range codes {
				wg.Go(func() { codes[i] = kf.quickJob(`"` + strconv.Itoa(i) + `"`).Code })
			}
			synctest.Wait()
			if runs := kf.runsOf("jobs.quick"); runs != len(codes) {
				t.Errorf("%d runs in flight, want all %d", runs, len(codes))
			}
			close(release)
			wg.Wait()
			for i, code := range codes {
				if code != http.StatusOK {
					t.Errorf("key %d answered %d, want 200", i, code)
				}
			}
		})
	})
}

func TestNewRefusesIdempotencyOptionsOutOfBounds(t *testing.T) {
	store := &txStore{}
	for name, tc := range map[string]struct {
		opts httpwire.IdempotencyOptions
		want string
	}{
		"a small MaxAnswer":         {httpwire.IdempotencyOptions{MaxAnswer: kib - 1}, "MaxAnswer"},
		"a large MaxAnswer":         {httpwire.IdempotencyOptions{MaxAnswer: mib + 1}, "MaxAnswer"},
		"a small MaxBytes":          {httpwire.IdempotencyOptions{MaxBytes: mib - 1}, "MaxBytes"},
		"a large MaxBytes":          {httpwire.IdempotencyOptions{MaxBytes: 1<<30 + 1}, "MaxBytes"},
		"a share below MaxAnswer":   {httpwire.IdempotencyOptions{MaxAnswer: 64 * kib, MaxBytesPerCredential: 64*kib - 1}, "MaxBytesPerCredential"},
		"a share above MaxBytes":    {httpwire.IdempotencyOptions{MaxBytes: mib, MaxBytesPerCredential: mib + 1}, "MaxBytesPerCredential"},
		"a negative share":          {httpwire.IdempotencyOptions{MaxBytesPerCredential: -1}, "MaxBytesPerCredential"},
		"a negative count":          {httpwire.IdempotencyOptions{MaxPerCredential: -1}, "MaxPerCredential"},
		"a count past the store":    {httpwire.IdempotencyOptions{MaxPerCredential: 10_001}, "MaxPerCredential"},
		"MaxBytes with a store":     {httpwire.IdempotencyOptions{Store: store, MaxBytes: mib}, "MaxBytes"},
		"a share with a store":      {httpwire.IdempotencyOptions{Store: store, MaxBytesPerCredential: mib}, "MaxBytesPerCredential"},
		"a count with a store":      {httpwire.IdempotencyOptions{Store: store, MaxPerCredential: 5}, "MaxPerCredential"},
		"a share past the defaults": {httpwire.IdempotencyOptions{MaxBytesPerCredential: 64*mib + 1}, "MaxBytesPerCredential"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildKeyed(t, tc.opts, nil)
			if err == nil || !strings.Contains(err.Error(), "WithIdempotency") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("New answered %v, want a WithIdempotency refusal naming %s", err, tc.want)
			}
		})
	}
	for name, opts := range map[string]httpwire.IdempotencyOptions{
		"the defaults":    {},
		"the low bounds":  {MaxAnswer: kib, MaxBytes: mib, MaxBytesPerCredential: kib, MaxPerCredential: 1},
		"the high bounds": {MaxAnswer: mib, MaxBytes: 1 << 30, MaxBytesPerCredential: 1 << 30, MaxPerCredential: 10_000},
		"a store":         {Store: store, MaxAnswer: mib},
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			if _, _, err := buildKeyed(t, opts, nil); err != nil {
				t.Errorf("New: %v", err)
			}
		})
	}
}
