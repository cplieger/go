package httpserver_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

func opsText(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	})
}

func opsConfig(addr string) httpserver.OpsConfig {
	return httpserver.OpsConfig{
		Setting: "OPS_LISTEN_ADDR",
		Addr:    addr,
		Metrics: opsText("metrics page"),
		Healthz: opsText("healthy"),
		Logger:  discardLogger(),
	}
}

// runOps starts RunWithOps and answers a stop that cancels it and returns its
// error, failing the test when it does not return within 10 s.
func runOps(t *testing.T, ops *httpserver.Ops, served ...httpserver.Served) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)
	go func() { done <- httpserver.RunWithOps(ctx, ops, served...) }()
	var (
		stopped atomic.Bool
		err     error
	)
	stop = func() error {
		if stopped.CompareAndSwap(false, true) {
			cancel()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Error("RunWithOps did not return within 10s of its context ending")
			}
		}
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

// opsDo sends one request to addr with Host host ("" keeps the address) and
// answers the response as a recorder, body read.
func opsDo(t *testing.T, method, addr, path, host string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	rec := httptest.NewRecorder()
	for k, v := range resp.Header {
		rec.Header()[k] = v
	}
	rec.Code = resp.StatusCode
	_, _ = io.Copy(rec.Body, resp.Body)
	return rec
}

func listenOps(t *testing.T, c httpserver.OpsConfig) *httpserver.Ops {
	t.Helper()
	o, err := httpserver.ListenOps(t.Context(), c)
	if err != nil {
		t.Fatalf("ListenOps: %v", err)
	}
	if o.Addr() == nil {
		t.Fatal("ListenOps bound nothing")
	}
	return o
}

func product(t *testing.T, h http.Handler) (httpserver.Served, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return httpserver.Served{Server: httpserver.New(h), Listener: ln}, ln.Addr().String()
}

func TestOpsAddr(t *testing.T) {
	for name, tc := range map[string]struct{ value, def, want string }{
		"blank takes the default":          {"", ":9738", ":9738"},
		"spaces only take the default":     {"  \t", ":9738", ":9738"},
		"edge spaces cut":                  {"  127.0.0.1:9000\t", ":9738", "127.0.0.1:9000"},
		"zero-width runes cut":             {"\u200b:9000\ufeff", ":9738", ":9000"},
		"a pasted newline cut":             {":9000\r\n", ":9738", ":9000"},
		"a bidi control cut":               {"\u202e:9000", ":9738", ":9000"},
		"invisible only takes the default": {"\u200b \u2060", ":9738", ":9738"},
		"blank with no default is off":     {" ", "", ""},
		"a value with no default is kept":  {":9000", "", ":9000"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := httpserver.OpsAddr(tc.value, tc.def); got != tc.want {
				t.Errorf("OpsAddr(%q, %q) = %q, want %q", tc.value, tc.def, got, tc.want)
			}
		})
	}
}

func TestOpsAddrBlankWithNoDefaultOpensNothing(t *testing.T) {
	o, err := httpserver.ListenOps(t.Context(), opsConfig(httpserver.OpsAddr(" ", "")))
	if err != nil {
		t.Fatalf("ListenOps: %v", err)
	}
	if a := o.Addr(); a != nil {
		t.Fatalf("Addr = %v, want nil with the listener off", a)
	}
	done := make(chan error, 1)
	go func() { done <- httpserver.RunWithOps(t.Context(), o) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunWithOps = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunWithOps with nothing to serve did not return at once")
	}
}

func TestRunWithOpsOffServesProductAlone(t *testing.T) {
	o, err := httpserver.ListenOps(t.Context(), opsConfig(""))
	if err != nil {
		t.Fatal(err)
	}
	s, addr := product(t, opsText("product"))
	stop := runOps(t, o, s)
	if rec := opsDo(t, http.MethodGet, addr, "/", ""); rec.Code != http.StatusOK || rec.Body.String() != "product" {
		t.Errorf("product answered %d %q, want 200 product", rec.Code, rec.Body.String())
	}
	if err := stop(); err != nil {
		t.Errorf("RunWithOps = %v, want nil", err)
	}
}

func TestListenOpsRefusesNilHandler(t *testing.T) {
	for name, mutate := range map[string]func(*httpserver.OpsConfig){
		"nil Metrics":               func(c *httpserver.OpsConfig) { c.Metrics = nil },
		"nil Healthz":               func(c *httpserver.OpsConfig) { c.Healthz = nil },
		"nil Metrics, listener off": func(c *httpserver.OpsConfig) { c.Metrics, c.Addr = nil, "" },
		"nil Healthz, listener off": func(c *httpserver.OpsConfig) { c.Healthz, c.Addr = nil, "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := opsConfig("127.0.0.1:0")
			mutate(&c)
			o, err := httpserver.ListenOps(t.Context(), c)
			if err == nil {
				t.Fatalf("ListenOps = %v, nil; want an error", o)
			}
			if o != nil {
				t.Errorf("ListenOps answered %v beside its error, want nil", o)
			}
		})
	}
}

func TestListenOpsRefusesInvalidHost(t *testing.T) {
	c := opsConfig("127.0.0.1:0")
	c.Hosts = []string{"ops.example", "http://scraper.example/metrics"}
	_, err := httpserver.ListenOps(t.Context(), c)
	if err == nil {
		t.Fatal("ListenOps accepted a host entry no Host can match")
	}
	if !strings.Contains(err.Error(), "http://scraper.example/metrics") {
		t.Errorf("error %q does not name the bad entry", err)
	}
}

func TestListenOpsBindsBeforeReturn(t *testing.T) {
	o := listenOps(t, opsConfig("127.0.0.1:0"))
	tcp, ok := o.Addr().(*net.TCPAddr)
	if !ok || tcp.Port == 0 {
		t.Fatalf("Addr = %v, want the kernel's port", o.Addr())
	}
	conn, err := net.Dial("tcp", o.Addr().String())
	if err != nil {
		t.Fatalf("dial before RunWithOps: %v", err)
	}
	_ = conn.Close()
	runOps(t, o)
}

func TestListenOpsTimeouts(t *testing.T) {
	o := listenOps(t, opsConfig("127.0.0.1:0"))
	srv := httpserver.OpsServer(o)
	if srv.ReadTimeout != 10*time.Second || srv.WriteTimeout != 30*time.Second {
		t.Errorf("ReadTimeout, WriteTimeout = %v, %v; want 10s, 30s", srv.ReadTimeout, srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want New's 10s", srv.ReadHeaderTimeout)
	}
	runOps(t, o)
}

func TestListenOpsRoutesAndHostCheck(t *testing.T) {
	c := opsConfig("127.0.0.1:0")
	c.Hosts = []string{"ops.example"}
	o := listenOps(t, c)
	stop := runOps(t, o)
	addr := o.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	for name, tc := range map[string]struct {
		method, path, host string
		status             int
		body               string
		code               httpserver.ErrorCode
		allow              string
	}{
		"healthz":                   {method: http.MethodGet, path: "/healthz", status: 200, body: "healthy"},
		"metrics":                   {method: http.MethodGet, path: "/metrics", status: 200, body: "metrics page"},
		"healthz by HEAD":           {method: http.MethodHead, path: "/healthz", status: 200},
		"metrics by HEAD":           {method: http.MethodHead, path: "/metrics", status: 200},
		"a configured host":         {method: http.MethodGet, path: "/metrics", host: "ops.example:" + port, status: 200, body: "metrics page"},
		"localhost from loopback":   {method: http.MethodGet, path: "/healthz", host: "localhost:" + port, status: 200, body: "healthy"},
		"::1 from loopback":         {method: http.MethodGet, path: "/healthz", host: "[::1]:" + port, status: 200, body: "healthy"},
		"a foreign host":            {method: http.MethodGet, path: "/healthz", host: "rebind.example", status: 403, code: "host_not_allowed"},
		"a foreign host on metrics": {method: http.MethodGet, path: "/metrics", host: "rebind.example:" + port, status: 403, code: "host_not_allowed"},
		"root":                      {method: http.MethodGet, path: "/", status: 404, code: "not_found"},
		"another path":              {method: http.MethodGet, path: "/api/things", status: 404, code: "not_found"},
		"a trailing slash":          {method: http.MethodGet, path: "/healthz/", status: 404, code: "not_found"},
		"POST on metrics":           {method: http.MethodPost, path: "/metrics", status: 405, code: "method_not_allowed", allow: "GET, HEAD"},
		"DELETE on healthz":         {method: http.MethodDelete, path: "/healthz", status: 405, code: "method_not_allowed", allow: "GET, HEAD"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := opsDo(t, tc.method, addr, tc.path, tc.host)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.code == "" {
				if rec.Body.String() != tc.body {
					t.Errorf("body = %q, want %q", rec.Body.String(), tc.body)
				}
				return
			}
			if env := strictEnvelope(t, rec); env.Code != tc.code {
				t.Errorf("code = %q, want %q", env.Code, tc.code)
			}
			if got := rec.Header().Get("Allow"); got != tc.allow {
				t.Errorf("Allow = %q, want %q", got, tc.allow)
			}
		})
	}
	if err := stop(); err != nil {
		t.Errorf("RunWithOps = %v, want nil", err)
	}
}

func TestListenOpsUsesErrors(t *testing.T) {
	c := opsConfig("127.0.0.1:0")
	var codes []httpserver.ErrorCode
	c.Errors = func(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
		codes = append(codes, code)
		httpserver.WriteError(w, r, status, code, msg)
	}
	o := listenOps(t, c)
	stop := runOps(t, o)
	addr := o.Addr().String()
	opsDo(t, http.MethodGet, addr, "/nope", "")
	opsDo(t, http.MethodPost, addr, "/healthz", "")
	opsDo(t, http.MethodGet, addr, "/healthz", "rebind.example")
	_ = stop()
	want := []httpserver.ErrorCode{"not_found", "method_not_allowed", "host_not_allowed"}
	if strings.Join(asStrings(codes), ",") != strings.Join(asStrings(want), ",") {
		t.Errorf("Errors saw %v, want %v", codes, want)
	}
}

func asStrings(codes []httpserver.ErrorCode) []string {
	out := make([]string, len(codes))
	for i, c := range codes {
		out[i] = string(c)
	}
	return out
}

func TestListenOpsRecoversPanic(t *testing.T) {
	c := opsConfig("127.0.0.1:0")
	c.Healthz = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("probe exploded") })
	o := listenOps(t, c)
	runOps(t, o)
	rec := opsDo(t, http.MethodGet, o.Addr().String(), "/healthz", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if env := strictEnvelope(t, rec); env.Code != "internal_error" {
		t.Errorf("code = %q, want internal_error", env.Code)
	}
}

func TestListenOpsProbeLogLevel(t *testing.T) {
	h := &captureHandler{}
	c := opsConfig("127.0.0.1:0")
	c.Logger = slog.New(h)
	o := listenOps(t, c)
	stop := runOps(t, o)
	addr := o.Addr().String()
	for _, p := range []string{"/healthz", "/metrics", "/nope"} {
		opsDo(t, http.MethodGet, addr, p, "")
	}
	_ = stop()
	levels := map[string]slog.Level{}
	for _, r := range h.snapshot() {
		if r.Message != "http" {
			continue
		}
		if p, ok := attrsOf(r)["path"].(string); ok {
			levels[p] = r.Level
		}
	}
	for path, want := range map[string]slog.Level{"/healthz": slog.LevelDebug, "/metrics": slog.LevelDebug, "/nope": slog.LevelInfo} {
		got, ok := levels[path]
		if !ok {
			t.Errorf("no access line for %s on the ops logger", path)
			continue
		}
		if got != want {
			t.Errorf("%s logged at %v, want %v", path, got, want)
		}
	}
}

// holdPort binds a loopback port and answers its address and a release.
func holdPort(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestListenOpsRetriesAddrInUse(t *testing.T) {
	addr, release := holdPort(t)
	start := time.Now()
	time.AfterFunc(600*time.Millisecond, release)
	o, err := httpserver.ListenOps(t.Context(), opsConfig(addr))
	if err != nil {
		t.Fatalf("ListenOps = %v, want a bind once the port is free", err)
	}
	if took := time.Since(start); took < 600*time.Millisecond {
		t.Errorf("bound after %v, before the port was free", took)
	}
	if o.Addr().String() != addr {
		t.Errorf("Addr = %v, want %s", o.Addr(), addr)
	}
	runOps(t, o)
}

func TestListenOpsErrorNamesSettingAndCleanedAddr(t *testing.T) {
	t.Run("address in use past the window", func(t *testing.T) {
		httpserver.SetOpsBindRetry(t, 10*time.Millisecond, 200*time.Millisecond)
		addr, _ := holdPort(t)
		start := time.Now()
		_, err := httpserver.ListenOps(t.Context(), opsConfig(addr))
		if err == nil {
			t.Fatal("ListenOps bound a port another socket holds")
		}
		if took := time.Since(start); took < 200*time.Millisecond {
			t.Errorf("gave up after %v, inside the retry window", took)
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			t.Errorf("error %v does not wrap EADDRINUSE", err)
		}
		for _, want := range []string{"OPS_LISTEN_ADDR", addr} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})
	t.Run("another bind error fails at once", func(t *testing.T) {
		start := time.Now()
		_, err := httpserver.ListenOps(t.Context(), opsConfig("127.0.0.1:99999"))
		if err == nil {
			t.Fatal("ListenOps accepted port 99999")
		}
		if took := time.Since(start); took >= 250*time.Millisecond {
			t.Errorf("failed after %v, want no retry", took)
		}
		if !strings.Contains(err.Error(), "OPS_LISTEN_ADDR") {
			t.Errorf("error %q does not name the setting", err)
		}
	})
	t.Run("a control character never reaches the error", func(t *testing.T) {
		_, err := httpserver.ListenOps(t.Context(), opsConfig("127.0.0.1\x1b[31m\n:0"))
		if err == nil {
			t.Fatal("ListenOps accepted a host with a control character")
		}
		if strings.ContainsAny(err.Error(), "\x1b\n") {
			t.Errorf("error %q carries a raw control character", err)
		}
		if !strings.Contains(err.Error(), "OPS_LISTEN_ADDR") {
			t.Errorf("error %q does not name the setting", err)
		}
	})
}

func TestListenOpsCancelledDuringRetry(t *testing.T) {
	addr, _ := holdPort(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	_, err := httpserver.ListenOps(ctx, opsConfig(addr))
	if err == nil {
		t.Fatal("ListenOps bound a port another socket holds")
	}
	if !httpserver.CausedByCancellation(ctx, err) {
		t.Errorf("error %v does not read as the context's cancellation", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("returned %v after the cancel, want at the next retry", took)
	}
}

func TestListenOpsReleasedWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	o, err := httpserver.ListenOps(ctx, opsConfig("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	s, productAddr := product(t, opsText("product"))
	cancel()
	err = httpserver.RunWithOps(t.Context(), o, s)
	if err == nil || !httpserver.CausedByCancellation(ctx, err) {
		t.Fatalf("RunWithOps = %v, want the ListenOps context's cancellation", err)
	}
	for _, a := range []string{o.Addr().String(), productAddr} {
		if conn, err := net.DialTimeout("tcp", a, time.Second); err == nil {
			_ = conn.Close()
			t.Errorf("%s still accepts connections", a)
		}
	}
}

func TestRunWithOpsServesHealthzThroughDrain(t *testing.T) {
	o := listenOps(t, opsConfig("127.0.0.1:0"))
	opsAddr := o.Addr().String()
	var during, after atomic.Int32
	s, _ := product(t, opsText("product"))
	probe := func(into *atomic.Int32) func(context.Context) {
		return func(context.Context) {
			if rec := opsDo(t, http.MethodGet, opsAddr, "/healthz", ""); rec.Body.String() == "healthy" {
				into.Store(int32(rec.Code))
			}
		}
	}
	s.Options = []httpserver.RunOption{httpserver.WithPreDrain(probe(&during)), httpserver.WithTeardown(probe(&after))}
	stop := runOps(t, o, s)
	if err := stop(); err != nil {
		t.Fatalf("RunWithOps = %v, want nil", err)
	}
	if during.Load() != http.StatusOK {
		t.Error("/healthz did not answer 200 during the product listener's pre-drain")
	}
	if after.Load() != http.StatusOK {
		t.Error("/healthz did not answer 200 after the product listener's drain")
	}
	if conn, err := net.DialTimeout("tcp", opsAddr, time.Second); err == nil {
		_ = conn.Close()
		t.Error("the ops listener still accepts connections after RunWithOps returned")
	}
}

func TestRunWithOpsPassesRunOptions(t *testing.T) {
	o := listenOps(t, opsConfig("127.0.0.1:0"))
	s, _ := product(t, opsText("product"))
	var exited atomic.Bool
	s.Options = []httpserver.RunOption{httpserver.WithServeExit(func(context.Context) { exited.Store(true) })}
	stop := runOps(t, o, s)
	_ = s.Listener.Close()
	_ = stop()
	if !exited.Load() {
		t.Error("the product's WithServeExit did not run when its listener failed")
	}
}

func TestRunWithOpsProductFailureReturns(t *testing.T) {
	o := listenOps(t, opsConfig("127.0.0.1:0"))
	s, _ := product(t, opsText("product"))
	done := make(chan error, 1)
	go func() { done <- httpserver.RunWithOps(context.WithoutCancel(t.Context()), o, s) }()
	if rec := opsDo(t, http.MethodGet, o.Addr().String(), "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d before the failure", rec.Code)
	}
	_ = s.Listener.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("RunWithOps = %v, want the product's serve error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunWithOps did not return after the product listener failed")
	}
	if conn, err := net.DialTimeout("tcp", o.Addr().String(), time.Second); err == nil {
		_ = conn.Close()
		t.Error("the ops listener outlived RunWithOps")
	}
}

func TestRunWithOpsOpsFailureStopsProduct(t *testing.T) {
	o := listenOps(t, opsConfig("127.0.0.1:0"))
	s, productAddr := product(t, opsText("product"))
	var drained atomic.Bool
	s.Options = []httpserver.RunOption{httpserver.WithPreDrain(func(context.Context) { drained.Store(true) })}
	done := make(chan error, 1)
	go func() { done <- httpserver.RunWithOps(context.WithoutCancel(t.Context()), o, s) }()
	if rec := opsDo(t, http.MethodGet, productAddr, "/", ""); rec.Code != http.StatusOK {
		t.Fatalf("product = %d before the failure", rec.Code)
	}
	_ = httpserver.OpsListener(o).Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) || !strings.Contains(err.Error(), "OPS_LISTEN_ADDR") {
			t.Errorf("RunWithOps = %v, want the ops serve error naming OPS_LISTEN_ADDR", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunWithOps did not return after the ops listener failed")
	}
	if !drained.Load() {
		t.Error("the product listener was not drained after the ops failure")
	}
}

func TestRunWithOpsOpsGraceIsShortAndNotAnError(t *testing.T) {
	h := &captureHandler{}
	c := opsConfig("127.0.0.1:0")
	c.Logger = slog.New(h)
	started, unblock := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	c.Healthz = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-unblock
	})
	o := listenOps(t, c)
	stop := runOps(t, o)
	go func() {
		req, _ := http.NewRequestWithContext(context.WithoutCancel(t.Context()), http.MethodGet, "http://"+o.Addr().String()+"/healthz", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	start := time.Now()
	if err := stop(); err != nil {
		t.Errorf("RunWithOps = %v, want nil when the ops grace cuts a probe", err)
	}
	if took := time.Since(start); took < 900*time.Millisecond || took > 3*time.Second {
		t.Errorf("stopped after %v, want the ops listener's 1s grace", took)
	}
	var warned bool
	for _, r := range h.snapshot() {
		if r.Level == slog.LevelWarn && attrsOf(r)["setting"] == "OPS_LISTEN_ADDR" {
			warned = true
		}
	}
	if !warned {
		t.Error("no WARN naming OPS_LISTEN_ADDR for the request cut at the grace's end")
	}
}

func TestRunWithOpsRefusesSecondRun(t *testing.T) {
	o := listenOps(t, opsConfig("127.0.0.1:0"))
	runOps(t, o)
	opsDo(t, http.MethodGet, o.Addr().String(), "/healthz", "")
	if err := httpserver.RunWithOps(t.Context(), o); err == nil {
		t.Error("a second RunWithOps over one Ops answered nil")
	}
}

func TestCheckListenAddrs(t *testing.T) {
	type s = httpserver.ListenSetting
	pass := map[string][]s{
		"two ports":                 {{"LISTEN_ADDR", ":9737"}, {"OPS_LISTEN_ADDR", ":9738"}},
		"two port-0 addresses":      {{"LISTEN_ADDR", ":0"}, {"OPS_LISTEN_ADDR", "127.0.0.1:0"}},
		"a blank setting is off":    {{"LISTEN_ADDR", ":9737"}, {"OPS_LISTEN_ADDR", ""}},
		"two specified addresses":   {{"LISTEN_ADDR", "127.0.0.1:9737"}, {"OPS_LISTEN_ADDR", "127.0.0.2:9737"}},
		"two families' loopbacks":   {{"LISTEN_ADDR", "[::1]:9737"}, {"OPS_LISTEN_ADDR", "127.0.0.1:9737"}},
		"no port is left to listen": {{"LISTEN_ADDR", "127.0.0.1"}, {"OPS_LISTEN_ADDR", ":9737"}},
		"an unknown service name":   {{"LISTEN_ADDR", ":no-such-service"}, {"OPS_LISTEN_ADDR", ":9737"}},
		"one setting":               {{"LISTEN_ADDR", ":9737"}},
	}
	for name, settings := range pass {
		t.Run("pass "+name, func(t *testing.T) {
			if err := httpserver.CheckListenAddrs(settings...); err != nil {
				t.Errorf("CheckListenAddrs(%v) = %v, want nil", settings, err)
			}
		})
	}
	refuse := map[string]struct {
		settings []s
		port     string
		extra    string
	}{
		"empty host beside a loopback":       {[]s{{"LISTEN_ADDR", ":9737"}, {"OPS_LISTEN_ADDR", "127.0.0.1:9737"}}, "9737", ""},
		"a leading zero":                     {[]s{{"LISTEN_ADDR", ":09737"}, {"OPS_LISTEN_ADDR", ":9737"}}, "9737", ""},
		"a service name":                     {[]s{{"LISTEN_ADDR", ":http"}, {"OPS_LISTEN_ADDR", "127.0.0.1:80"}}, "80", ""},
		"the two unspecified addresses":      {[]s{{"LISTEN_ADDR", "[::]:9737"}, {"OPS_LISTEN_ADDR", "0.0.0.0:9737"}}, "9737", ""},
		"0.0.0.0 beside a loopback":          {[]s{{"LISTEN_ADDR", "0.0.0.0:9737"}, {"OPS_LISTEN_ADDR", "127.0.0.1:9737"}}, "9737", ""},
		"[::] beside an IPv4 address":        {[]s{{"LISTEN_ADDR", "[::]:9737"}, {"OPS_LISTEN_ADDR", "10.0.0.5:9737"}}, "9737", ""},
		"a mapped unspecified address":       {[]s{{"LISTEN_ADDR", "[::ffff:0.0.0.0]:9737"}, {"OPS_LISTEN_ADDR", "127.0.0.1:9737"}}, "9737", ""},
		"one address in two spellings":       {[]s{{"LISTEN_ADDR", "[0:0::1]:9737"}, {"OPS_LISTEN_ADDR", "[::1]:9737"}}, "9737", ""},
		"a mapped address and its IPv4 form": {[]s{{"LISTEN_ADDR", "[::ffff:127.0.0.1]:9737"}, {"OPS_LISTEN_ADDR", "127.0.0.1:9737"}}, "9737", ""},
		"a name beside an address":           {[]s{{"LISTEN_ADDR", "myhost:9737"}, {"OPS_LISTEN_ADDR", "127.0.0.1:9737"}}, "9737", "IP literal"},
	}
	for name, tc := range refuse {
		t.Run("refuse "+name, func(t *testing.T) {
			err := httpserver.CheckListenAddrs(tc.settings...)
			if err == nil {
				t.Fatalf("CheckListenAddrs(%v) = nil, want a refusal", tc.settings)
			}
			for _, want := range []string{tc.settings[0].Name, tc.settings[1].Name, tc.port, tc.extra} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
	t.Run("one check over three settings names the clashing pair", func(t *testing.T) {
		err := httpserver.CheckListenAddrs(
			s{"LISTEN_ADDR", ":9737"}, s{"OPS_LISTEN_ADDR", ":9738"}, s{"HOSTS_LISTEN_ADDR", "127.0.0.1:9737"})
		if err == nil {
			t.Fatal("CheckListenAddrs = nil, want HOSTS_LISTEN_ADDR refused beside LISTEN_ADDR")
		}
		msg := err.Error()
		if !strings.Contains(msg, "LISTEN_ADDR") || !strings.Contains(msg, "HOSTS_LISTEN_ADDR") || strings.Contains(msg, "OPS_LISTEN_ADDR") {
			t.Errorf("error %q, want LISTEN_ADDR and HOSTS_LISTEN_ADDR named and OPS_LISTEN_ADDR not", msg)
		}
	})
	t.Run("a control character never reaches the error", func(t *testing.T) {
		err := httpserver.CheckListenAddrs(s{"LISTEN_ADDR", "my\x1bhost\n:9737"}, s{"OPS_LISTEN_ADDR", ":9737"})
		if err == nil {
			t.Fatal("CheckListenAddrs = nil, want a refusal")
		}
		if strings.ContainsAny(err.Error(), "\x1b\n") {
			t.Errorf("error %q carries a raw control character", err)
		}
	})
}

func TestRemovedMetricsListener(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{"ListenMetrics": true, "MetricsHandlers": true, "MetricsServer": true}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			for _, name := range declNames(d) {
				if removed[name] {
					t.Errorf("%s still declares %s", e.Name(), name)
				}
			}
		}
	}
}

// declNames answers the top-level names d declares.
func declNames(d ast.Decl) []string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil {
			return []string{d.Name.Name}
		}
	case *ast.GenDecl:
		var names []string
		for _, spec := range d.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, spec.Name.Name)
			case *ast.ValueSpec:
				for _, n := range spec.Names {
					names = append(names, n.Name)
				}
			}
		}
		return names
	}
	return nil
}
