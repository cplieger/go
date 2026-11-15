package chartdata

import (
	"time"

	"github.com/cplieger/go/measure/v2"
)

// NewCalendarFrame builds a CalendarFrame of d's values, one per calendar day
// from start, NaN for no data. start must be a day's first instant in its
// location, an IANA zone (not time.Local, whose name says no zone), and the
// frame holds at most MaxCalendarDays days. It takes WithOpen, WithRefs and
// WithNotes.
func NewCalendarFrame(d measure.Declared, start time.Time, values []float64, opts ...Option) (CalendarFrame, error) {
	zone := start.Location().String()
	if start.Location() == time.Local || zone == "Local" {
		return CalendarFrame{}, refuse(RuleShape, "start", "must be in an IANA zone, not time.Local")
	}
	y, m, day := start.Date()
	if !time.Date(y, m, day, 0, 0, 0, 0, start.Location()).Equal(start) {
		return CalendarFrame{}, refusef(RuleShape, "start", "must be its day's midnight in %s", zone)
	}
	if err := checkMost("values", len(values), MaxCalendarDays, "days"); err != nil {
		return CalendarFrame{}, err
	}
	decl, o, err := begin(d, opts, optMarks|optOpen, len(values))
	if err != nil {
		return CalendarFrame{}, err
	}
	days, err := gapped("values", values)
	if err != nil {
		return CalendarFrame{}, err
	}
	return CalendarFrame{
		Type:   TypeCalendar,
		Name:   decl.Name,
		Unit:   string(decl.Unit),
		Zone:   zone,
		Start:  start.UnixMilli(),
		Values: days,
		Refs:   o.refs,
		Notes:  o.notes,
		Open:   o.open,
	}, nil
}
