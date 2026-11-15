package measure

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestADescRegisteredOnItsOwnReservesItsNameAndServesNothing(t *testing.T) {
	r := NewRegistry("app")
	runs := NewCounterDesc("runs_total", "Runs by result and host.", UnitCount, []string{"result", "host"},
		WithExport(ExportChartdata), WithUnbounded("host"))
	if err := r.Register(runs); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := runs.Declaration().Name; got != "app_runs_total" {
		t.Errorf("Declaration().Name = %q, want the registered name app_runs_total", got)
	}
	if err := r.Register(NewCounter("runs_total", "h", UnitCount)); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Errorf("Register of a counter under the desc's name = %v, want a name collision", err)
	}

	rec := httptest.NewRecorder()
	r.Handler()(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody))
	body, _ := io.ReadAll(rec.Body)
	if strings.Contains(string(body), "runs_total") {
		t.Errorf("the page serves the desc:\n%s", body)
	}

	fams, err := r.GatherOf(t.Context(), runs)
	if err != nil {
		t.Fatalf("GatherOf: %v", err)
	}
	if len(fams) != 1 || fams[0].Name != "app_runs_total" || fams[0].Type != TypeCounter || len(fams[0].Samples) != 0 {
		t.Errorf("GatherOf = %+v, want one counter family app_runs_total with no samples", fams)
	}
	all, err := r.Gather(t.Context())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if f, ok := Find(all, "app_runs_total"); !ok || len(f.Samples) != 0 {
		t.Errorf("Gather lists the desc = %v, %+v, want it with no samples", ok, f)
	}
}

func TestADescRegisteredOnItsOwnReservesAHistogramsSeriesNames(t *testing.T) {
	r := NewRegistry("")
	waits := NewHistogramDesc("wait_seconds", "h", UnitSeconds, nil, []float64{1, 10}, WithExport(ExportChartdata))
	r.MustRegister(waits)
	if err := r.Register(NewGauge("wait_seconds_count", "h", UnitNone)); err == nil {
		t.Error("Register of wait_seconds_count beside the histogram desc succeeded, want a collision")
	}
}

func TestRegisterRefusesADescOnItsOwnThatCannotStandAlone(t *testing.T) {
	adopted := NewGaugeDesc("adopted", "h", UnitNone, nil, WithExport(ExportChartdata))
	_ = NewCollector(func(context.Context, *Emitter) {}, adopted)
	twice := NewGaugeDesc("twice", "h", UnitNone, nil, WithExport(ExportChartdata))
	NewRegistry("").MustRegister(twice)
	for _, tc := range []struct {
		d    *Desc
		name string
		want string
	}{
		{name: "onThePage", d: NewGaugeDesc("paged", "h", UnitNone, nil), want: "must be ExportChartdata"},
		{name: "onBoth", d: NewGaugeDesc("both", "h", UnitNone, nil, WithExport(ExportBoth)), want: "must be ExportChartdata"},
		{name: "adopted", d: adopted, want: "belongs to a collector"},
		{name: "registeredTwice", d: twice, want: "already registered"},
		{name: "badDeclaration", d: NewGaugeDesc("bad_total", "h", UnitNone, nil, WithExport(ExportChartdata)), want: "_total"},
		{name: "zero", d: &Desc{}, want: "not built by a NewXDesc constructor"},
		{name: "nil", d: nil, want: "nil metric"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := NewRegistry("").Register(tc.d); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Register = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestARefusedDescStaysFreeForACollector(t *testing.T) {
	r := NewRegistry("")
	r.MustRegister(NewGauge("taken", "h", UnitNone))
	d := NewGaugeDesc("taken", "h", UnitNone, nil, WithExport(ExportChartdata))
	if err := r.Register(d); err == nil {
		t.Fatal("Register of a desc whose name is taken succeeded")
	}
	if err := NewRegistry("").Register(NewCollector(func(context.Context, *Emitter) {}, d)); err != nil {
		t.Errorf("a collector adopting the refused desc = %v, want it registered", err)
	}
}

func TestNewCollectorRefusesADescRegisteredOnItsOwn(t *testing.T) {
	d := NewGaugeDesc("standalone", "h", UnitNone, nil, WithExport(ExportChartdata))
	NewRegistry("").MustRegister(d)
	err := NewRegistry("").Register(NewCollector(func(context.Context, *Emitter) {}, d))
	if err == nil || !strings.Contains(err.Error(), "registered on its own") {
		t.Errorf("Register of a collector over the desc = %v, want it refused as registered on its own", err)
	}
}
