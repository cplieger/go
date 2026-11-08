package slogx

import (
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"
)

func BenchmarkParseLevel(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, _ = ParseLevel("warning", slog.LevelInfo)
	}
}

func BenchmarkNewHandler(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, _ = NewHandler(Options{Output: io.Discard})
	}
}

func BenchmarkUTCTime(b *testing.B) {
	attr := slog.Time(slog.TimeKey, time.Now())
	b.ReportAllocs()
	for b.Loop() {
		_ = UTCTime(nil, attr)
	}
}

// The three benchmarks above cover the startup path: ParseLevel and NewHandler
// run once per process, and UTCTime is pinned at zero allocations by
// TestUTCTimeIsAllocationFree. The series below cover the hot path — a handler
// this library configured, emitting records — which nothing measured before.
// They exist to give the weekly tracker the absolute per-record numbers that the
// contracts in handler_test.go deliberately do not assert, because CI runs the
// suite under -race and the race detector perturbs those counts. Fixtures
// (emitFormats, emitAttrs, emitMix) are shared with those contracts and defined
// in handler_test.go beside them.
//
// Every series writes to io.Discard: the subject is the handler's rendering and
// allocation behavior, and a real file or stderr would measure the kernel.

// BenchmarkEmitRecord is the series that matters most in this file: one log line
// through each format, at the attribute counts either side of slog.Record's five
// inline slots. Read allocs/op as a step function, not a trend — the sixth
// attribute adds exactly one allocation for the overflow slice and the count is
// then flat, so a rise between attrs_6 and attrs_50 is a real regression while
// the step from attrs_5 to attrs_6 is the data structure.
//
// B/op does grow with the attribute count, and that is the number to watch here:
// it is what actually scales with how much an app logs.
func BenchmarkEmitRecord(b *testing.B) {
	for _, f := range emitFormats {
		b.Run(f.name, func(b *testing.B) {
			handler, _ := NewHandler(Options{Output: io.Discard, Format: f.format})
			logger := slog.New(handler)

			b.Run("empty", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					logger.Info("request handled")
				}
			})

			// The realistic mix: a string, an int, a duration, an error and a
			// group. This is the series a consuming app should recognize as its
			// own cost per line.
			mix := emitMix()
			b.Run("realistic_mix", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					logger.Info("request handled", mix...)
				}
			})

			for _, n := range []int{1, 5, 6, 50} {
				args := emitAttrs(n)
				b.Run("attrs_"+strconv.Itoa(n), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						logger.Info("request handled", args...)
					}
				})
			}
		})
	}
}

// BenchmarkEmitRecordWith charts the two ways an app can attach base attributes
// to every line, and the gap between them is the whole reason Logger.With
// exists. derived_10 pays for the ten attributes once, at derivation; inline_10
// passes them on every call; rederived_10 is the mistake — deriving inside the
// logging path — and it is here as the visible cost of getting it wrong rather
// than as a series anyone should try to improve.
//
// TestWithPreformatsOncePerLoggerNotPerRecord gates the property; this shows
// what it is worth.
func BenchmarkEmitRecordWith(b *testing.B) {
	const width = 10
	for _, f := range emitFormats {
		b.Run(f.name, func(b *testing.B) {
			handler, _ := NewHandler(Options{Output: io.Discard, Format: f.format})
			logger := slog.New(handler)
			args := emitAttrs(width)
			derived := logger.With(args...)

			b.Run("derived_10", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					derived.Info("request handled")
				}
			})
			b.Run("inline_10", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					logger.Info("request handled", args...)
				}
			})
			b.Run("rederived_10", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					logger.With(args...).Info("request handled")
				}
			})
		})
	}
}

// BenchmarkEmitBelowLevel charts what a suppressed call costs, which is the
// number every Debug call left in hot code depends on. It should be a level load
// and a comparison, with allocs/op at zero whatever the arguments are
// (TestBelowLevelCallIsAllocationFree gates that); ns/op rising here means the
// level check stopped being the first thing that happens.
//
// The arguments are built in setup for the same reason the contracts build
// theirs there: boxing them per iteration would chart the caller's cost as the
// handler's.
func BenchmarkEmitBelowLevel(b *testing.B) {
	args := emitAttrs(10)
	for _, f := range emitFormats {
		b.Run(f.name, func(b *testing.B) {
			handler, _ := NewHandler(Options{Output: io.Discard, Format: f.format, Level: slog.LevelInfo})
			logger := slog.New(handler)
			if logger.Enabled(b.Context(), slog.LevelDebug) {
				b.Fatal("Logger.Enabled(Debug) = true at level Info, want false: this series must measure a SUPPRESSED call")
			}

			b.Run("empty", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					logger.Debug("expensive diagnostic")
				}
			})
			b.Run("attrs_10", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					logger.Debug("expensive diagnostic", args...)
				}
			})
		})
	}
}
