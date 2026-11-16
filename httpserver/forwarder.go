package httpserver

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

// The Linux route files ForwarderCheck reads by default.
const (
	DefaultRouteFile     = "/proc/net/route"
	DefaultIPv6RouteFile = "/proc/net/ipv6_route"
)

// rtfGateway is the kernel's RTF_GATEWAY route flag.
const rtfGateway = 0x2

// InterfaceAddrs is one network interface as a ForwarderCheck reads it: its
// name, its address prefixes, and whether it is a loopback interface.
type InterfaceAddrs struct {
	Name     string
	Prefixes []netip.Prefix
	Loopback bool
}

// ForwarderOption configures NewForwarderCheck.
type ForwarderOption func(*ForwarderCheck)

// WithRouteFiles reads the IPv4 and IPv6 route tables from these paths, in
// the formats of Linux's /proc/net/route and /proc/net/ipv6_route. An empty
// path keeps its default. An absent IPv6 file reads as no IPv6 routes, as on
// a kernel without IPv6; an absent IPv4 file is a failed read.
func WithRouteFiles(ipv4, ipv6 string) ForwarderOption {
	return func(c *ForwarderCheck) {
		if ipv4 != "" {
			c.routeFile = ipv4
		}
		if ipv6 != "" {
			c.ipv6RouteFile = ipv6
		}
	}
}

// WithInterfaceAddrs replaces the read of the host's interfaces, which is
// net.Interfaces by default. A nil func keeps the default.
func WithInterfaceAddrs(read func() ([]InterfaceAddrs, error)) ForwarderOption {
	return func(c *ForwarderCheck) {
		if read != nil {
			c.interfaces = read
		}
	}
}

// WithForwarderLogger sends the failed-read warning to logger. Nil, the
// default, means slog.Default() as it is when NewForwarderCheck runs.
func WithForwarderLogger(logger *slog.Logger) ForwarderOption {
	return func(c *ForwarderCheck) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// ForwarderCheck reports whether a socket peer is a forwarder in front of
// this host, such as Docker's bridge gateway, rootless Podman's slirp4netns
// address or the Swarm ingress, each relaying a remote client under its own
// address. It re-reads the route tables and interfaces on every call, and a
// failed read, as on a host without Linux's route files, answers true so the
// caller fails closed. It is safe for concurrent use.
type ForwarderCheck struct {
	interfaces    func() ([]InterfaceAddrs, error)
	logger        *slog.Logger
	routeFile     string
	ipv6RouteFile string
	warnedRoutes  atomic.Bool
	warnedIfaces  atomic.Bool
}

// NewForwarderCheck returns a check over this host's network, or over the
// sources the options name.
func NewForwarderCheck(opts ...ForwarderOption) *ForwarderCheck {
	c := &ForwarderCheck{
		routeFile:     DefaultRouteFile,
		ipv6RouteFile: DefaultIPv6RouteFile,
		interfaces:    readInterfaceAddrs,
		logger:        slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// IsForwarder reports that peer is a gateway of any route, an address of a
// non-loopback interface, or inside a connected subnet of an interface that
// carries no default route of peer's family. Its zone is dropped and an
// IPv4-mapped address compares as IPv4. An invalid address, a nil check and a
// failed read of either source each answer true. A failed read logs one Warn
// line per source for the check's life.
func (c *ForwarderCheck) IsForwarder(peer netip.Addr) bool {
	if c == nil || !peer.IsValid() {
		return true
	}
	peer = peer.WithZone("").Unmap()
	routes, err := c.readRoutes()
	if err != nil {
		c.warnOnce(&c.warnedRoutes, "route table", err)
		return true
	}
	ifaces, err := c.interfaces()
	if err != nil {
		c.warnOnce(&c.warnedIfaces, "interface addresses", err)
		return true
	}
	if slices.Contains(routes.gateways, peer) {
		return true
	}
	defaults := routes.default4
	if peer.Is6() {
		defaults = routes.default6
	}
	return onInterface(peer, ifaces, defaults)
}

func (c *ForwarderCheck) warnOnce(warned *atomic.Bool, source string, err error) {
	if warned.CompareAndSwap(false, true) {
		c.logger.Warn("httpserver: forwarder check could not read the host's network, so every peer counts as a forwarder",
			"source", source, "error", err)
	}
}

// onInterface reports that peer is an address of a non-loopback interface,
// or inside a subnet of one that is not among defaults, the interfaces
// carrying a default route of peer's family.
func onInterface(peer netip.Addr, ifaces []InterfaceAddrs, defaults []string) bool {
	for _, ifc := range ifaces {
		if ifc.Loopback {
			continue
		}
		carriesDefault := slices.Contains(defaults, ifc.Name)
		for _, p := range ifc.Prefixes {
			if p.Addr().WithZone("").Unmap() == peer || (!carriesDefault && p.Masked().Contains(peer)) {
				return true
			}
		}
	}
	return false
}

// routeTable is the gateways of every route, both families, and the
// interfaces that carry each family's default route.
type routeTable struct {
	gateways []netip.Addr
	default4 []string
	default6 []string
}

func (c *ForwarderCheck) readRoutes() (routeTable, error) {
	v4, err := os.ReadFile(c.routeFile)
	if err != nil {
		return routeTable{}, err
	}
	v6, err := os.ReadFile(c.ipv6RouteFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return routeTable{}, err
	}
	return parseRouteTable(v4, v6)
}

// parseRouteTable reads /proc/net/route and /proc/net/ipv6_route in the
// kernel's formats. A malformed row fails the whole table.
func parseRouteTable(v4, v6 []byte) (routeTable, error) {
	var t routeTable
	if err := parseIPv4Routes(v4, &t); err != nil {
		return routeTable{}, fmt.Errorf("ipv4 route table: %w", err)
	}
	if err := parseIPv6Routes(v6, &t); err != nil {
		return routeTable{}, fmt.Errorf("ipv6 route table: %w", err)
	}
	return t, nil
}

// parseIPv4Routes reads rows of Iface, Destination, Gateway, Flags, RefCnt,
// Use, Metric and Mask after one header line. Addresses are little-endian hex.
func parseIPv4Routes(b []byte, t *routeTable) error {
	s := bufio.NewScanner(bytes.NewReader(b))
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return err
		}
		return errors.New("no header line")
	}
	for line := 2; s.Scan(); line++ {
		f := strings.Fields(s.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) < 8 {
			return fmt.Errorf("line %d: %d fields, want at least 8", line, len(f))
		}
		dst, err1 := hexIPv4(f[1])
		gw, err2 := hexIPv4(f[2])
		flags, err3 := strconv.ParseUint(f[3], 16, 32)
		mask, err4 := hexIPv4(f[7])
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if flags&rtfGateway != 0 {
			t.gateways = append(t.gateways, gw)
		}
		if dst.IsUnspecified() && mask.IsUnspecified() {
			t.default4 = append(t.default4, f[0])
		}
	}
	return s.Err()
}

func hexIPv4(s string) (netip.Addr, error) {
	if len(s) != 8 {
		return netip.Addr{}, fmt.Errorf("address %q is not 8 hex digits", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("address %q is not 8 hex digits", s)
	}
	var a [4]byte
	binary.LittleEndian.PutUint32(a[:], uint32(v))
	return netip.AddrFrom4(a), nil
}

// parseIPv6Routes reads rows of destination, its prefix length, source, its
// prefix length, next hop, metric, reference count, use, flags and device,
// with no header line.
func parseIPv6Routes(b []byte, t *routeTable) error {
	s := bufio.NewScanner(bytes.NewReader(b))
	for line := 1; s.Scan(); line++ {
		f := strings.Fields(s.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 10 {
			return fmt.Errorf("line %d: %d fields, want 10", line, len(f))
		}
		dst, err1 := hexIPv6(f[0])
		plen, err2 := strconv.ParseUint(f[1], 16, 8)
		hop, err3 := hexIPv6(f[4])
		flags, err4 := strconv.ParseUint(f[8], 16, 32)
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if flags&rtfGateway != 0 {
			t.gateways = append(t.gateways, hop.Unmap())
		}
		if dst.IsUnspecified() && plen == 0 {
			t.default6 = append(t.default6, f[9])
		}
	}
	return s.Err()
}

func hexIPv6(s string) (netip.Addr, error) {
	var a [16]byte
	if len(s) != 32 {
		return netip.Addr{}, fmt.Errorf("address %q is not 32 hex digits", s)
	}
	if _, err := hex.Decode(a[:], []byte(s)); err != nil {
		return netip.Addr{}, fmt.Errorf("address %q: %w", s, err)
	}
	return netip.AddrFrom16(a), nil
}

func readInterfaceAddrs() ([]InterfaceAddrs, error) {
	ifcs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]InterfaceAddrs, 0, len(ifcs))
	for _, ifc := range ifcs {
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ifc.Name, err)
		}
		ia := InterfaceAddrs{Name: ifc.Name, Loopback: ifc.Flags&net.FlagLoopback != 0}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(n.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			ones, bits := n.Mask.Size()
			if bits != ip.BitLen() {
				continue
			}
			ia.Prefixes = append(ia.Prefixes, netip.PrefixFrom(ip, ones))
		}
		out = append(out, ia)
	}
	return out, nil
}
