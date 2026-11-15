package chartdata

import (
	"time"

	"github.com/cplieger/go/measure/v2"
)

// NewCalendarFrame builds a CalendarFrame of d's values, one per calendar day
// of zone from start, NaN for no data. zone is an IANA name that
// time.LoadLocation resolves, and not one naming the host's own zone ("",
// "Local", "localtime", "posixrules"); an app whose image has no zoneinfo
// imports time/tzdata. start must be the first instant of a day in zone,
// whatever start's own location. The frame holds at most MaxCalendarDays
// days. It takes WithOpen, WithRefs and WithNotes.
func NewCalendarFrame(d measure.Declared, zone string, start time.Time, values []float64, opts ...Option) (CalendarFrame, error) {
	if err := checkMost("values", len(values), MaxCalendarDays, "days"); err != nil {
		return CalendarFrame{}, err
	}
	switch zone {
	case "", "Local", "localtime", "posixrules":
		return CalendarFrame{}, refusef(RuleShape, "zone", "must be an IANA zone name, not %q", zone)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return CalendarFrame{}, refusef(RuleShape, "zone", "names no zone this process can load, %q: %v", zone, err)
	}
	if start.In(loc).Day() == start.Add(-time.Nanosecond).In(loc).Day() {
		return CalendarFrame{}, refusef(RuleShape, "start", "must be the first instant of a day in %s", zone)
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
