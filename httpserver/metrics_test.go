package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func text(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	})
}

// serving starts m.Serve and returns the base URL and a stop function that
// cancels it and returns Serve's error.
func serving(t *testing.T, m *MetricsServer) (base string, stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Serve(ctx) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			err = <-done
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return "http://" + m.Addr().String(), stop
}

func get(t *testing.T, method, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestListenMetrics_servesExactlyTheTwoRoutes: GET /metrics and GET /healthz
// reach their handlers, any other path is 404 and any other method 405, so the
// listener never serves the app's own routes.
func TestListenMetrics_servesExactlyTheTwoRoutes(t *testing.T) {
	m, err := ListenMetrics(context.Background(), "127.0.0.1:0", text("metrics page"), text("healthy"))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := serving(t, m)
	for name, tc := range map[string]struct {
		method, path string
		code         int
		body         string
	}{
		"metrics":         {http.MethodGet, "/metrics", http.StatusOK, "metrics page"},
		"healthz":         {http.MethodGet, "/healthz", http.StatusOK, "healthy"},
		"another path":    {http.MethodGet, "/api/things", http.StatusNotFound, ""},
		"root":            {http.MethodGet, "/", http.StatusNotFound, ""},
		"another method":  {http.MethodPost, "/metrics", http.StatusMethodNotAllowed, ""},
		"healthz by POST": {http.MethodPost, "/healthz", http.StatusMethodNotAllowed, ""},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := get(t, tc.method, base+tc.path)
			if code != tc.code {
				t.Errorf("%s %s = %d, want %d", tc.method, tc.path, code, tc.code)
			}
			if tc.body != "" && body != tc.body {
				t.Errorf("%s %s body = %q, want %q", tc.method, tc.path, body, tc.body)
			}
		})
	}
}

// TestListenMetrics_bindsBeforeServing: the address is taken when
// ListenMetrics returns, so a second bind of it fails there, naming the
// address, before the caller starts any work.
func TestListenMetrics_bindsBeforeServing(t *testing.T) {
	first, err := ListenMetrics(context.Background(), "127.0.0.1:0", text(""), text(""))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = serving(t, first)
	addr := first.Addr().String()
	second, err := ListenMetrics(context.Background(), addr, text(""), text(""))
	if err == nil {
		_ = second.ln.Close()
		t.Fatalf("a second ListenMetrics on %s succeeded, want a bind error", addr)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("bind error %q does not name %s", err, addr)
	}
}

// TestListenMetrics_emptyAddressOpensNothing: an empty address is the
// listener switched off, so Serve returns at once and Addr is nil.
func TestListenMetrics_emptyAddressOpensNothing(t *testing.T) {
	m, err := ListenMetrics(context.Background(), "", text(""), text(""))
	if err != nil {
		t.Fatalf("ListenMetrics(\"\") = %v, want nil", err)
	}
	if m.Addr() != nil {
		t.Errorf("Addr() = %v, want nil", m.Addr())
	}
	done := make(chan error, 1)
	go func() { done <- m.Serve(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve with the listener off = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve with the listener off did not return")
	}
}

// TestListenMetrics_stopsCleanlyOnCancel: cancelling Serve's context shuts
// the listener down and returns nil, and the address is free again.
func TestListenMetrics_stopsCleanlyOnCancel(t *testing.T) {
	m, err := ListenMetrics(context.Background(), "127.0.0.1:0", text(""), text(""))
	if err != nil {
		t.Fatal(err)
	}
	addr := m.Addr().String()
	_, stop := serving(t, m)
	if err := stop(); err != nil {
		t.Fatalf("Serve after cancel = %v, want nil", err)
	}
	again, err := ListenMetrics(context.Background(), addr, text(""), text(""))
	if err != nil {
		t.Fatalf("rebinding %s after shutdown: %v", addr, err)
	}
	_ = again.ln.Close()
}

// TestListenMetrics_boundsTheWholeRequest: both routes answer in one short
// response, so the server bounds the request body and the response, where
// New leaves both open for streams.
func TestListenMetrics_boundsTheWholeRequest(t *testing.T) {
	m, err := ListenMetrics(context.Background(), "127.0.0.1:0", text(""), text(""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.ln.Close() })
	if m.srv.ReadTimeout != metricsReadTimeout || m.srv.WriteTimeout != metricsWriteTimeout {
		t.Errorf("timeouts read %v write %v, want %v and %v",
			m.srv.ReadTimeout, m.srv.WriteTimeout, metricsReadTimeout, metricsWriteTimeout)
	}
	if m.srv.ReadHeaderTimeout != defaultReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want New's %v", m.srv.ReadHeaderTimeout, defaultReadHeaderTimeout)
	}
}

// TestListenMetrics_logsBothRoutesAtDebugAndRecovers: a scrape and a probe
// stay out of the log at the default level, and a panicking handler answers
// 500 instead of dropping the connection.
func TestListenMetrics_logsBothRoutesAtDebugAndRecovers(t *testing.T) {
	var buf strings.Builder
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	panics := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	m, err := ListenMetrics(context.Background(), "127.0.0.1:0", text("page"), panics)
	if err != nil {
		t.Fatal(err)
	}
	base, stop := serving(t, m)
	if code, _ := get(t, http.MethodGet, base+"/metrics"); code != http.StatusOK {
		t.Fatalf("GET /metrics = %d", code)
	}
	if code, _ := get(t, http.MethodGet, base+"/healthz"); code != http.StatusInternalServerError {
		t.Errorf("GET /healthz with a panicking handler = %d, want 500", code)
	}
	_ = stop()
	mu.Lock()
	logged := buf.String()
	mu.Unlock()
	for line := range strings.SplitSeq(strings.TrimSpace(logged), "\n") {
		if strings.Contains(line, "path=/metrics") && !strings.Contains(line, "level=DEBUG") {
			t.Errorf("the scrape's access line is not at Debug: %s", line)
		}
	}
	if !strings.Contains(logged, "path=/metrics") {
		t.Errorf("no access line for /metrics; log:\n%s", logged)
	}
	if !strings.Contains(logged, "recovered from panic") {
		t.Errorf("the panic was not recovered and logged; log:\n%s", logged)
	}
}

// TestListenMetrics_nilHandlerPanicsWithTheListenerOff: a nil handler is the
// caller's bug, so it panics even when no listener would open.
func TestListenMetrics_nilHandlerPanicsWithTheListenerOff(t *testing.T) {
	for name, handlers := range map[string][2]http.Handler{
		"nil metrics": {nil, text("")},
		"nil healthz": {text(""), nil},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("ListenMetrics with a nil handler did not panic")
				}
			}()
			_, _ = ListenMetrics(context.Background(), "", handlers[0], handlers[1])
		})
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
