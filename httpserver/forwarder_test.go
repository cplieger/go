package httpserver_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver"
)

// v4route is one /proc/net/route row: addresses in little-endian hex, flags
// 0x1 (up) plus 0x2 (RTF_GATEWAY) when gw is set.
func v4route(iface, dst, gw, mask string) string {
	le := func(s string) string {
		a := netip.MustParseAddr(s).As4()
		return fmt.Sprintf("%08X", binary.LittleEndian.Uint32(a[:]))
	}
	flags := 0x1
	if gw != "0.0.0.0" {
		flags |= 0x2
	}
	return fmt.Sprintf("%s\t%s\t%s\t%04X\t0\t0\t0\t%s\t0\t0\t0", iface, le(dst), le(gw), flags, le(mask))
}

const v4header = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT"

func v4table(rows ...string) string { return v4header + "\n" + strings.Join(rows, "\n") + "\n" }

// v6route is one /proc/net/ipv6_route row.
func v6route(dst string, plen int, hop, iface string) string {
	h := func(s string) string {
		a := netip.MustParseAddr(s).As16()
		return fmt.Sprintf("%x", a[:])
	}
	flags := 0x1
	if hop != "::" {
		flags |= 0x2
	}
	zero := strings.Repeat("0", 32)
	return fmt.Sprintf("%s %02x %s 00 %s 00000400 00000001 00000000 %08x %8s", h(dst), plen, zero, h(hop), flags, iface)
}

// v6RejectDefault is the unreachable ::/0 row the kernel lists on lo.
const v6RejectDefault = "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo"

func iface(name string, prefixes ...string) httpserver.InterfaceAddrs {
	ia := httpserver.InterfaceAddrs{Name: name, Loopback: name == "lo"}
	for _, p := range prefixes {
		ia.Prefixes = append(ia.Prefixes, netip.MustParsePrefix(p))
	}
	return ia
}

type topology struct {
	v4, v6   string
	noV6File bool
	ifaces   []httpserver.InterfaceAddrs
	ifaceErr error
}

// check writes tp's route files into a fresh directory and returns a check
// over them, logging to logger.
func (tp topology) check(t *testing.T, logger *slog.Logger) *httpserver.ForwarderCheck {
	t.Helper()
	dir := t.TempDir()
	v4, v6 := filepath.Join(dir, "route"), filepath.Join(dir, "ipv6_route")
	if tp.v4 != "" {
		if err := os.WriteFile(v4, []byte(tp.v4), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !tp.noV6File {
		if err := os.WriteFile(v6, []byte(tp.v6), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return httpserver.NewForwarderCheck(
		httpserver.WithRouteFiles(v4, v6),
		httpserver.WithInterfaceAddrs(func() ([]httpserver.InterfaceAddrs, error) { return tp.ifaces, tp.ifaceErr }),
		httpserver.WithForwarderLogger(logger),
	)
}

var (
	// bridge is a rootful Docker bridge: default via 172.17.0.1 on eth0, a
	// second route via 172.17.0.254, and an IPv6 default via fd00:1::1.
	bridge = topology{
		v4: v4table(
			v4route("eth0", "0.0.0.0", "172.17.0.1", "0.0.0.0"),
			v4route("eth0", "172.17.0.0", "0.0.0.0", "255.255.0.0"),
			v4route("eth0", "10.20.0.0", "172.17.0.254", "255.255.0.0"),
		),
		v6: strings.Join([]string{
			v6route("fd00:1::", 64, "::", "eth0"),
			v6route("::", 0, "fd00:1::1", "eth0"),
			v6RejectDefault,
		}, "\n"),
		ifaces: []httpserver.InterfaceAddrs{iface("lo", "127.0.0.1/8", "::1/128"), iface("eth0", "172.17.0.2/16", "fd00:1::2/64")},
	}
	// slirp is rootless Podman 4's slirp4netns with rootlesskit: every client
	// arrives as the container's own 10.0.2.100.
	slirp = topology{
		v4:     v4table(v4route("tap0", "0.0.0.0", "10.0.2.2", "0.0.0.0"), v4route("tap0", "10.0.2.0", "0.0.0.0", "255.255.255.0")),
		ifaces: []httpserver.InterfaceAddrs{iface("lo", "127.0.0.1/8"), iface("tap0", "10.0.2.100/24")},
	}
	// podmanNet is a rootless Podman user-defined network: clients arrive as
	// the bridge's gateway.
	podmanNet = topology{
		v4:     v4table(v4route("eth0", "0.0.0.0", "10.89.0.1", "0.0.0.0"), v4route("eth0", "10.89.0.0", "0.0.0.0", "255.255.255.0")),
		ifaces: []httpserver.InterfaceAddrs{iface("lo", "127.0.0.1/8"), iface("eth0", "10.89.0.7/24")},
	}
	// swarm is the routing mesh: eth0 on ingress carries no default route,
	// which sits on eth1 (docker_gwbridge).
	swarm = topology{
		v4: v4table(
			v4route("eth1", "0.0.0.0", "172.18.0.1", "0.0.0.0"),
			v4route("eth0", "10.0.0.0", "0.0.0.0", "255.255.255.0"),
			v4route("eth1", "172.18.0.0", "0.0.0.0", "255.255.0.0"),
		),
		ifaces: []httpserver.InterfaceAddrs{iface("lo", "127.0.0.1/8"), iface("eth0", "10.0.0.5/24"), iface("eth1", "172.18.0.3/16")},
	}
)

func TestForwarderCheck_IsForwarder(t *testing.T) {
	cases := []struct {
		name string
		tp   topology
		peer string
		want bool
	}{
		{"loopback beside an interface list holding lo", bridge, "127.0.0.1", false},
		{"IPv6 loopback", bridge, "::1", false},
		{"LAN peer beside a 172.17.0.1 gateway", bridge, "192.168.1.50", false},
		{"peer in the subnet of the interface carrying the default route", bridge, "172.17.0.9", false},
		{"IPv4 default-route gateway", bridge, "172.17.0.1", true},
		{"IPv4-mapped default-route gateway", bridge, "::ffff:172.17.0.1", true},
		{"IPv6 default-route gateway", bridge, "fd00:1::1", true},
		{"zoned IPv6 default-route gateway", bridge, "fd00:1::1%eth0", true},
		{"gateway of a non-default route", bridge, "172.17.0.254", true},
		{"address of a non-loopback interface", bridge, "172.17.0.2", true},
		{"container's own address under slirp4netns", slirp, "10.0.2.100", true},
		{"LAN peer under slirp4netns", slirp, "192.168.1.50", false},
		{"gateway of a rootless user-defined network", podmanNet, "10.89.0.1", true},
		{"LAN peer on a rootless user-defined network", podmanNet, "192.168.1.50", false},
		{"ingress address on the routing mesh", swarm, "10.0.0.2", true},
		{"LAN peer beside the routing mesh", swarm, "192.168.1.50", false},
		{"kernel without an IPv6 route file", topology{v4: bridge.v4, noV6File: true, ifaces: bridge.ifaces}, "192.168.1.50", false},
		{"route file that cannot be read", topology{ifaces: bridge.ifaces}, "192.168.1.50", true},
		{"malformed route file", topology{v4: v4header + "\neth0\t0000\n", ifaces: bridge.ifaces}, "192.168.1.50", true},
		{"interface list that cannot be read", topology{v4: bridge.v4, ifaceErr: errors.New("netlink: operation not permitted")}, "127.0.0.1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.tp.check(t, discardLogger())
			if got := c.IsForwarder(netip.MustParseAddr(tc.peer)); got != tc.want {
				t.Errorf("IsForwarder(%s) = %v, want %v", tc.peer, got, tc.want)
			}
		})
	}
}

func TestForwarderCheck_failsClosed(t *testing.T) {
	t.Run("invalid address", func(t *testing.T) {
		if !bridge.check(t, discardLogger()).IsForwarder(netip.Addr{}) {
			t.Error("IsForwarder(zero) = false, want true")
		}
	})
	t.Run("nil check", func(t *testing.T) {
		var c *httpserver.ForwarderCheck
		if !c.IsForwarder(netip.MustParseAddr("127.0.0.1")) {
			t.Error("nil IsForwarder = false, want true")
		}
	})
}

func TestForwarderCheck_hostDefaults(t *testing.T) {
	if _, err := os.Stat(httpserver.DefaultRouteFile); err != nil {
		t.Skipf("no %s on this host: %v", httpserver.DefaultRouteFile, err)
	}
	if httpserver.NewForwarderCheck(httpserver.WithForwarderLogger(discardLogger())).IsForwarder(netip.MustParseAddr("127.0.0.1")) {
		t.Error("IsForwarder(127.0.0.1) on this host = true, want false")
	}
}

func TestForwarderCheck_warnsOncePerSource(t *testing.T) {
	cases := []struct {
		name, source string
		tp           topology
	}{
		{"route table", "route table", topology{ifaces: bridge.ifaces}},
		{"interfaces", "interface addresses", topology{v4: bridge.v4, ifaceErr: errors.New("netlink: denied")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &captureHandler{}
			c := tc.tp.check(t, slog.New(h))
			for range 3 {
				if !c.IsForwarder(netip.MustParseAddr("192.168.1.50")) {
					t.Error("IsForwarder = false, want true")
				}
			}
			recs := h.snapshot()
			if len(recs) != 1 {
				t.Fatalf("log lines = %d, want 1", len(recs))
			}
			if recs[0].Level != slog.LevelWarn {
				t.Errorf("level = %v, want WARN", recs[0].Level)
			}
			if got := attrsOf(recs[0])["source"]; got != tc.source {
				t.Errorf("source = %v, want %q", got, tc.source)
			}
		})
	}
}
