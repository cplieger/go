//go:build !linux && !(darwin && !ios)

package measure

import "testing"

// TestCollectProcessMetrics_other checks that a platform with no process
// reader serves none of the read families and never reports degradation.
func TestCollectProcessMetrics_other(t *testing.T) {
	var d processMetricsData
	collectProcessMetrics(&d)
	if d.hasCPU() || d.hasRSS() || d.hasOpenFDs() || d.hasMaxFDs() || d.hasVirtual() || d.hasMaxVirtual() || d.hasNetwork() {
		t.Errorf("a read family is present: %+v", d)
	}
	if got := procOKMask(procExpected, &d); got != procOKAll {
		t.Errorf("procOKMask = %#09b, want procOKAll", got)
	}
}
