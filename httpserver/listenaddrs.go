package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/cplieger/go/textsafe"
)

// ListenSetting is one TCP listener's setting and value, such as
// {"LISTEN_ADDR", ":9737"}.
type ListenSetting struct{ Name, Addr string }

// listenRetry is how Listen retries EADDRINUSE: a restarted container's
// previous process can still hold the port for a moment.
var listenRetry = struct{ interval, window time.Duration }{250 * time.Millisecond, 5 * time.Second}

// Listen binds s.Addr over TCP. A port in use (EADDRINUSE) is retried every
// 250 ms for up to 5 s; another error, or ctx ending, fails at once. The error
// names s.Name and the address, control characters cleaned, and wraps the
// cause.
func Listen(ctx context.Context, s ListenSetting) (net.Listener, error) {
	deadline := time.Now().Add(listenRetry.window)
	var lc net.ListenConfig
	for {
		ln, err := lc.Listen(ctx, "tcp", s.Addr)
		if err == nil {
			return ln, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) || ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, listenError(s, err)
		}
		t := time.NewTimer(listenRetry.interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, listenError(s, context.Cause(ctx))
		case <-t.C:
		}
	}
}

func listenError(s ListenSetting, err error) error {
	return fmt.Errorf("httpserver: %s %q: %w", clean(s.Name), clean(s.Addr), cleanError{err})
}

// cleanError carries err with its text cleaned: a resolver's error quotes the
// raw address, which may hold a control character.
type cleanError struct{ err error }

func (e cleanError) Error() string { return textsafe.SanitizeSingleLineBounded(e.err.Error(), 256) }
func (e cleanError) Unwrap() error { return e.err }

// CheckListenAddrs refuses, naming both settings and the port, every pair of
// settings that would bind one socket: the same port as net.Listen resolves it
// (09737 is 9737, http is 80) with the same host, or with either host
// unspecified (empty, 0.0.0.0 or [::], each overlapping every address on its
// port, since on Linux Go's "tcp" [::] listener takes IPv4 too) or a name,
// which may resolve to the other's address. A blank value, a port of 0 and a
// value with no resolvable port pass, the last left to net.Listen. Pass every
// listener the app opens, once.
func CheckListenAddrs(settings ...ListenSetting) error {
	var spots []listenSpot
	for _, s := range settings {
		if l, ok := parseListen(s); ok {
			spots = append(spots, l)
		}
	}
	var errs []error
	for i, a := range spots {
		for _, b := range spots[i+1:] {
			if a.port == b.port && a.overlaps(b) {
				errs = append(errs, a.clash(b))
			}
		}
	}
	return errors.Join(errs...)
}

type listenSpot struct {
	ip             netip.Addr
	setting, value string
	port           int
	unspecified    bool
	name           bool
}

func parseListen(s ListenSetting) (listenSpot, bool) {
	host, port, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return listenSpot{}, false
	}
	n, err := net.DefaultResolver.LookupPort(context.Background(), "tcp", port)
	if err != nil || n == 0 {
		return listenSpot{}, false
	}
	l := listenSpot{setting: s.Name, value: s.Addr, port: n}
	if host == "" {
		l.unspecified = true
		return l, true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		l.name = true
		return l, true
	}
	l.ip = ip.Unmap()
	l.unspecified = l.ip.IsUnspecified()
	return l, true
}

func (a listenSpot) overlaps(b listenSpot) bool {
	return a.unspecified || b.unspecified || a.name || b.name || a.ip == b.ip
}

func (a listenSpot) clash(b listenSpot) error {
	msg := fmt.Sprintf("httpserver: %s %q and %s %q would listen on one socket at port %d: give one of them another port",
		clean(a.setting), clean(a.value), clean(b.setting), clean(b.value), a.port)
	if a.name || b.name {
		msg += ", or set its host as an IP literal, since a host name may resolve to the other's address"
	}
	return errors.New(msg)
}

func clean(v string) string { return textsafe.SanitizeSingleLineBounded(v, maxErrorValue) }
