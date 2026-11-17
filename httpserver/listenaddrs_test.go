package httpserver_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

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

func listenSetting(addr string) httpserver.ListenSetting {
	return httpserver.ListenSetting{Name: "LISTEN_ADDR", Addr: addr}
}

func TestListenAddr(t *testing.T) {
	for name, tc := range map[string]struct{ value, def, want string }{
		"blank takes the default":          {"", ":9738", ":9738"},
		"spaces only take the default":     {"  \t", ":9738", ":9738"},
		"edge spaces cut":                  {"  127.0.0.1:9000\t", ":9738", "127.0.0.1:9000"},
		"zero-width runes cut":             {"\u200b:9000\ufeff", ":9738", ":9000"},
		"a pasted newline cut":             {":9000\r\n", ":9738", ":9000"},
		"a bidi control cut":               {"\u202e:9000", ":9738", ":9000"},
		"a line separator cut":             {":9000\u2028", ":9738", ":9000"},
		"invisible only takes the default": {"\u200b \u2060", ":9738", ":9738"},
		"inner runes are kept":             {"[::1]: 9000", ":9738", "[::1]: 9000"},
		"blank with no default is off":     {" ", "", ""},
		"a value with no default is kept":  {":9000", "", ":9000"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := httpserver.ListenAddr(tc.value, tc.def); got != tc.want {
				t.Errorf("ListenAddr(%q, %q) = %q, want %q", tc.value, tc.def, got, tc.want)
			}
		})
	}
}

func TestListenBindsAddr(t *testing.T) {
	ln, err := httpserver.Listen(t.Context(), listenSetting("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("Listen = %v, want a bound listener", err)
	}
	defer ln.Close()
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", ln.Addr(), err)
	}
	_ = conn.Close()
}

func TestListenTrimsAddrEdges(t *testing.T) {
	ln, err := httpserver.Listen(t.Context(), listenSetting(" \u200b127.0.0.1:0\r\n"))
	if err != nil {
		t.Fatalf("Listen = %v, want the address bound with its edges cut", err)
	}
	defer ln.Close()
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); host != "127.0.0.1" {
		t.Errorf("bound %v, want 127.0.0.1", ln.Addr())
	}
}

func TestListenRetriesAddrInUse(t *testing.T) {
	addr, release := holdPort(t)
	start := time.Now()
	time.AfterFunc(600*time.Millisecond, release)
	ln, err := httpserver.Listen(t.Context(), listenSetting(addr))
	if err != nil {
		t.Fatalf("Listen = %v, want a bind once the port is free", err)
	}
	defer ln.Close()
	if took := time.Since(start); took < 600*time.Millisecond {
		t.Errorf("bound after %v, before the port was free", took)
	}
	if ln.Addr().String() != addr {
		t.Errorf("Addr = %v, want %s", ln.Addr(), addr)
	}
}

func TestListenErrorNamesSettingAndCleanedAddr(t *testing.T) {
	t.Run("address in use past the window", func(t *testing.T) {
		httpserver.SetListenRetry(t, 10*time.Millisecond, 200*time.Millisecond)
		addr, _ := holdPort(t)
		start := time.Now()
		ln, err := httpserver.Listen(t.Context(), listenSetting(addr))
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen bound a port another socket holds")
		}
		if took := time.Since(start); took < 200*time.Millisecond {
			t.Errorf("gave up after %v, inside the retry window", took)
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			t.Errorf("error %v does not wrap EADDRINUSE", err)
		}
		for _, want := range []string{"LISTEN_ADDR", addr} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})
	t.Run("another bind error fails at once", func(t *testing.T) {
		start := time.Now()
		ln, err := httpserver.Listen(t.Context(), listenSetting("127.0.0.1:99999"))
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen accepted port 99999")
		}
		if took := time.Since(start); took >= 250*time.Millisecond {
			t.Errorf("failed after %v, want no retry", took)
		}
		for _, want := range []string{"LISTEN_ADDR", "127.0.0.1:99999"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})
	t.Run("a control character never reaches the error", func(t *testing.T) {
		ln, err := httpserver.Listen(t.Context(), listenSetting("127.0.0.1\x1b[31m\n:0"))
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen accepted a host with a control character")
		}
		if strings.ContainsAny(err.Error(), "\x1b\n") {
			t.Errorf("error %q carries a raw control character", err)
		}
		if !strings.Contains(err.Error(), "LISTEN_ADDR") {
			t.Errorf("error %q does not name the setting", err)
		}
	})
	t.Run("a blank address is refused", func(t *testing.T) {
		for _, addr := range []string{"", " \t", "\u200b\ufeff "} {
			ln, err := httpserver.Listen(t.Context(), listenSetting(addr))
			if err == nil {
				t.Errorf("Listen(%q) bound %v, want an error", addr, ln.Addr())
				_ = ln.Close()
				continue
			}
			if !strings.Contains(err.Error(), "LISTEN_ADDR") || !strings.Contains(err.Error(), "no address") {
				t.Errorf("Listen(%q) error %q does not name the setting and the blank address", addr, err)
			}
		}
	})
	t.Run("a control character in the name never reaches the error", func(t *testing.T) {
		ln, err := httpserver.Listen(t.Context(), httpserver.ListenSetting{Name: "LISTEN\x1b[31m\n_ADDR", Addr: "127.0.0.1:99999"})
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen accepted port 99999")
		}
		if strings.ContainsAny(err.Error(), "\x1b\n") {
			t.Errorf("error %q carries a raw control character", err)
		}
	})
}

func TestListenCancelledDuringRetry(t *testing.T) {
	addr, _ := holdPort(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	ln, err := httpserver.Listen(ctx, listenSetting(addr))
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen bound a port another socket holds")
	}
	if !httpserver.CausedByCancellation(ctx, err) {
		t.Errorf("error %v does not read as the context's cancellation", err)
	}
	if !strings.Contains(err.Error(), "LISTEN_ADDR") {
		t.Errorf("error %q does not name the setting", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("returned %v after the cancel, want at the next retry", took)
	}
}

func TestCheckListenAddrs(t *testing.T) {
	type s = httpserver.ListenSetting
	pass := map[string][]s{
		"two ports":                        {{"LISTEN_ADDR", ":9737"}, {"OPS_LISTEN_ADDR", ":9738"}},
		"two port-0 addresses":             {{"LISTEN_ADDR", ":0"}, {"OPS_LISTEN_ADDR", "127.0.0.1:0"}},
		"a blank setting is off":           {{"LISTEN_ADDR", ":9737"}, {"OPS_LISTEN_ADDR", ""}},
		"two specified addresses":          {{"LISTEN_ADDR", "127.0.0.1:9737"}, {"OPS_LISTEN_ADDR", "127.0.0.2:9737"}},
		"two families' loopbacks":          {{"LISTEN_ADDR", "[::1]:9737"}, {"OPS_LISTEN_ADDR", "127.0.0.1:9737"}},
		"no port is left to listen":        {{"LISTEN_ADDR", "127.0.0.1"}, {"OPS_LISTEN_ADDR", ":9737"}},
		"an unknown service name":          {{"LISTEN_ADDR", ":no-such-service"}, {"OPS_LISTEN_ADDR", ":9737"}},
		"one setting":                      {{"LISTEN_ADDR", ":9737"}},
		"an invisible-only setting is off": {{"LISTEN_ADDR", ":9737"}, {"OPS_LISTEN_ADDR", "\u200b \ufeff"}},
		"edge runes cut to two ports":      {{"LISTEN_ADDR", " 127.0.0.1:9737\n"}, {"OPS_LISTEN_ADDR", "\u200b127.0.0.1:9738"}},
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
		"a pasted newline after the port":    {[]s{{"LISTEN_ADDR", ":9737"}, {"OPS_LISTEN_ADDR", "127.0.0.1:9737\r\n"}}, "9737", ""},
		"zero-width runes at both edges":     {[]s{{"LISTEN_ADDR", "\ufeff[::1]:9737\u200b"}, {"OPS_LISTEN_ADDR", "[::1]:9737"}}, "9737", ""},
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
			s{"LISTEN_ADDR", ":9737"}, s{"OPS_LISTEN_ADDR", ":9738"}, s{"HOSTS_LISTEN_ADDR", "127.0.0.1:9737"},
		)
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
