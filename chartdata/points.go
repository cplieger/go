package chartdata

import (
	"github.com/cplieger/go/measure/v2"
)

// Measures are the declarations a PointsFrame draws: one per axis, and R, a
// point's size, or nil for none.
type Measures struct {
	X measure.Declared
	Y measure.Declared
	R measure.Declared
}

// Points is one group given to NewPointsFrame: each point's name and its x,
// y and, when the frame has an R, r values, NaN for no point.
type Points struct {
	Key   string
	Names []string
	X     []float64
	Y     []float64
	R     []float64
}

// Grid is the cells given to NewGridFrame: each axis's names or edges, and a
// row of values per y entry, each a value per x entry, NaN for no data.
type Grid struct {
	X      FrameGridAxis
	Y      FrameGridAxis
	Values [][]float64
}

// NewPointsFrame builds a PointsFrame of items measured on m.X and m.Y, and
// sized on m.R when it is not nil; the frame takes its name from m.Y. Each
// group's names, x, y and r are equally long, every name is unique across the
// frame and never empty, every group has r exactly when m.R is set, and an r
// is 0 or more. It takes WithRefs and WithNotes.
func NewPointsFrame(m Measures, series []Points, opts ...Option) (PointsFrame, error) {
	charge := 0
	for i := range series {
		charge += max(1, len(series[i].Names))
	}
	o, err := charged(opts, optMarks, charge,
		func() error { return checkKeys("series", func(i int) string { return series[i].Key }, len(series)) },
		func() error {
			var all []string
			for i := range series {
				all = append(all, series[i].Names...)
			}
			return checkNames("names", all)
		})
	if err != nil {
		return PointsFrame{}, err
	}
	name, units, err := m.units()
	if err != nil {
		return PointsFrame{}, err
	}
	out := make([]FramePoints, len(series))
	for i := range series {
		if out[i], err = buildPoints(indexed("series", i), &series[i], m.R != nil); err != nil {
			return PointsFrame{}, err
		}
	}
	return PointsFrame{
		Type:   TypePoints,
		Name:   name,
		Units:  units,
		Series: out,
		Refs:   o.refs,
		Notes:  o.notes,
	}, nil
}

// units returns the frame's name, from Y, and its units.
func (m Measures) units() (string, FrameUnits, error) {
	x, err := declarationOf(m.X, "measures.x")
	if err != nil {
		return "", FrameUnits{}, err
	}
	y, err := declarationOf(m.Y, "measures.y")
	if err != nil {
		return "", FrameUnits{}, err
	}
	units := FrameUnits{X: string(x.Unit), Y: string(y.Unit)}
	if m.R != nil {
		r, err := declarationOf(m.R, "measures.r")
		if err != nil {
			return "", FrameUnits{}, err
		}
		units.R = string(r.Unit)
	}
	return y.Name, units, nil
}

func buildPoints(field string, s *Points, sized bool) (FramePoints, error) {
	n := len(s.Names)
	px, err := gappedLen(field+".x", s.X, n, "name")
	if err != nil {
		return FramePoints{}, err
	}
	py, err := gappedLen(field+".y", s.Y, n, "name")
	if err != nil {
		return FramePoints{}, err
	}
	var pr []*float64
	switch {
	case sized:
		if pr, err = gappedLen(field+".r", s.R, n, "name"); err != nil {
			return FramePoints{}, err
		}
		for i, r := range pr {
			if r != nil && *r < 0 {
				return FramePoints{}, refuse(RuleNegative, indexed(field+".r", i), "must be 0 or more, an area")
			}
		}
	case s.R != nil:
		return FramePoints{}, refuse(RuleShape, field+".r", "has no place without measures.r")
	}
	return FramePoints{Key: s.Key, Names: clone(s.Names), X: px, Y: py, R: pr}, nil
}

// NewGridFrame builds a GridFrame of d's values over g. Each axis has exactly
// one of names, unique and never empty, or two or more finite, strictly
// increasing edges; g.Values holds a row per y entry and each row a value per
// x entry. Its y holds at most MaxRows entries. It takes WithRefs and
// WithNotes.
func NewGridFrame(d measure.Declared, g *Grid, opts ...Option) (GridFrame, error) {
	if g == nil {
		return GridFrame{}, refuse(RuleEmpty, "grid", "is nil")
	}
	columns, rows, err := checkGridAxes(g)
	if err != nil {
		return GridFrame{}, err
	}
	decl, o, err := begin(d, opts, optMarks, seriesCharge(rows, columns))
	if err != nil {
		return GridFrame{}, err
	}
	if len(g.Values) != rows {
		return GridFrame{}, refusef(RuleLength, "values", "holds %d rows, not one per y entry (%d)", len(g.Values), rows)
	}
	cells := make([][]*float64, rows)
	for r, row := range g.Values {
		if cells[r], err = gappedLen(indexed("values", r), row, columns, "x entry"); err != nil {
			return GridFrame{}, err
		}
	}
	return GridFrame{
		Type:   TypeGrid,
		Name:   decl.Name,
		Unit:   string(decl.Unit),
		X:      FrameGridAxis{Names: clone(g.X.Names), Edges: clone(g.X.Edges)},
		Y:      FrameGridAxis{Names: clone(g.Y.Names), Edges: clone(g.Y.Edges)},
		Values: cells,
		Refs:   o.refs,
		Notes:  o.notes,
	}, nil
}

// checkGridAxes returns how many columns and rows g's axes have, refusing a
// y of more than MaxRows entries.
func checkGridAxes(g *Grid) (columns, rows int, err error) {
	if columns, err = checkGridAxis("x", g.X); err != nil {
		return 0, 0, err
	}
	if rows, err = checkGridAxis("y", g.Y); err != nil {
		return 0, 0, err
	}
	return columns, rows, checkMost("y", rows, MaxRows, "rows")
}

// checkGridAxis returns how many entries the axis has.
func checkGridAxis(field string, a FrameGridAxis) (int, error) {
	switch {
	case len(a.Names) > 0 && len(a.Edges) > 0:
		return 0, refuse(RuleShape, field, "must have names or edges, not both")
	case len(a.Names) > 0:
		return len(a.Names), checkNames(field+".names", a.Names)
	case len(a.Edges) >= 2:
		return len(a.Edges) - 1, checkOrder(field+".edges", a.Edges, false)
	case len(a.Edges) == 1:
		return 0, refuse(RuleShape, field+".edges", "must hold two or more, a bin's low and high edges")
	default:
		return 0, refuse(RuleEmpty, field, "must have names or edges")
	}
}
