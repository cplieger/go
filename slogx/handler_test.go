package slogx

import (
	"bytes"
	"errors"
	"io"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNewHandlerTextToBuffer(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler, lv := NewHandler(Options{Output: &buf})
	if lv.Level() != slog.LevelInfo {
		t.Errorf("default level = %v, want Info", lv.Level())
	}
	slog.New(handler).Info("hello", "k", "v")
	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "msg=hello") || !strings.Contains(out, "k=v") {
		t.Errorf("text output missing expected fields: %q", out)
	}
}

func TestNewHandlerJSONToBuffer(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler, _ := NewHandler(Options{Output: &buf, Format: JSON})
	slog.New(handler).Info("hello")
	out := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(out, "{") || !strings.Contains(out, `"msg":"hello"`) {
		t.Errorf("JSON output is not JSON-shaped: %q", out)
	}
}

func TestNewHandlerUTCNormalizesTime(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler, _ := NewHandler(Options{Output: &buf})
	zone := time.FixedZone("plusfive", 5*60*60)
	record := slog.NewRecord(time.Date(2026, 7, 10, 12, 0, 0, 0, zone), slog.LevelInfo, "hi", 0)

	if err := handler.Handle(t.Context(), record); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	timeField, _, _ := strings.Cut(buf.String(), " ")
	if timeField != "time=2026-07-10T07:00:00.000Z" {
		t.Errorf("time field = %q, want UTC-normalized %q", timeField, "time=2026-07-10T07:00:00.000Z")
	}
}

func TestNewHandlerLevelFiltering(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler, lv := NewHandler(Options{Output: &buf, Level: slog.LevelWarn})
	logger := slog.New(handler)

	logger.Info("suppressed")
	if buf.Len() != 0 {
		t.Errorf("Info emitted below the Warn threshold: %q", buf.String())
	}
	logger.Warn("shown")
	if !strings.Contains(buf.String(), "msg=shown") {
		t.Errorf("Warn not emitted at the Warn threshold: %q", buf.String())
	}

	// The returned LevelVar controls the live level.
	buf.Reset()
	lv.Set(slog.LevelDebug)
	logger.Debug("now-visible")
	if !strings.Contains(buf.String(), "msg=now-visible") {
		t.Errorf("Debug not emitted after lowering the level via the LevelVar: %q", buf.String())
	}
}

func TestNewHandlerAddSource(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler, _ := NewHandler(Options{Output: &buf, AddSource: true})
	slog.New(handler).Info("hi")
	if !strings.Contains(buf.String(), "source=") {
		t.Errorf("AddSource did not add a source attr: %q", buf.String())
	}
}

func TestNewHandlerInvalidFormatPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewHandler with an out-of-range Format did not panic")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "invalid Format value 99") {
			t.Errorf("panic value = %v, want a string naming the invalid Format value", r)
		}
	}()
	NewHandler(Options{Format: Format(99)})
}

// restoreLoggingDefaults registers a Cleanup putting back every global
// slog.SetDefault mutates: slog's default logger, and the log package's output
// writer and flags, which SetDefault redirects through the installed handler and
// does not put back when the previous default is reinstalled (capture.SwapDefault
// documents why). Restore slog first: restoring a non-default logger redirects
// log again.
func restoreLoggingDefaults(tb testing.TB) {
	tb.Helper()
	logger, writer, flags := slog.Default(), log.Writer(), log.Flags()
	tb.Cleanup(func() {
		slog.SetDefault(logger)
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
}

func TestSetupInstallsDefault(t *testing.T) {
	// Not parallel: Setup mutates the global slog default and the log package.
	restoreLoggingDefaults(t)

	var buf bytes.Buffer
	lv := Setup(Options{Output: &buf})
	if lv == nil {
		t.Fatal("Setup returned a nil LevelVar")
	}
	slog.Info("through-default", "k", "v")
	if !strings.Contains(buf.String(), "msg=through-default") {
		t.Errorf("Setup did not install the default logger: %q", buf.String())
	}
}

func TestSetupLevelVarControlsInstalledLogger(t *testing.T) {
	// Not parallel: Setup mutates the global slog default and the log package.
	restoreLoggingDefaults(t)

	var buf bytes.Buffer
	lv := Setup(Options{Output: &buf})

	slog.Debug("suppressed")
	if buf.Len() != 0 {
		t.Fatalf("Debug emitted at the Info default: %q", buf.String())
	}

	lv.Set(slog.LevelDebug)
	slog.Debug("now-visible")
	if !strings.Contains(buf.String(), "msg=now-visible") {
		t.Errorf("the LevelVar Setup returned does not control the installed logger: %q", buf.String())
	}
}

func TestNewHandlerNilOutputDefaultsToStderr(t *testing.T) {
	// Not parallel: temporarily swaps the global os.Stderr.
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })
	t.Cleanup(func() { _ = r.Close() })

	handler, _ := NewHandler(Options{}) // nil Output must default to os.Stderr
	slog.New(handler).Info("to-stderr")

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if !strings.Contains(string(out), "msg=to-stderr") {
		t.Errorf("nil Output did not default to os.Stderr; captured %q", out)
	}
}

// The contracts below measure the cost of EMITTING a record through a handler
// this library built, which is the only slogx code path that runs more than once
// per process. A busy app calls it millions of times a day, and until these
// landed nothing in the repo measured it: the existing benchmarks cover
// ParseLevel and NewHandler, both of which run once at startup, so their
// allocation counts are close to operationally irrelevant.
//
// Every assertion here is about SCALING rather than about an absolute number, and
// that is deliberate for two reasons. The weekly benchmark tracker already charts
// the absolute per-record cost (BenchmarkEmitRecord in bench_test.go), and it
// compares a series against its own previous value, so it is good at spotting a
// level shift and blind to a shape change — a handler whose cost silently became
// linear in the attribute count looks fine on the chart as long as the benchmark
// keeps passing the same number of attributes. The second reason is mechanical:
// CI runs the suite under -race, and the race detector adds allocations of its
// own per record — measured on go1.27.0, about one per record through the text
// handler and two to three through JSON, and for JSON the count varies run to
// run. An exact equality against a measured literal would therefore flake in CI,
// while a comparison between two measurements taken in the SAME process cancels
// the instrumentation's systematic part and needs only a small allowance for its
// jitter.
//
// What that costs, stated plainly: these contracts do not catch a regression that
// adds one or two allocations per record without changing how cost scales. That
// gap belongs to the benchmark series, and the tracker's own arithmetic makes it
// a weak guard there too (2 allocs/op becoming 3 is a ratio of 1.5, which does
// not alert). Closing it would mean an exact-equality contract compiled only
// when the race detector is off, which CI's -race run would then skip entirely.
//
// Measured on go1.27.0 without instrumentation, for reference when reading a
// failure: an empty record costs 1 allocation through the text handler and 2
// through JSON; up to five attributes are free (slog.Record carries five inline);
// the sixth adds exactly one allocation for the overflow slice, which slog
// pre-sizes, so the count then stays flat to at least 200 attributes.

// emitFormats is the pair of handlers NewHandler can build. Every per-record
// contract runs against both: the two stdlib handlers render a record through
// different code, so a cost regression can land in one and not the other.
var emitFormats = []struct {
	name   string
	format Format
}{
	{"text", Text},
	{"json", JSON},
}

// emitRuns is the sample size for every per-record measurement. The counts are
// integral and stable well below this, so a larger sample buys nothing but time.
const emitRuns = 200

// emitAllocSlack allows for race instrumentation and Go 1.27's pooled JSON
// coder. Independent measurements can differ by three with unchanged code.
// The regressions this test catches are unbounded: a per-attribute allocation
// adds 194 over this span, and a With implementation that re-preformats adds
// 12 for ten attributes.
const emitAllocSlack = 3

// emitAttrs builds n key-value argument pairs, the form the kv-only
// sloglint setting requires at a call site. The slice is returned rather than
// built in place so a caller can construct it outside a measured closure; a
// closure that built its own arguments would report the fixture's allocations as
// the handler's.
func emitAttrs(n int) []any {
	args := make([]any, 0, 2*n)
	for i := range n {
		args = append(args, "key"+strconv.Itoa(i), "value"+strconv.Itoa(i))
	}
	return args
}

// emitMix is the attribute shape a request-handling app actually emits: a
// string, an int, a duration, an error and a group. The kinds matter to cost
// because the handlers render them through different code — the error is the
// expensive one, since a non-Stringer value reaches fmt formatting.
func emitMix() []any {
	return []any{
		"path", "/api/v1/items",
		"count", 42,
		"took", 150 * time.Millisecond,
		"err", errors.New("upstream refused the connection"),
		slog.Group("peer", "addr", "10.0.0.4", "port", 8443),
	}
}

// TestEmitAllocationsAreBoundedRegardlessOfAttributeCount is the central
// per-record contract: the cost of emitting one line must be bounded by a small
// constant above the cost of emitting an empty one, whatever the call site
// passes. An emission cost that grew per attribute would mean every call site
// that adds a field pays for it on every line forever, which is the kind of
// regression that is cheap to introduce (one stray copy in a rendering loop) and
// invisible in a fixed-shape benchmark.
//
// The span runs to 200 attributes, forty times the realistic mix, so a
// per-attribute allocation cannot hide inside the instrumentation allowance: it
// would show up as +194.
func TestEmitAllocationsAreBoundedRegardlessOfAttributeCount(t *testing.T) {
	// Not parallel, and none of the contracts in this file are: AllocsPerRun
	// counts allocations process-wide and pins GOMAXPROCS to 1 while it measures,
	// so a parallel sibling's work would be charged here.
	for _, f := range emitFormats {
		t.Run(f.name, func(t *testing.T) {
			handler, _ := NewHandler(Options{Output: io.Discard, Format: f.format})
			logger := slog.New(handler)

			// The floor every comparison below is made against: one record with
			// no attributes at all, measured in this same process so the
			// instrumentation's systematic overhead cancels out.
			bare := testing.AllocsPerRun(emitRuns, func() {
				logger.Info("request handled")
			})

			mix := emitMix()
			gotMix := testing.AllocsPerRun(emitRuns, func() {
				logger.Info("request handled", mix...)
			})
			if want := bare + emitAllocSlack; gotMix > want {
				t.Errorf("Logger.Info through a %s handler with a realistic 5-attribute mix (string, int, duration, error, group) allocated %v times per run, want at most %v (an empty record costs %v, plus %v for measurement variance): a realistic log line must cost a small constant, not a per-attribute charge that multiplies by log volume",
					f.name, gotMix, want, bare, emitAllocSlack)
			}

			// Counts chosen around slog.Record's five inline attribute slots: 5
			// is the last free one, 6 crosses into the pre-sized overflow slice,
			// and everything above tests that the crossing is charged once
			// rather than per attribute.
			counts := []int{1, 5, 6, 12, 25, 50, 100, 200}
			measured := make([]float64, len(counts))
			for i, n := range counts {
				// Built here, outside the closure: emitAttrs allocates per pair.
				args := emitAttrs(n)
				measured[i] = testing.AllocsPerRun(emitRuns, func() {
					logger.Info("request handled", args...)
				})
				if want := bare + emitAllocSlack; measured[i] > want {
					t.Errorf("Logger.Info through a %s handler with %d attributes allocated %v times per run, want at most %v (an empty record costs %v, plus %v for measurement variance): per-record allocation cost must not grow with the attribute count",
						f.name, n, measured[i], want, bare, emitAllocSlack)
				}
			}

			// The same property as a slope, which is the form that survives a
			// constant offset: the fixed cost of a record cancels between the two
			// ends, leaving the per-attribute rate on its own. Measured 0 — the
			// overflow slice is pre-sized from the argument count, so it is one
			// allocation whether there are six attributes or two hundred.
			lowIdx, highIdx := 2, len(counts)-1 // 6 attributes and 200
			span := float64(counts[highIdx] - counts[lowIdx])
			rate := (measured[highIdx] - measured[lowIdx]) / span
			maxRate := emitAllocSlack / span
			if rate > maxRate {
				t.Errorf("Logger.Info through a %s handler allocated %v times per run at %d attributes and %v at %d, a rate of %.4f per attribute, want at most %.4f: an emission cost that is linear in the attribute count charges every call site for every field on every line",
					f.name, measured[lowIdx], counts[lowIdx], measured[highIdx], counts[highIdx], rate, maxRate)
			}
			t.Logf("%s: empty record %v allocs, realistic mix %v, %d attributes %v, %d attributes %v (%.4f per attribute)",
				f.name, bare, gotMix, counts[lowIdx], measured[lowIdx], counts[highIdx], measured[highIdx], rate)
		})
	}
}

// TestEmitAllocationsDoNotScaleWithValueSize pins the axis an application does
// not fully control: attribute values often carry upstream text whose length is
// somebody else's choice, so emission cost must not track value length. The
// sizes stop at 8 KiB on purpose: slog returns a rendered record's pooled buffer
// only under 16 KiB capacity, so a larger record re-grows each time and costs a
// few more allocations (go1.27.0: 4 text and 5 JSON at 32 KiB, against 1 and 2).
// That step is the standard library's bounded pool policy, not this library's
// cost model, so asserting across it would compare two regimes.
func TestEmitAllocationsDoNotScaleWithValueSize(t *testing.T) {
	sizes := []int{8, 64, 512, 4096, 8192}
	for _, f := range emitFormats {
		t.Run(f.name, func(t *testing.T) {
			handler, _ := NewHandler(Options{Output: io.Discard, Format: f.format})
			logger := slog.New(handler)

			measured := make([]float64, len(sizes))
			for i, size := range sizes {
				// strings.Repeat allocates, so the whole argument slice is built
				// here rather than inside the closure.
				args := []any{"upstream_error", strings.Repeat("x", size)}
				measured[i] = testing.AllocsPerRun(emitRuns, func() {
					logger.Info("request failed", args...)
				})
			}
			for i, got := range measured {
				if want := measured[0] + emitAllocSlack; got > want {
					t.Errorf("Logger.Info through a %s handler with one %d-byte value allocated %v times per run, want at most %v (its cost at %d bytes is %v, plus %v for measurement variance): emission cost must not track the SIZE of a value, or an app that is handed more upstream text pays more allocations per line for it",
						f.name, sizes[i], got, want, sizes[0], measured[0], emitAllocSlack)
				}
			}
			t.Logf("%s: one %d-byte value costs %v allocations and one %d-byte value %v, at %.0fx the bytes",
				f.name, sizes[0], measured[0], sizes[len(sizes)-1], measured[len(measured)-1],
				float64(sizes[len(sizes)-1])/float64(sizes[0]))
		})
	}
}

// TestWithPreformatsOncePerLoggerNotPerRecord pins the whole point of
// Logger.With, which a benchmark chart cannot see: a derived logger renders its
// inherited attributes ONCE, when With is called, and copies the prepared bytes
// into every later record. Redone per record, a logger carrying a dozen base
// attributes (a service name, a version, a request id) would pay for all of them
// on every line. The assertion is that a derived logger's per-record cost equals
// a bare logger's, within the instrumentation allowance, at every preformat width
// from one attribute to two hundred.
func TestWithPreformatsOncePerLoggerNotPerRecord(t *testing.T) {
	widths := []int{1, 5, 10, 50, 200}
	for _, f := range emitFormats {
		t.Run(f.name, func(t *testing.T) {
			handler, _ := NewHandler(Options{Output: io.Discard, Format: f.format})
			logger := slog.New(handler)

			bare := testing.AllocsPerRun(emitRuns, func() {
				logger.Info("request handled")
			})

			widthCosts := make([]float64, len(widths))
			for i, n := range widths {
				// The derivation happens here, in setup, which is where an app
				// does it too: once, at wiring time.
				derived := logger.With(emitAttrs(n)...)
				widthCosts[i] = testing.AllocsPerRun(emitRuns, func() {
					derived.Info("request handled")
				})
				if want := bare + emitAllocSlack; widthCosts[i] > want {
					t.Errorf("Logger.With(%d attributes) then Info through a %s handler allocated %v times per run, want at most %v (an undecorated logger costs %v, plus %v for measurement variance): a derived logger must render its inherited attributes once at With time, not on every record, or every base attribute an app attaches is charged to every line it logs",
						n, f.name, widthCosts[i], want, bare, emitAllocSlack)
				}
			}

			// WithGroup nesting takes a different path through the handler's
			// preformatting (it opens groups in the prepared prefix), so it gets
			// its own check rather than riding on the flat case.
			nested := logger.WithGroup("request").With(emitAttrs(5)...).WithGroup("peer").With("addr", "10.0.0.4")
			gotNested := testing.AllocsPerRun(emitRuns, func() {
				nested.Info("request handled")
			})
			if want := bare + emitAllocSlack; gotNested > want {
				t.Errorf("Logger.WithGroup then With then Info through a %s handler allocated %v times per run, want at most %v (an undecorated logger costs %v, plus %v for measurement variance): group nesting must be preformatted with the attributes it wraps",
					f.name, gotNested, want, bare, emitAllocSlack)
			}

			// A control, so the assertions above cannot pass vacuously. This is
			// the regression they claim to guard against, written out: deriving
			// the logger inside the measured closure instead of once in setup.
			// If this does NOT measure materially more than a bare record, the
			// measurement has no resolving power and the contract above is
			// meaningless.
			reArgs := emitAttrs(10)
			reDerived := testing.AllocsPerRun(emitRuns, func() {
				logger.With(reArgs...).Info("request handled")
			})
			if floor := bare + emitAllocSlack; reDerived <= floor {
				t.Errorf("control: re-deriving a logger with 10 attributes per record through a %s handler allocated %v times per run, want more than %v: preformatting 10 attributes on every record cannot be as cheap as emitting a bare one, so this measurement cannot see the regression the assertions above rely on it seeing",
					f.name, reDerived, floor)
			}
			t.Logf("%s: an undecorated record costs %v allocations; a logger derived with %d attributes costs %v and one derived with %d costs %v; re-deriving 10 attributes per record instead costs %v",
				f.name, bare, widths[0], widthCosts[0], widths[len(widths)-1], widthCosts[len(widthCosts)-1], reDerived)
		})
	}
}

// TestBelowLevelCallIsAllocationFree pins the highest-leverage property any
// logging setup has. Callers leave Debug calls in hot code on the understanding
// that a suppressed line is free, and some deliberately put work in the
// arguments because of it. If a below-level call allocated, every one of those
// call sites would be paying at production log level for output nobody reads,
// and the cost would be invisible in the logs by definition.
//
// The measured answer on go1.27.0 is zero, under both handlers, with and without
// the race detector: slog checks the level before it reads the clock, captures a
// caller PC or builds a Record, so the whole cost of a suppressed call is a
// LevelVar load and a comparison. The one exception is on the caller's side of
// the API and is asserted below as such.
func TestBelowLevelCallIsAllocationFree(t *testing.T) {
	for _, f := range emitFormats {
		t.Run(f.name, func(t *testing.T) {
			handler, levelVar := NewHandler(Options{Output: io.Discard, Format: f.format, Level: slog.LevelInfo})
			logger := slog.New(handler)

			// Fixture guard: the whole test is vacuous if Debug is enabled, and
			// asserting the level directly says so at the point it matters.
			if levelVar.Level() != slog.LevelInfo {
				t.Fatalf("NewHandler(Options{Level: slog.LevelInfo}) built a handler at level %v, want Info: the fixture must suppress Debug", levelVar.Level())
			}
			if logger.Enabled(t.Context(), slog.LevelDebug) {
				t.Fatalf("Logger.Enabled(Debug) at level %v = true, want false: the fixture must suppress Debug", levelVar.Level())
			}

			// Arguments are built in setup, so what is measured is the cost slog
			// pays to discard the call, not the cost the caller paid to describe
			// it. The derived logger is built here for the same reason: deriving
			// is not free and does not consult the level, which
			// TestDerivingALoggerPerCallCostsEvenBelowTheLevel covers.
			args := []any{
				"path", "/api/v1/items",
				"payload", strings.Repeat("x", 1<<20),
				"err", errors.New("upstream refused the connection"),
				slog.Group("peer", "addr", "10.0.0.4", "port", 8443),
			}
			derived := logger.With("service", "slogx", "version", "1.4.0")
			shapes := []struct {
				name string
				call func()
			}{
				{"no arguments", func() { logger.Debug("expensive diagnostic") }},
				{"1 attribute", func() { logger.Debug("expensive diagnostic", "key0", "value0") }},
				{"4 attributes, one a megabyte", func() { logger.Debug("expensive diagnostic", args...) }},
				{"a logger derived with 2 attributes", func() { derived.Debug("expensive diagnostic") }},
			}
			for _, shape := range shapes {
				t.Run(shape.name, func(t *testing.T) {
					if got := testing.AllocsPerRun(emitRuns, shape.call); got != 0 {
						t.Errorf("Logger.Debug through a %s handler at level Info, %s, allocated %v times per run, want 0: callers put expensive work behind a below-level call on the assumption that a suppressed line costs nothing beyond the level check",
							f.name, shape.name, got)
					}
				})
			}

			// Argument counts and sizes, to show the zero is not an artifact of a
			// small call. A suppressed call must cost nothing whatever it was
			// handed, or an app that logs more detail at Debug pays for the
			// detail in production.
			for _, n := range []int{1, 5, 25, 100} {
				wide := emitAttrs(n)
				if got := testing.AllocsPerRun(emitRuns, func() {
					logger.Debug("expensive diagnostic", wide...)
				}); got != 0 {
					t.Errorf("Logger.Debug through a %s handler at level Info with %d attributes allocated %v times per run, want 0: the cost of a suppressed call must not grow with the number of arguments",
						f.name, n, got)
				}
			}
		})
	}
}

// TestBelowLevelCallChargesTheCallerForBoxedArguments records the one way a
// suppressed log call is not free, because it is the caller's half of the
// contract above and the only one a call site can get wrong.
//
// slog takes its key-value arguments as ...any, so every value is boxed into an
// interface at the CALL SITE, before slog is entered and before it can check the
// level. A scalar costs nothing (the compiler keeps the box off the heap, and
// small integers come from a static table), but a value type wider than a
// pointer — time.Time is the one every app reaches for — is copied to the heap
// even though the record is about to be discarded. Logger.LogAttrs is the escape
// hatch: a slog.Attr carries the value without boxing it.
//
// The numbers here are the compiler's, not this library's. A change in either
// direction is worth knowing about: upward means suppressed calls started paying
// for their arguments, downward means escape analysis improved and this contract
// should be tightened.
func TestBelowLevelCallChargesTheCallerForBoxedArguments(t *testing.T) {
	handler, _ := NewHandler(Options{Output: io.Discard, Level: slog.LevelInfo})
	logger := slog.New(handler)
	ctx := t.Context()
	// Built in setup: measuring time.Now() inside the closure would measure the
	// clock read as well as the boxing.
	instant := time.Now()

	shapes := []struct {
		name       string
		wantAllocs float64
		reason     string
		call       func()
	}{
		{
			name:       "scalar arguments",
			wantAllocs: 0,
			reason:     "a string and a small int box without touching the heap",
			call:       func() { logger.Debug("expensive diagnostic", "path", "/api/v1/items", "count", 42) },
		},
		{
			name:       "a time.Time argument",
			wantAllocs: 1,
			reason:     "a 24-byte value type does not fit in an interface word, so boxing it copies it to the heap before slog can discard the call",
			call:       func() { logger.Debug("expensive diagnostic", "at", instant) },
		},
		{
			name:       "the same time.Time through LogAttrs",
			wantAllocs: 0,
			reason:     "slog.Time carries the value in an Attr, so nothing is boxed and the suppressed call is free",
			call:       func() { logger.LogAttrs(ctx, slog.LevelDebug, "expensive diagnostic", slog.Time("at", instant)) },
		},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(emitRuns, shape.call); got != shape.wantAllocs {
				t.Errorf("Logger.Debug at level Info with %s allocated %v times per run, want %v (%s): a number above %v means a suppressed call started paying for its arguments; a number below it is an improvement worth tightening this contract to",
					shape.name, got, shape.wantAllocs, shape.reason, shape.wantAllocs)
			}
		})
	}
}

// TestDerivingALoggerPerCallCostsEvenBelowTheLevel records the trap that the
// zero-cost contract above makes easy to walk into. Logger.With calls
// Handler.WithAttrs, which renders the attributes into the handler's prepared
// prefix immediately; nothing in that path consults the level, because a derived
// logger has no level yet — it is a value that may be logged through at any
// level later. So `logger.With("id", id).Debug(msg)` pays the full derivation on
// every call at production level, even though the record is discarded, which is
// exactly the cost a reader of "a below-level call is free" would not expect.
//
// The remedy is the one Logger.With is designed for and
// TestWithPreformatsOncePerLoggerNotPerRecord measures: derive once, keep the
// derived logger, log through it.
//
// The assertion is that the cost is greater than zero rather than an exact
// number, because the exact number is the standard library handler's
// preformatting cost and would pin an implementation detail. If this ever fails,
// slog has made WithAttrs lazy: that is an improvement, and this contract should
// be replaced with a zero-allocation assertion.
func TestDerivingALoggerPerCallCostsEvenBelowTheLevel(t *testing.T) {
	for _, f := range emitFormats {
		t.Run(f.name, func(t *testing.T) {
			handler, _ := NewHandler(Options{Output: io.Discard, Format: f.format, Level: slog.LevelInfo})
			logger := slog.New(handler)
			if logger.Enabled(t.Context(), slog.LevelDebug) {
				t.Fatalf("Logger.Enabled(Debug) at level Info = true, want false: the fixture must suppress Debug")
			}
			args := emitAttrs(2)

			got := testing.AllocsPerRun(emitRuns, func() {
				logger.With(args...).Debug("expensive diagnostic")
			})
			if got <= 0 {
				t.Errorf("Logger.With(2 attributes).Debug through a %s handler at level Info allocated %v times per run, want more than 0: Handler.WithAttrs preformats without checking the level, so this measurement documents a real caller-side cost — a result of 0 means slog made derivation lazy and this contract should become a zero-allocation assertion",
					f.name, got)
			}
			t.Logf("%s: deriving a logger with 2 attributes per call costs %v allocations even at a level that discards the record; derive once instead", f.name, got)
		})
	}
}
