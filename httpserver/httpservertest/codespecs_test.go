package httpservertest

import (
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

// recordingTB keeps the failures a check reports instead of failing the test
// that runs it. Fatalf ends the goroutine as testing's does, so a check runs
// through record.
type recordingTB struct {
	testing.TB
	errors []string
	fatal  string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// failures joins every failure a check reported.
func (r *recordingTB) failures() string {
	return strings.Join(append(slices.Clone(r.errors), r.fatal), "\n")
}

// record runs check on its own goroutine against a recordingTB.
func record(t *testing.T, check func(testing.TB)) *recordingTB {
	t.Helper()
	r := &recordingTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		check(r)
	}()
	<-done
	return r
}

const fixtureDir = "testdata/codespecs/app"

var fixtureCodes = []httpserver.ErrorCode{
	"const_code", "converted_code", "field_code", "func_type_code",
	"literal_code", "option_code", "responder_code",
}

func TestWrittenCodesFindsEachWriterShape(t *testing.T) {
	got, err := writtenCodes(fixtureDir)
	if err != nil {
		t.Fatalf("writtenCodes: %v", err)
	}
	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, fixtureCodes) {
		t.Errorf("written codes = %q, want %q", keys, fixtureCodes)
	}
	if at := got["literal_code"]; !strings.Contains(at, "app.go:") {
		t.Errorf("position of literal_code = %q, want it in app.go", at)
	}
}

func specsFor(codes ...httpserver.ErrorCode) map[httpserver.ErrorCode]httpserver.CodeSpec {
	m := make(map[httpserver.ErrorCode]httpserver.CodeSpec, len(codes))
	for _, c := range codes {
		m[c] = httpserver.CodeSpec{Status: 400}
	}
	return m
}

func TestCheckCodeSpecs(t *testing.T) {
	t.Run("exact set passes", func(t *testing.T) {
		r := record(t, func(tb testing.TB) { CheckCodeSpecs(tb, fixtureDir, specsFor(fixtureCodes...)) })
		if len(r.errors) != 0 || r.fatal != "" {
			t.Errorf("failures = %q %q, want none", r.errors, r.fatal)
		}
	})
	t.Run("missing and extra codes are named", func(t *testing.T) {
		r := record(t, func(tb testing.TB) {
			CheckCodeSpecs(tb, fixtureDir, specsFor(append(slices.Clone(fixtureCodes[1:]), "never_written")...))
		})
		joined := strings.Join(r.errors, "\n")
		if len(r.errors) != 2 || !strings.Contains(joined, `"const_code"`) || !strings.Contains(joined, `"never_written"`) {
			t.Errorf("failures = %q, want one for const_code and one for never_written", r.errors)
		}
	})
	t.Run("a directory with no source is fatal", func(t *testing.T) {
		r := record(t, func(tb testing.TB) { CheckCodeSpecs(tb, t.TempDir(), nil) })
		if r.fatal == "" {
			t.Error("no fatal failure for an empty directory")
		}
	})
}
