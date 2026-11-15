package chartdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/go/measure/v2"
)

// Track is a registered declaration a Sampler holds the history of. Build it
// with Sampled.
type Track struct {
	decl   measure.Declared
	where  func(labels []measure.Label) bool
	err    error
	key    string
	by     string
	form   form
	q      float64
	window time.Duration
}

type form uint8

const (
	formValue form = iota
	formQuantile
	formBins
)

// TrackOption narrows or shapes a Track.
type TrackOption func(*Track)

// Sampled tracks d, a declaration registered in the Sampler's registry. A
// gauge, state set or info series is sampled as its value, a counter as its
// increase over each step, and a histogram, which needs AsQuantile or
// AsBins, as the increase of each bucket over each step.
func Sampled(d measure.Declared, opts ...TrackOption) *Track {
	t := &Track{decl: d}
	for _, opt := range opts {
		if opt == nil {
			t.err = errors.New("chartdata: Sampled holds a nil TrackOption")
			continue
		}
		opt(t)
	}
	return t
}

// Where keeps only the label sets keep returns true for, such as status
// codes from 500 to 599. keep is called on the sampler's goroutine.
func Where(keep func(labels []measure.Label) bool) TrackOption {
	return func(t *Track) { t.where = keep }
}

// By sums the series over every label but label, as PromQL's sum by does,
// keeping a histogram's buckets apart; each value of label is one series of
// the frame, keyed by it. Without By, the series are summed into one, keyed
// by the declaration's name.
func By(label string) TrackOption {
	return func(t *Track) { t.by = label }
}

// Key names the track in the events the sampler emits, so a page drawing
// two tracks of one declaration, such as its p50 and its p95, can tell
// their events apart. Without Key, a track's events carry its
// declaration's name.
func Key(key string) TrackOption {
	return func(t *Track) { t.key = key }
}

// AsQuantile samples a histogram as its q-quantile over each step, from 0 to
// 1, estimated as PromQL's histogram_quantile estimates it.
func AsQuantile(q float64) TrackOption {
	return func(t *Track) { t.form, t.q = formQuantile, q }
}

// AsBins samples a histogram as the observations in each of its buckets over
// the last window, read with Sampler.Bins.
func AsBins(window time.Duration) TrackOption {
	return func(t *Track) { t.form, t.window = formBins, window }
}

// Sampling configures a Sampler: the registry its tracks are registered in,
// the step between samples and how long a sample is kept.
type Sampling struct {
	// Registry holds every tracked declaration. Required.
	Registry *measure.Registry
	// Logger takes the warning for a declaration past MaxLabelSets label
	// sets; nil means slog.Default.
	Logger *slog.Logger
	// Emit, when set, receives the events that keep a drawn frame current,
	// on the sampler's goroutine, after each step.
	Emit func(FrameEvent)
	// Every is the step: samples are taken on its multiples since the Unix
	// epoch, at least MinEvery apart.
	Every time.Duration
	// Keep is how long a sample is held, at most MaxPoints steps.
	Keep time.Duration
}

// Sampler holds, in bounded rings, the recent history of registered series
// that the app keeps nowhere else. It keeps no store: its rings start empty
// when the process starts. Build it with NewSampler and run it with Run.
type Sampler struct {
	reg     *measure.Registry
	logger  *slog.Logger
	emit    func(FrameEvent)
	cols    *ring[int64]
	tracks  map[*Track]*tracked
	order   []*tracked
	decls   []measure.Declared
	every   time.Duration
	keep    time.Duration
	start   int64 // the first column's edge in Unix milliseconds; 0 before it
	mu      sync.Mutex
	running atomic.Bool
}

// tracked is a Track's state: its rings, one per key, aligned with the
// sampler's columns, and the last cumulative reading of each label set.
type tracked struct {
	*Track
	values map[string]*ring[float64]   // formValue and counters
	hists  map[string]*ring[[]float64] // formQuantile and formBins: bucket increases, +Inf last
	prev   map[string][]float64        // a counter's or histogram's last reading by label set
	kind   measure.Kind
	byAt   int
	capped bool
	warned bool
}

// NewSampler builds a Sampler of tracks. It refuses an Every below MinEvery,
// a Keep below Every or past MaxPoints steps, and a track whose options do
// not fit its declaration.
func NewSampler(cfg Sampling, tracks ...*Track) (*Sampler, error) {
	switch {
	case cfg.Registry == nil:
		return nil, errors.New("chartdata: Sampling.Registry is nil")
	case cfg.Every < MinEvery:
		return nil, fmt.Errorf("chartdata: Sampling.Every %v is below %v", cfg.Every, MinEvery)
	case cfg.Keep < cfg.Every:
		return nil, fmt.Errorf("chartdata: Sampling.Keep %v is below Every %v", cfg.Keep, cfg.Every)
	}
	points := int64(cfg.Keep / cfg.Every)
	if cfg.Keep%cfg.Every != 0 {
		points++
	}
	if points > MaxPoints {
		return nil, fmt.Errorf("chartdata: Sampling.Keep holds %d steps of %v, past the most, %d", points, cfg.Every, MaxPoints)
	}
	s := &Sampler{
		reg: cfg.Registry, logger: cfg.Logger, emit: cfg.Emit, every: cfg.Every, keep: cfg.Keep,
		cols: newRing[int64](int(points)), tracks: make(map[*Track]*tracked, len(tracks)),
	}
	for i, t := range tracks {
		ts, err := s.track(t)
		if err != nil {
			return nil, fmt.Errorf("chartdata: track %d: %w", i, err)
		}
		s.tracks[t] = ts
		s.order = append(s.order, ts)
		if !slices.Contains(s.decls, t.decl) {
			s.decls = append(s.decls, t.decl)
		}
	}
	return s, nil
}

func (s *Sampler) track(t *Track) (*tracked, error) {
	if t == nil {
		return nil, errors.New("is nil")
	}
	if t.err != nil {
		return nil, t.err
	}
	if _, dup := s.tracks[t]; dup {
		return nil, errors.New("is given twice")
	}
	decl, err := declarationOf(t.decl, "declaration")
	if err != nil {
		return nil, err
	}
	ts := &tracked{Track: t, kind: decl.Kind, byAt: -1, prev: map[string][]float64{}}
	if t.by != "" {
		if ts.byAt = slices.Index(decl.Labels, t.by); ts.byAt < 0 {
			return nil, fmt.Errorf("By(%q) names no label of %s", t.by, decl.Name)
		}
	}
	if err := s.checkForm(t, &decl); err != nil {
		return nil, err
	}
	if t.form == formValue {
		ts.values = map[string]*ring[float64]{}
	} else {
		ts.hists = map[string]*ring[[]float64]{}
	}
	return ts, nil
}

// checkForm refuses a form that does not fit decl's kind, or a bad quantile
// or bins window.
func (s *Sampler) checkForm(t *Track, decl *measure.Declaration) error {
	switch {
	case decl.Kind == measure.KindHistogram && t.form == formValue:
		return fmt.Errorf("%s is a histogram, sampled with AsQuantile or AsBins", decl.Name)
	case decl.Kind != measure.KindHistogram && t.form != formValue:
		return fmt.Errorf("%s is not a histogram, so it takes no AsQuantile or AsBins", decl.Name)
	case t.form == formQuantile && !(t.q >= 0 && t.q <= 1):
		return fmt.Errorf("AsQuantile(%v) is not from 0 to 1", t.q)
	case t.form == formBins && (t.window < s.every || t.window > s.keep):
		return fmt.Errorf("AsBins(%v) is not from Every %v to Keep %v", t.window, s.every, s.keep)
	}
	return nil
}

// Run samples on every step until ctx ends, then returns nil. It returns an
// error, and stops, when a tracked declaration is not registered in the
// registry. Call it once.
func (s *Sampler) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("chartdata: Sampler.Run called twice")
	}
	for {
		next := nextEdge(time.Now(), s.every)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		events, err := s.tick(ctx, next)
		if err != nil {
			return err
		}
		if s.emit != nil {
			for i := range events {
				s.emit(events[i])
			}
		}
	}
}

// nextEdge is the first multiple of every since the Unix epoch after now.
func nextEdge(now time.Time, every time.Duration) time.Time {
	step := int64(every)
	return time.Unix(0, (now.UnixNano()/step+1)*step)
}

// tick takes the column at edge and returns the events it makes.
func (s *Sampler) tick(ctx context.Context, edge time.Time) ([]FrameEvent, error) {
	fams, err := s.reg.GatherOf(ctx, s.decls...)
	if fams == nil && err != nil {
		return nil, err
	}
	at := edge.UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	first := s.start == 0
	if first {
		s.start = at
	}
	s.cols.push(at)
	var events []FrameEvent
	for _, ts := range s.order {
		fam := fams[slices.Index(s.decls, ts.decl)]
		before := ts.keys()
		if ts.form == formValue {
			s.observeValues(ts, &fam)
		} else {
			s.observeHistograms(ts, &fam)
		}
		events = append(events, s.eventsOf(ts, before, first)...)
	}
	return events, nil
}

// keyOf is the series key of a sample's labels.
func (ts *tracked) keyOf(labels []measure.Label) string {
	if ts.byAt < 0 {
		return ts.decl.Declaration().Name
	}
	return labels[ts.byAt].Value
}

// labelSet is a sample's labels as one map key.
func labelSet(labels []measure.Label) string {
	var b strings.Builder
	for _, l := range labels {
		b.WriteString(l.Value)
		b.WriteByte(0xff)
	}
	return b.String()
}

// admit reports whether a sample counts toward ts, the n-th admitted, and
// logs once the first time a declaration passes MaxLabelSets.
func (s *Sampler) admit(ts *tracked, labels []measure.Label, n int) bool {
	if ts.where != nil && !ts.where(labels) {
		return false
	}
	if n < MaxLabelSets {
		return true
	}
	if !ts.capped {
		ts.capped = true
		logger := s.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("chartdata: a sampled declaration has more label sets than the sampler keeps; the rest are dropped",
			"name", ts.decl.Declaration().Name, "most", MaxLabelSets)
	}
	return false
}

func (s *Sampler) observeValues(ts *tracked, fam *measure.Family) {
	sums := map[string]float64{}
	next := map[string][]float64{}
	n := 0
	for i := range fam.Samples {
		smp := &fam.Samples[i]
		if !s.admit(ts, smp.Labels, n) {
			continue
		}
		n++
		v := smp.Value
		if ts.kind == measure.KindCounter {
			set := labelSet(smp.Labels)
			next[set] = []float64{v}
			prev, ok := ts.prev[set]
			if !ok {
				continue
			}
			v = increase(v, prev[0])
		}
		sums[ts.keyOf(smp.Labels)] += v
	}
	ts.prev = next
	update(ts.values, sums, s.cols, math.NaN(), math.IsNaN)
}

// increase is a counter's increase from prev to cur, reset-aware as PromQL's
// increase is: a fall is a reset, and cur is then the increase.
func increase(cur, prev float64) float64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

func (s *Sampler) observeHistograms(ts *tracked, fam *measure.Family) {
	sums := map[string][]float64{}
	next := map[string][]float64{}
	n := 0
	for i := range fam.Samples {
		smp := &fam.Samples[i]
		if smp.Histogram == nil || !s.admit(ts, smp.Labels, n) {
			continue
		}
		n++
		cur := cumulative(smp.Histogram)
		set := labelSet(smp.Labels)
		next[set] = cur
		if prev, ok := ts.prev[set]; ok && len(prev) == len(cur) {
			addInto(sums, ts.keyOf(smp.Labels), bucketIncrease(cur, prev))
		}
	}
	ts.prev = next
	update(ts.hists, sums, s.cols, nil, func(v []float64) bool { return v == nil })
}

// bucketIncrease is each cumulative bucket's increase from prev to cur, all
// of cur when the count fell, a reset.
func bucketIncrease(cur, prev []float64) []float64 {
	delta := slices.Clone(cur)
	if cur[len(cur)-1] < prev[len(prev)-1] {
		return delta
	}
	for k := range delta {
		delta[k] -= prev[k]
	}
	return delta
}

// addInto adds delta to key's sum, bucket by bucket.
func addInto(sums map[string][]float64, key string, delta []float64) {
	sum, ok := sums[key]
	if !ok || len(sum) != len(delta) {
		sums[key] = delta
		return
	}
	for k := range sum {
		sum[k] += delta[k]
	}
}

// update pushes each key's value in sums onto its ring, and gap onto the ring
// of a key with none, dropping a ring left with no point; a new key's ring
// starts with a gap per earlier column, so every ring stays aligned with
// cols, whose newest column is the one pushed.
func update[T any](rings map[string]*ring[T], sums map[string]T, cols *ring[int64], gap T, isGap func(T) bool) {
	for key, r := range rings {
		if v, ok := sums[key]; ok {
			r.push(v)
			delete(sums, key)
			continue
		}
		r.push(gap)
		if allGaps(r, isGap) {
			delete(rings, key)
		}
	}
	for key, v := range sums {
		r := newRing[T](len(cols.buf))
		for range cols.n - 1 {
			r.push(gap)
		}
		r.push(v)
		rings[key] = r
	}
}

// cumulative is a histogram reading's cumulative bucket counts, the +Inf
// bucket's, its Count, last.
func cumulative(h *measure.HistogramValue) []float64 {
	out := make([]float64, len(h.Buckets)+1)
	for i, b := range h.Buckets {
		out[i] = float64(b.Count)
	}
	out[len(h.Buckets)] = float64(h.Count)
	return out
}

func allGaps[T any](r *ring[T], gap func(T) bool) bool {
	for i := range r.n {
		if !gap(r.at(i)) {
			return false
		}
	}
	return true
}

// keys returns the track's keys, sorted.
func (ts *tracked) keys() []string {
	var out []string
	if ts.form == formValue {
		for k := range ts.values {
			out = append(out, k)
		}
	} else {
		for k := range ts.hists {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// quantile estimates the q-quantile of cumulative bucket counts cum over
// bounds, +Inf last, as Prometheus's histogram_quantile does: linearly
// within the bucket the rank falls in, the lowest from 0 when its bound is
// above 0; the highest finite bound for a rank in +Inf; NaN for fewer than
// two buckets or no observations.
// https://github.com/prometheus/prometheus/blob/main/promql/quantile.go
func quantile(q float64, bounds, cum []float64) float64 {
	if len(cum) < 2 || len(cum) != len(bounds)+1 {
		return math.NaN()
	}
	cum = slices.Clone(cum)
	for i := 1; i < len(cum); i++ {
		cum[i] = max(cum[i], cum[i-1])
	}
	total := cum[len(cum)-1]
	if !(total > 0) {
		return math.NaN()
	}
	rank := q * total
	b, _ := slices.BinarySearch(cum, rank)
	if b == len(cum)-1 {
		return bounds[len(bounds)-1]
	}
	if b == 0 && bounds[0] <= 0 {
		return bounds[0]
	}
	start, count := 0.0, cum[b]
	if b > 0 {
		start, count, rank = bounds[b-1], cum[b]-cum[b-1], rank-cum[b-1]
	}
	return start + (bounds[b]-start)*(rank/count)
}

// window returns the indices of the columns after from, up to the newest.
func (s *Sampler) window(from int64) (first int) {
	first = s.cols.n
	for first > 0 && s.cols.at(first-1) > from {
		first--
	}
	return first
}

// end is the newest column's edge, or the edge before now before the first.
func (s *Sampler) end() int64 {
	if s.cols.n > 0 {
		return s.cols.at(s.cols.n - 1)
	}
	return nextEdge(time.Now(), s.every).Add(-s.every).UnixMilli()
}

func (s *Sampler) state(t *Track) (*tracked, error) {
	ts, ok := s.tracks[t]
	if !ok {
		return nil, errors.New("chartdata: the track is not one this sampler holds")
	}
	return ts, nil
}

// Series reads a value or quantile track as a SeriesFrame of its columns in
// the last span, from Every to Keep, with the span as its domain and, when
// the process started inside it, the first column as its reset. A series
// with no value in the span is left out.
func (s *Sampler) Series(t *Track, last time.Duration) (SeriesFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.state(t)
	if err != nil {
		return SeriesFrame{}, err
	}
	if ts.form == formBins {
		return SeriesFrame{}, errors.New("chartdata: an AsBins track is read with Bins")
	}
	if last < s.every || last > s.keep {
		return SeriesFrame{}, fmt.Errorf("chartdata: Series(%v) is not from Every %v to Keep %v", last, s.every, s.keep)
	}
	return s.seriesFrame(ts, last)
}

func (s *Sampler) seriesFrame(ts *tracked, last time.Duration) (SeriesFrame, error) {
	end := s.end()
	from := end - last.Milliseconds()
	first := s.window(from)
	x := make([]float64, 0, s.cols.n-first)
	for i := first; i < s.cols.n; i++ {
		x = append(x, float64(s.cols.at(i)))
	}
	var series []Series
	for _, key := range ts.keys() {
		values := make([]float64, len(x))
		seen := false
		for i := range values {
			values[i] = ts.valueAt(key, first+i)
			seen = seen || !math.IsNaN(values[i])
		}
		if seen {
			series = append(series, Series{Key: key, Values: values})
		}
	}
	opts := []Option{WithDomain(Range{From: float64(from), To: float64(end)})}
	if s.start > from {
		opts = append(opts, WithResets(time.UnixMilli(s.start)))
	}
	return NewSeriesFrame(ts.decl, x, series, opts...)
}

// valueAt is key's value at column i, NaN for a gap.
func (ts *tracked) valueAt(key string, i int) float64 {
	if ts.form == formValue {
		return ts.values[key].at(i)
	}
	cum := ts.hists[key].at(i)
	if cum == nil {
		return math.NaN()
	}
	return quantile(ts.q, ts.decl.Declaration().Buckets, cum)
}

// Bins reads an AsBins track as a BinsFrame of the observations in each
// finite bucket over its window: the first bucket from 0 when its bound is
// above 0, and no bin for a bucket with no finite lower edge, the +Inf
// bucket's included.
func (s *Sampler) Bins(t *Track) (BinsFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.state(t)
	if err != nil {
		return BinsFrame{}, err
	}
	if ts.form != formBins {
		return BinsFrame{}, errors.New("chartdata: only an AsBins track is read with Bins")
	}
	return ts.binsFrame(s.window(s.end()-ts.window.Milliseconds()), s.cols.n)
}

func (ts *tracked) binsFrame(first, last int) (BinsFrame, error) {
	bounds := ts.decl.Declaration().Buckets
	lowest := 0
	if len(bounds) > 0 && bounds[0] <= 0 {
		lowest = 1
	}
	bins := make([]Bin, 0, len(bounds)-lowest)
	for i := lowest; i < len(bounds); i++ {
		bins = append(bins, Bin{From: below(bounds, i), To: bounds[i]})
	}
	var series []Series
	for _, key := range ts.keys() {
		sum := sumColumns(ts.hists[key], first, last)
		if sum == nil {
			continue
		}
		values := make([]float64, 0, len(bins))
		for i := lowest; i < len(bounds); i++ {
			values = append(values, max(0, sum[i]-below(sum, i)))
		}
		series = append(series, Series{Key: key, Values: values})
	}
	return NewBinsFrame(ts.decl, AxisValue, bins, series)
}

// below is the value before index i, 0 before the first.
func below(vs []float64, i int) float64 {
	if i == 0 {
		return 0
	}
	return vs[i-1]
}

// sumColumns adds the bucket increases of r's columns first to last, nil when
// every one is a gap.
func sumColumns(r *ring[[]float64], first, last int) []float64 {
	var sum []float64
	for i := first; i < last; i++ {
		cum := r.at(i)
		if cum == nil {
			continue
		}
		if sum == nil {
			sum = make([]float64, len(cum))
		}
		for k := range cum {
			sum[k] += cum[k]
		}
	}
	return sum
}

// eventKey is the key ts's events carry.
func (ts *tracked) eventKey() string {
	if ts.key != "" {
		return ts.key
	}
	return ts.decl.Declaration().Name
}

// eventsOf returns the events the newest column makes for ts, whose keys
// were before ahead of it: a reset at the first column of the process, then
// an append, or a set of the whole ring when its series changed; an AsBins
// track sets its window's frame on every step.
func (s *Sampler) eventsOf(ts *tracked, before []string, first bool) []FrameEvent {
	key := ts.eventKey()
	var events []FrameEvent
	if first {
		events = append(events, ResetAt(key, time.UnixMilli(s.start)))
	}
	newest := s.cols.n - 1
	switch keys := ts.keys(); {
	case ts.form == formBins:
		f, err := ts.binsFrame(s.window(s.end()-ts.window.Milliseconds()), s.cols.n)
		if err != nil {
			s.warnOnce(ts, err)
			return events
		}
		e, _ := SetFrame(key, &f)
		return append(events, e)
	case slices.Equal(keys, before):
		values := make([]*float64, len(keys))
		for i, k := range keys {
			if v := ts.valueAt(k, newest); !math.IsNaN(v) {
				values[i] = &v
			}
		}
		x := float64(s.cols.at(newest))
		return append(events, FrameEvent{Key: key, Kind: KindAppend, X: &x, Values: values})
	default:
		f, err := s.seriesFrame(ts, s.keep)
		if err != nil {
			s.warnOnce(ts, err)
			return events
		}
		e, _ := SetFrame(key, &f)
		return append(events, e)
	}
}

// warnOnce logs, the first time for a track, a frame the sampler could not
// build for a set event, such as one past MaxValues.
func (s *Sampler) warnOnce(ts *tracked, err error) {
	if ts.warned {
		return
	}
	ts.warned = true
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("chartdata: a sampled frame was refused, so its set event was not sent",
		"name", ts.decl.Declaration().Name, "err", err)
}
