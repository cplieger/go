package chartdata

import "time"

// The limits table: the ceilings the chart elements draw within, which every
// constructor enforces, and the sampler's bounds, which a [Sampler] enforces.
// A refusal that depends on how the page presents a frame, such as a log
// axis's thinnest bin or a plot's size in pixels, is left to the element.
const (
	// MaxValues is the most values one frame holds: its samples, bars, dots,
	// cells, tree parts, flow links, refs and notes together, each series
	// counted as one value at least. It is about seven series of a minute's
	// samples over 90 days.
	MaxValues = 1_000_000
	// MaxRows is the most rows a band plot holds, which bounds a
	// CategoriesFrame's names and series (a slope draws each series on a
	// line of its own), a BoxesFrame's names, a GridFrame's y, an
	// EventsFrame's rows and a CalendarFrame's weeks.
	MaxRows = 1_000
	// MaxSubRows is the most rows a BoxesFrame's boxes take, one per series
	// in each row: a 40,000 px plot over 16 px rows.
	MaxSubRows = 2_500
	// MaxMagnitude is the largest magnitude a value may have, so the chart's
	// sums and scales stay finite.
	MaxMagnitude = 1e300
	// MaxCalendarDays is the most days a CalendarFrame holds: MaxRows weeks.
	MaxCalendarDays = 7 * MaxRows
	// MinEvery is the shortest step a [Sampler] takes.
	MinEvery = time.Second
	// MaxPoints is the most points one sampled ring holds, keep / every.
	MaxPoints = 10_000
	// MaxLabelSets is the most label sets a [Sampler] reads per track; one
	// past it is dropped, and the first drop logged.
	MaxLabelSets = 1_000
)
