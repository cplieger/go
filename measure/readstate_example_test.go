package measure_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/cplieger/go/measure/v2"
)

// A connection's read folds its repositories' reads. A read cut short after
// a whole one keeps the whole read's time, and a repository the person left
// out does not count.
func ExampleFoldParts() {
	at := time.Date(2026, 11, 18, 14, 5, 0, 0, time.UTC)
	repos := []measure.Read{
		measure.Whole(at.Add(10 * time.Minute)),
		measure.Whole(at).Cut(measure.ReadPartial, "rate_limited"),
		{State: measure.ReadExcluded},
	}
	r := measure.FoldParts(repos...)
	fmt.Println(r.State, r.Reason, r.AsOf.Format("15:04"))
	// Output: partial rate_limited 14:05
}

// A count needs every signal it is computed from, so one signal's failed
// read makes the count's read blind, where FoldParts would read a scope with
// one failed part as partial.
func ExampleFoldNeeds() {
	at := time.Date(2026, 11, 18, 14, 5, 0, 0, time.UTC)
	pulls := measure.Whole(at)
	checks := measure.Whole(at.Add(-time.Hour)).Cut(measure.ReadBlind, "rate_limited")
	fmt.Println(measure.FoldNeeds(pulls, checks).State, measure.FoldParts(pulls, checks).State)
	// Output: blind partial
}

// Each read scope exports its state and the time of its last whole read
// beside the count, so an alert keys on the read, never on the count alone.
func ExampleEmitter_EmitRead() {
	signal := measure.NewReadDescs("signal", "Each signal's read", []string{"signal"})
	open := measure.NewGaugeDesc("open_pulls", "Open pull requests, as of the last whole read", measure.UnitCount, nil)
	last := 12.0
	read := measure.Whole(time.Unix(1_763_474_700, 0)).Cut(measure.ReadPartial, "rate_limited")

	r := measure.NewRegistry("app")
	r.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
		e.Emit(open, last)
		e.EmitRead(signal, read, "pulls")
	}, open, signal.State, signal.Complete))

	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for line := range strings.SplitSeq(w.Body.String(), "\n") {
		if strings.HasPrefix(line, "app_") && (strings.HasSuffix(line, " 1") || !strings.Contains(line, "state=")) {
			fmt.Println(line)
		}
	}
	// Output:
	// app_open_pulls 12
	// app_signal_state{signal="pulls",state="partial"} 1
	// app_signal_complete_timestamp_seconds{signal="pulls"} 1763474700
}

// A collector whose own read failed calls Keep, so the scrape serves the
// last successful run's samples instead of none, and no alert reads a
// missing series as resolved.
func ExampleEmitter_Keep() {
	depth := measure.NewGaugeDesc("queue_depth", "Items waiting", measure.UnitCount, nil)
	var readErr error
	n := 3

	r := measure.NewRegistry("app", measure.WithLogger(slog.New(slog.DiscardHandler)))
	r.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
		if readErr != nil {
			e.Keep(readErr)
			return
		}
		e.Emit(depth, float64(n))
	}, depth))

	show := func() {
		fams, err := r.Gather(context.Background())
		f, _ := measure.Find(fams, "app_queue_depth")
		s, _ := f.Sample()
		fmt.Println(s.Value, err)
	}
	show()
	readErr, n = errors.New("store read failed"), 0
	show()
	// Output:
	// 3 <nil>
	// 3 <nil>
}
