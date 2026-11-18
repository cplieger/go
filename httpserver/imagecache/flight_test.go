package imagecache_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpserver/v2/imagecache"
)

// A kept image older than Refresh is served at once and refetched in the
// background.
func TestStaleServedThenRefetched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v1, v2 := pngOf(512, 1), pngOf(512, 2)
		f := newFake()
		f.set(keyA, reply{img: v1, found: true})
		c, _ := newCache(t, imagecache.Options{Refresh: time.Hour}, f)
		if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusOK || !bytes.Equal(body(rec), v1.Data) {
			t.Fatalf("first answer = %d, want 200 with the fetched bytes", rec.Code)
		}

		time.Sleep(time.Hour)
		if rec := serve(c, get(t.Context()), keyA); !bytes.Equal(body(rec), v1.Data) || f.calls.Load() != 1 {
			t.Fatalf("at exactly Refresh: fetches = %d, want the kept image and no refetch", f.calls.Load())
		}

		time.Sleep(time.Second)
		f.set(keyA, reply{img: v2, found: true})
		rec := serve(c, get(t.Context()), keyA)
		if rec.Code != http.StatusOK || !bytes.Equal(body(rec), v1.Data) {
			t.Fatalf("stale answer = %d, want 200 with the kept bytes", rec.Code)
		}
		synctest.Wait()
		if got := f.calls.Load(); got != 2 {
			t.Fatalf("fetches after the stale serve = %d, want 2", got)
		}
		if rec := serve(c, get(t.Context()), keyA); !bytes.Equal(body(rec), v2.Data) {
			t.Error("after the refetch: body is not the refetched bytes")
		}
		if got := f.calls.Load(); got != 2 {
			t.Errorf("fetches after serving the refetched image = %d, want 2", got)
		}
	})
}

// A refetch that fails keeps the old image and its old fetch time, so the
// next stale Serve tries again.
func TestFailedRefetchKeepsImage(t *testing.T) {
	rows := map[string]reply{
		"error":        {err: errors.New("upstream down")},
		"not found":    {},
		"dropped type": {img: imagecache.Image{Type: "image/png", Data: []byte(svgDoc)}, found: true},
	}
	for name, failed := range rows {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				v1 := pngOf(512, 1)
				f := newFake()
				f.set(keyA, reply{img: v1, found: true})
				c, _ := newCache(t, imagecache.Options{Refresh: time.Hour}, f)
				serve(c, get(t.Context()), keyA)
				time.Sleep(time.Hour + time.Second)
				f.set(keyA, failed)
				for want := int64(2); want <= 3; want++ {
					rec := serve(c, get(t.Context()), keyA)
					if rec.Code != http.StatusOK || !bytes.Equal(body(rec), v1.Data) {
						t.Fatalf("answer = %d, want 200 with the kept bytes", rec.Code)
					}
					synctest.Wait()
					if got := f.calls.Load(); got != want {
						t.Errorf("fetches = %d, want %d", got, want)
					}
				}
			})
		})
	}
}

func TestFetchesBound(t *testing.T) {
	rows := map[string]struct{ fetches, want int }{
		"Fetches 2": {2, 2},
		"default 4": {0, 4},
	}
	for name, tc := range rows {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFake()
				for i := range 6 {
					f.set(keyN(i), reply{img: pngOf(512, byte(i)), found: true})
				}
				release := f.holdCalls()
				c, _ := newCache(t, imagecache.Options{Fetches: tc.fetches}, f)
				codes := make([]int, 6)
				var wg sync.WaitGroup
				for i := range 6 {
					wg.Go(func() { codes[i] = serve(c, get(t.Context()), keyN(i)).Code })
				}
				synctest.Wait()
				if _, _, inFlight, _ := f.seen(); inFlight != tc.want {
					t.Errorf("in flight = %d, want %d", inFlight, tc.want)
				}
				close(release)
				wg.Wait()
				if _, _, _, peak := f.seen(); peak != tc.want {
					t.Errorf("peak in flight = %d, want %d", peak, tc.want)
				}
				for i, code := range codes {
					if code != http.StatusOK {
						t.Errorf("key %d: status = %d, want 200", i, code)
					}
				}
			})
		})
	}
}

func TestOneKeyCallersShareOneFetch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		img := pngOf(2048, 4)
		f := newFake()
		f.set(keyA, reply{img: img, found: true})
		release := f.holdCalls()
		c, _ := newCache(t, imagecache.Options{}, f)
		recs := make([]*httptest.ResponseRecorder, 8)
		var wg sync.WaitGroup
		for i := range recs {
			wg.Go(func() { recs[i] = serve(c, get(t.Context()), keyA) })
		}
		synctest.Wait()
		close(release)
		wg.Wait()
		if n := f.calls.Load(); n != 1 {
			t.Errorf("fetches = %d, want 1", n)
		}
		for i, rec := range recs {
			if rec.Code != http.StatusOK || !bytes.Equal(body(rec), img.Data) {
				t.Errorf("caller %d: %d, want 200 with the fetched bytes", i, rec.Code)
			}
		}
	})
}

func TestCoalescedFetchOutlivesOneCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		img := pngOf(2048, 4)
		f := newFake()
		f.set(keyA, reply{img: img, found: true})
		release := f.holdCalls()
		c, _ := newCache(t, imagecache.Options{}, f)
		leaving, leave := context.WithCancel(t.Context())
		var first, second *httptest.ResponseRecorder
		var wg sync.WaitGroup
		wg.Go(func() { first = serve(c, get(leaving), keyA) })
		synctest.Wait()
		wg.Go(func() { second = serve(c, get(t.Context()), keyA) })
		synctest.Wait()
		leave()
		synctest.Wait()
		if _, errs, _, _ := f.seen(); len(errs) != 0 {
			t.Errorf("the fetch saw %v after one caller left, want it running", errs)
		}
		close(release)
		wg.Wait()
		if first.Code != http.StatusServiceUnavailable || envelopeCode(first) != "image_unavailable" {
			t.Errorf("the leaver got %d %q, want 503 image_unavailable", first.Code, envelopeCode(first))
		}
		if second.Code != http.StatusOK || !bytes.Equal(body(second), img.Data) {
			t.Errorf("the stayer got %d, want 200 with the fetched bytes", second.Code)
		}
		if n := f.calls.Load(); n != 1 {
			t.Errorf("fetches = %d, want 1", n)
		}
	})
}

func TestFetchCancelledWhenEveryCallerLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.set(keyA, reply{img: pngOf(2048, 4), found: true})
		release := f.holdCalls()
		c, dir := newCache(t, imagecache.Options{}, f)
		ctx, leave := context.WithCancel(t.Context())
		recs := make([]*httptest.ResponseRecorder, 2)
		var wg sync.WaitGroup
		for i := range recs {
			wg.Go(func() { recs[i] = serve(c, get(ctx), keyA) })
		}
		synctest.Wait()
		leave()
		wg.Wait()
		synctest.Wait()
		if _, errs, _, _ := f.seen(); len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
			t.Errorf("the fetch saw %v, want context.Canceled", errs)
		}
		for i, rec := range recs {
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("caller %d: status = %d, want 503", i, rec.Code)
			}
		}
		if got := files(t, dir); len(got) != 0 {
			t.Errorf("root holds %q, want nothing kept", got)
		}
		close(release)
		if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusOK || f.calls.Load() != 2 {
			t.Errorf("next Serve = %d after %d fetches, want 200 from a fresh fetch", rec.Code, f.calls.Load())
		}
	})
}

// subflux's posterBudget: a fetch whose request has no deadline gets 30 s.
func TestFetchBudget30sWithoutDeadline(t *testing.T) {
	t.Run("no deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newFake()
			f.holdCalls()
			c, _ := newCache(t, imagecache.Options{}, f)
			start := time.Now()
			rec := serve(c, get(t.Context()), keyA)
			if rec.Code != http.StatusServiceUnavailable || envelopeCode(rec) != "image_unavailable" {
				t.Errorf("answer = %d %q, want 503 image_unavailable", rec.Code, envelopeCode(rec))
			}
			if waited := time.Since(start); waited != 30*time.Second {
				t.Errorf("answered after %v, want 30s", waited)
			}
			dls, errs, _, _ := f.seen()
			if len(dls) != 1 || !dls[0].Equal(start.Add(30*time.Second)) {
				t.Errorf("fetch deadlines = %v, want start + 30s", dls)
			}
			if len(errs) != 1 || !errors.Is(errs[0], context.DeadlineExceeded) {
				t.Errorf("fetch ctx errors = %v, want DeadlineExceeded", errs)
			}
		})
	})
	t.Run("the request's deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newFake()
			f.holdCalls()
			c, _ := newCache(t, imagecache.Options{}, f)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			start := time.Now()
			if rec := serve(c, get(ctx), keyA); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			synctest.Wait()
			if dls, _, _, _ := f.seen(); len(dls) != 1 || !dls[0].Equal(start.Add(5*time.Second)) {
				t.Errorf("fetch deadlines = %v, want start + 5s", dls)
			}
		})
	})
}

// seadex-scout's covers refresh after 7 days.
func TestRefreshDefault7Days(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.set(keyA, reply{img: pngOf(512, 1), found: true})
		c, _ := newCache(t, imagecache.Options{}, f)
		serve(c, get(t.Context()), keyA)
		time.Sleep(7 * 24 * time.Hour)
		serve(c, get(t.Context()), keyA)
		synctest.Wait()
		if n := f.calls.Load(); n != 1 {
			t.Errorf("at 7 days: fetches = %d, want 1", n)
		}
		time.Sleep(time.Second)
		serve(c, get(t.Context()), keyA)
		synctest.Wait()
		if n := f.calls.Load(); n != 2 {
			t.Errorf("at 7 days + 1 s: fetches = %d, want 2", n)
		}
	})
}

func TestShutdownStopsBackgroundRefetch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v1 := pngOf(512, 1)
		f := newFake()
		f.set(keyA, reply{img: v1, found: true})
		c, _ := newCache(t, imagecache.Options{Refresh: time.Hour}, f)
		serve(c, get(t.Context()), keyA)
		time.Sleep(time.Hour + time.Second)
		f.holdCalls()
		if rec := serve(c, get(t.Context()), keyA); !bytes.Equal(body(rec), v1.Data) {
			t.Fatal("stale answer is not the kept bytes")
		}
		synctest.Wait()
		if err := c.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if _, errs, _, _ := f.seen(); len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
			t.Errorf("the refetch saw %v, want context.Canceled", errs)
		}
		if rec := serve(c, get(t.Context()), keyA); rec.Code != http.StatusOK || !bytes.Equal(body(rec), v1.Data) {
			t.Errorf("after Shutdown: %d, want the kept image", rec.Code)
		}
		other := imagecache.Key{Space: "radarr-1", ID: "43", File: "poster-250.jpg"}
		rec := serve(c, get(t.Context()), other)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "60" {
			t.Errorf("a miss after Shutdown = %d, want 503 with Retry-After 60", rec.Code)
		}
		synctest.Wait()
		if n := f.calls.Load(); n != 2 {
			t.Errorf("fetches = %d, want 2 (none after Shutdown)", n)
		}
	})
}

func TestShutdownReturnsWhenContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		stubborn := func(context.Context, imagecache.Key) (imagecache.Image, bool, error) {
			<-release
			return imagecache.Image{}, false, nil
		}
		c, _ := newCache(t, imagecache.Options{Fetch: stubborn}, newFake())
		var wg sync.WaitGroup
		wg.Go(func() { serve(c, get(t.Context()), keyA) })
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := c.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown = %v, want DeadlineExceeded", err)
		}
		close(release)
		wg.Wait()
		if err := c.Shutdown(t.Context()); err != nil {
			t.Errorf("second Shutdown = %v, want nil", err)
		}
	})
}

func TestForgetDuringFetchKeepsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.set(keyA, reply{img: pngOf(512, 1), found: true})
		release := f.holdCalls()
		c, dir := newCache(t, imagecache.Options{}, f)
		var rec *httptest.ResponseRecorder
		var wg sync.WaitGroup
		wg.Go(func() { rec = serve(c, get(t.Context()), keyA) })
		synctest.Wait()
		if err := c.Forget(keyA); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		close(release)
		wg.Wait()
		if rec.Code != http.StatusOK {
			t.Errorf("the waiting caller got %d, want 200", rec.Code)
		}
		if got := files(t, dir); len(got) != 0 {
			t.Errorf("root holds %q, want nothing kept", got)
		}
		serve(c, get(t.Context()), keyA)
		if n := f.calls.Load(); n != 2 {
			t.Errorf("fetches = %d, want 2 (the next Serve fetches anew)", n)
		}
	})
}
