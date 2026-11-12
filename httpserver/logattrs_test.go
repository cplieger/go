package httpserver_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/go/httpserver"
)

// keysOf lists a record's attribute keys in order.
func keysOf(r slog.Record) []string {
	var keys []string
	r.Attrs(func(a slog.Attr) bool {
		keys = append(keys, a.Key)
		return true
	})
	return keys
}

// addingHandler answers 200 after adding attrs to its request's access line.
func addingHandler(attrs ...slog.Attr) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpserver.AddLogAttrs(r.Context(), attrs...)
		w.WriteHeader(http.StatusOK)
	})
}

// oneLine serves h once under Logging and returns its only access line.
func oneLine(t *testing.T, h http.Handler, opts ...httpserver.LogOption) slog.Record {
	t.Helper()
	logCap := &captureHandler{}
	opts = append([]httpserver.LogOption{httpserver.WithLogger(slog.New(logCap))}, opts...)
	serve(httpserver.Logging(opts...)(h), http.MethodGet, "/v1/thing", nil)
	recs := logCap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	return recs[0]
}

func TestAddLogAttrs_appendsAfterTheLinesOwnAttributes(t *testing.T) {
	trusted := netip.MustParsePrefix("192.0.2.0/24")
	r := oneLine(t, addingHandler(slog.String("key", "deploy-bot"), slog.Int("scopes", 2)), httpserver.WithClientIP(trusted))

	got := strings.Join(keysOf(r), ",")
	if want := "method,path,status,duration_ms,request_id,client_ip,key,scopes"; got != want {
		t.Errorf("attribute keys = %s, want %s", got, want)
	}
	m := attrsOf(r)
	if m["key"] != "deploy-bot" {
		t.Errorf("key = %v, want deploy-bot", m["key"])
	}
	if m["scopes"] != int64(2) {
		t.Errorf("scopes = %v, want 2", m["scopes"])
	}
}

func TestAddLogAttrs_doesNothingWithoutAnAccessLine(t *testing.T) {
	t.Run("no Logging in the chain", func(t *testing.T) {
		httpserver.AddLogAttrs(context.Background(), slog.String("key", "x"))
	})
	t.Run("a skipped path", func(t *testing.T) {
		logCap := &captureHandler{}
		h := httpserver.Logging(httpserver.WithLogger(slog.New(logCap)), httpserver.WithSkipPaths("/v1/thing"))(addingHandler(slog.String("key", "x")))
		serve(h, http.MethodGet, "/v1/thing", nil)
		if n := len(logCap.snapshot()); n != 0 {
			t.Errorf("got %d log records for a skipped path, want 0", n)
		}
	})
}

func TestAddLogAttrs_dropsWhatTheLineCannotTake(t *testing.T) {
	t.Run("reserved and empty keys", func(t *testing.T) {
		r := oneLine(t, addingHandler(
			slog.String("status", "forged"),
			slog.String("request_id", "forged"),
			slog.String("client_ip", "forged"),
			slog.Group("", slog.String("path", "forged")),
			slog.String("key", "kept"),
		))
		m := attrsOf(r)
		if m["status"] != int64(http.StatusOK) {
			t.Errorf("status = %v, want the recorded 200", m["status"])
		}
		if id, _ := m["request_id"].(string); id == "forged" {
			t.Error("request_id was overwritten by an added attribute")
		}
		if m["path"] != "/v1/thing" {
			t.Errorf("path = %v, want the routed path, not an inlined group's", m["path"])
		}
		if _, ok := m["client_ip"]; ok {
			t.Error("an added client_ip reached a line without WithClientIP")
		}
		if got := strings.Join(keysOf(r), ","); got != "method,path,status,duration_ms,request_id,key" {
			t.Errorf("attribute keys = %s", got)
		}
	})
	t.Run("past the eighth across calls", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for i := range 5 {
				httpserver.AddLogAttrs(r.Context(), slog.Int("a"+string(rune('0'+i)), i), slog.Int("b"+string(rune('0'+i)), i))
			}
			w.WriteHeader(http.StatusOK)
		})
		keys := keysOf(oneLine(t, h))
		added := keys[5:]
		if got := strings.Join(added, ","); got != "a0,b0,a1,b1,a2,b2,a3,b3" {
			t.Errorf("added keys = %s, want the first eight", got)
		}
	})
}

type panickingValuer struct{}

func (panickingValuer) LogValue() slog.Value { panic("valuer exploded") }

func TestAddLogAttrs_boundsEachValue(t *testing.T) {
	long := strings.Repeat("é", 200)
	members := make([]any, 0, 10)
	for i := range 10 {
		members = append(members, slog.Int("m"+string(rune('0'+i)), i))
	}
	r := oneLine(t, addingHandler(
		slog.String("label", long),
		slog.Any("err", errors.New(strings.Repeat("x", 1000))),
		slog.Group("grp", members...),
		slog.Any("valuer", panickingValuer{}),
	))
	m := attrsOf(r)

	t.Run("a long string is cut on a rune boundary", func(t *testing.T) {
		label, _ := m["label"].(string)
		if !strings.HasSuffix(label, "...(truncated)") {
			t.Fatalf("label = %q, want the truncated marker", label)
		}
		kept := strings.TrimSuffix(label, "...(truncated)")
		if len(kept) > 256 || !strings.HasPrefix(long, kept) || len(kept)%2 != 0 {
			t.Errorf("kept %d bytes, want at most 256 whole runes of the value", len(kept))
		}
	})
	t.Run("a non-string value is bounded as text", func(t *testing.T) {
		if s, ok := m["err"].(string); !ok || len(s) > 256+len("...(truncated)") {
			t.Errorf("err = %T of %d bytes, want a string of at most 256 bytes plus the marker", m["err"], len(s))
		}
	})
	t.Run("a group keeps eight members", func(t *testing.T) {
		g, _ := m["grp"].([]slog.Attr)
		if len(g) != 8 {
			t.Errorf("grp has %d members, want 8", len(g))
		}
	})
	t.Run("a panicking LogValuer is recorded, not raised", func(t *testing.T) {
		// slog.Value.Resolve turns a panicking LogValue into an error value
		// reading "LogValue panicked" and the call stack.
		if s, _ := m["valuer"].(string); !strings.HasPrefix(s, "LogValue panicked") {
			t.Errorf("valuer = %v, want slog's recovered-panic value", m["valuer"])
		}
	})
}

func TestAddLogAttrs_concurrentAddsAllLand(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wg sync.WaitGroup
		for i := range 4 {
			wg.Go(func() { httpserver.AddLogAttrs(r.Context(), slog.Int("g"+string(rune('0'+i)), i)) })
		}
		wg.Wait()
		w.WriteHeader(http.StatusOK)
	})
	if n := len(keysOf(oneLine(t, h))); n != 9 {
		t.Errorf("got %d attributes, want the line's 5 plus 4 added", n)
	}
}

func TestAddLogAttrs_survivesAPanickingHandler(t *testing.T) {
	logCap := &captureHandler{}
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		httpserver.AddLogAttrs(r.Context(), slog.String("key", "deploy-bot"))
		panic("boom")
	})
	h := httpserver.Chain(next, httpserver.Logging(httpserver.WithLogger(slog.New(logCap))), httpserver.Recoverer(httpserver.WithRecoverLogger(discardLogger())))
	serve(h, http.MethodGet, "/v1/thing", nil)

	var line slog.Record
	for _, r := range logCap.snapshot() {
		if r.Message == "http" {
			line = r
		}
	}
	m := attrsOf(line)
	if m["status"] != int64(http.StatusInternalServerError) {
		t.Errorf("status = %v, want 500", m["status"])
	}
	if m["key"] != "deploy-bot" {
		t.Errorf("key = %v, want deploy-bot", m["key"])
	}
}
