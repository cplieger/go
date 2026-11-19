package httpserver_test

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

// hostList builds an active policy over hosts, failing the test on an entry
// ParseHostList refuses.
func hostList(t *testing.T, hosts []string, opts ...httpserver.HostAllowlistOption) *httpserver.HostPolicy {
	t.Helper()
	p, invalid := httpserver.ParseHostList(hosts, opts...)
	if len(invalid) > 0 || !p.Active() {
		t.Fatalf("ParseHostList(%q) = invalid %q, active %v", hosts, invalid, p.Active())
	}
	return p
}

// liveOps serves an ops listener whose HostPolicy answers what live holds.
func liveOps(t *testing.T, c httpserver.OpsConfig, live *atomic.Pointer[httpserver.HostPolicy]) string {
	t.Helper()
	c.HostPolicy = live.Load
	o := listenOps(t, c)
	runOps(t, o)
	return o.Addr().String()
}

// recordedRefusal is one call an Errors responder saw.
type recordedRefusal struct {
	code   httpserver.ErrorCode
	host   string
	status int
}

// refusalRecorder is an ErrorResponder that records each call and writes the
// envelope through WriteError.
type refusalRecorder struct {
	seen []recordedRefusal
	mu   sync.Mutex
}

func (rr *refusalRecorder) respond(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
	rr.mu.Lock()
	rr.seen = append(rr.seen, recordedRefusal{code: code, host: r.Host, status: status})
	rr.mu.Unlock()
	httpserver.WriteError(w, r, status, code, msg)
}

func (rr *refusalRecorder) calls() []recordedRefusal {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]recordedRefusal(nil), rr.seen...)
}

func wantOpsAnswer(t *testing.T, addr, host string, status int) {
	t.Helper()
	rec := opsDo(t, http.MethodGet, addr, "/metrics", host)
	if rec.Code != status {
		t.Errorf("Host %q: status %d, want %d (body %q)", host, rec.Code, status, rec.Body.String())
		return
	}
	if status == http.StatusOK {
		if rec.Body.String() != "metrics page" {
			t.Errorf("Host %q: body %q, want metrics page", host, rec.Body.String())
		}
		return
	}
	if env := strictEnvelope(t, rec); env.Code != "host_not_allowed" {
		t.Errorf("Host %q: code %q, want host_not_allowed", host, env.Code)
	}
}

func TestListenOpsHostPolicyFollowsASwap(t *testing.T) {
	var live atomic.Pointer[httpserver.HostPolicy]
	live.Store(hostList(t, []string{"a.example"}))
	addr := liveOps(t, opsConfig("127.0.0.1:0"), &live)

	wantOpsAnswer(t, addr, "a.example", http.StatusOK)
	wantOpsAnswer(t, addr, "b.example", http.StatusForbidden)

	live.Store(hostList(t, []string{"b.example"}))
	wantOpsAnswer(t, addr, "a.example", http.StatusForbidden)
	wantOpsAnswer(t, addr, "b.example", http.StatusOK)
}

func TestListenOpsHostPolicyIPLiteralHosts(t *testing.T) {
	for name, tc := range map[string]struct {
		opts   []httpserver.HostAllowlistOption
		status int
	}{
		"admitted with WithIPLiteralHosts(true)": {opts: []httpserver.HostAllowlistOption{httpserver.WithIPLiteralHosts(true)}, status: http.StatusOK},
		"refused without it":                     {status: http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			var live atomic.Pointer[httpserver.HostPolicy]
			live.Store(hostList(t, []string{"ops.example"}, tc.opts...))
			addr := liveOps(t, opsConfig("127.0.0.1:0"), &live)
			_, port, _ := net.SplitHostPort(addr)
			for _, host := range []string{"192.0.2.10", "198.51.100.7:" + port, "[2001:db8::1]:" + port} {
				wantOpsAnswer(t, addr, host, tc.status)
			}
			wantOpsAnswer(t, addr, "ops.example", http.StatusOK)
			wantOpsAnswer(t, addr, "rebind.example", http.StatusForbidden)
		})
	}
}

// The image's probe reaches the ops port on loopback, which passes whatever
// the policy answers, nil included.
func TestListenOpsHostPolicyLoopbackExempt(t *testing.T) {
	for name, policy := range map[string]func(*testing.T) *httpserver.HostPolicy{
		"a policy refusing every Host": func(t *testing.T) *httpserver.HostPolicy {
			return hostList(t, []string{"nothing.example"})
		},
		"a nil policy": func(*testing.T) *httpserver.HostPolicy { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			var live atomic.Pointer[httpserver.HostPolicy]
			live.Store(policy(t))
			addr := liveOps(t, opsConfig("127.0.0.1:0"), &live)
			_, port, _ := net.SplitHostPort(addr)
			for _, host := range []string{"", "localhost:" + port, "127.0.0.1"} {
				wantOpsAnswer(t, addr, host, http.StatusOK)
			}
			wantOpsAnswer(t, addr, "rebind.example", http.StatusForbidden)
		})
	}
}

func TestListenOpsHostPolicyNilRefuses(t *testing.T) {
	h := &captureHandler{}
	rr := &refusalRecorder{}
	c := opsConfig("127.0.0.1:0")
	c.Logger = slog.New(h)
	c.Errors = rr.respond
	var live atomic.Pointer[httpserver.HostPolicy]
	addr := liveOps(t, c, &live)

	wantOpsAnswer(t, addr, "ops.example", http.StatusForbidden)
	want := []recordedRefusal{{code: "host_not_allowed", host: "ops.example", status: http.StatusForbidden}}
	if got := rr.calls(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("Errors saw %+v, want %+v", got, want)
	}
	if n := hostRefusalLines(h, "ops.example"); n != 1 {
		t.Errorf("refusal lines on Logger for the nil policy = %d, want 1", n)
	}
}

// A refusal is written through Errors, never the policy's own responder, and
// logged through the policy's logger, as a product listener's check logs it.
func TestListenOpsHostPolicyRefusalPath(t *testing.T) {
	opsLog, policyLog := &captureHandler{}, &captureHandler{}
	rr := &refusalRecorder{}
	var policyResponder atomic.Int32
	c := opsConfig("127.0.0.1:0")
	c.Logger = slog.New(opsLog)
	c.Errors = rr.respond
	var live atomic.Pointer[httpserver.HostPolicy]
	live.Store(hostList(t, []string{"ops.example"},
		httpserver.WithHostAllowlistLogger(slog.New(policyLog)),
		httpserver.WithHostResponder(func(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
			policyResponder.Add(1)
			httpserver.WriteError(w, r, status, code, msg)
		})))
	addr := liveOps(t, c, &live)

	wantOpsAnswer(t, addr, "rebind.example", http.StatusForbidden)
	want := recordedRefusal{code: "host_not_allowed", host: "rebind.example", status: http.StatusForbidden}
	if got := rr.calls(); len(got) != 1 || got[0] != want {
		t.Errorf("Errors saw %+v, want [%+v]", got, want)
	}
	if n := policyResponder.Load(); n != 0 {
		t.Errorf("the policy's own responder wrote %d refusals, want 0", n)
	}
	if n := hostRefusalLines(policyLog, "rebind.example"); n != 1 {
		t.Errorf("refusal lines on the policy's logger = %d, want 1", n)
	}
	if n := hostRefusalLines(opsLog, "rebind.example"); n != 0 {
		t.Errorf("refusal lines on OpsConfig.Logger = %d, want 0 beside the policy's", n)
	}
}

func hostRefusalLines(h *captureHandler, host string) int {
	var n int
	for _, r := range h.snapshot() {
		if r.Message == "httpserver: host not allowed" && r.Level == slog.LevelWarn && attrsOf(r)["host"] == host {
			n++
		}
	}
	return n
}

func TestListenOpsRefusesHostPolicyBesideHosts(t *testing.T) {
	policy := func() *httpserver.HostPolicy { return nil }
	for name, addr := range map[string]string{"listener on": "127.0.0.1:0", "listener off": ""} {
		t.Run(name, func(t *testing.T) {
			c := opsConfig(addr)
			c.HostPolicy = policy
			c.Hosts = []string{"", "ops.example"}
			o, err := httpserver.ListenOps(t.Context(), c)
			if err == nil {
				t.Fatalf("ListenOps = %v, nil; want an error with both HostPolicy and Hosts set", o)
			}
			for _, field := range []string{"OpsConfig.HostPolicy", "OpsConfig.Hosts"} {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("error %q does not name %s", err, field)
				}
			}
		})
	}
	t.Run("blank Hosts beside HostPolicy", func(t *testing.T) {
		c := opsConfig("127.0.0.1:0")
		c.HostPolicy = policy
		c.Hosts = []string{" ", ""}
		o := listenOps(t, c)
		runOps(t, o)
	})
}
