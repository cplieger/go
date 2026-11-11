package httpserver

import (
	"context"
	"errors"
	"fmt"
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
	ctx, cancel := context.WithCancel(t.Context())
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
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
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
// reach their handlers, HEAD too since a GET pattern matches it, any other
// path is 404 and any other method 405, so the listener never serves the app's
// own routes.
func TestListenMetrics_servesExactlyTheTwoRoutes(t *testing.T) {
	m, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text("metrics page"), Healthz: text("healthy")})
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
		"metrics by HEAD": {http.MethodHead, "/metrics", http.StatusOK, ""},
		"healthz by HEAD": {http.MethodHead, "/healthz", http.StatusOK, ""},
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
	first, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	addr := first.Addr().String()
	second, err := ListenMetrics(t.Context(), addr, MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err == nil {
		_ = second.Close()
		t.Fatalf("a second ListenMetrics on %s succeeded, want a bind error", addr)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("bind error %q does not name %s", err, addr)
	}
}

// TestListenMetrics_emptyAddressOpensNothing: an empty address is the
// listener switched off, so Serve returns at once and Addr is nil.
func TestListenMetrics_emptyAddressOpensNothing(t *testing.T) {
	m, err := ListenMetrics(t.Context(), "", MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatalf("ListenMetrics(\"\") = %v, want nil", err)
	}
	if m.Addr() != nil {
		t.Errorf("Addr() = %v, want nil", m.Addr())
	}
	done := make(chan error, 1)
	go func() { done <- m.Serve(t.Context()) }()
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
	m, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatal(err)
	}
	addr := m.Addr().String()
	_, stop := serving(t, m)
	if err := stop(); err != nil {
		t.Fatalf("Serve after cancel = %v, want nil", err)
	}
	again, err := ListenMetrics(t.Context(), addr, MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatalf("rebinding %s after shutdown: %v", addr, err)
	}
	_ = again.Close()
}

// TestListenMetrics_boundsTheWholeRequest: both routes answer in one short
// response, so the server bounds the request body and the response, where
// New leaves both open for streams.
func TestListenMetrics_boundsTheWholeRequest(t *testing.T) {
	m, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if m.srv.ReadTimeout != 10*time.Second || m.srv.WriteTimeout != 30*time.Second {
		t.Errorf("timeouts read %v write %v, want the documented 10s and 30s", m.srv.ReadTimeout, m.srv.WriteTimeout)
	}
	if m.srv.ReadHeaderTimeout != defaultReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want New's %v", m.srv.ReadHeaderTimeout, defaultReadHeaderTimeout)
	}
}

// TestListenMetrics_passesServerOptionsToNew: opts reach New, so an app that
// routes net/http's own error lines into slog (plex-language-sync at Warn)
// keeps that bridge on the shared listener.
func TestListenMetrics_passesServerOptionsToNew(t *testing.T) {
	plain, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	bridged, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text(""), Healthz: text("")}, WithSlogErrorLog(slog.LevelWarn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridged.Close() })
	if plain.srv.ErrorLog != nil || bridged.srv.ErrorLog == nil {
		t.Errorf("ErrorLog plain %v, bridged %v; want nil and a slog bridge", plain.srv.ErrorLog, bridged.srv.ErrorLog)
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
	m, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text("page"), Healthz: panics})
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
	access := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(logged), "\n") {
		for _, path := range []string{"/metrics", "/healthz"} {
			if strings.Contains(line, "msg=http ") && strings.Contains(line, "path="+path+" ") {
				access[path] = line
			}
		}
	}
	for path, status := range map[string]string{"/metrics": "status=200", "/healthz": "status=500"} {
		line, ok := access[path]
		if !ok {
			t.Errorf("no access line for %s; log:\n%s", path, logged)
			continue
		}
		if !strings.Contains(line, "level=DEBUG") || !strings.Contains(line, status) {
			t.Errorf("access line for %s = %s, want level=DEBUG and %s", path, line, status)
		}
	}
	if !strings.Contains(logged, "recovered from panic") {
		t.Errorf("the panic was not recovered and logged; log:\n%s", logged)
	}
}

// TestListenMetrics_nilHandlerPanicsWithTheListenerOff: a nil handler is the
// caller's bug, so it panics even when no listener would open.
func TestListenMetrics_nilHandlerPanicsWithTheListenerOff(t *testing.T) {
	for name, handlers := range map[string][2]http.Handler{
		"Metrics": {nil, text("")},
		"Healthz": {text(""), nil},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "MetricsHandlers."+name) {
					t.Errorf("ListenMetrics with a nil %s: recovered %v, want a panic naming MetricsHandlers.%s", name, r, name)
				}
			}()
			_, _ = ListenMetrics(t.Context(), "", MetricsHandlers{Metrics: handlers[0], Healthz: handlers[1]})
		})
	}
}

// TestMetricsServer_closeReleasesAnUnservedListener: a startup that gives up
// after ListenMetrics closes the listener, and the address is free again.
func TestMetricsServer_closeReleasesAnUnservedListener(t *testing.T) {
	m, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatal(err)
	}
	addr := m.Addr().String()
	if err := m.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	again, err := ListenMetrics(t.Context(), addr, MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatalf("rebinding %s after Close: %v", addr, err)
	}
	_ = again.Close()
	off, _ := ListenMetrics(t.Context(), "", MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err := off.Close(); err != nil {
		t.Errorf("Close with the listener off = %v, want nil", err)
	}
}

// TestMetricsServer_secondServeIsRefused: a second Serve returns an error at
// once instead of blocking on the listener the first one serves.
func TestMetricsServer_secondServeIsRefused(t *testing.T) {
	m, err := ListenMetrics(t.Context(), "127.0.0.1:0", MetricsHandlers{Metrics: text(""), Healthz: text("")})
	if err != nil {
		t.Fatal(err)
	}
	base, stop := serving(t, m)
	// A request answered proves the first Serve owns the listener, so the
	// call below is the second.
	if code, _ := get(t, http.MethodGet, base+"/healthz"); code != http.StatusOK {
		t.Fatalf("GET /healthz = %d", code)
	}
	if err := m.Serve(t.Context()); !errors.Is(err, errServedTwice) {
		t.Errorf("second Serve = %v, want errServedTwice", err)
	}
	if err := stop(); err != nil {
		t.Errorf("the first Serve after cancel = %v, want nil", err)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
