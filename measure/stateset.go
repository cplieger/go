package measure

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"unicode/utf8"
)

// noState is a state set series' value before its first Set: every state 0.
const noState = -1

// StateSet is a set of states of which one is current: its series is 1 for
// the current state's label value and 0 for each other state, one state set
// per combination of its other labels. It renders as a gauge.
type StateSet struct {
	vals   map[labelKey]*atomic.Int64 // by the other labels' values: the current state's index, or noState
	help   string
	err    error // construction-time validation error; surfaces at registration
	labels []string
	states []string
	order  []int // state indexes sorted by state value, the page's order
	meta   declMeta
	series
	registered atomic.Bool
	warned     atomic.Bool // one-time inert-record warning emitted
}

// NewStateSet creates a state set. Its last label is the state label, whose
// values are states, and any labels before it identify one state set each,
// such as a provider whose health the states name. With only the state
// label, every state starts at 0; with more, [WithSeed] takes the other
// labels' values and starts each such set at 0. A state set's unit is
// [UnitNone]. No labels, no states, an empty or repeated state or one that
// is not valid UTF-8 is a construction error, as is any error
// [NewLabeledGauge] captures.
func NewStateSet(name, help string, labels, states []string, opts ...Option) *StateSet {
	help = sanitizeHelp(name, help)
	o := applyOptions(opts)
	owned, err := checkNameAndLabels("StateSet", name, labels)
	s := &StateSet{
		help:   help,
		labels: owned,
		states: slices.Clone(states),
		vals:   make(map[labelKey]*atomic.Int64),
	}
	s.name = name
	s.meta.fill(name, help, KindStateSet, UnitNone, owned, &o)
	s.meta.states = s.states
	s.err = firstErr(err, checkStates(name, owned, s.states), checkOptionFits(name, &o, false, len(owned)-1),
		checkDeclaration(name, KindStateSet, UnitNone, owned, &o))
	if s.err != nil {
		return s
	}
	s.order = make([]int, len(s.states))
	for i := range s.order {
		s.order[i] = i
	}
	slices.SortFunc(s.order, func(a, b int) int { return cmp.Compare(s.states[a], s.states[b]) })
	if len(owned) == 1 {
		s.seed(nil)
	}
	for _, set := range o.seeds {
		s.seed(set)
	}
	return s
}

func checkStates(name string, labels, states []string) error {
	switch {
	case len(labels) == 0:
		return fmt.Errorf("measure: state set %q has no state label", name)
	case len(states) == 0:
		return fmt.Errorf("measure: state set %q declares no states", name)
	}
	for i, st := range states {
		if st == "" {
			// Prometheus stores an empty label value as no label, so this state's series would carry no state label.
			return fmt.Errorf("measure: state set %q declares an empty state", name)
		}
		if !utf8.ValidString(st) {
			return fmt.Errorf("measure: state set %q declares state %q, which is not valid UTF-8", name, st)
		}
		if slices.Contains(states[:i], st) {
			return fmt.Errorf("measure: state set %q declares state %q twice", name, st)
		}
	}
	return nil
}

func (s *StateSet) seed(set []string) {
	key := labelKeyFor(s.labels[:len(s.labels)-1], set)
	s.loadOrStore(s.vals, &key, func() *atomic.Int64 {
		v := &atomic.Int64{}
		v.Store(noState)
		return v
	})
}

// Declaration returns the state set's metadata.
func (s *StateSet) Declaration() Declaration { return s.meta.declaration() }

func (s *StateSet) declared() *declMeta {
	if s == nil {
		return nil
	}
	return &s.meta
}

// Set makes state current for the state set that labelVals, one value per
// label before the state label, identify: a scrape sees the old state or the
// new one, never both or neither. It panics on a label-arity mismatch or a
// state the set does not declare. A state set carrying a construction error
// records nothing (one warning on the first drop).
func (s *StateSet) Set(state string, labelVals ...string) {
	if s.err != nil {
		warnInertOnce(s.err, &s.warned, &s.name)
		return
	}
	idx := int64(slices.Index(s.states, state))
	if idx < 0 {
		panic("measure: StateSet.Set called with a state the set does not declare")
	}
	key := labelKeyFor(s.labels[:len(s.labels)-1], labelVals)
	if v, loaded := s.loadOrStore(s.vals, &key,
		func() *atomic.Int64 { v := &atomic.Int64{}; v.Store(idx); return v }); loaded {
		v.Store(idx)
	}
}

// Delete removes every state of the state set that labelVals identify. It
// panics on a label-arity mismatch. Values are sanitized as Set sanitizes
// them. A state set carrying a construction error has no series, so Delete
// is a no-op.
func (s *StateSet) Delete(labelVals ...string) {
	if s.err != nil {
		return
	}
	s.deleteSeries(s.vals, s.labels[:len(s.labels)-1], labelVals)
}

// eachSample calls f with each series' label values in declared order and
// its value, in the page's order.
func (s *StateSet) eachSample(f func(key *labelKey, value float64)) {
	keys := s.sortedLabelKeys(s.vals)
	stateAt := len(s.labels) - 1
	for i := range keys {
		s.mu.RLock()
		v := s.vals[keys[i]]
		s.mu.RUnlock()
		if v == nil {
			continue
		}
		cur := int(v.Load())
		for _, j := range s.order {
			full := keys[i]
			full[stateAt] = s.states[j]
			value := 0.0
			if j == cur {
				value = 1
			}
			f(&full, value)
		}
	}
}

func (s *StateSet) family() (metricFamily, bool) {
	var samples []sample
	s.eachSample(func(key *labelKey, value float64) {
		samples = append(samples, sample{labels: buildLabelString(s.labels, key), value: formatValue(value)})
	})
	return metricFamily{name: s.name, typ: typeGauge, help: s.help, samples: samples}, len(samples) > 0
}

func (s *StateSet) gather() Family {
	var samples []Sample
	s.eachSample(func(key *labelKey, value float64) {
		samples = append(samples, Sample{Labels: labelsFor(s.labels, key), Value: value})
	})
	return Family{Name: s.name, Help: s.help, Type: TypeGauge, Samples: samples}
}

// Info is an info series: one label set, set to 1, whose labels carry the
// information, such as the version a build_info series names. It renders as
// a gauge.
type Info struct {
	cur    *labelKey // the current label set; nil before the first Set; guarded by mu
	help   string
	err    error // construction-time validation error; surfaces at registration
	labels []string
	meta   declMeta
	series
	registered atomic.Bool
	warned     atomic.Bool // one-time inert-record warning emitted
}

// NewInfo creates an info series with the given label names. Its unit is
// [UnitNone]. It captures the errors [NewLabeledGauge] captures, and
// [WithSeed] is a construction error, since an info series has no 0.
func NewInfo(name, help string, labels []string, opts ...Option) *Info {
	help = sanitizeHelp(name, help)
	o := applyOptions(opts)
	owned, err := checkNameAndLabels("Info", name, labels)
	i := &Info{help: help, labels: owned}
	i.name = name
	i.meta.fill(name, help, KindInfo, UnitNone, owned, &o)
	i.err = firstErr(err, checkOptionFits(name, &o, false, -1),
		checkDeclaration(name, KindInfo, UnitNone, owned, &o))
	return i
}

// Declaration returns the info series' metadata.
func (i *Info) Declaration() Declaration { return i.meta.declaration() }

func (i *Info) declared() *declMeta {
	if i == nil {
		return nil
	}
	return &i.meta
}

// Set makes labelVals, one value per label name, the info series' one label
// set, replacing the previous one. It panics on a label-arity mismatch.
// Invalid UTF-8 is replaced with U+FFFD, with one warning per such Set that
// changes the series. An info series carrying a construction error records
// nothing (one warning on the first drop).
func (i *Info) Set(labelVals ...string) {
	if i.err != nil {
		warnInertOnce(i.err, &i.warned, &i.name)
		return
	}
	key := labelKeyFor(i.labels, labelVals)
	rep, sanitized := sanitizeLabelKey(&key)
	i.mu.Lock()
	changed := i.cur == nil || *i.cur != key
	i.cur = &key
	name := i.name
	i.mu.Unlock()
	if changed && sanitized {
		slog.Warn("measure: label value contained invalid UTF-8; sanitized with U+FFFD", "metric", name, "value", rep)
	}
}

// Delete takes the info series off the page until the next Set.
func (i *Info) Delete() {
	i.mu.Lock()
	i.cur = nil
	i.mu.Unlock()
}

func (i *Info) current() *labelKey {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.cur
}

func (i *Info) family() (metricFamily, bool) {
	key := i.current()
	if key == nil {
		return metricFamily{}, false
	}
	return metricFamily{
		name: i.name, typ: typeGauge, help: i.help,
		samples: []sample{{labels: buildLabelString(i.labels, key), value: "1"}},
	}, true
}

func (i *Info) gather() Family {
	f := Family{Name: i.name, Help: i.help, Type: TypeGauge}
	if key := i.current(); key != nil {
		f.Samples = []Sample{{Labels: labelsFor(i.labels, key), Value: 1}}
	}
	return f
}
