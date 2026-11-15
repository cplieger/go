package chartdata

import (
	"math"

	"github.com/cplieger/go/measure/v2"
)

// Boxes is one series given to NewBoxesFrame: each category's five numbers,
// NaN in all five where the category has no box, and the values beyond its
// whiskers, one list per category or none.
type Boxes struct {
	Key      string
	Low      []float64
	Q1       []float64
	Median   []float64
	Q3       []float64
	High     []float64
	Outliers [][]float64
}

// NewCategoriesFrame builds a CategoriesFrame of d's series over names,
// unique and never empty. Each series holds a value per name and a key no
// other series has. It holds at most MaxRows names and MaxRows series, and
// takes WithRefs and WithNotes.
func NewCategoriesFrame(d measure.Declared, names []string, series []Series, opts ...Option) (CategoriesFrame, error) {
	if err := checkMost("names", len(names), MaxRows, "names"); err != nil {
		return CategoriesFrame{}, err
	}
	if err := checkMost("series", len(series), MaxRows, "series"); err != nil {
		return CategoriesFrame{}, err
	}
	decl, o, err := begin(d, opts, optMarks, seriesCharge(len(series), len(names)),
		func() error { return checkNames("names", names) })
	if err != nil {
		return CategoriesFrame{}, err
	}
	out, err := buildSeries(series, len(names), "name")
	if err != nil {
		return CategoriesFrame{}, err
	}
	return CategoriesFrame{
		Type:   TypeCategories,
		Name:   decl.Name,
		Unit:   string(decl.Unit),
		Names:  clone(names),
		Series: out,
		Refs:   o.refs,
		Notes:  o.notes,
	}, nil
}

// NewBoxesFrame builds a BoxesFrame of d's distributions over names, unique
// and never empty. Each category of each series has low ≤ q1 ≤ median ≤ q3 ≤
// high, or all five NaN and no outliers, and its outliers are finite and lie
// below low or above high. It holds at most MaxRows names and MaxSubRows
// boxes' rows (names times series), and takes WithRefs and WithNotes.
func NewBoxesFrame(d measure.Declared, names []string, series []Boxes, opts ...Option) (BoxesFrame, error) {
	if err := checkMost("names", len(names), MaxRows, "names"); err != nil {
		return BoxesFrame{}, err
	}
	if err := checkMost("series", len(names)*len(series), MaxSubRows, "sub-rows"); err != nil {
		return BoxesFrame{}, err
	}
	charge := seriesCharge(len(series), len(names))
	for i := range series {
		for _, o := range series[i].Outliers {
			charge += len(o)
		}
	}
	decl, o, err := begin(d, opts, optMarks, charge,
		func() error { return checkNames("names", names) },
		func() error { return checkKeys("series", func(i int) string { return series[i].Key }, len(series)) })
	if err != nil {
		return BoxesFrame{}, err
	}
	out := make([]FrameBoxes, len(series))
	for i := range series {
		if out[i], err = buildBoxes(indexed("series", i), &series[i], len(names)); err != nil {
			return BoxesFrame{}, err
		}
	}
	return BoxesFrame{
		Type:   TypeBoxes,
		Name:   decl.Name,
		Unit:   string(decl.Unit),
		Names:  clone(names),
		Series: out,
		Refs:   o.refs,
		Notes:  o.notes,
	}, nil
}

func buildBoxes(field string, s *Boxes, n int) (FrameBoxes, error) {
	five := [5][]float64{s.Low, s.Q1, s.Median, s.Q3, s.High}
	fields := [5]string{"low", "q1", "median", "q3", "high"}
	var wire [5][]*float64
	for k := range five {
		var err error
		if wire[k], err = gappedLen(field+"."+fields[k], five[k], n, "name"); err != nil {
			return FrameBoxes{}, err
		}
	}
	if s.Outliers != nil && len(s.Outliers) != n {
		return FrameBoxes{}, refusef(RuleLength, field+".outliers", "holds %d lists, not one per name (%d)", len(s.Outliers), n)
	}
	var outliers [][]float64
	if s.Outliers != nil {
		outliers = make([][]float64, n)
	}
	for c := range n {
		var extra []float64
		if s.Outliers != nil {
			extra = s.Outliers[c]
			outliers[c] = clone(extra)
		}
		if err := checkBox(indexed(field, c), &five, c, extra); err != nil {
			return FrameBoxes{}, err
		}
	}
	return FrameBoxes{
		Key: s.Key, Low: wire[0], Q1: wire[1], Median: wire[2], Q3: wire[3], High: wire[4],
		Outliers: outliers,
	}, nil
}

// checkBox checks category c of a box series, whose values are already
// known finite or NaN.
func checkBox(field string, five *[5][]float64, c int, outliers []float64) error {
	gaps := 0
	for k := range five {
		if math.IsNaN(five[k][c]) {
			gaps++
		}
	}
	switch {
	case gaps == len(five) && len(outliers) > 0:
		return refuse(RuleShape, field, "has outliers but no five numbers for them to lie beyond")
	case gaps == len(five):
		return nil
	case gaps > 0:
		return refuse(RuleShape, field, "must have all five numbers, low, q1, median, q3 and high, or none")
	}
	for k := 1; k < len(five); k++ {
		if five[k][c] < five[k-1][c] {
			return refuse(RuleOrder, field, "must have low ≤ q1 ≤ median ≤ q3 ≤ high")
		}
	}
	low, high := five[0][c], five[4][c]
	for i, v := range outliers {
		if err := checkFinite(field+"."+indexed("outliers", i), v); err != nil {
			return err
		}
		if v >= low && v <= high {
			return refuse(RuleShape, field+"."+indexed("outliers", i), "must lie below low or above high")
		}
	}
	return nil
}
