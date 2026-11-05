package measure_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/cplieger/go/measure"
)

// A collector reads its values when Prometheus scrapes. Here the queue depths
// come from a map; in an app they come from a store, a file or a socket.
func ExampleNewCollector() {
	depth := measure.NewGaugeDesc("queue_depth", "Items waiting in each queue.", []string{"queue"})
	queues := map[string]int{"mail": 3, "push": 0}

	r := measure.NewRegistry("app")
	r.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
		for name, n := range queues {
			e.Emit(depth, float64(n), name)
		}
	}, depth))

	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for line := range strings.SplitSeq(w.Body.String(), "\n") {
		if strings.Contains(line, "app_queue_depth") {
			fmt.Println(line)
		}
	}
	// Output:
	// # HELP app_queue_depth Items waiting in each queue.
	// # TYPE app_queue_depth gauge
	// app_queue_depth{queue="mail"} 3
	// app_queue_depth{queue="push"} 0
}

// Gather reads a registry as structured values, so a test can check a family
// without parsing the page.
func ExampleRegistry_Gather() {
	r := measure.NewRegistry("app")
	errs := measure.NewLabeledCounter("errors_total", "Errors by kind.", []string{"kind"})
	r.MustRegister(errs)
	errs.Inc("timeout")
	errs.Add(2, "refused")

	fams, err := r.Gather(context.Background())
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, f := range fams {
		if f.Name != "app_errors_total" {
			continue
		}
		for _, s := range f.Samples {
			fmt.Println(f.Type, s.Labels[0].Name, s.Labels[0].Value, s.Value)
		}
	}
	// Output:
	// counter kind refused 2
	// counter kind timeout 1
}
