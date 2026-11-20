package sealed

import (
	"strconv"
	"testing"
	"time"
)

// benchPurpose is the purpose every benchmark seals for.
const benchPurpose Purpose = "example/bench"

// cursorLen is a long page cursor's payload, 1,025 bytes.
const cursorLen = 1025

func BenchmarkSeal(b *testing.B) {
	for _, n := range []int{cursorLen, MaxPayload} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			s := New(k1)
			payload := make([]byte, n)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.Seal(benchPurpose, payload, time.Hour); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkOpen(b *testing.B) {
	for _, n := range []int{cursorLen, MaxPayload} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			s := New(k1)
			benchOpen(b, s.Open, sealExpiring(b, s, n), true)
		})
	}
}

func BenchmarkSealAtRest(b *testing.B) {
	s := New(k1)
	payload := make([]byte, MaxAtRestPayload)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.SealAtRest(benchPurpose, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOpenAtRest(b *testing.B) {
	s := New(k1)
	value, err := s.SealAtRest(benchPurpose, make([]byte, MaxAtRestPayload))
	if err != nil {
		b.Fatal(err)
	}
	benchOpen(b, s.OpenAtRest, value, true)
}

// BenchmarkOpenPrevious opens a cursor under the previous key, which the
// open reaches after the current key fails.
func BenchmarkOpenPrevious(b *testing.B) {
	s := ringSealer(b, k2, k1)
	benchOpen(b, s.Open, sealExpiring(b, New(k1), cursorLen), false)
}

// sealExpiring returns s's Seal of n zero bytes for an hour.
func sealExpiring(b *testing.B, s *Sealer, n int) string {
	b.Helper()
	value, err := s.Seal(benchPurpose, make([]byte, n), time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	return value
}

// benchOpen times open of value, after checking once that it opens with
// the current flag want, so the loop times the path it names.
func benchOpen(b *testing.B, open opener, value string, want bool) {
	b.Helper()
	if _, current, err := open(benchPurpose, value); err != nil || current != want {
		b.Fatalf("open = %v, %v; want %v, nil", current, err, want)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := open(benchPurpose, value); err != nil {
			b.Fatal(err)
		}
	}
}
