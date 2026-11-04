package measure

import (
	"strings"
	"testing"
	"time"
)

func TestRecordHTTPNilSafe(t *testing.T) {
	RecordHTTP(nil, nil, time.Second) // must not panic
	c := NewLabeledCounter("req_total", "", []string{"method", "status"})
	h := NewHistogram("req_seconds", "")
	RecordHTTP(c, h, 250*time.Millisecond, "GET", "200")
	r := NewRegistry("")
	r.MustRegister(c)
	r.MustRegister(h)
	out := body(t, r)
	if !strings.Contains(out, `req_total{method="GET",status="200"} 1`) {
		t.Errorf("RecordHTTP did not record counter:\n%s", out)
	}
	if !strings.Contains(out, "req_seconds_count 1") {
		t.Errorf("RecordHTTP did not record histogram:\n%s", out)
	}
}
