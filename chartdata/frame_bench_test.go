package chartdata_test

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

// ceilingSeries is a SeriesFrame's input at MaxValues: 1,000 series of
// 1,000 values, every tenth a gap.
func ceilingSeries() ([]float64, []chartdata.Series) {
	x := make([]float64, 1_000)
	for i := range x {
		x[i] = 1_700_000_000_000 + float64(i)*60_000
	}
	series := nSeries(1_000)
	for s := range series {
		values := make([]float64, len(x))
		for i := range values {
			values[i] = float64(s*i) / 7
			if i%10 == 0 {
				values[i] = nan
			}
		}
		series[s].Values = values
	}
	return x, series
}

func BenchmarkNewSeriesFrameAtTheCeiling(b *testing.B) {
	d := measure.NewGauge("depth", "h", measure.UnitCount)
	x, series := ceilingSeries()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := chartdata.NewSeriesFrame(d, x, series); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeSeriesFrameAtTheCeiling(b *testing.B) {
	x, series := ceilingSeries()
	f, err := chartdata.NewSeriesFrame(measure.NewGauge("depth", "h", measure.UnitCount), x, series)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := json.NewEncoder(io.Discard).Encode(f); err != nil {
			b.Fatal(err)
		}
	}
}
