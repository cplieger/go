package chartdata

// DurationEdges returns the duration ladder, the bin edges in seconds that a
// duration axis and a histogram of waits share: 0, 1, 2, 5, 10 and 30
// seconds, 1, 2, 5, 10 and 30 minutes, 1, 2, 6 and 12 hours, then 1, 3, 7,
// 14, 30 and 90 days. Neighbouring positive edges differ by a factor of 2 to
// 3, so no bin is more than 1.6 times as wide as another on a log axis. Pass
// a prefix of it to measure.WithBuckets, and bin stored durations on the
// same edges, so sampled and built BinsFrames land on the page's edges. Each
// call returns a fresh slice.
func DurationEdges() []float64 {
	const (
		minute = 60
		hour   = 60 * minute
		day    = 24 * hour
	)
	return []float64{
		0, 1, 2, 5, 10, 30,
		minute, 2 * minute, 5 * minute, 10 * minute, 30 * minute,
		hour, 2 * hour, 6 * hour, 12 * hour,
		day, 3 * day, 7 * day, 14 * day, 30 * day, 90 * day,
	}
}
