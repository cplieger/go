package chartdata

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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
// 1, which Quantile estimates from the step's bucket increases.
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
	column  int   // the columns pushed, the newest's number
	mu      sync.Mutex
	running atomic.Bool
}

// warning is a line for the Sampler's logger.
type warning struct {
	msg  string
	args []any
}

// tracked is a Track's state: its rings, one per key, the label sets that
// hold a place, and the baselines of the label sets it read last step.
type tracked struct {
	*Track
	// values holds a value or quantile track's rings, aligned with the
	// sampler's columns.
	values map[string]*ring[float64]
	// hists holds an AsBins track's rings of bucket increases, +Inf last,
	// each holding the window's columns only.
	hists map[string]*ring[[]float64]
	// places holds the label sets whose points are in their key's ring, at
	// most MaxLabelSets.
	places map[string]place
	// bases holds a counter's or histogram's label sets read last step, at
	// most MaxBaselines, each with its last usable cumulative reading.
	bases    map[string]base
	kind     measure.Kind
	byAt     int
	most     int // the most keys the track holds
	capacity int // a ring's capacity: the sampler's columns, or an AsBins window's
	// mostSets and mostBases are MaxLabelSets and MaxBaselines. mostSets
	// must not pass mostBases: keepBases keeps every placed baseline, and
	// slicing its unplaced list past its length panics.
	mostSets  int
	mostBases int
	// rebase makes the next read take no increase, since the steps before
	// it were missed.
	rebase bool
	capped bool
	warned bool
}

// place is a label set's place among a track's MaxLabelSets: the key its
// points go to and the column of its newest point.
type place struct {
	key  string
	last int
}

// base is a label set's last usable cumulative reading, a counter's value or
// a histogram's buckets, and the column at which it last changed.
type base struct {
	cum     []float64
	changed int
}

// next is the base a usable reading of cum at column leaves: b, when cum
// equals it, or cum, changed at column.
func (b base) next(cum []float64, column int) base {
	if b.cum != nil && slices.Equal(b.cum, cum) {
		return b
	}
	return base{cum: cum, changed: column}
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
	ts := &tracked{
		Track: t, kind: decl.Kind, byAt: -1, places: map[string]place{}, bases: map[string]base{},
		mostSets: MaxLabelSets, mostBases: MaxBaselines,
	}
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
		ts.capacity = steps(t.window, s.every)
		ts.most = min(MaxLabelSets, MaxValues/max(1, len(binsOf(decl.Buckets))))
	} else {
		ts.values = map[string]*ring[float64]{}
		ts.capacity = len(s.cols.buf)
		ts.most = min(MaxLabelSets, MaxValues/ts.capacity)
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
	s.push(edge)
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
		s.push(edge.Add(-time.Duration(k) * s.every))
	}
	return n
}

// push adds the column at edge.
func (s *Sampler) push(edge time.Time) {
	s.cols.push(edge.UnixMilli())
	s.column++
}

// observe reads ts's samples in fam into its rings, as the newest column.
func (s *Sampler) observe(ts *tracked, fam *measure.Family) {
	ts.unplace()
	next := map[string]base{}
	earlier := s.cols.n - 1
	var dropped bool
	switch ts.form {
	case formValue:
		dropped = settle(ts, s.column, earlier, ts.values, ts.valuePoints(fam, next, s.column), valueShape)
	case formQuantile:
		sh := quantileShape(ts.q, ts.decl.Declaration().Buckets)
		dropped = settle(ts, s.column, earlier, ts.values, ts.bucketPoints(fam, next, s.column), sh)
	case formBins:
		dropped = settle(ts, s.column, earlier, ts.hists, ts.bucketPoints(fam, next, s.column), binsShape)
	}
	ts.rebase = false
	ts.unplace()
	s.keepBases(ts, next) // after settle, so the step's places keep their baselines
	if dropped {
		s.dropped(ts)
	}
}

// point is a label set's share of its key's newest column.
type point[P any] struct {
	v   P
	set string
	key string
}

// shape is how a track's points sum into the value its ring holds.
type shape[P, T any] struct {
	gap   T
	add   func(sum, v P) P
	value func(sum P) T // the ring's value of a key's sum
	isGap func(T) bool
}

// valueShape sums a value track's points, a sum no frame can carry, beyond
// ±MaxMagnitude, such as a By sum that overflows, a gap.
var valueShape = shape[float64, float64]{
	gap: math.NaN(), isGap: math.IsNaN, value: carried,
	add: func(sum, v float64) float64 { return sum + v },
}

// binsShape sums an AsBins track's bucket increases.
var binsShape = shape[[]float64, []float64]{
	add: addBuckets, value: func(sum []float64) []float64 { return sum }, isGap: isNoBuckets,
}

// quantileShape sums a quantile track's bucket increases and takes their
// q-quantile over bounds.
func quantileShape(q float64, bounds []float64) shape[[]float64, float64] {
	return shape[[]float64, float64]{
		gap: math.NaN(), isGap: math.IsNaN, add: addBuckets,
		value: func(sum []float64) float64 { return carried(quantileOf(q, bounds, sum)) },
	}
}

// carried is v, or NaN, a gap, when no frame carries v.
func carried(v float64) float64 {
	if !isWithin(v) {
		return math.NaN()
	}
	return v
}

func isNoBuckets(v []float64) bool { return v == nil }

// settle pushes the column at column onto rings from pts, the step's points
// in Gather's order, and reports whether it dropped a label set or key. It
// gives places, at most mostSets, first to label sets holding one, then to
// new label sets of keys holding a ring, then to new keys' label sets, in key
// order, while fewer than most rings are held. While a held key's sum is a
// gap, the gap keys finalGaps picks lose their points and the step's places
// are settled again from the start, so their places go to the others.
func settle[P, T any](ts *tracked, column, earlier int, rings map[string]*ring[T], pts []point[P], sh shape[P, T]) bool {
	var held, fresh []point[P]
	for _, p := range pts {
		if _, ok := rings[p.key]; ok {
			held = append(held, p)
		} else {
			fresh = append(fresh, p)
		}
	}
	before := maps.Clone(ts.places)
	sums, refused := placeHeld(ts, column, held, sh.add)
	for gaps := gapKeys(sums, sh); len(gaps) > 0; gaps = gapKeys(sums, sh) {
		drop := finalGaps(gaps, refused)
		held = slices.DeleteFunc(held, func(p point[P]) bool { return drop[p.key] })
		ts.places = maps.Clone(before)
		sums, refused = placeHeld(ts, column, held, sh.add)
	}
	dropped := len(refused) > 0
	for key, r := range rings {
		v := sh.gap
		if sum, ok := sums[key]; ok {
			v = sh.value(sum)
		}
		r.push(v)
		if sh.isGap(v) && allGaps(r, sh.isGap) {
			delete(rings, key)
		}
	}
	return addFresh(ts, column, earlier, rings, fresh, sh) || dropped
}

// gapKeys holds the keys of sums whose sum is a gap.
func gapKeys[P, T any](sums map[string]P, sh shape[P, T]) map[string]bool {
	gaps := map[string]bool{}
	for key, sum := range sums {
		if sh.isGap(sh.value(sum)) {
			gaps[key] = true
		}
	}
	return gaps
}

// finalGaps is the gap keys to drop first: those refused no label set, whose
// sums no freed place can change, or, when every one was refused one, the
// first in key order, since a place another gap key frees may end its gap.
func finalGaps(gaps, refused map[string]bool) map[string]bool {
	final := map[string]bool{}
	for key := range gaps {
		if !refused[key] {
			final[key] = true
		}
	}
	if len(final) == 0 {
		final[slices.Min(slices.Collect(maps.Keys(gaps)))] = true
	}
	return final
}

// placeHeld sums by key the points of pts, all of keys holding a ring, whose
// label sets hold or take a place, and returns the keys it refused a label
// set a place. Each held place is renewed before release runs, so a place is
// never freed in the step its label set gives a point, whatever the ring's
// capacity.
func placeHeld[P any](ts *tracked, column int, pts []point[P], add func(sum, v P) P) (sums map[string]P, refused map[string]bool) {
	refused = map[string]bool{}
	sums = map[string]P{}
	for _, p := range pts {
		if _, ok := ts.places[p.set]; ok {
			ts.places[p.set] = place{key: p.key, last: column}
			addTo(sums, p, add)
		}
	}
	ts.release(column)
	for _, p := range pts {
		if _, ok := ts.places[p.set]; ok {
			continue
		}
		if len(ts.places) >= ts.mostSets {
			refused[p.key] = true
			continue
		}
		ts.places[p.set] = place{key: p.key, last: column}
		addTo(sums, p, add)
	}
	return sums, refused
}

// addFresh gives each new key of pts a ring, in key order, while fewer than
// most rings are held, with the label sets that fit in the places left, in
// Gather's order; a key whose sum is a gap takes neither. It reports whether
// it dropped a key or label set.
func addFresh[P, T any](ts *tracked, column, earlier int, rings map[string]*ring[T], pts []point[P], sh shape[P, T]) (dropped bool) {
	byKey := map[string][]point[P]{}
	for _, p := range pts {
		byKey[p.key] = append(byKey[p.key], p)
	}
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		all := byKey[key]
		taken := all[:min(len(all), max(0, ts.mostSets-len(ts.places)))]
		if len(taken) == 0 {
			dropped = true
			continue
		}
		sum := taken[0].v
		for _, p := range taken[1:] {
			sum = sh.add(sum, p.v)
		}
		v := sh.value(sum)
		if sh.isGap(v) {
			continue
		}
		if len(rings) >= ts.most {
			dropped = true
			continue
		}
		dropped = dropped || len(taken) < len(all)
		r := newRing[T](ts.capacity)
		for range min(earlier, ts.capacity-1) {
			r.push(sh.gap)
		}
		r.push(v)
		rings[key] = r
		for _, p := range taken {
			ts.places[p.set] = place{key: key, last: column}
		}
	}
	return dropped
}

// addTo adds p's value to its key's sum.
func addTo[P any](sums map[string]P, p point[P], add func(sum, v P) P) {
	if sum, ok := sums[p.key]; ok {
		sums[p.key] = add(sum, p.v)
		return
	}
	sums[p.key] = p.v
}

// release frees the place of each label set whose newest point has left its
// key's ring by column.
func (ts *tracked) release(column int) {
	for set, p := range ts.places {
		if column-p.last >= ts.capacity {
			delete(ts.places, set)
		}
	}
}

// unplace frees the place of each label set whose key holds no ring, such as
// one whose ring missed edges left with no usable point, or whose sum was a
// gap.
func (ts *tracked) unplace() {
	for set, p := range ts.places {
		if !ts.holds(p.key) {
			delete(ts.places, set)
		}
	}
}

// holds reports whether ts holds a ring for key.
func (ts *tracked) holds(key string) bool {
	if ts.form == formBins {
		_, ok := ts.hists[key]
		return ok
	}
	_, ok := ts.values[key]
	return ok
}

// gap pushes a gap column onto ts's rings, and drops a ring it leaves with
// no usable point.
func (ts *tracked) gap() {
	if ts.form == formBins {
		pushGap(ts.hists, nil, isNoBuckets)
		return
	}
	pushGap(ts.values, math.NaN(), math.IsNaN)
}

func pushGap[T any](rings map[string]*ring[T], gap T, isGap func(T) bool) {
	for key, r := range rings {
		r.push(gap)
		if allGaps(r, isGap) {
			delete(rings, key)
		}
	}
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

// reading is one sample a track reads, with its label set.
type reading struct {
	smp *measure.Sample
	set string
}

// read returns the samples in fam that Where keeps, in Gather's order.
func (ts *tracked) read(fam *measure.Family) []reading {
	var out []reading
	for i := range fam.Samples {
		smp := &fam.Samples[i]
		if ts.where == nil || ts.where(smp.Labels) {
			out = append(out, reading{smp: smp, set: labelSet(smp.Labels)})
		}
	}
	return out
}

// keepBases makes next ts's baselines. Past mostBases it forgets the
// baselines of label sets holding no place, the least recently changed
// first, then in label-set order: a label set holding a place never loses
// its baseline.
func (s *Sampler) keepBases(ts *tracked, next map[string]base) {
	if over := len(next) - ts.mostBases; over > 0 {
		var unplaced []string
		for set := range next {
			if _, ok := ts.places[set]; !ok {
				unplaced = append(unplaced, set)
			}
		}
		slices.SortFunc(unplaced, func(a, b string) int {
			return cmp.Or(cmp.Compare(next[a].changed, next[b].changed), strings.Compare(a, b))
		})
		for _, set := range unplaced[:over] {
			delete(next, set)
		}
		s.dropped(ts)
	}
	ts.bases = next
}

// dropped queues, the first time for a track, the warning that it dropped a
// new label set or series, or forgot a baseline.
func (s *Sampler) dropped(ts *tracked) {
	if ts.capped {
		return
	}
	ts.capped = true
	s.pending = append(s.pending, warning{
		msg:  "chartdata: a sampled track has more label sets or series than the sampler holds; new ones are dropped, or old baselines forgotten",
		args: []any{"name", ts.decl.Declaration().Name, "most_label_sets", ts.mostSets, "most_series", ts.most, "most_baselines", ts.mostBases},
	})
}

func (s *Sampler) log() *slog.Logger {
	if s.logger == nil {
		return slog.Default()
	}
	return s.logger
}

// valuePoints returns the points of ts's samples in fam, a gauge's value or a
// counter's increase since its baseline, and puts in next the baseline each
// leaves. A reading that is not finite, or beyond ±MaxMagnitude, gives no
// point and leaves its label set's baseline as it was, but none past missed
// edges, whose increase would cover several steps.
func (ts *tracked) valuePoints(fam *measure.Family, next map[string]base, column int) []point[float64] {
	var pts []point[float64]
	for _, r := range ts.read(fam) {
		if v, ok := ts.valueOf(r, next, column); ok {
			pts = append(pts, point[float64]{v: v, set: r.set, key: ts.keyOf(r.smp.Labels)})
		}
	}
	return pts
}

// valueOf returns r's point at column, a counter's increase, and puts the
// baseline r leaves in next; false when r gives no point.
func (ts *tracked) valueOf(r reading, next map[string]base, column int) (float64, bool) {
	v := r.smp.Value
	if ts.kind != measure.KindCounter {
		return v, isWithin(v)
	}
	b, ok := ts.bases[r.set]
	if !isWithin(v) {
		if ok && !ts.rebase {
			next[r.set] = b
		}
		return 0, false
	}
	next[r.set] = b.next([]float64{v}, column)
	if !ok || ts.rebase {
		return 0, false
	}
	return increase(v, b.cum[0]), true
}

// increase is a counter's increase from prev to cur, reset-aware as PromQL's
// increase is: a fall is a reset, and cur is then the increase.
func increase(cur, prev float64) float64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// bucketPoints returns the points of ts's samples in fam, the bucket
// increases of each that observed since its baseline, and puts in next the
// baseline each leaves.
func (ts *tracked) bucketPoints(fam *measure.Family, next map[string]base, column int) []point[[]float64] {
	var pts []point[[]float64]
	for _, r := range ts.read(fam) {
		cur := cumulative(r.smp.Histogram)
		b, ok := ts.bases[r.set]
		next[r.set] = b.next(cur, column)
		if !ok || ts.rebase {
			continue
		}
		if delta := bucketIncrease(cur, b.cum); delta[len(delta)-1] > 0 {
			pts = append(pts, point[[]float64]{v: delta, set: r.set, key: ts.keyOf(r.smp.Labels)})
		}
	}
	return pts
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

// addBuckets is sum plus v, bucket by bucket, in a new slice: a key's sum
// starts as its first point's slice, and settle sums the same points again.
func addBuckets(sum, v []float64) []float64 {
	out := slices.Clone(sum)
	for k := range out {
		out[k] += v[k]
	}
	return out
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

// quantileOf is Quantile of the cumulative bucket counts cum, +Inf last, NaN
// where it answers none. A count below the one before it adds nothing, as
// histogram_quantile's monotonic fix takes it.
func quantileOf(q float64, bounds, cum []float64) float64 {
	counts := make([]float64, len(cum))
	top := 0.0
	for i, c := range cum {
		if c > top {
			counts[i], top = c-top, c
		}
	}
	v, ok := Quantile(bounds, counts, q)
	if !ok {
		return math.NaN()
	}
	return v
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
// settle and gap drop an all-gap ring.
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
