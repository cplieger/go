package chartdata_test

import (
	"context"
	"log/slog"
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
func step() {
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
		step()
		depth.Set(7)
		step()
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
		step() // the first column sets the baseline
		runs.Add(2, "ok")
		step()
		runs.Delete("ok")
		runs.Add(1, "ok") // a fall from 5 to 1 is a reset: the increase is 1
		step()
		step()
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
		step()
		reqs.Add(4, "/a", "200")
		reqs.Add(1, "/a", "500")
		reqs.Add(2, "/b", "503")
		reqs.Add(3, "/b", "500")
		step()
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
		step()
		for _, v := range []float64{0.5, 1.5, 1.5, 3} {
			wait.Observe(v)
		}
		step()
		wait.Observe(10) // past the last bound: the +Inf bucket
		step()
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

func TestSamplerTakesAHistogramsIncreaseResetAware(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		wait := measure.NewLabeledHistogram("wait_seconds", "h", measure.UnitSeconds, []string{"profile"}, measure.WithBuckets([]float64{1, 2, 4}))
		reg.MustRegister(wait)
		p50 := chartdata.Sampled(wait, chartdata.AsQuantile(0.5))
		bins := chartdata.Sampled(wait, chartdata.AsBins(time.Minute))
		h := start(t, reg, 10*time.Minute, p50, bins)
		defer h.stop()

		wait.Observe(1.5, "ui")
		step() // the first column sets the baseline
		wait.Observe(1.5, "ui")
		wait.Observe(1.5, "ui")
		step()
		// Re-created inside one step and out-observing its old count of 3: the
		// bucket up to 2 falls from 3 to 2, a reset, so the increase is the new
		// histogram's 2 observations in (0, 1] and 2 in (2, 4].
		wait.Delete("ui")
		for _, v := range []float64{0.5, 0.5, 3, 3} {
			wait.Observe(v, "ui")
		}
		step()
		if got := valuesOf(new(seriesOf(t, h.s, p50, 10*time.Minute)))["wait_seconds"]; !sameValues(got, []float64{nan, 1.5, 1}) {
			t.Errorf("p50 = %v, want [NaN 1.5 1]: half-way through (1, 2], then the top of (0, 1]", got)
		}
		f, err := h.s.Bins(bins)
		if err != nil {
			t.Fatalf("Bins: %v", err)
		}
		if len(f.Series) != 1 || !sameValues(plain(f.Series[0].Values), []float64{2, 0, 2}) {
			t.Errorf("observations per bin over the last minute = %+v, want [2 0 2], the new histogram's", f.Series)
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
		step()
		up.Delete("b")
		for range 4 {
			step()
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
		step()
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
		step()
		events = h.takeEvents()
		if len(events) != 2 || events[0].Kind != chartdata.KindAppend || *events[0].X != float64(t0+2*min1) ||
			!sameValues(plain(events[0].Values), []float64{2}) {
			t.Fatalf("second step's events = %+v, want an append of the column at its edge with a's 2, then the bins set", events)
		}

		up.Set(1, "b")
		step()
		events = h.takeEvents()
		if events[0].Kind != chartdata.KindSet || len(events[0].Series.Series) != 2 {
			t.Errorf("a new series' step = %+v, want a set of the frame with both series", events[0])
		}
	})
}

// A page draws a span shorter than the ring and keeps it current with append
// events, whose values follow the ring's series: the frame it read first
// must hold the same series in the same order, gaps for one with no value
// in its span.
func TestSamplerSeriesMatchTheAppendsValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		up := measure.NewLabeledGauge("up", "h", measure.UnitNone, []string{"host"})
		reg.MustRegister(up)
		track := chartdata.Sampled(up, chartdata.By("host"))
		h := start(t, reg, 10*time.Minute, track)
		defer h.stop()

		up.Set(1, "a")
		up.Set(1, "b")
		step()
		up.Delete("a")
		step()
		h.takeEvents()
		f := seriesOf(t, h.s, track, time.Minute)
		if len(f.Series) != 2 || f.Series[0].Key != "a" || f.Series[1].Key != "b" {
			t.Fatalf("the last minute's series = %+v, want a then b, as the ring holds them", f.Series)
		}
		if !sameValues(plain(f.Series[0].Values), []float64{nan}) {
			t.Errorf("a over the last minute = %v, want one gap", plain(f.Series[0].Values))
		}
		up.Set(3, "b")
		step()
		events := h.takeEvents()
		if len(events) != 1 || events[0].Kind != chartdata.KindAppend || !sameValues(plain(events[0].Values), []float64{nan, 3}) {
			t.Errorf("the next step's events = %+v, want an append of a gap for a and 3 for b", events)
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
		if n := logs.count(dropLog); n != 1 {
			t.Errorf("logged the cap %d times, want once", n)
		}
	})
}

// dropLog is the warning a track logs when it drops a new label set or
// series.
const dropLog = "more label sets or series than the sampler holds"

// churn registers an ExportChartdata gauge of an unbounded agent label whose
// collector emits the agents agents returns at each read.
func churn(reg *measure.Registry, agents func() []string) *measure.Desc {
	turns := measure.NewGaugeDesc("turns", "h", measure.UnitCount, []string{"agent"},
		measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("agent"))
	reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
		for _, a := range agents() {
			e.Emit(turns, 1, a)
		}
	}, turns))
	return turns
}

// sampled runs a sampler of track on reg, every minute, keeping keep, with
// its warnings in logs, and moves the clock half a step past the first
// column.
func sampled(t *testing.T, reg *measure.Registry, keep time.Duration, logs *logCapture, track *chartdata.Track) *chartdata.Sampler {
	t.Helper()
	s, err := chartdata.NewSampler(chartdata.Sampling{Registry: reg, Every: every, Keep: keep, Logger: logs.logger()}, track)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = s.Run(ctx) }()
	time.Sleep(every + every/2)
	synctest.Wait()
	return s
}

// Label values that change at every read, as an unbounded label's may, must
// not pile up rings past the cap: the series a track holds stay at
// MaxLabelSets however many steps bring new ones.
func TestSamplerHoldsAtMostMaxLabelSetsSeriesAcrossChurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		var mu sync.Mutex
		next := 0
		turns := churn(reg, func() []string {
			mu.Lock()
			defer mu.Unlock()
			out := make([]string, chartdata.MaxLabelSets)
			for i := range out {
				out[i] = strconv.Itoa(1_000_000 + next)
				next++
			}
			return out
		})
		var logs logCapture
		track := chartdata.Sampled(turns, chartdata.By("agent"))
		s := sampled(t, reg, 10*time.Minute, &logs, track)
		for range 7 {
			step()
		}
		if got := len(seriesOf(t, s, track, 10*time.Minute).Series); got != chartdata.MaxLabelSets {
			t.Errorf("series after 8 steps of new agents = %d, want %d", got, chartdata.MaxLabelSets)
		}
		if n := logs.count(dropLog); n != 1 {
			t.Errorf("logged the drop %d times, want once", n)
		}
	})
}

// readingHandler reads a frame each time it handles a record, as a handler
// that blocks on the sampler's lock would.
type readingHandler struct {
	slog.Handler
	read func()
}

func (h *readingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.read()
	return h.Handler.Handle(ctx, r)
}

// The sampler's warnings are written with its lock released, so a logger
// that reads the sampler, or is slow, never holds up a read.
func TestSamplerLogsWithItsLockReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		turns := churn(reg, func() []string {
			out := make([]string, chartdata.MaxLabelSets+1)
			for i := range out {
				out[i] = strconv.Itoa(1_000_000 + i)
			}
			return out
		})
		var logs logCapture
		var s *chartdata.Sampler
		reads := 0
		track := chartdata.Sampled(turns, chartdata.By("agent"))
		logger := slog.New(&readingHandler{Handler: slog.NewTextHandler(&logs, nil), read: func() {
			if _, err := s.Series(track, time.Minute); err != nil {
				t.Errorf("Series from the logger: %v", err)
			}
			reads++
		}})
		s, err := chartdata.NewSampler(chartdata.Sampling{Registry: reg, Every: every, Keep: 10 * time.Minute, Logger: logger}, track)
		if err != nil {
			t.Fatalf("NewSampler: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = s.Run(ctx) }()
		time.Sleep(every + every/2)
		synctest.Wait()
		if n := logs.count(dropLog); n != 1 || reads != 1 {
			t.Errorf("logged the drop %d times with %d reads, want once each", n, reads)
		}
	})
}

// A new label set that sorts first must not take the place of one already
// held, whether it is a series of its own or a label set of a held series:
// the held sets keep their values, and the new one is dropped. With Keep
// equal to Every, each ring holds one column, so a held place is renewed in
// the very step its newest point leaves the ring.
func TestSamplerKeepsTheHeldLabelSetsOverANewOneThatSortsFirst(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep time.Duration
		by   bool
	}{
		{"newSeries", 10 * time.Minute, true},
		{"newSeriesKeepIsEvery", every, true},
		{"newLabelSet", 10 * time.Minute, false},
		{"newLabelSetKeepIsEvery", every, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reg := measure.NewRegistry("")
				turns := measure.NewGaugeDesc("turns", "h", measure.UnitCount, []string{"agent"},
					measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("agent"))
				var mu sync.Mutex
				newcomer := false
				reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
					mu.Lock()
					defer mu.Unlock()
					if newcomer {
						e.Emit(turns, 1_000, "a")
					}
					for i := range chartdata.MaxLabelSets {
						e.Emit(turns, 1, "m"+strconv.Itoa(10_000+i))
					}
				}, turns))
				var opts []chartdata.TrackOption
				if tc.by {
					opts = append(opts, chartdata.By("agent"))
				}
				var logs logCapture
				track := chartdata.Sampled(turns, opts...)
				s := sampled(t, reg, tc.keep, &logs, track)
				mu.Lock()
				newcomer = true
				mu.Unlock()
				step()
				got := valuesOf(new(seriesOf(t, s, track, every)))
				if !tc.by {
					if v := got["turns"]; !sameValues(v, []float64{chartdata.MaxLabelSets}) {
						t.Errorf("turns = %v, want [%d], the held label sets' sum", v, chartdata.MaxLabelSets)
					}
					return
				}
				if len(got) != chartdata.MaxLabelSets {
					t.Errorf("series = %d, want %d", len(got), chartdata.MaxLabelSets)
				}
				if _, ok := got["a"]; ok {
					t.Errorf("the new agent a is held, want it dropped")
				}
				if last := got["m10999"]; !sameValues(last, []float64{1}) {
					t.Errorf("the held agent that sorts last = %v, want [1]", last)
				}
			})
		})
	}
}

// A label set gives its place up once its series' ring holds none of its
// points, even while the series holds the points of another.
func TestSamplerFreesThePlaceOfAnIdleLabelSetInALiveSeries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		load := measure.NewGaugeDesc("load", "h", measure.UnitNone, []string{"dc", "host"},
			measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("dc", "host"))
		var mu sync.Mutex
		idle := false
		reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
			mu.Lock()
			defer mu.Unlock()
			e.Emit(load, 1, "one", "live")
			for h := range chartdata.MaxLabelSets - 1 {
				v := 1.0
				if idle {
					v = inf
				}
				e.Emit(load, v, "one", "h"+strconv.Itoa(1_000+h))
			}
			if idle {
				e.Emit(load, 1, "two", "x")
			}
		}, load))
		var logs logCapture
		track := chartdata.Sampled(load, chartdata.By("dc"))
		s := sampled(t, reg, 3*time.Minute, &logs, track)
		step()
		mu.Lock()
		idle = true
		mu.Unlock()
		for range 4 {
			step()
		}
		got := valuesOf(new(seriesOf(t, s, track, 3*time.Minute)))
		if want := []float64{1, 1, 1}; !sameValues(got["one"], want) {
			t.Errorf("one = %v, want %v, its live host alone", got["one"], want)
		}
		if want := []float64{nan, 1, 1}; !sameValues(got["two"], want) {
			t.Errorf("two = %v, want %v: the idle hosts' places are free once Keep holds none of their points", got["two"], want)
		}
	})
}

// A label set of a series past the most a track holds takes no place, so
// the places stay free for the label sets of the series it holds, whether
// the refused series sorts before or after the held one.
func TestSamplerGivesNoPlaceToALabelSetOfASeriesPastTheMost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refused string // the prefix of the 100 series that arrive past the most
		grown   string // the held series that gains a host
	}{
		{"refusedSortsAfter", "e", "d1000"},
		{"refusedSortsBefore", "c", "d1099"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reg := measure.NewRegistry("")
				load := measure.NewGaugeDesc("load", "h", measure.UnitNone, []string{"dc", "host"},
					measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("dc", "host"))
				var mu sync.Mutex
				second := false
				reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
					mu.Lock()
					defer mu.Unlock()
					for dc := range 100 { // 5 hosts each, 500 label sets
						for h := range 5 {
							e.Emit(load, 1, "d"+strconv.Itoa(1_000+dc), "h"+strconv.Itoa(h))
							if second {
								e.Emit(load, 1, tc.refused+strconv.Itoa(1_000+dc), "h"+strconv.Itoa(h))
							}
						}
					}
					if second {
						e.Emit(load, 1, tc.grown, "h5")
					}
				}, load))
				var logs logCapture
				track := chartdata.Sampled(load, chartdata.By("dc"))
				// 10,000 steps hold MaxValues / 10,000 = 100 series.
				s, err := chartdata.NewSampler(chartdata.Sampling{
					Registry: reg, Every: time.Second, Keep: chartdata.MaxPoints * time.Second, Logger: logs.logger(),
				}, track)
				if err != nil {
					t.Fatalf("NewSampler: %v", err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				go func() { _ = s.Run(ctx) }()
				time.Sleep(1500 * time.Millisecond)
				synctest.Wait()
				mu.Lock()
				second = true
				mu.Unlock()
				time.Sleep(time.Second)
				synctest.Wait()
				f := seriesOf(t, s, track, 2*time.Second)
				got := valuesOf(&f)
				if v := got[tc.grown]; !sameValues(v, []float64{5, 6}) {
					t.Errorf("%s = %v, want [5 6]: its new host takes a place the refused series' hosts do not hold", tc.grown, v)
				}
				if len(got) != 100 {
					t.Errorf("series = %d, want the 100 held", len(got))
				}
				if n := logs.count(dropLog); n != 1 {
					t.Errorf("logged the drop %d times, want once", n)
				}
			})
		})
	}
}

// A value track's whole ring must fit one frame: 10,000 one-second steps
// leave room for 100 series of MaxValues.
func TestSamplerHoldsNoMoreSeriesThanAFrameOfTheRingTakes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		agents := make([]string, 150)
		for i := range agents {
			agents[i] = strconv.Itoa(1_000 + i)
		}
		turns := churn(reg, func() []string { return agents })
		var logs logCapture
		track := chartdata.Sampled(turns, chartdata.By("agent"))
		s, err := chartdata.NewSampler(chartdata.Sampling{
			Registry: reg, Every: time.Second, Keep: chartdata.MaxPoints * time.Second, Logger: logs.logger(),
		}, track)
		if err != nil {
			t.Fatalf("NewSampler: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = s.Run(ctx) }()
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		f, err := s.Series(track, chartdata.MaxPoints*time.Second)
		if err != nil {
			t.Fatalf("Series of the whole ring: %v", err)
		}
		if want := chartdata.MaxValues / chartdata.MaxPoints; len(f.Series) != want {
			t.Errorf("series = %d, want %d, MaxValues over the ring's points", len(f.Series), want)
		}
		if n := logs.count(dropLog); n != 1 {
			t.Errorf("logged the drop %d times, want once", n)
		}
	})
}

// A gauge may hold ±Inf or a value past MaxMagnitude, which no frame can
// carry: the sampler takes it as a gap, so the ring still reads and every
// event still encodes.
func TestSamplerTakesAReadingNoFrameCarriesAsAGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		depth := measure.NewGauge("depth", "h", measure.UnitCount)
		reg.MustRegister(depth)
		track := chartdata.Sampled(depth)
		h := start(t, reg, 10*time.Minute, track)
		defer h.stop()

		for _, v := range []float64{1, inf, -inf, 2e300, 2} {
			depth.Set(v)
			step()
		}
		f := seriesOf(t, h.s, track, 10*time.Minute)
		if got := valuesOf(&f)["depth"]; !sameValues(got, []float64{1, nan, nan, nan, 2}) {
			t.Errorf("values = %v, want [1 NaN NaN NaN 2]", got)
		}
		for _, e := range h.takeEvents() {
			encode(t, e)
		}
	})
}

// A reading that is not finite, or past MaxMagnitude, is no reading: its
// column is a gap, and the next usable reading takes its increase from the
// last usable one. measure drops a NaN counter sample, so its label set is
// not read at all, and its next reading is a first appearance.
func TestSamplerTakesAnUnusableCounterReadingAsNoReading(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  float64
		want []float64
	}{
		{"inf", inf, []float64{nan, nan, 5, 2}},
		{"pastMaxMagnitude", 1e301, []float64{nan, nan, 5, 2}},
		{"nanDroppedByMeasure", nan, []float64{nan, nan, nan, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reg := measure.NewRegistry("")
				jobs := measure.NewCounterDesc("jobs_total", "h", measure.UnitCount, nil)
				var mu sync.Mutex
				total := 10.0
				reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
					mu.Lock()
					defer mu.Unlock()
					e.Emit(jobs, total)
				}, jobs))
				set := func(v float64) {
					mu.Lock()
					total = v
					mu.Unlock()
				}
				track := chartdata.Sampled(jobs)
				h := start(t, reg, 10*time.Minute, track)
				defer h.stop()
				for _, v := range []float64{10, tc.bad, 15, 17} {
					set(v)
					step()
				}
				if got := valuesOf(new(seriesOf(t, h.s, track, 10*time.Minute)))["jobs_total"]; !sameValues(got, tc.want) {
					t.Errorf("increases of 10, %v, 15, 17 = %v, want %v", tc.bad, got, tc.want)
				}
			})
		})
	}
}

// An unusable gauge reading is a gap that leaves the series in place while
// its ring holds a usable value, so the page keeps getting appends, and in a
// By sum it adds nothing while the key's other label sets still count.
func TestSamplerTakesAnUnusableGaugeReadingAsNoReading(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  float64
	}{
		{"inf", inf},
		{"nan", nan},
		{"pastMaxMagnitude", 1e301},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reg := measure.NewRegistry("")
				up := measure.NewLabeledGauge("load", "h", measure.UnitNone, []string{"dc", "host"})
				reg.MustRegister(up)
				track := chartdata.Sampled(up, chartdata.By("dc"))
				h := start(t, reg, 10*time.Minute, track)
				defer h.stop()
				steps := []struct{ a, b, c float64 }{
					{1, 2, 4},
					{tc.bad, 2, tc.bad},
					{tc.bad, 3, tc.bad},
					{tc.bad, 3, tc.bad},
					{1, 3, 5},
				}
				for i, v := range steps {
					up.Set(v.a, "one", "a")
					up.Set(v.b, "one", "b")
					up.Set(v.c, "two", "c")
					step()
					if i == 0 {
						h.takeEvents() // the first column's set of both series
					}
				}
				f := seriesOf(t, h.s, track, 2*time.Minute)
				got := valuesOf(&f)
				if want := []float64{3, 4}; !sameValues(got["one"], want) {
					t.Errorf("dc one = %v, want %v: host a's unusable readings add nothing", got["one"], want)
				}
				if want := []float64{nan, 5}; !sameValues(got["two"], want) {
					t.Errorf("dc two = %v, want %v", got["two"], want)
				}
				for _, e := range h.takeEvents() {
					encode(t, e)
					if e.Kind == chartdata.KindSet {
						t.Errorf("a set event, want appends only: an unusable reading keeps the series in place")
					}
				}
			})
		})
	}
}

// readings is what a collector emits at each step: for each key, whether
// its label sets read usably. A key absent from the step is not emitted.
type readings struct {
	step   map[string]bool
	totals map[string]float64 // a counter's total or a histogram's count, by label set
	mu     sync.Mutex
}

func (f *readings) set(step map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.step = step
}

// usableKind is a kind of track in TestSamplerTakesAKeyOnlyFromItsFirstUsableReading:
// its track of reg's declaration, which f feeds, and how many label sets
// make up one of its keys.
type usableKind struct {
	track func(reg *measure.Registry, f *readings) *chartdata.Track
	name  string
	hosts int
	bins  bool
}

// emitEach emits, for each label set of the step's keys, its value: the
// usable or the unusable one value gives.
func (f *readings) emitEach(hosts int, emit func(key, host string, usable bool)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, usable := range f.step {
		for h := range hosts {
			emit(key, "h"+strconv.Itoa(h), usable)
		}
	}
}

func usableKinds() []usableKind {
	value := func(d func() *measure.Desc, usable, unusable func(f *readings, set string) float64, opts ...chartdata.TrackOption) func(*measure.Registry, *readings) *chartdata.Track {
		return func(reg *measure.Registry, f *readings) *chartdata.Track {
			desc := d()
			reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
				f.emitEach(len(desc.Declaration().Labels), func(key, host string, ok bool) {
					labels := []string{key}
					if len(desc.Declaration().Labels) == 2 {
						labels = append(labels, host)
					}
					v := unusable(f, key+host)
					if ok {
						v = usable(f, key+host)
					}
					e.Emit(desc, v, labels...)
				})
			}, desc))
			return chartdata.Sampled(desc, opts...)
		}
	}
	agent := func(newDesc func(string, string, measure.Unit, []string, ...measure.Option) *measure.Desc, name string) func() *measure.Desc {
		return func() *measure.Desc {
			return newDesc(name, "h", measure.UnitCount, []string{"agent"}, measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("agent"))
		}
	}
	five := func(*readings, string) float64 { return 5 }
	nanOf := func(*readings, string) float64 { return nan }
	counted := func(f *readings, set string) float64 { f.totals[set] += 2; return f.totals[set] }
	infOf := func(*readings, string) float64 { return inf }
	histogram := func(opt chartdata.TrackOption) func(*measure.Registry, *readings) *chartdata.Track {
		return func(reg *measure.Registry, f *readings) *chartdata.Track {
			desc := measure.NewHistogramDesc("wait_seconds", "h", measure.UnitSeconds, []string{"agent"}, []float64{1, 2, 4},
				measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("agent"))
			reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
				f.emitEach(1, func(key, _ string, usable bool) {
					if usable {
						f.totals[key]++ // an observation of 0.5
					}
					n := uint64(f.totals[key])
					e.EmitHistogram(desc, []uint64{n, n, n}, n, f.totals[key]/2, key)
				})
			}, desc))
			return chartdata.Sampled(desc, opt, chartdata.By("agent"))
		}
	}
	return []usableKind{
		{name: "counter", hosts: 1, track: value(agent(measure.NewCounterDesc, "jobs_total"), counted, infOf, chartdata.By("agent"))},
		{name: "gauge", hosts: 1, track: value(agent(measure.NewGaugeDesc, "depth"), five, infOf, chartdata.By("agent"))},
		{name: "quantile", hosts: 1, track: histogram(chartdata.AsQuantile(0.5))},
		{name: "bins", hosts: 1, bins: true, track: histogram(chartdata.AsBins(3 * time.Minute))},
		{name: "byKey", hosts: 2, track: value(func() *measure.Desc {
			return measure.NewGaugeDesc("load", "h", measure.UnitNone, []string{"dc", "host"},
				measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("dc", "host"))
		}, five, nanOf, chartdata.By("dc"))},
	}
}

// heldSeries returns the series a track's frame holds, by key, as plain
// values: a Series frame's columns, or a Bins frame's bins.
func heldSeries(t *testing.T, s *chartdata.Sampler, track *chartdata.Track, bins bool) map[string][]float64 {
	t.Helper()
	if !bins {
		return valuesOf(new(seriesOf(t, s, track, 3*time.Minute)))
	}
	f, err := s.Bins(track)
	if err != nil {
		t.Fatalf("Bins: %v", err)
	}
	out := map[string][]float64{}
	for _, sr := range f.Series {
		out[sr.Key] = plain(sr.Values)
	}
	return out
}

// keysFrom returns n keys of prefix, each a label set per host.
func keysFrom(prefix string, n int, usable bool, into map[string]bool) map[string]bool {
	for i := range n {
		into[prefix+strconv.Itoa(10_000+i)] = usable
	}
	return into
}

// A key comes into being only with its first usable reading, holds no place
// before it, and loses its ring once the ring holds no usable point inside
// Keep, however often it is read. A label set gives its place up once its
// key's ring holds none of its points. An unusable reading is a counter's or
// gauge's +Inf, a By gauge's NaN, or a histogram step with no observation.
func TestSamplerTakesAKeyOnlyFromItsFirstUsableReading(t *testing.T) {
	for _, kind := range usableKinds() {
		n := chartdata.MaxLabelSets / kind.hosts
		for _, tc := range []struct {
			check func(t *testing.T, held func() map[string][]float64, logs *logCapture)
			steps func(i int) map[string]bool
			name  string
			count int
		}{
			{
				name: "newKeyOnlyUnusable", count: 4,
				steps: func(i int) map[string]bool {
					step := keysFrom("m", n-1, true, map[string]bool{"b": false})
					if i >= 3 {
						step["z"] = true
					}
					return step
				},
				check: func(t *testing.T, held func() map[string][]float64, logs *logCapture) {
					got := held()
					if _, ok := got["b"]; ok {
						t.Errorf("the unusable key b is held")
					}
					if _, ok := got["z"]; !ok || len(got) != n {
						t.Errorf("z held %v among %d series, want it held among %d: b holds no place", ok, len(got), n)
					}
					if c := logs.count(dropLog); c != 0 {
						t.Errorf("logged a drop %d times, want none", c)
					}
				},
			},
			{
				name: "newKeyUnusableThenUsable", count: 3,
				steps: func(i int) map[string]bool { return map[string]bool{"x": i >= 2} },
				check: func(t *testing.T, held func() map[string][]float64, _ *logCapture) {
					want := map[string][]float64{
						"counter": {nan, nan, 2}, "gauge": {nan, 5, 5}, "quantile": {nan, 0.5, 0.5},
						"bins": {2, 0, 0}, "byKey": {nan, 10, 10},
					}[kind.name]
					if got := held()["x"]; !sameValues(got, want) {
						t.Errorf("x = %v, want %v", got, want)
					}
				},
			},
			{
				name: "existingKeyIdlePastKeep", count: 5,
				steps: func(i int) map[string]bool { return map[string]bool{"a": true, "y": i <= 2} },
				check: func(t *testing.T, held func() map[string][]float64, _ *logCapture) {
					got := held()
					if _, ok := got["y"]; ok {
						t.Errorf("y = %v after three unusable steps, want its ring removed", got["y"])
					}
					if _, ok := got["a"]; !ok {
						t.Errorf("a is not held")
					}
				},
			},
			{
				name: "existingSetsIdleFillTheCap", count: 6,
				steps: func(i int) map[string]bool {
					step := keysFrom("a", n, i <= 2, map[string]bool{})
					if i >= 3 {
						step["z"] = true
					}
					return step
				},
				check: func(t *testing.T, held func() map[string][]float64, logs *logCapture) {
					got := held()
					if _, ok := got["z"]; !ok || len(got) != 1 {
						t.Errorf("z held %v among %d series, want it the only one: the idle label sets give their places up", ok, len(got))
					}
					if c := logs.count(dropLog); c != 1 {
						t.Errorf("logged a drop %d times, want once, while the idle sets held every place", c)
					}
				},
			},
			{
				name: "capFullOfUnusable", count: 2,
				steps: func(int) map[string]bool { return keysFrom("a", n, false, map[string]bool{"z": true}) },
				check: func(t *testing.T, held func() map[string][]float64, logs *logCapture) {
					got := held()
					if _, ok := got["z"]; !ok || len(got) != 1 {
						t.Errorf("z held %v among %d series, want it the only one", ok, len(got))
					}
					if c := logs.count(dropLog); c != 0 {
						t.Errorf("logged a drop %d times, want none", c)
					}
				},
			},
		} {
			t.Run(kind.name+"_"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					reg := measure.NewRegistry("")
					f := &readings{totals: map[string]float64{}}
					track := kind.track(reg, f)
					var logs logCapture
					f.set(tc.steps(1))
					s := sampled(t, reg, 3*time.Minute, &logs, track)
					held := func() map[string][]float64 { return heldSeries(t, s, track, kind.bins) }
					for i := 2; i <= tc.count; i++ {
						if tc.name == "existingKeyIdlePastKeep" && i == 5 {
							if _, ok := held()["y"]; !ok {
								t.Errorf("y is not held after one usable column and two unusable ones")
							}
						}
						f.set(tc.steps(i))
						step()
					}
					tc.check(t, held, &logs)
				})
			})
		}
	}
}

// A track keeps at most MaxBaselines baselines: past it, the least recently
// changed of those whose label set holds no place is forgotten, so its next
// reading is a first appearance, a gap, and only the reading after it gives
// an increase. A label set holding a place, such as an idle counter's,
// whose increase of 0 is drawn, never loses its baseline.
func TestSamplerForgetsTheLeastRecentlyChangedBaselinePastTheCap(t *testing.T) {
	counter := func(name string, labels []string, opts ...measure.Option) *measure.Desc {
		return measure.NewCounterDesc(name, "h", measure.UnitCount, labels, opts...)
	}
	emitCounter := func(e *measure.Emitter, d *measure.Desc, agent string, total float64) {
		e.Emit(d, total, agent)
	}
	for _, tc := range []struct {
		emit   func(e *measure.Emitter, d *measure.Desc, agent string, total float64)
		newVec func(name string, labels []string, opts ...measure.Option) *measure.Desc
		opt    chartdata.TrackOption
		name   string
		a      []float64 // agent a's total or count at each step
		want   []float64
	}{
		{
			// a's increase of 0 at the second step gives it a place.
			name: "idleCounterWithAPlace", newVec: counter, emit: emitCounter,
			a:    []float64{10, 10, 15, 20},
			want: []float64{nan, 0, 5, 5},
		},
		{
			// a's +Inf at the second step gives no point, so a holds no place.
			name: "counterWithNoPlace", newVec: counter, emit: emitCounter,
			a:    []float64{10, inf, 15, 20},
			want: []float64{nan, nan, nan, 5},
		},
		{
			name: "histogram",
			newVec: func(name string, labels []string, opts ...measure.Option) *measure.Desc {
				return measure.NewHistogramDesc(name, "h", measure.UnitSeconds, labels, []float64{1, 2, 4}, opts...)
			},
			emit: func(e *measure.Emitter, d *measure.Desc, agent string, count float64) {
				n := uint64(count)
				e.EmitHistogram(d, []uint64{n, n, n}, n, count/2, agent) // n observations of 0.5
			},
			opt:  chartdata.AsQuantile(0.5),
			a:    []float64{1, 1, 2, 3},
			want: []float64{nan, nan, nan, 0.5},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reg := measure.NewRegistry("")
				d := tc.newVec("runs", []string{"agent"}, measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("agent"))
				others := make([]string, chartdata.MaxBaselines)
				for i := range others {
					others[i] = "m" + strconv.Itoa(10_000+i)
				}
				var mu sync.Mutex
				at := 0
				reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
					mu.Lock()
					defer mu.Unlock()
					tc.emit(e, d, "a", tc.a[at])
					if at == 1 { // MaxBaselines new label sets, newer than a, read once
						for _, o := range others {
							tc.emit(e, d, o, 1)
						}
					}
				}, d))
				opts := []chartdata.TrackOption{chartdata.By("agent")}
				if tc.opt != nil {
					opts = append(opts, tc.opt)
				}
				var logs logCapture
				track := chartdata.Sampled(d, opts...)
				s := sampled(t, reg, 10*time.Minute, &logs, track)
				for range len(tc.a) - 1 {
					mu.Lock()
					at++
					mu.Unlock()
					step()
				}
				if got := valuesOf(new(seriesOf(t, s, track, 10*time.Minute)))["a"]; !sameValues(got, tc.want) {
					t.Errorf("a = %v, want %v", got, tc.want)
				}
			})
		})
	}
}

// A step that overruns its next edges, such as a slow Emit, leaves a gap
// column at each edge it missed, and a counter's next column is a gap too,
// since its increase would cover several steps.
func TestSamplerLeavesAGapAtEachMissedEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := measure.NewRegistry("")
		depth := measure.NewGauge("depth", "h", measure.UnitCount)
		runs := measure.NewCounter("runs_total", "h", measure.UnitCount)
		reg.MustRegister(depth, runs)
		gauge, counter := chartdata.Sampled(depth), chartdata.Sampled(runs)
		var mu sync.Mutex
		var events []chartdata.FrameEvent
		slow := true
		s, err := chartdata.NewSampler(chartdata.Sampling{
			Registry: reg, Every: every, Keep: 10 * time.Minute,
			Emit: func(e chartdata.FrameEvent) {
				mu.Lock()
				events = append(events, e)
				stall := slow && e.X != nil && *e.X == float64(t0+2*min1)
				if stall {
					slow = false
				}
				mu.Unlock()
				if stall {
					time.Sleep(2*every + every/2)
				}
			},
		}, gauge, counter)
		if err != nil {
			t.Fatalf("NewSampler: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { _ = s.Run(ctx) }()
		depth.Set(1)
		runs.Add(1)
		time.Sleep(every + every/2)
		synctest.Wait()
		runs.Add(2)
		time.Sleep(4 * every)
		synctest.Wait()
		// Columns 1 and 2 are taken; Emit stalls column 2 until 4.5 minutes,
		// so 3 and 4 are missed and 5 is taken.
		g := seriesOf(t, s, gauge, 10*time.Minute)
		want := []float64{float64(t0 + min1), float64(t0 + 2*min1), float64(t0 + 3*min1), float64(t0 + 4*min1), float64(t0 + 5*min1)}
		if !sameValues(g.X, want) {
			t.Fatalf("x = %v, want a column at each edge, missed ones included, %v", g.X, want)
		}
		if got := valuesOf(&g)["depth"]; !sameValues(got, []float64{1, 1, nan, nan, 1}) {
			t.Errorf("gauge = %v, want [1 1 NaN NaN 1]", got)
		}
		if got := valuesOf(new(seriesOf(t, s, counter, 10*time.Minute)))["runs_total"]; !sameValues(got, []float64{nan, 2, nan, nan, nan}) {
			t.Errorf("counter = %v, want [NaN 2 NaN NaN NaN]: no increase across the missed steps", got)
		}
		mu.Lock()
		defer mu.Unlock()
		var appended []float64
		for _, e := range events {
			if e.Kind == chartdata.KindAppend && e.Key == "depth" {
				appended = append(appended, *e.X)
				if *e.X == want[2] && e.Values[0] != nil {
					t.Errorf("the missed column's append = %v, want a gap", *e.Values[0])
				}
			}
		}
		if !sameValues(appended, want[1:]) {
			t.Errorf("appends at %v, want one per column after the first, %v", appended, want[1:])
		}
	})
}

func TestNewSamplerRefusesABadConfiguration(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	depth := measure.NewLabeledGauge("depth", "h", measure.UnitCount, []string{"queue"})
	wait := measure.NewHistogram("wait_seconds", "h", measure.UnitSeconds)
	huge := measure.NewHistogram("size_bytes", "h", measure.UnitBytes, measure.WithBuckets([]float64{1, 1e301}))
	reg.MustRegister(depth, wait, huge)
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
		{"binsBoundPastMaxMagnitude", ok, []*chartdata.Track{chartdata.Sampled(huge, chartdata.AsBins(time.Hour))}, "bucket bound beyond"},
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
	if _, err := chartdata.NewSampler(ok, chartdata.Sampled(huge, chartdata.AsQuantile(0.5))); err != nil {
		t.Errorf("NewSampler of a quantile past MaxMagnitude = %v, want it taken: such a quantile is a gap", err)
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
			step()
		}
		cancel()
		<-read
		if got := len(seriesOf(t, h.s, track, 10*time.Minute).X); got != 5 {
			t.Errorf("columns = %d, want 5", got)
		}
	})
}
