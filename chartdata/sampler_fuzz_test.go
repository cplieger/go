package chartdata

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/go/measure/v2"
)

// refSampler is one track of a Sampler written as plainly as its rules
// read, for FuzzSampler_matchesItsReference: a ring is a slice of its last
// capacity columns, nil for a gap, and a value track's entries hold one
// value.
type refSampler struct {
	rings                                                map[string][][]float64
	places                                               map[string]place
	bases                                                map[string]base
	name                                                 string
	bounds                                               []float64
	cols                                                 []int64
	kind                                                 measure.Kind
	form                                                 form
	q                                                    float64
	every, keepCols, capacity, mostSets, mostBases, most int
	column                                               int
	by, rebase                                           bool
}

// lastOf is the last n of s.
func lastOf[T any](s []T, n int) []T { return s[max(0, len(s)-n):] }

func (m *refSampler) push(edge int64, sums map[string][]float64) {
	m.cols = lastOf(append(m.cols, edge), m.keepCols)
	m.column++
	for key, r := range m.rings {
		m.rings[key] = lastOf(append(r, m.value(sums[key])), m.capacity)
		if !slices.ContainsFunc(m.rings[key], func(v []float64) bool { return v != nil }) {
			delete(m.rings, key)
		}
	}
}

func (m *refSampler) step(edge int64, fam *measure.Family) {
	if len(m.cols) > 0 {
		if missed := int((edge-m.cols[len(m.cols)-1])/int64(m.every)) - 1; missed > 0 {
			m.rebase = true
			for k := min(missed, m.keepCols-1); k > 0; k-- {
				m.push(edge-int64(k*m.every), nil)
			}
		}
	}
	m.unplace()
	m.column++ // the step's column, pushed below
	type pt struct {
		set, key string
		v        []float64
	}
	next := map[string]base{}
	var pts []pt
	for i := range fam.Samples {
		smp := &fam.Samples[i]
		key := m.name
		if m.by {
			key = smp.Labels[0].Value
		}
		if v := m.point(smp, labelSet(smp.Labels), next); v != nil {
			pts = append(pts, pt{labelSet(smp.Labels), key, v})
		}
	}
	sums := map[string][]float64{}
	take := func(p pt) {
		m.places[p.set] = place{key: p.key, last: m.column}
		sums[p.key] = plus(sums[p.key], p.v)
	}
	held := map[string]bool{}
	for key := range m.rings {
		held[key] = true
	}
	release := func() {
		for set, pl := range m.places {
			if m.column-pl.last >= m.capacity {
				delete(m.places, set)
			}
		}
	}
	before := maps.Clone(m.places)
	gapKeys := map[string]bool{}
	for {
		// A place held with a point is kept, then one whose newest point has
		// left its ring is freed, then new label sets of held keys take the
		// places left. The step starts over without the keys whose value is a
		// gap and that were refused no place, or else without the first gap
		// key in key order.
		m.places = maps.Clone(before)
		sums = map[string][]float64{}
		refused := map[string]bool{}
		for _, p := range pts {
			if _, ok := m.places[p.set]; ok && !gapKeys[p.key] {
				take(p)
			}
		}
		release()
		for _, p := range pts {
			if _, ok := m.places[p.set]; ok || !held[p.key] || gapKeys[p.key] {
				continue
			}
			if len(m.places) < m.mostSets {
				take(p)
			} else {
				refused[p.key] = true
			}
		}
		var gaps, final []string
		for key, sum := range sums {
			if m.value(sum) == nil {
				gaps = append(gaps, key)
				if !refused[key] {
					final = append(final, key)
				}
			}
		}
		if len(gaps) == 0 {
			break
		}
		if len(final) == 0 {
			slices.Sort(gaps)
			final = gaps[:1]
		}
		for _, key := range final {
			gapKeys[key] = true
		}
	}
	m.column--
	m.push(edge, sums)
	// New keys in key order, while fewer than most rings are held.
	fresh := map[string]bool{}
	for _, p := range pts {
		if !held[p.key] {
			fresh[p.key] = true
		}
	}
	for _, key := range slices.Sorted(maps.Keys(fresh)) {
		var taken []pt
		for _, p := range pts {
			if p.key == key && len(taken) < m.mostSets-len(m.places) {
				taken = append(taken, p)
				sums[key] = plus(sums[key], p.v)
			}
		}
		if v := m.value(sums[key]); v != nil && len(m.rings) < m.most {
			m.rings[key] = append(make([][]float64, min(len(m.cols)-1, m.capacity-1)), v)
			for _, p := range taken {
				m.places[p.set] = place{key: key, last: m.column}
			}
		}
	}
	m.rebase = false
	m.unplace()
	// Past mostBases, the baselines of label sets holding no place go, the
	// least recently changed first.
	for len(next) > m.mostBases {
		var oldest string
		for set, b := range next {
			_, placed := m.places[set]
			if o, ok := next[oldest]; !placed && (!ok || b.changed < o.changed || b.changed == o.changed && set < oldest) {
				oldest = set
			}
		}
		delete(next, oldest)
	}
	m.bases = next
}

func (m *refSampler) unplace() {
	for set, p := range m.places {
		if m.rings[p.key] == nil {
			delete(m.places, set)
		}
	}
}

// point is a sample's point, nil for none, and puts its baseline in next.
func (m *refSampler) point(smp *measure.Sample, set string, next map[string]base) []float64 {
	cur := []float64{smp.Value}
	if h := smp.Histogram; h != nil {
		cur = nil
		for _, b := range h.Buckets {
			cur = append(cur, float64(b.Count))
		}
		cur = append(cur, float64(h.Count))
	}
	last := len(cur) - 1
	b, had := m.bases[set]
	switch {
	case m.kind == measure.KindGauge && isWithin(cur[0]):
		return cur
	case m.kind == measure.KindGauge:
		return nil
	case !isWithin(cur[last]):
		if had && !m.rebase {
			next[set] = b
		}
		return nil
	case had && slices.Equal(b.cum, cur):
		next[set] = b
	default:
		next[set] = base{cum: cur, changed: m.column}
	}
	if !had || m.rebase {
		return nil
	}
	delta := slices.Clone(cur)
	for k := range delta {
		if cur[last] >= b.cum[last] { // else a reset: the increase is cur
			delta[k] -= b.cum[k]
		}
	}
	if m.kind == measure.KindHistogram && delta[last] == 0 {
		return nil
	}
	return delta
}

// plus is sum + v, entry by entry, v when sum is nil.
func plus(sum, v []float64) []float64 {
	out := slices.Clone(v)
	for k := range sum {
		out[k] += sum[k]
	}
	return out
}

// value is the ring entry of a key's summed points, nil for a gap.
func (m *refSampler) value(sum []float64) []float64 {
	if sum == nil || m.form == formBins {
		return sum
	}
	v := sum[0]
	if m.form == formQuantile {
		v = quantile(m.q, m.bounds, sum)
	}
	if !isWithin(v) {
		return nil
	}
	return []float64{v}
}

// frame is the reference's frame by key: a value track's columns, NaN for a
// gap, or an AsBins track's bins.
func (m *refSampler) frame() map[string][]float64 {
	out := map[string][]float64{}
	for key, r := range m.rings {
		var sum []float64
		for _, v := range r {
			switch {
			case m.form == formBins && v != nil:
				sum = plus(sum, v)
			case m.form != formBins && v == nil:
				out[key] = append(out[key], math.NaN())
			case m.form != formBins:
				out[key] = append(out[key], v[0])
			}
		}
		for i := range m.bounds {
			if sum != nil && (i > 0 || m.bounds[0] > 0) {
				out[key] = append(out[key], max(0, sum[i]-below(sum, i)))
			}
		}
	}
	return out
}

// scenario is one random run of FuzzSampler_matchesItsReference: a track of
// one kind over label sets of keys k0 to k3 and hosts h0 to h3, with caps
// small enough for 16 label sets to cross them.
type scenario struct {
	rng    *rand.Rand
	desc   *measure.Desc
	totals map[string]float64  // a counter's total, by label set
	obs    map[string][]uint64 // a histogram's observations per bucket, +Inf last
	emit   map[string]float64  // what a counter or gauge emits at this step
	bounds []float64
	kind   measure.Kind
}

func (sc *scenario) collect(_ context.Context, e *measure.Emitter) {
	for _, set := range slices.Sorted(maps.Keys(sc.emit)) {
		k, h := set[:2], set[2:]
		if sc.kind != measure.KindHistogram {
			e.Emit(sc.desc, sc.emit[set], k, h)
			continue
		}
		cum := make([]uint64, len(sc.bounds))
		var count uint64
		for i, n := range sc.obs[set] {
			count += n
			if i < len(cum) {
				cum[i] = count
			}
		}
		e.EmitHistogram(sc.desc, cum, count, 0, k, h)
	}
}

// next picks the label sets read at the next step and their readings:
// churn, NaN, ±Inf and 1e301, gauges of keys whose sums pass 1e300 or not by
// which label sets hold a place, counter and histogram resets, histograms
// that stop observing, and, past a bound of 1e301, quantiles that are gaps
// beside quantiles that are not.
func (sc *scenario) next() {
	odd := []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1e301}
	present := []float64{0.2, 0.6, 0.95}[sc.rng.IntN(3)]
	sc.emit = map[string]float64{}
	for k := range 4 {
		huge := sc.rng.IntN(2) == 0
		for h := range 4 {
			set := "k" + strconv.Itoa(k) + "h" + strconv.Itoa(h)
			if sc.rng.Float64() >= present {
				continue
			}
			switch sc.kind {
			case measure.KindGauge:
				sc.emit[set] = float64(sc.rng.IntN(4))
				switch {
				case sc.rng.IntN(8) == 0:
					sc.emit[set] = odd[sc.rng.IntN(len(odd))]
				case huge:
					sc.emit[set] = []float64{6e299, 6e299, -6e299}[sc.rng.IntN(3)]
				}
			case measure.KindCounter:
				if sc.rng.IntN(20) == 0 {
					sc.totals[set] = 0
				}
				sc.totals[set] += float64(sc.rng.IntN(3))
				sc.emit[set] = sc.totals[set]
				if sc.rng.IntN(8) == 0 {
					sc.emit[set] = odd[sc.rng.IntN(len(odd))]
				}
			default:
				if sc.obs[set] == nil || sc.rng.IntN(20) == 0 {
					sc.obs[set] = make([]uint64, len(sc.bounds)+1)
				}
				n := max(0, sc.rng.IntN(5)-2)
				if sc.bounds[1] == 1e301 && k < 2 {
					n = 1 + sc.rng.IntN(3)
				}
				for range n {
					bucket := sc.rng.IntN(len(sc.bounds) + 1)
					if sc.bounds[1] == 1e301 && k < 2 && bucket == 1 {
						bucket = 0 // below 1 or past 1e301: a median that is no gap
					}
					sc.obs[set][bucket]++
				}
				sc.emit[set] = 0
			}
		}
	}
}

// A Sampler and refSampler fed the same random steps hold the same frame,
// places and baselines after every step: label sets churning past both
// caps, unusable readings, sums that are gaps, resets, idle histograms, Keep
// from Every to four steps, a clock that skips edges or steps back, and late
// timers.
func FuzzSampler_matchesItsReference(f *testing.F) {
	for seed := range uint64(300) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		rng := rand.New(rand.NewPCG(seed, seed>>32|1))
		forms := []form{formValue, formValue, formQuantile, formBins}
		kinds := []measure.Kind{measure.KindGauge, measure.KindCounter, measure.KindHistogram, measure.KindHistogram}
		pick := rng.IntN(len(forms))
		every := time.Minute
		keepCols := 1 + rng.IntN(4)
		m := &refSampler{
			rings: map[string][][]float64{}, places: map[string]place{}, bases: map[string]base{},
			name: "runs", kind: kinds[pick], form: forms[pick], q: []float64{0, 0.5, 0.95}[rng.IntN(3)],
			bounds: [][]float64{{1, 2, 4}, {0, 1, 3}}[rng.IntN(2)], every: int(every.Milliseconds()),
			keepCols: keepCols, capacity: keepCols, mostSets: 2 + rng.IntN(5), by: rng.IntN(5) > 0,
		}
		if m.form == formQuantile && rng.IntN(2) == 0 {
			m.bounds = []float64{1, 1e301} // a quantile past 1e300 is a gap
		}
		m.mostBases = m.mostSets + rng.IntN(6)
		m.most = 1 + rng.IntN(m.mostSets)
		sc := &scenario{rng: rng, kind: m.kind, bounds: m.bounds, totals: map[string]float64{}, obs: map[string][]uint64{}}
		opts := []measure.Option{measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("k", "h")}
		labels := []string{"k", "h"}
		switch m.kind {
		case measure.KindGauge:
			sc.desc = measure.NewGaugeDesc(m.name, "h", measure.UnitCount, labels, opts...)
		case measure.KindCounter:
			sc.desc = measure.NewCounterDesc(m.name, "h", measure.UnitCount, labels, opts...)
		default:
			sc.desc = measure.NewHistogramDesc(m.name, "h", measure.UnitSeconds, labels, m.bounds, opts...)
		}
		reg := measure.NewRegistry("", measure.WithLogger(slog.New(slog.DiscardHandler)))
		reg.MustRegister(measure.NewCollector(sc.collect, sc.desc))
		var trackOpts []TrackOption
		if m.by {
			trackOpts = append(trackOpts, By("k"))
		}
		switch m.form {
		case formQuantile:
			trackOpts = append(trackOpts, AsQuantile(m.q))
		case formBins:
			m.capacity = 1 + rng.IntN(keepCols)
			trackOpts = append(trackOpts, AsBins(time.Duration(m.capacity)*every))
		}
		track := Sampled(sc.desc, trackOpts...)
		s, err := NewSampler(Sampling{Registry: reg, Every: every, Keep: time.Duration(keepCols) * every, Logger: slog.New(slog.DiscardHandler)}, track)
		if err != nil {
			t.Fatalf("NewSampler: %v", err)
		}
		ts := s.tracks[track]
		ts.mostSets, ts.mostBases, ts.most = m.mostSets, m.mostBases, m.most
		now := time.Unix(1_000_000*60, 0)
		for i := range 40 {
			sc.next()
			// Run's wait: set for the next edge after now, firing on time or
			// late, as in a paused process.
			next := s.after(now)
			fired := next
			if rng.IntN(8) == 0 {
				fired = next.Add(time.Duration(rng.IntN(keepCols+2))*every + every/3)
			}
			edge := s.due(next, fired)
			if _, _, err := s.tick(t.Context(), edge); err != nil {
				t.Fatalf("tick: %v", err)
			}
			fams, err := reg.GatherOf(t.Context(), sc.desc) // err names the samples measure drops
			if fams == nil {
				t.Fatalf("GatherOf: %v", err)
			}
			m.step(edge.UnixMilli(), &fams[0])
			if diff := differ(s, ts, m); diff != "" {
				t.Fatalf("seed %d, step %d (%+v): %s", seed, i, *m, diff)
			}
			jump := 1
			switch r := rng.IntN(10); {
			case r == 0:
				jump = -rng.IntN(3) // the clock steps back
			case r <= 2:
				jump = 2 + rng.IntN(keepCols+2) // edges missed, as by a slow step
			}
			now = edge.Add(time.Duration(jump)*every - every/2)
		}
	})
}

// differ describes where s's track ts and the reference m disagree, or is
// empty.
func differ(s *Sampler, ts *tracked, m *refSampler) string {
	got := map[string][]float64{}
	if m.form == formBins {
		f, err := s.Bins(ts.Track)
		if err != nil {
			return "Bins: " + err.Error()
		}
		for _, sr := range f.Series {
			got[sr.Key] = plainValues(sr.Values)
		}
	} else {
		f, err := s.Series(ts.Track, time.Duration(m.keepCols)*time.Minute)
		if err != nil {
			return "Series: " + err.Error()
		}
		x := make([]int64, len(f.X))
		for i, v := range f.X {
			x[i] = int64(v)
		}
		if !slices.Equal(x, m.cols) {
			return fmt.Sprintf("columns %v, want %v", x, m.cols)
		}
		for _, sr := range f.Series {
			got[sr.Key] = plainValues(sr.Values)
		}
	}
	want := m.frame()
	if !maps.EqualFunc(got, want, func(a, b []float64) bool { return slices.EqualFunc(a, b, sameFloat) }) {
		return fmt.Sprintf("frame %v, want %v", got, want)
	}
	if !maps.Equal(ts.places, m.places) {
		return fmt.Sprintf("places %v, want %v", ts.places, m.places)
	}
	sameBase := func(a, b base) bool { return a.changed == b.changed && slices.Equal(a.cum, b.cum) }
	if !maps.EqualFunc(ts.bases, m.bases, sameBase) {
		return fmt.Sprintf("baselines %v, want %v", ts.bases, m.bases)
	}
	return ""
}
