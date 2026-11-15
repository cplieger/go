package chartdata_test

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

// The bubble's clock starts at midnight UTC 2000-01-01, a multiple of every
// step, so the first column is a minute later.
const (
	t0    = int64(946_684_800_000)
	every = time.Minute
	min1  = int64(60_000)
)

// harness runs a Sampler in a synctest bubble, one step at a time.
type harness struct {
	s      *chartdata.Sampler
	mu     sync.Mutex
	events []chartdata.FrameEvent
	stop   func()
}

// start runs a sampler of tracks on reg, every minute, keeping keep, and
// moves the clock to half a step before the first column.
func start(t *testing.T, reg *measure.Registry, keep time.Duration, tracks ...*chartdata.Track) *harness {
	t.Helper()
	h := &harness{}
	s, err := chartdata.NewSampler(chartdata.Sampling{
		Registry: reg, Every: every, Keep: keep,
		Emit: func(e chartdata.FrameEvent) {
			h.mu.Lock()
			h.events = append(h.events, e)
			h.mu.Unlock()
		},
	}, tracks...)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	h.s = s
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	h.stop = func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v, want nil once its context ends", err)
		}
	}
	time.Sleep(every / 2)
	synctest.Wait()
	return h
}

// step moves the clock one column on and waits for the sampler to take it.
func (h *harness) step() {
	time.Sleep(every)
	synctest.Wait()
}

func (h *harness) takeEvents() []chartdata.FrameEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.events
	h.events = nil
	return out
}

// plain returns wire values as plain ones, NaN for a gap.
func plain(values []*float64) []float64 {
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = math.NaN()
		if v != nil {
			out[i] = *v
		}
	}
	return out
}

// valuesOf returns f's series as plain values by key.
func valuesOf(f *chartdata.SeriesFrame) map[string][]float64 {
	out := map[string][]float64{}
	for _, s := range f.Series {
		out[s.Key] = plain(s.Values)
	}
	return out
}

func sameValues(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] && !(math.IsNaN(got[i]) && math.IsNaN(want[i])) {
			return false
		}
	}
	return true
}

func seriesOf(t *testing.T, s *chartdata.Sampler, tr *chartdata.Track, last time.Duration) chartdata.SeriesFrame {
	t.Helper()
	f, err := s.Series(tr, last)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	return f
}

func TestSamplerHoldsAGaugesValueOnEachStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("app")
		depth := measure.NewGauge("queue_depth", "h", measure.UnitCount)
		reg.MustRegister(depth)
		track := chartdata.Sampled(depth)
		h := start(t, reg, 10*time.Minute, track)
		defer h.stop()

		depth.Set(5)
		h.step()
		depth.Set(7)
		h.step()
		f := seriesOf(t, h.s, track, 10*time.Minute)
		if want := []float64{float64(t0 + min1), float64(t0 + 2*min1)}; !sameValues(f.X, want) {
			t.Errorf("x = %v, want each column stamped with its edge, %v", f.X, want)
		}
		if got := valuesOf(&f)["app_queue_depth"]; !sameValues(got, []float64{5, 7}) {
			t.Errorf("values = %v, want [5 7], keyed by the declaration's name", got)
		}
		if want := []float64{float64(t0 + 2*min1 - 10*min1), float64(t0 + 2*min1)}; !sameValues(f.Domain, want) {
			t.Errorf("domain = %v, want the last 10 minutes to the newest column, %v", f.Domain, want)
		}
		if len(f.Resets) != 1 || f.Resets[0] != t0+min1 {
			t.Errorf("resets = %v, want the first column of the process, %d", f.Resets, t0+min1)
		}
		short := seriesOf(t, h.s, track, time.Minute)
		if !sameValues(short.X, []float64{float64(t0 + 2*min1)}) || len(short.Resets) != 0 {
			t.Errorf("the last minute = x %v, resets %v, want the newest column and no reset", short.X, short.Resets)
		}
	})
}

func TestSamplerTakesACountersIncreaseResetAware(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		runs := measure.NewLabeledCounter("runs_total", "h", measure.UnitCount, []string{"result"})
		reg.MustRegister(runs)
		track := chartdata.Sampled(runs, chartdata.By("result"))
		h := start(t, reg, 10*time.Minute, track)
		defer h.stop()

		runs.Add(3, "ok")
		h.step() // the first column sets the baseline
		runs.Add(2, "ok")
		h.step()
		runs.Delete("ok")
		runs.Add(1, "ok") // a fall from 5 to 1 is a reset: the increase is 1
		h.step()
		h.step()
		got := valuesOf(new(seriesOf(t, h.s, track, 10*time.Minute)))["ok"]
		if want := []float64{nan, 2, 1, 0}; !sameValues(got, want) {
			t.Errorf("increases = %v, want %v", got, want)
		}
	})
}

func TestSamplerNarrowsWithWhereAndSumsWithBy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		reqs := measure.NewLabeledCounter("http_requests_total", "h", measure.UnitCount, []string{"route", "code"})
		reg.MustRegister(reqs)
		serverErrors := func(labels []measure.Label) bool {
			code, err := strconv.Atoi(labels[1].Value)
			return err == nil && code >= 500 && code <= 599
		}
		total := chartdata.Sampled(reqs, chartdata.Where(serverErrors))
		byCode := chartdata.Sampled(reqs, chartdata.Where(serverErrors), chartdata.By("code"))
		byRoute := chartdata.Sampled(reqs, chartdata.By("route"))
		h := start(t, reg, 10*time.Minute, total, byCode, byRoute)
		defer h.stop()

		for _, l := range [][]string{{"/a", "200"}, {"/a", "500"}, {"/b", "503"}, {"/b", "500"}} {
			reqs.Add(0, l...)
		}
		h.step()
		reqs.Add(4, "/a", "200")
		reqs.Add(1, "/a", "500")
		reqs.Add(2, "/b", "503")
		reqs.Add(3, "/b", "500")
		h.step()
		if got := valuesOf(new(seriesOf(t, h.s, total, 10*time.Minute)))["http_requests_total"]; !sameValues(got, []float64{nan, 6}) {
			t.Errorf("5xx in all = %v, want [NaN 6], the first column the baseline", got)
		}
		codes := valuesOf(new(seriesOf(t, h.s, byCode, 10*time.Minute)))
		if len(codes) != 2 || !sameValues(codes["500"], []float64{nan, 4}) || !sameValues(codes["503"], []float64{nan, 2}) {
			t.Errorf("5xx by code = %v, want 500 [NaN 4] and 503 [NaN 2]", codes)
		}
		routes := valuesOf(new(seriesOf(t, h.s, byRoute, 10*time.Minute)))
		if !sameValues(routes["/a"], []float64{nan, 5}) || !sameValues(routes["/b"], []float64{nan, 5}) {
			t.Errorf("by route = %v, want /a [NaN 5] and /b [NaN 5]", routes)
		}
	})
}

func TestSamplerEstimatesAQuantileAndBinsAHistogram(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		wait := measure.NewHistogram("wait_seconds", "h", measure.UnitSeconds, measure.WithBuckets([]float64{1, 2, 4}))
		reg.MustRegister(wait)
		p50 := chartdata.Sampled(wait, chartdata.AsQuantile(0.5), chartdata.Key("p50"))
		p99 := chartdata.Sampled(wait, chartdata.AsQuantile(0.99))
		bins := chartdata.Sampled(wait, chartdata.AsBins(2*time.Minute))
		h := start(t, reg, 10*time.Minute, p50, p99, bins)
		defer h.stop()

		wait.Observe(100) // before the baseline: in no step
		h.step()
		for _, v := range []float64{0.5, 1.5, 1.5, 3} {
			wait.Observe(v)
		}
		h.step()
		wait.Observe(10) // past the last bound: the +Inf bucket
		h.step()
		// Step 2's cumulative buckets are 1, 3, 4 and +Inf 4: the median's rank,
		// 2, falls in (1, 2], half-way through its 2 observations.
		if got := valuesOf(new(seriesOf(t, h.s, p50, 10*time.Minute)))["wait_seconds"]; !sameValues(got, []float64{nan, 1.5, 4}) {
			t.Errorf("p50 = %v, want [NaN 1.5 4]: no observations, then 1.5, then the +Inf bucket's highest finite bound", got)
		}
		// Rank 3.96 falls in (2, 4], which holds 1: 2 + 2 × 0.96.
		if got := valuesOf(new(seriesOf(t, h.s, p99, 10*time.Minute)))["wait_seconds"]; !sameValues(got[1:2], []float64{3.92}) {
			t.Errorf("p99 = %v, want 3.92 at step 2", got)
		}
		f, err := h.s.Bins(bins)
		if err != nil {
			t.Fatalf("Bins: %v", err)
		}
		if !sameValues(f.X1, []float64{0, 1, 2}) || !sameValues(f.X2, []float64{1, 2, 4}) {
			t.Errorf("bins = %v to %v, want [0,1] [1,2] [2,4]", f.X1, f.X2)
		}
		if len(f.Series) != 1 || !sameValues(plain(f.Series[0].Values), []float64{1, 2, 1}) {
			t.Errorf("observations per bin over 2 minutes = %+v, want [1 2 1], the +Inf one in no bin", f.Series)
		}
	})
}

func TestSamplerRingsAreBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		up := measure.NewLabeledGauge("up", "h", measure.UnitNone, []string{"host"})
		reg.MustRegister(up)
		track := chartdata.Sampled(up, chartdata.By("host"))
		h := start(t, reg, 3*time.Minute, track)
		defer h.stop()

		up.Set(1, "a")
		up.Set(1, "b")
		h.step()
		up.Delete("b")
		for range 4 {
			h.step()
		}
		f := seriesOf(t, h.s, track, 3*time.Minute)
		if want := []float64{float64(t0 + 3*min1), float64(t0 + 4*min1), float64(t0 + 5*min1)}; !sameValues(f.X, want) {
			t.Errorf("x = %v, want the 3 newest columns, %v", f.X, want)
		}
		if got := valuesOf(&f); len(got) != 1 || !sameValues(got["a"], []float64{1, 1, 1}) {
			t.Errorf("series = %v, want only a: b has no point inside keep", got)
		}
		if len(f.Resets) != 0 {
			t.Errorf("resets = %v, want none once the first column has left the window", f.Resets)
		}
	})
}

func TestSamplerEmitsEventsForEachStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		up := measure.NewLabeledGauge("up", "h", measure.UnitNone, []string{"host"})
		wait := measure.NewHistogram("wait_seconds", "h", measure.UnitSeconds, measure.WithBuckets([]float64{1}))
		reg.MustRegister(up, wait)
		hosts := chartdata.Sampled(up, chartdata.By("host"), chartdata.Key("hosts"))
		bins := chartdata.Sampled(wait, chartdata.AsBins(time.Minute))
		h := start(t, reg, 10*time.Minute, hosts, bins)
		defer h.stop()

		up.Set(1, "a")
		h.step()
		events := h.takeEvents()
		kinds := func(es []chartdata.FrameEvent) []chartdata.FrameEventKind {
			out := make([]chartdata.FrameEventKind, len(es))
			for i, e := range es {
				out[i] = e.Kind
			}
			return out
		}
		// The first column resets each track, a new series sets its frame, and
		// a bins track sets its frame on every step.
		want := []chartdata.FrameEventKind{chartdata.KindReset, chartdata.KindSet, chartdata.KindReset, chartdata.KindSet}
		if got := kinds(events); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
			t.Fatalf("first step's events = %v, want %v", got, want)
		}
		if events[0].Key != "hosts" || *events[0].At != t0+min1 || events[1].Series == nil {
			t.Errorf("first events = %+v, %+v, want a reset of hosts at the first column and a set carrying a SeriesFrame", events[0], events[1])
		}
		if events[2].Key != "wait_seconds" || events[3].Bins == nil {
			t.Errorf("bins events = %+v, %+v, want them keyed by the declaration's name, the set carrying a BinsFrame", events[2], events[3])
		}

		up.Set(2, "a")
		h.step()
		events = h.takeEvents()
		if len(events) != 2 || events[0].Kind != chartdata.KindAppend || *events[0].X != float64(t0+2*min1) ||
			!sameValues(plain(events[0].Values), []float64{2}) {
			t.Fatalf("second step's events = %+v, want an append of the column at its edge with a's 2, then the bins set", events)
		}

		up.Set(1, "b")
		h.step()
		events = h.takeEvents()
		if events[0].Kind != chartdata.KindSet || len(events[0].Series.Series) != 2 {
			t.Errorf("a new series' step = %+v, want a set of the frame with both series", events[0])
		}
	})
}

func TestSamplerKeepsAtMostMaxLabelSets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		agents := measure.NewGaugeDesc("turns", "h", measure.UnitCount, []string{"agent"}, measure.WithExport(measure.ExportChartdata))
		reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
			for i := range chartdata.MaxLabelSets + 5 {
				e.Emit(agents, 1, strconv.Itoa(1_000_000+i))
			}
		}, agents))
		var logs logCapture
		track := chartdata.Sampled(agents, chartdata.By("agent"))
		s, err := chartdata.NewSampler(chartdata.Sampling{
			Registry: reg, Every: every, Keep: 10 * time.Minute, Logger: logs.logger(),
		}, track)
		if err != nil {
			t.Fatalf("NewSampler: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = s.Run(ctx) }()
		time.Sleep(every + every/2)
		synctest.Wait()
		time.Sleep(every)
		synctest.Wait()
		if got := len(seriesOf(t, s, track, 10*time.Minute).Series); got != chartdata.MaxLabelSets {
			t.Errorf("series = %d, want %d, the rest dropped", got, chartdata.MaxLabelSets)
		}
		if n := logs.count("more label sets than the sampler keeps"); n != 1 {
			t.Errorf("logged the cap %d times, want once", n)
		}
	})
}

func TestNewSamplerRefusesABadConfiguration(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	depth := measure.NewLabeledGauge("depth", "h", measure.UnitCount, []string{"queue"})
	wait := measure.NewHistogram("wait_seconds", "h", measure.UnitSeconds)
	reg.MustRegister(depth, wait)
	ok := chartdata.Sampling{Registry: reg, Every: time.Minute, Keep: time.Hour}
	with := func(edit func(*chartdata.Sampling)) chartdata.Sampling {
		c := ok
		edit(&c)
		return c
	}
	twice := chartdata.Sampled(depth)
	for _, tc := range []struct {
		name   string
		cfg    chartdata.Sampling
		tracks []*chartdata.Track
		want   string
	}{
		{"noRegistry", with(func(c *chartdata.Sampling) { c.Registry = nil }), nil, "Registry is nil"},
		{"everyBelowASecond", with(func(c *chartdata.Sampling) { c.Every = 999 * time.Millisecond }), nil, "below 1s"},
		{"keepBelowEvery", with(func(c *chartdata.Sampling) { c.Keep = 30 * time.Second }), nil, "Keep 30s is below Every"},
		{"pastMaxPoints", with(func(c *chartdata.Sampling) { c.Every, c.Keep = time.Second, 10_001*time.Second }), nil, "10001 steps"},
		{"partStepPastMaxPoints", with(func(c *chartdata.Sampling) { c.Every, c.Keep = time.Second, 10_000*time.Second+1 }), nil, "10001 steps"},
		{"nilTrack", ok, []*chartdata.Track{nil}, "track 0: is nil"},
		{"nilTrackOption", ok, []*chartdata.Track{chartdata.Sampled(depth, nil)}, "nil TrackOption"},
		{"trackTwice", ok, []*chartdata.Track{twice, twice}, "track 1: is given twice"},
		{"nilDeclaration", ok, []*chartdata.Track{chartdata.Sampled(nil)}, "declaration is nil"},
		{"byUnknownLabel", ok, []*chartdata.Track{chartdata.Sampled(depth, chartdata.By("host"))}, `By("host") names no label`},
		{"histogramAsValue", ok, []*chartdata.Track{chartdata.Sampled(wait)}, "AsQuantile or AsBins"},
		{"gaugeAsQuantile", ok, []*chartdata.Track{chartdata.Sampled(depth, chartdata.AsQuantile(0.5))}, "not a histogram"},
		{"quantileAboveOne", ok, []*chartdata.Track{chartdata.Sampled(wait, chartdata.AsQuantile(1.5))}, "not from 0 to 1"},
		{"quantileNaN", ok, []*chartdata.Track{chartdata.Sampled(wait, chartdata.AsQuantile(nan))}, "not from 0 to 1"},
		{"binsPastKeep", ok, []*chartdata.Track{chartdata.Sampled(wait, chartdata.AsBins(2*time.Hour))}, "not from Every"},
		{"binsBelowEvery", ok, []*chartdata.Track{chartdata.Sampled(wait, chartdata.AsBins(time.Second))}, "not from Every"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := chartdata.NewSampler(tc.cfg, tc.tracks...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("NewSampler = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	if _, err := chartdata.NewSampler(with(func(c *chartdata.Sampling) { c.Every, c.Keep = time.Second, 10_000*time.Second })); err != nil {
		t.Errorf("NewSampler at MaxPoints = %v, want it taken", err)
	}
}

func TestSamplerReadsRefuseWhatTheTrackCannotGive(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	depth := measure.NewGauge("depth", "h", measure.UnitCount)
	wait := measure.NewHistogram("wait_seconds", "h", measure.UnitSeconds)
	reg.MustRegister(depth, wait)
	value, bins := chartdata.Sampled(depth), chartdata.Sampled(wait, chartdata.AsBins(time.Hour))
	s, err := chartdata.NewSampler(chartdata.Sampling{Registry: reg, Every: time.Minute, Keep: time.Hour}, value, bins)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	for _, tc := range []struct {
		name string
		read func() error
		want string
	}{
		{"seriesOfBins", func() error { _, err := s.Series(bins, time.Hour); return err }, "read with Bins"},
		{"binsOfValue", func() error { _, err := s.Bins(value); return err }, "only an AsBins track"},
		{"spanPastKeep", func() error { _, err := s.Series(value, 2*time.Hour); return err }, "not from Every"},
		{"spanBelowEvery", func() error { _, err := s.Series(value, time.Second); return err }, "not from Every"},
		{"anotherTrack", func() error { _, err := s.Series(chartdata.Sampled(depth), time.Hour); return err }, "not one this sampler holds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.read(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("read = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestSamplerRunStopsOnAnUnregisteredDeclaration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		loose := measure.NewGauge("loose", "h", measure.UnitCount)
		s, err := chartdata.NewSampler(chartdata.Sampling{Registry: measure.NewRegistry(""), Every: every, Keep: time.Hour},
			chartdata.Sampled(loose))
		if err != nil {
			t.Fatalf("NewSampler: %v", err)
		}
		if err := s.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Errorf("Run = %v, want it stopped by the unregistered declaration", err)
		}
		if err := s.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "twice") {
			t.Errorf("a second Run = %v, want it refused", err)
		}
	})
}

func TestSamplerReadsWhileItSamples_Concurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		depth := measure.NewGauge("depth", "h", measure.UnitCount)
		reg.MustRegister(depth)
		track := chartdata.Sampled(depth)
		h := start(t, reg, 10*time.Minute, track)
		defer h.stop()
		ctx, cancel := context.WithCancel(t.Context())
		read := make(chan struct{})
		go func() {
			defer close(read)
			for ctx.Err() == nil {
				if _, err := h.s.Series(track, 10*time.Minute); err != nil {
					t.Errorf("Series: %v", err)
				}
				time.Sleep(time.Second)
			}
		}()
		for i := range 5 {
			depth.Set(float64(i))
			h.step()
		}
		cancel()
		<-read
		if got := len(seriesOf(t, h.s, track, 10*time.Minute).X); got != 5 {
			t.Errorf("columns = %d, want 5", got)
		}
	})
}
