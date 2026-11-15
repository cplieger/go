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
	// MaxCalendarDays is the most days a CalendarFrame holds: the most that
	// fit in MaxRows weeks at any week start, since the page picks it and a
	// first day up to 6 days into its week adds a partial week.
	MaxCalendarDays = 7*(MaxRows-1) + 1
	// MinEvery is the shortest step a [Sampler] takes.
	MinEvery = time.Second
	// MaxPoints is the most points one sampled ring holds, keep / every.
	MaxPoints = 10_000
	// MaxLabelSets is the most label sets that hold a place in a [Sampler]
	// track, and the most series it holds per track. A label set takes its
	// place with its first point, a value, increase, bin or quantile, and
	// gives it up once its key's ring holds none of its points, however
	// often it is read; a new one past the cap is dropped, and the first drop
	// logged. A value or quantile track holds at most MaxValues / (Keep /
	// Every) series, so its whole ring fits one frame.
	MaxLabelSets = 1_000
	// MaxBaselines is the most counter or histogram label sets whose last
	// reading a track keeps, place or not, to take the next increase from.
	// Past it, the least recently changed is forgotten, and its next
	// reading is a first appearance.
	MaxBaselines = 4 * MaxLabelSets
)
