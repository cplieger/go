package chartdata_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

var nan = math.NaN()

func gauge(name string, unit measure.Unit) *measure.Gauge {
	return measure.NewGauge(name, "h", unit)
}

// wantRefusal fails t unless err is a RefusalError of rule on field.
func wantRefusal(t *testing.T, err error, rule chartdata.Rule, field string) {
	t.Helper()
	var refusal *chartdata.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a %q refusal of %s", err, rule, field)
	}
	if refusal.Rule != rule || refusal.Field != field {
		t.Errorf("refusal = %q of %s (%v), want %q of %s", refusal.Rule, refusal.Field, err, rule, field)
	}
}

// encode returns v's JSON, failing t when it does not encode.
func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

// logCapture collects the lines of a logger handed to the code under test.
type logCapture struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(c, nil))
}

// count returns how many lines hold msg.
func (c *logCapture) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Count(c.buf.String(), msg)
}
