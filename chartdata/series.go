package chartdata

import (
	"github.com/cplieger/go/measure/v2"
)

// Series is one series given to a constructor: its key, a label value or the
// declaration's name, and a value per x or category, NaN for a gap.
type Series struct {
	Key    string
	Values []float64
}

// Bin is one bin given to NewBinsFrame, from From to To on its axis.
type Bin struct {
	From float64
	To   float64
}

// NewSeriesFrame builds a SeriesFrame of d's series along x, instants in Unix
// milliseconds or plain numbers, strictly increasing and finite. Each series
// holds a value per x and a key no other series has. It takes WithDomain,
// WithOpen, WithResets, WithRefs and WithNotes.
func NewSeriesFrame(d measure.Declared, x []float64, series []Series, opts ...Option) (SeriesFrame, error) {
	decl, o, err := begin(d, opts, optMarks|optDomain|optOpen|optResets, seriesCharge(len(series), len(x)),
		func() error { return checkOrder("x", x, false) })
	if err != nil {
		return SeriesFrame{}, err
	}
	out, err := buildSeries(series, len(x), "x")
	if err != nil {
		return SeriesFrame{}, err
	}
	return SeriesFrame{
		Type:   TypeSeries,
		Name:   decl.Name,
		Unit:   string(decl.Unit),
		X:      clone(x),
		Series: out,
		Domain: o.domainSlice(),
		Resets: o.resetMillis(),
		Refs:   o.refs,
		Notes:  o.notes,
		Open:   o.open,
	}, nil
}

// NewBinsFrame builds a BinsFrame of d's series over bins along axis. Each
// bin's From is below its To, the Froms strictly increase and the Tos never
// decrease. Each series holds a value per bin and a key no other series has.
// It takes WithDomain, WithOpen, WithResets, WithRefs and WithNotes.
func NewBinsFrame(d measure.Declared, axis FrameAxis, bins []Bin, series []Series, opts ...Option) (BinsFrame, error) {
	if axis != AxisTime && axis != AxisValue {
		return BinsFrame{}, refusef(RuleShape, "axis", "must be %q or %q, not %q", AxisTime, AxisValue, axis)
	}
	decl, o, err := begin(d, opts, optMarks|optDomain|optOpen|optResets, seriesCharge(len(series), len(bins)))
	if err != nil {
		return BinsFrame{}, err
	}
	e, err := splitBins(bins)
	if err != nil {
		return BinsFrame{}, err
	}
	out, err := buildSeries(series, len(bins), "bin")
	if err != nil {
		return BinsFrame{}, err
	}
	return BinsFrame{
		Type:   TypeBins,
		Name:   decl.Name,
		Unit:   string(decl.Unit),
		Axis:   axis,
		X1:     e.x1,
		X2:     e.x2,
		Series: out,
		Domain: o.domainSlice(),
		Resets: o.resetMillis(),
		Refs:   o.refs,
		Notes:  o.notes,
		Open:   o.open,
	}, nil
}

// edges are a BinsFrame's bins on the wire, each From in x1 and To in x2.
type edges struct{ x1, x2 []float64 }

func splitBins(bins []Bin) (edges, error) {
	e := edges{x1: make([]float64, len(bins)), x2: make([]float64, len(bins))}
	for i, b := range bins {
		e.x1[i], e.x2[i] = b.From, b.To
	}
	if err := checkOrder("x1", e.x1, false); err != nil {
		return edges{}, err
	}
	if err := checkOrder("x2", e.x2, true); err != nil {
		return edges{}, err
	}
	for i, b := range bins {
		if b.From >= b.To {
			return edges{}, refuse(RuleBin, indexed("bins", i), "must end above where it starts")
		}
	}
	return e, nil
}

// buildSeries copies series as the wire's, each n values long, refusing a
// key given twice.
func buildSeries(series []Series, n int, per string) ([]FrameSeries, error) {
	if err := checkKeys("series", func(i int) string { return series[i].Key }, len(series)); err != nil {
		return nil, err
	}
	out := make([]FrameSeries, len(series))
	for i, s := range series {
		values, err := gappedLen(indexed("series", i)+".values", s.Values, n, per)
		if err != nil {
			return nil, err
		}
		out[i] = FrameSeries{Key: s.Key, Values: values}
	}
	return out, nil
}

// clone copies s, keeping an empty s non-nil, so a frame's required list
// encodes as [] rather than null.
func clone[T any](s []T) []T {
	out := make([]T, len(s))
	copy(out, s)
	return out
}
