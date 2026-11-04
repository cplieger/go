package slogx

import (
	"log/slog"
	"testing"
	"time"
)

func TestUTCTimeConvertsTopLevelTime(t *testing.T) {
	t.Parallel()
	// A time in a +05:00 zone must be rewritten to the same instant in UTC.
	zone := time.FixedZone("plusfive", 5*60*60)
	instant := time.Date(2026, 7, 10, 12, 0, 0, 0, zone)

	got := UTCTime(nil, slog.Time(slog.TimeKey, instant))

	gotTime := got.Value.Time()
	if _, offset := gotTime.Zone(); offset != 0 {
		t.Errorf("zone offset = %d, want 0 (UTC)", offset)
	}
	if !gotTime.Equal(instant) {
		t.Errorf("instant changed: got %v, want the same instant as %v", gotTime, instant)
	}
}

func TestUTCTimeLeavesNonTimeAttrs(t *testing.T) {
	t.Parallel()
	got := UTCTime(nil, slog.String("msg", "hello"))
	if got.Value.String() != "hello" {
		t.Errorf("non-time attr altered: got %q, want %q", got.Value.String(), "hello")
	}
}

func TestUTCTimeLeavesGroupedTimeKey(t *testing.T) {
	t.Parallel()
	// A "time"-keyed attr inside a group (groups non-empty) is a user attr, not
	// the record's built-in timestamp, so it must be left untouched.
	zone := time.FixedZone("plusfive", 5*60*60)
	instant := time.Date(2026, 7, 10, 12, 0, 0, 0, zone)

	got := UTCTime([]string{"grp"}, slog.Time(slog.TimeKey, instant))

	if _, offset := got.Value.Time().Zone(); offset != 5*60*60 {
		t.Errorf("grouped time attr offset = %d, want %d (untouched)", offset, 5*60*60)
	}
}

func TestUTCTimeLeavesTopLevelNonTimeValue(t *testing.T) {
	t.Parallel()
	// A top-level attr keyed "time" whose value is not a time (Kind != KindTime)
	// must be returned untouched: the Kind guard prevents calling Value.Time()
	// on a non-time value, which would panic.
	got := UTCTime(nil, slog.String(slog.TimeKey, "not-a-timestamp"))
	if got.Value.Kind() != slog.KindString || got.Value.String() != "not-a-timestamp" {
		t.Errorf("top-level non-time %q attr altered: got kind=%v value=%q", slog.TimeKey, got.Value.Kind(), got.Value.String())
	}
}

// TestUTCTimeIsAllocationFree gates the one allocation number in this library
// that an application actually pays per log line. Setup and NewHandler install
// UTCTime as the handler's ReplaceAttr, and slog calls ReplaceAttr once for the
// built-in time, level and message attrs AND once for every attribute the call
// site passes, so an allocation added here is multiplied by log volume times
// attribute count. Everything else slogx does — ParseLevel, ParseFormat,
// NewHandler, Setup — runs once per process, which is why none of them carries a
// contract: pinning their allocation counts would fix a number nobody pays.
//
// The assertion is an equality against zero rather than a threshold, because
// AllocsPerRun is exact for a function that allocates either always or never,
// and because zero is the number the weekly benchmark tracker cannot protect.
// The tracker alerts on a ratio of current to previous, so a series moving from
// 0 to any non-zero value is a division by zero that alerts at any threshold —
// but only a week later. This makes the same regression fail at merge.
//
// Every shape the hook is reachable with is covered, because the guard in UTCTime
// is a three-way condition (top-level, key is the time key, value is a time) and
// each shape takes a different branch through it.
func TestUTCTimeIsAllocationFree(t *testing.T) {
	// Not parallel: AllocsPerRun sets GOMAXPROCS to 1 for the whole process while
	// it measures, and it counts allocations process-wide, so a parallel sibling's
	// work would be charged to this test.
	zone := time.FixedZone("plusfive", 5*60*60)
	inRange := time.Date(2026, 7, 10, 12, 0, 0, 0, zone)

	shapes := []struct {
		name string
		// desc says what the shape means at a call site, printed on failure
		// because the fixture alone does not explain why the shape exists.
		desc       string
		groups     []string
		attr       slog.Attr
		wantAllocs float64
	}{
		{
			name:       "rewritten time key",
			desc:       "the record's own timestamp, the one attr UTCTime exists to rewrite",
			attr:       slog.Time(slog.TimeKey, inRange),
			wantAllocs: 0,
		},
		{
			name:       "time key from Now",
			desc:       "the timestamp a real record carries, which slog builds with time.Now",
			attr:       slog.Time(slog.TimeKey, time.Now()),
			wantAllocs: 0,
		},
		{
			name:       "passed-through non-time key",
			desc:       "an ordinary application attr, the overwhelming majority of calls",
			attr:       slog.String("user", "alice"),
			wantAllocs: 0,
		},
		{
			name:       "grouped time key",
			desc:       "a user attr named time inside a group, which UTCTime must leave alone",
			groups:     []string{"grp"},
			attr:       slog.Time(slog.TimeKey, inRange),
			wantAllocs: 0,
		},
		{
			name:       "group value",
			desc:       "the group attr itself, whose members slog visits separately",
			attr:       slog.Group("grp", "k", "v"),
			wantAllocs: 0,
		},
		{
			name:       "empty attr",
			desc:       "the zero Attr, which slog passes to ReplaceAttr for a discarded field",
			attr:       slog.Attr{},
			wantAllocs: 0,
		},
		{
			name:       "zero time",
			desc:       "a zero time.Time, which slog.TimeValue stores as a nil location",
			attr:       slog.Time(slog.TimeKey, time.Time{}),
			wantAllocs: 0,
		},
		{
			// This is the one shape that is NOT free, and the cost is the standard
			// library's rather than this hook's: slog.Value can hold a time in
			// eight bytes only while it round-trips through UnixNano, so a time
			// outside roughly 1678-2262 falls back to a boxed time.Time, and
			// UTCTime builds a new Value with slog.TimeValue. It is asserted
			// rather than left in a comment so the scope of the zero claim above
			// is checked instead of merely described. A record's own timestamp is
			// always in range (slog reads the wall clock for it), so nothing on
			// the hot path reaches this; only a call site that logs an
			// out-of-range time under the "time" key does.
			name:       "out-of-range time",
			desc:       "a time slog.Value cannot hold inline, so rewriting it re-boxes",
			attr:       slog.Time(slog.TimeKey, time.Date(1600, 1, 1, 0, 0, 0, 0, zone)),
			wantAllocs: 1,
		},
	}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			// The attr and the group slice are built above, outside the measured
			// closure: slog.Time and slog.Group allocate on construction, and
			// measuring that would report the fixture's cost as the hook's.
			groups, attr := shape.groups, shape.attr
			got := testing.AllocsPerRun(200, func() {
				_ = UTCTime(groups, attr)
			})
			if got != shape.wantAllocs {
				t.Errorf("UTCTime(%v, %s) allocated %v times per run, want %v (%s): "+
					"this hook runs once per attribute per log line, so a single allocation here "+
					"is multiplied by log volume times attribute count. A number above %v is a "+
					"regression; a number below it is an improvement worth tightening this contract to.",
					shape.groups, attr, got, shape.wantAllocs, shape.desc, shape.wantAllocs)
			}
		})
	}
}
