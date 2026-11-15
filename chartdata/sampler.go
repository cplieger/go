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
	// Logger takes the warnings of a track past the label sets or series it
	// holds and of a frame refused; nil means slog.Default. Run writes them on
	// the sampler's goroutine, so a slow handler never delays Series or Bins.
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
	newest  time.Time // the newest column's edge; zero before the first
	reg     *measure.Registry
	logger  *slog.Logger
	emit    func(FrameEvent)
	cols    *ring[int64]
	tracks  map[*Track]*tracked
	order   []*tracked
	decls   []measure.Declared
	pending []warning // the step's warnings, which Run logs once unlocked
	every   time.Duration
	keep    time.Duration
	start   int64 // the first column's edge in Unix milliseconds; 0 before it
	mu      sync.Mutex
	running atomic.Bool
}

// warning is a line for the Sampler's logger.
type warning struct {
	msg  string
	args []any
}

// tracked is a Track's state: its rings, one per key, and the label sets it
// read last step.
type tracked struct {
	*Track
	// values holds a value or quantile track's rings, aligned with the
	// sampler's columns.
	values map[string]*ring[float64]
	// hists holds an AsBins track's rings of bucket increases, +Inf last,
	// each holding the window's columns only.
	hists map[string]*ring[[]float64]
	// prev holds the label sets the track holds, read last step, each with a
	// counter's or histogram's cumulative reading.
	prev map[string][]float64
	// unseen holds a histogram's label sets read last step with no
	// observation yet, which hold no place but count from zero when they
	// first observe.
	unseen map[string]struct{}
	kind   measure.Kind
	byAt   int
	most   int // the most keys the track holds
	span   int // an AsBins ring's capacity, the window's columns
	// rebase makes the next read take no increase, since the steps before
	// it were missed.
	rebase bool
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
	points := steps(cfg.Keep, cfg.Every)
	if points > MaxPoints {
		return nil, fmt.Errorf("chartdata: Sampling.Keep holds %d steps of %v, past the most, %d", points, cfg.Every, MaxPoints)
	}
	s := &Sampler{
		reg: cfg.Registry, logger: cfg.Logger, emit: cfg.Emit, every: cfg.Every, keep: cfg.Keep,
		cols: newRing[int64](points), tracks: make(map[*Track]*tracked, len(tracks)),
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

// steps is how many steps of every span holds, a part step counted whole.
func steps(span, every time.Duration) int {
	n := int(span / every)
	if span%every != 0 {
		n++
	}
	return n
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
	if t.form == formBins {
		ts.hists = map[string]*ring[[]float64]{}
		ts.span = steps(t.window, s.every)
		ts.most = min(MaxLabelSets, MaxValues/max(1, len(binsOf(decl.Buckets))))
	} else {
		ts.values = map[string]*ring[float64]{}
		ts.most = min(MaxLabelSets, MaxValues/len(s.cols.buf))
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
	case t.form == formBins && slices.ContainsFunc(decl.Buckets, func(b float64) bool { return !isWithin(b) }):
		return fmt.Errorf("%s has a bucket bound beyond ±%g, which no BinsFrame carries", decl.Name, MaxMagnitude)
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
		next := s.after(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if err := s.wake(ctx, next, time.Now()); err != nil {
			return err
		}
	}
}

// wake takes the column a timer set for next and fired at now reads at, then
// logs its warnings and emits its events, unlocked.
func (s *Sampler) wake(ctx context.Context, next, now time.Time) error {
	events, warns, err := s.tick(ctx, s.due(next, now))
	if err != nil {
		return err
	}
	for _, w := range warns {
		s.log().Warn(w.msg, w.args...)
	}
	if s.emit != nil {
		for i := range events {
			s.emit(events[i])
		}
	}
	return nil
}

// after is the next column's edge: the first multiple of every after now,
// but never at or before the newest column, which a wall clock stepped back
// would give.
func (s *Sampler) after(now time.Time) time.Time {
	next := nextEdge(now, s.every)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.newest.IsZero() && !next.After(s.newest) {
		return s.newest.Add(s.every)
	}
	return next
}

// due is the edge a timer set for next and fired at now reads at: next, or,
// when the wait ran past later edges, such as in a paused process, the
// latest edge at or before now, so the reading is stamped when it was taken
// and the edges between are gaps.
func (s *Sampler) due(next, now time.Time) time.Time {
	if latest := nextEdge(now, s.every).Add(-s.every); latest.After(next) {
		return latest
	}
	return next
}

// nextEdge is the first multiple of every since the Unix epoch after now.
func nextEdge(now time.Time, every time.Duration) time.Time {
	step := int64(every)
	return time.Unix(0, (now.UnixNano()/step+1)*step)
}

// tick takes the column at edge, after a gap column for each edge missed
// since the newest, and returns the events they make and the warnings to
// log. edge must be after the newest column.
func (s *Sampler) tick(ctx context.Context, edge time.Time) ([]FrameEvent, []warning, error) {
	fams, err := s.reg.GatherOf(ctx, s.decls...)
	if fams == nil && err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	first := s.newest.IsZero()
	if first {
		s.start = edge.UnixMilli()
	}
	gaps := s.gaps(edge)
	s.newest = edge
	s.cols.push(edge.UnixMilli())
	var events []FrameEvent
	for _, ts := range s.order {
		fam := fams[slices.Index(s.decls, ts.decl)]
		before := ts.keys()
		for range gaps {
			ts.gap()
		}
		s.observe(ts, &fam)
		events = append(events, s.eventsOf(ts, before, first, s.cols.n-1-gaps)...)
	}
	warns := s.pending
	s.pending = nil
	return events, warns, nil
}

// gaps pushes a gap column for each edge missed between the newest column
// and edge, the most the ring holds, and returns how many it pushed. A track
// past missed edges takes no increase on its next read, which would cover
// several steps.
func (s *Sampler) gaps(edge time.Time) int {
	if s.newest.IsZero() {
		return 0
	}
	missed := int(edge.Sub(s.newest)/s.every) - 1
	if missed <= 0 {
		return 0
	}
	for _, ts := range s.order {
		ts.rebase = true
	}
	n := min(missed, len(s.cols.buf)-1)
	for k := n; k > 0; k-- {
		s.cols.push(edge.Add(-time.Duration(k) * s.every).UnixMilli())
	}
	return n
}

// observe reads ts's samples in fam into its rings, as the newest column.
func (s *Sampler) observe(ts *tracked, fam *measure.Family) {
	capacity, earlier := len(s.cols.buf), s.cols.n-1
	var dropped bool
	switch ts.form {
	case formValue:
		dropped = ts.pushValues(s.observeValues(ts, fam), capacity, earlier)
	case formQuantile:
		bounds := ts.decl.Declaration().Buckets
		sums := map[string]float64{}
		for key, cum := range s.observeHistograms(ts, fam) {
			sums[key] = quantile(ts.q, bounds, cum)
		}
		dropped = ts.pushValues(sums, capacity, earlier)
	case formBins:
		dropped = update(ts.hists, s.observeHistograms(ts, fam), ts.span, earlier, ts.most, nil, isNoBuckets)
	}
	ts.rebase = false
	if dropped {
		s.dropped(ts)
	}
}

// pushValues pushes sums onto a value or quantile track's rings, a sum that
// is not finite, or beyond ±MaxMagnitude, such as a By sum that overflows,
// as a gap, since no frame can carry it.
func (ts *tracked) pushValues(sums map[string]float64, capacity, earlier int) bool {
	for key, v := range sums {
		if !isWithin(v) {
			sums[key] = math.NaN()
		}
	}
	return update(ts.values, sums, capacity, earlier, ts.most, math.NaN(), math.IsNaN)
}

// gap pushes a gap column onto ts's rings.
func (ts *tracked) gap() {
	if ts.form == formBins {
		update(ts.hists, nil, 0, 0, 0, nil, isNoBuckets)
		return
	}
	update(ts.values, nil, 0, 0, 0, math.NaN(), math.IsNaN)
}

func isNoBuckets(v []float64) bool { return v == nil }

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

// reading is one sample a track reads, with its label set.
type reading struct {
	smp *measure.Sample
	set string
	// unseen marks a histogram's label set read last step with no
	// observation, whose increase counts from zero.
	unseen bool
}

// read returns the samples in fam ts reads: every label set Where keeps that
// the track holds, then new ones with a usable reading, in Gather's order,
// while fewer than MaxLabelSets are held, so a new label set never displaces
// a held one and one with no usable reading takes no place.
func (s *Sampler) read(ts *tracked, fam *measure.Family) []reading {
	var held, fresh []reading
	unseen := map[string]struct{}{}
	for i := range fam.Samples {
		smp := &fam.Samples[i]
		if ts.where != nil && !ts.where(smp.Labels) {
			continue
		}
		r := reading{smp: smp, set: labelSet(smp.Labels)}
		_, r.unseen = ts.unseen[r.set]
		switch _, ok := ts.prev[r.set]; {
		case ok:
			held = append(held, r)
		case usable(smp):
			fresh = append(fresh, r)
		case smp.Histogram != nil && len(unseen) < MaxLabelSets:
			unseen[r.set] = struct{}{}
		}
	}
	ts.unseen = unseen
	if room := MaxLabelSets - len(held); len(fresh) > room {
		fresh = fresh[:room]
		s.dropped(ts)
	}
	return append(held, fresh...)
}

// usable reports whether smp can give its key a value: a histogram reading
// with an observation, or a value within ±MaxMagnitude.
func usable(smp *measure.Sample) bool {
	if smp.Histogram != nil {
		return smp.Histogram.Count > 0
	}
	return isWithin(smp.Value)
}

// dropped queues, the first time for a track, the warning that it dropped a
// new label set or series.
func (s *Sampler) dropped(ts *tracked) {
	if ts.capped {
		return
	}
	ts.capped = true
	s.pending = append(s.pending, warning{
		msg:  "chartdata: a sampled track has more label sets or series than the sampler holds; new ones are dropped",
		args: []any{"name", ts.decl.Declaration().Name, "most_label_sets", MaxLabelSets, "most_series", ts.most},
	})
}

func (s *Sampler) log() *slog.Logger {
	if s.logger == nil {
		return slog.Default()
	}
	return s.logger
}

// observeValues returns the sum of ts's samples in fam for each key given a
// value: a gauge's value, or a counter's increase since its last reading. A
// reading that is not finite, or beyond ±MaxMagnitude, adds nothing, and its
// label set keeps its baseline, but none past missed edges.
func (s *Sampler) observeValues(ts *tracked, fam *measure.Family) map[string]float64 {
	sums := map[string]float64{}
	next := map[string][]float64{}
	for _, r := range s.read(ts, fam) {
		if v, ok := ts.valueOf(r, next); ok {
			sums[ts.keyOf(r.smp.Labels)] += v
		}
	}
	ts.prev = next
	return sums
}

// valueOf returns r's value for the column, a counter's increase, and puts
// the baseline r leaves in next; false when r gives the column no value.
func (ts *tracked) valueOf(r reading, next map[string][]float64) (float64, bool) {
	v := r.smp.Value
	prev := ts.prev[r.set]
	switch {
	case !isWithin(v):
		next[r.set] = ts.kept(prev)
		return 0, false
	case ts.kind != measure.KindCounter:
		next[r.set] = nil
		return v, true
	}
	next[r.set] = []float64{v}
	if prev == nil || ts.rebase {
		return 0, false
	}
	return increase(v, prev[0]), true
}

// kept is the baseline a label set keeps past a step that read it no
// value: none past missed edges, whose increase would cover several steps.
func (ts *tracked) kept(prev []float64) []float64 {
	if ts.rebase {
		return nil
	}
	return prev
}

// increase is a counter's increase from prev to cur, reset-aware as PromQL's
// increase is: a fall is a reset, and cur is then the increase.
func increase(cur, prev float64) float64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// observeHistograms returns the sum of the bucket increases of ts's samples
// in fam since their last reading for each key that observed any.
func (s *Sampler) observeHistograms(ts *tracked, fam *measure.Family) map[string][]float64 {
	sums := map[string][]float64{}
	next := map[string][]float64{}
	for _, r := range s.read(ts, fam) {
		cur := cumulative(r.smp.Histogram)
		next[r.set] = cur
		prev, ok := ts.prev[r.set]
		if r.unseen {
			prev, ok = make([]float64, len(cur)), true
		}
		if !ok || ts.rebase {
			continue
		}
		if delta := bucketIncrease(cur, prev); delta[len(delta)-1] > 0 {
			addInto(sums, ts.keyOf(r.smp.Labels), delta)
		}
	}
	ts.prev = next
	return sums
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
	sum := sums[key]
	if sum == nil {
		sums[key] = delta
		return
	}
	for k := range sum {
		sum[k] += delta[k]
	}
}

// update pushes each held key's value in sums onto its ring, a gap for a key
// with none, and drops a ring left with no usable point, however often its
// key is read. It then adds a ring of capacity for each new key with a
// value, in key order, while fewer than most are held, and reports whether
// it dropped one. A new key's ring starts with a gap per earlier column, so
// its indices match the older rings'.
func update[T any](rings map[string]*ring[T], sums map[string]T, capacity, earlier, most int, gap T, isGap func(T) bool) (dropped bool) {
	for key, r := range rings {
		v, ok := sums[key]
		if !ok {
			v = gap
		}
		r.push(v)
		if isGap(v) && allGaps(r, isGap) {
			delete(rings, key)
		}
	}
	var fresh []string
	for key, v := range sums {
		if _, held := rings[key]; !held && !isGap(v) {
			fresh = append(fresh, key)
		}
	}
	slices.Sort(fresh)
	for _, key := range fresh {
		if len(rings) >= most {
			return true
		}
		r := newRing[T](capacity)
		for range min(earlier, capacity-1) {
			r.push(gap)
		}
		r.push(sums[key])
		rings[key] = r
	}
	return false
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
	if ts.form == formBins {
		for k := range ts.hists {
			out = append(out, k)
		}
	} else {
		for k := range ts.values {
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
	if len(cum) < 2 {
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

// window returns the index of the first column after from.
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
// the process started inside it, the first column as its reset. It holds
// every series the track's rings hold, in the order the append events'
// values take, a series with no value in the span as gaps.
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
	keys := ts.keys()
	series := make([]Series, 0, len(keys))
	for _, key := range keys {
		r := ts.values[key]
		values := make([]float64, len(x))
		for i := range values {
			values[i] = r.at(first + i)
		}
		series = append(series, Series{Key: key, Values: values})
	}
	opts := []Option{WithDomain(Range{From: float64(from), To: float64(end)})}
	if s.start > from {
		opts = append(opts, WithResets(time.UnixMilli(s.start)))
	}
	return NewSeriesFrame(ts.decl, x, series, opts...)
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
	return ts.binsFrame()
}

// binsOf is the bins of a histogram's bounds: each finite bucket with a
// finite lower edge, the first from 0 when its bound is above 0.
func binsOf(bounds []float64) []Bin {
	lowest := 0
	if len(bounds) > 0 && bounds[0] <= 0 {
		lowest = 1
	}
	bins := make([]Bin, 0, max(0, len(bounds)-lowest))
	for i := lowest; i < len(bounds); i++ {
		bins = append(bins, Bin{From: below(bounds, i), To: bounds[i]})
	}
	return bins
}

func (ts *tracked) binsFrame() (BinsFrame, error) {
	bounds := ts.decl.Declaration().Buckets
	bins := binsOf(bounds)
	lowest := len(bounds) - len(bins)
	keys := ts.keys()
	series := make([]Series, 0, len(keys))
	for _, key := range keys {
		sum := sumColumns(ts.hists[key])
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

// sumColumns adds the bucket increases of r's columns. r must hold one:
// update drops an all-gap ring.
func sumColumns(r *ring[[]float64]) []float64 {
	var sum []float64
	for i := range r.n {
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

// eventsOf returns the events the columns from index from to the newest
// make for ts, whose keys were before ahead of them: a reset at the first
// column of the process, then an append of each column, or a set of the
// whole ring when its series changed; an AsBins track sets its window's
// frame on every step.
func (s *Sampler) eventsOf(ts *tracked, before []string, first bool, from int) []FrameEvent {
	key := ts.eventKey()
	var events []FrameEvent
	if first {
		events = append(events, ResetAt(key, time.UnixMilli(s.start)))
	}
	switch keys := ts.keys(); {
	case ts.form == formBins:
		f, err := ts.binsFrame()
		return append(events, s.set(ts, key, &f, err)...)
	case slices.Equal(keys, before):
		for i := from; i < s.cols.n; i++ {
			values := make([]*float64, len(keys))
			for k, name := range keys {
				if v := ts.values[name].at(i); !math.IsNaN(v) {
					values[k] = &v
				}
			}
			x := float64(s.cols.at(i))
			events = append(events, FrameEvent{Key: key, Kind: KindAppend, X: &x, Values: values})
		}
		return events
	default:
		f, err := s.seriesFrame(ts, s.keep)
		return append(events, s.set(ts, key, &f, err)...)
	}
}

// set returns the set event of f, or none when its constructor refused it
// with err. NewSampler admits only tracks whose frames every constructor
// takes, so err is a broken invariant, warned of once per track.
func (s *Sampler) set(ts *tracked, key string, f Frame, err error) []FrameEvent {
	if err != nil {
		if !ts.warned {
			ts.warned = true
			s.pending = append(s.pending, warning{
				msg:  "chartdata: a sampled frame was refused, so its set event was not sent",
				args: []any{"name", ts.decl.Declaration().Name, "err", err},
			})
		}
		return nil
	}
	e, _ := SetFrame(key, f) // refuses only a nil frame
	return []FrameEvent{e}
}
