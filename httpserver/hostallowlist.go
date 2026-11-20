package httpserver

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// CanonicalHost parses a Host header value or an allowlist entry into the key
// exact matching compares: lowercase, with no port, brackets or trailing dot,
// and an IP literal in net.IP's spelling. It is idempotent, and returns "" for
// anything that is not a well-formed RFC 3986 authority, never repairing one,
// since a repair maps distinct wire values onto an allowed key. The fold is
// ASCII-only: strings.ToLower maps U+212A and U+0130 to "k" and "i". ssrf's
// numeric-IPv4 check refuses dotted hex, which this accepts as a name; the two
// answer different questions and stay separate.
func CanonicalHost(hostport string) string {
	if ip := net.ParseIP(hostport); ip != nil {
		return ip.String() // bare IPv4/IPv6 literal
	}
	if strings.HasPrefix(hostport, "[") {
		return canonicalBracketed(hostport)
	}
	host := hostport
	if i := strings.IndexByte(host, ':'); i >= 0 {
		if !validPort(host[i+1:]) {
			return "" // never repaired by truncating at the colon
		}
		host = host[:i]
	}
	host = strings.TrimSuffix(lowerASCIIString(host), ".") // at most one FQDN dot
	if ip := net.ParseIP(host); ip != nil {
		return ip.String() // IPv4 that carried a port or a trailing dot
	}
	if !validHostname(host) {
		return ""
	}
	return host
}

// canonicalBracketed parses a "[IPv6]" or "[IPv6]:port" authority. Brackets
// are IPv6-only syntax in RFC 3986, so a bracketed hostname or IPv4 literal is
// rejected, as is anything after "]" that is not exactly one valid ":port".
func canonicalBracketed(s string) string {
	inner, ok := bracketedHost(s)
	if !ok {
		return ""
	}
	ip := net.ParseIP(inner)
	if ip == nil || !strings.Contains(inner, ":") {
		return ""
	}
	return ip.String()
}

// bracketedHost returns what sits between the brackets of a "[...]" or
// "[...]:port" authority, false when "]" is missing or anything after it is
// not exactly one valid ":port".
func bracketedHost(s string) (string, bool) {
	end := strings.IndexByte(s, ']')
	if !strings.HasPrefix(s, "[") || end < 0 {
		return "", false
	}
	if rest := s[end+1:]; rest != "" {
		port, ok := strings.CutPrefix(rest, ":")
		if !ok || !validPort(port) {
			return "", false
		}
	}
	return s[1:end], true
}

// ipLiteralHost reports whether hostport, canonicalized as canon, is an IP
// address: canon parses as one, or hostport is a bracketed IPv6 address with a
// zone, which CanonicalHost refuses so that no allowlist entry holds one.
func ipLiteralHost(hostport, canon string) bool {
	if _, err := netip.ParseAddr(canon); err == nil {
		return true
	}
	inner, ok := bracketedHost(hostport)
	if !ok {
		return false
	}
	a, err := netip.ParseAddr(inner)
	return err == nil && a.Is6()
}

// validPort reports whether a port string is all digits and at most 65535.
func validPort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	n := 0
	for i := range len(port) {
		c := port[i]
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n <= 65535
}

// validHostname reports whether a lowercased, port- and dot-stripped candidate
// is an acceptable DNS name: non-empty ASCII labels of letters, digits,
// hyphens, and underscores, each at most 63 bytes. An all-numeric dotted
// candidate is rejected — it reaches here only because net.ParseIP refused it,
// and an IPv4-looking string that Go will not parse (leading zeros, five
// octets) is a configuration error, not a hostname.
func validHostname(host string) bool {
	nonNumeric := false
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := range len(label) {
			switch c := label[i]; {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c == '-', c == '_':
				nonNumeric = true
			default:
				return false
			}
		}
	}
	return nonNumeric
}

// HostAllowlistOption configures a HostPolicy built by ParseHostList.
type HostAllowlistOption func(*hostPolicyConfig)

// hostPolicyConfig holds resolved HostPolicy configuration.
type hostPolicyConfig struct {
	logger         *slog.Logger
	responder      ErrorResponder
	loopbackExempt bool
	ipLiteralHosts bool
}

// maxLoggedHost bounds the Host a refusal line records. r.Host is
// caller-chosen and has no length bound, and on HTTP/2 or an absolute-form
// request line it skips net/http's Host byte check, so the slog handler quotes
// it.
const maxLoggedHost = 128

// The refusal is the same on every listener of every app, so a probe or an
// alert rule reads one answer.
const (
	hostRefusalCode ErrorCode = "host_not_allowed"
	hostRefusalMsg  string    = "host not allowed"
)

// WithLoopbackExempt selects whether a request from this host's own network
// stack bypasses the allowlist: a socket peer and Host that are both loopback,
// or a peer at the connection's own local address with that address as Host
// (the probe of a service bound to one non-loopback address). It keeps a
// container healthcheck or in-container client working under an allowlist of
// browser hostnames; neither a rebinding request (the attacker's name in Host)
// nor a remote peer can pass. False, the default, is the same as leaving it
// out, and options are last-wins.
func WithLoopbackExempt(exempt bool) HostAllowlistOption {
	return func(c *hostPolicyConfig) { c.loopbackExempt = exempt }
}

// WithIPLiteralHosts selects whether a Host that is an IP address passes
// beside the allowlist: IPv4 or IPv6, bracketed IPv6 optionally with a zone as
// netip.ParseAddr reads one, and a port wherever CanonicalHost takes one. A
// deployment reached by address keeps working with no entry per address. It
// is safe because DNS rebinding needs the attacker's name in Host, and an
// address literal names no one's domain; a name stays under the exact-match
// list. False, the default, is the same as leaving it out, and options are
// last-wins.
func WithIPLiteralHosts(admit bool) HostAllowlistOption {
	return func(c *hostPolicyConfig) { c.ipLiteralHosts = admit }
}

// WithHostResponder sets the ErrorResponder that writes Middleware's refusal,
// in place of WriteError: it receives 403, host_not_allowed and "host not
// allowed", so an application's catalog writes its own remediation. A nil fn
// keeps WriteError.
func WithHostResponder(fn ErrorResponder) HostAllowlistOption {
	return func(c *hostPolicyConfig) {
		if fn != nil {
			c.responder = fn
		}
	}
}

// WithHostAllowlistLogger sets the slog.Logger that Middleware and LogRefusal
// report each refusal to. Nil, the default, means slog.Default() as it is when
// the refusal is logged. Pass a logger with a discarding handler to silence it.
func WithHostAllowlistLogger(l *slog.Logger) HostAllowlistOption {
	return func(c *hostPolicyConfig) { c.logger = l }
}

// HostPolicy is an immutable, canonicalized exact-match Host allowlist. Build
// one with ParseHostList and apply it with Middleware; the same policy answers
// the per-request decision through Allows. The zero value and a nil pointer are
// both inactive (permissive), so a policy parsed from unset configuration is a
// safe pass-through.
type HostPolicy struct {
	allowed        map[string]struct{}
	logger         *slog.Logger
	responder      ErrorResponder
	loopbackExempt bool
	ipLiteralHosts bool
	active         bool
}

// ParseHostList parses allowlist entries into a HostPolicy and returns the
// entries it could not use, as ParseCIDRs does. Each entry is trimmed and
// canonicalized by CanonicalHost; a blank one is skipped. The policy is
// inactive (every Host passes) only when no entry is non-blank; any non-blank
// entry makes it active, so a list of only malformed entries refuses every
// request instead of switching the check off. A caller that must fail loud
// refuses to start on a non-empty invalid slice.
func ParseHostList(entries []string, opts ...HostAllowlistOption) (policy *HostPolicy, invalid []string) {
	c := &hostPolicyConfig{responder: WriteError}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	p := &HostPolicy{
		allowed:        make(map[string]struct{}),
		logger:         c.logger,
		responder:      c.responder,
		loopbackExempt: c.loopbackExempt,
		ipLiteralHosts: c.ipLiteralHosts,
	}
	for _, e := range entries {
		s := strings.TrimSpace(e)
		if s == "" {
			continue // a blank entry never engages the gate
		}
		p.active = true // any non-blank entry engages the gate (fail closed)
		key := CanonicalHost(s)
		if key == "" {
			invalid = append(invalid, s)
			continue
		}
		p.allowed[key] = struct{}{}
	}
	return p, invalid
}

// Active reports whether the gate is engaged. A policy parsed from unset or
// all-blank configuration is inactive: Middleware is a pass-through and Allows
// returns true for every request. A nil policy is inactive.
func (p *HostPolicy) Active() bool {
	return p != nil && p.active
}

// Size reports the number of valid, canonicalized hosts in the allowlist
// (excluding entries ParseHostList reported as invalid). A nil policy has size
// zero.
func (p *HostPolicy) Size() int {
	if p == nil {
		return 0
	}
	return len(p.allowed)
}

// Allows reports whether the request is admitted. An inactive (or nil) policy
// admits every request. An active policy admits a request only when its
// canonicalized Host is in the allowlist, when WithIPLiteralHosts was set and
// the Host is an IP address, or, when WithLoopbackExempt was set, when the
// request is local by LoopbackRequest's rule. r.Host is the only host value
// consulted; X-Forwarded-Host is deliberately ignored (it is
// client-controlled and this check must hold on the direct-exposure path). A
// malformed Host canonicalizes to "" and can never match.
func (p *HostPolicy) Allows(r *http.Request) bool {
	if p == nil || !p.active {
		return true
	}
	return p.admits(r)
}

// admits applies p's rule to r whether or not p is active.
func (p *HostPolicy) admits(r *http.Request) bool {
	host := CanonicalHost(r.Host)
	if _, ok := p.allowed[host]; ok {
		return true
	}
	if p.ipLiteralHosts && ipLiteralHost(r.Host, host) {
		return true
	}
	if p.loopbackExempt && localRequest(r, host) {
		return true
	}
	return false
}

// Middleware returns the allowlist as httpserver middleware. An inactive or nil
// policy returns the next handler unwrapped, the "off" contract RateLimiter and
// RouteTimeout share. An active policy answers a disallowed Host with 403
// host_not_allowed through its responder (WithHostResponder) before the next
// handler runs, and logs one Warn with the reason, host, remote and, when the
// context has one, request_id (see WithHostAllowlistLogger). Put it outermost among
// request-authorization middleware: a DNS-rebinding request makes Origin and
// Host agree, so only the exact-Host check breaks it (CWE-346).
func (p *HostPolicy) Middleware() Middleware {
	if p == nil || !p.active {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p.Allows(r) {
				next.ServeHTTP(w, r)
				return
			}
			p.LogRefusal(r)
			p.responder(w, r, http.StatusForbidden, hostRefusalCode, hostRefusalMsg)
		})
	}
}

// LoopbackRequest reports whether r came from this host's own network stack:
// its socket peer is loopback and its Host names the local host, or its peer is
// the connection's own local address and its Host is that address. Both legs
// must hold, and each fails closed on a malformed value. It reads r.RemoteAddr,
// r.Host and the local address http.Server records, never a forwarded header.
// It decides admission only, for a loopback-only route. Unlike HostPolicy,
// whose allowlist admits a remote caller sending an allowed Host such as
// localhost, nothing outside this host can satisfy it.
func LoopbackRequest(r *http.Request) bool {
	return localRequest(r, CanonicalHost(r.Host))
}

// localRequest reports whether r came from this host's own network stack and
// names the address it reached: a loopback peer whose Host is the local host,
// or a peer at the connection's own local address whose Host is that address
// (the probe of a service bound to one non-loopback address). No remote peer
// can hold the server's own address: the handshake's reply to it never leaves
// the host. canon is CanonicalHost(r.Host).
func localRequest(r *http.Request, canon string) bool {
	peer, ok := peerAddr(r.RemoteAddr)
	if !ok {
		return false
	}
	if peer.IsLoopback() {
		return isLoopbackHost(canon)
	}
	if peer != listenerAddr(r) {
		return false
	}
	host, err := netip.ParseAddr(canon)
	return err == nil && host == peer
}

// isLoopbackHost reports whether a CanonicalHost-normalized value names the
// local host: the literal "localhost" or any loopback IP (127.0.0.0/8, ::1).
func isLoopbackHost(canon string) bool {
	if canon == "localhost" {
		return true
	}
	ip := net.ParseIP(canon)
	return ip != nil && ip.IsLoopback()
}

// peerAddr parses an http.Request.RemoteAddr, which the server sets from the
// accepted connection ("host:port"), so it cannot be spoofed at this layer.
// Anything that does not split as host:port, or carries a zone, fails closed:
// a zone marks a link-local peer, which no Host names (CanonicalHost refuses a
// zoned literal), and a lenient fallback would let a hand-built RemoteAddr
// widen the carve-out. It stays unexported: a peer-only gate admits a
// DNS-rebound page on this host (CWE-346).
func peerAddr(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(host)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// listenerAddr is the local address http.Server records under
// LocalAddrContextKey, or the zero Addr when there is no TCP one or it is
// unspecified, which no peer matches.
func listenerAddr(r *http.Request) netip.Addr {
	ta, _ := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	a := ta.AddrPort().Addr().Unmap()
	if a.IsUnspecified() {
		return netip.Addr{}
	}
	return a
}

// LogRefusal writes the Warn line Middleware writes for a refused Host, for a
// caller that admits by its own rule and writes its own refusal. It logs
// through the policy's logger (WithHostAllowlistLogger), or slog.Default as it
// is at that moment when the policy has none or is nil. The line carries no
// path, which can hold a token; the access line with the same request_id has
// it. The Host is empty when the request named none: HTTP/1.0 or HTTP/2
// without one, or an empty Host header.
func (p *HostPolicy) LogRefusal(r *http.Request) {
	var logger *slog.Logger
	if p != nil {
		logger = p.logger
	}
	reason := "host not in allowlist"
	switch {
	case r.Host == "":
		reason = "missing host"
	case CanonicalHost(r.Host) == "":
		reason = "malformed host"
	}
	attrs := []slog.Attr{
		slog.String("reason", reason),
		slog.String("host", boundLoggedPath(r.Host, maxLoggedHost)),
		slog.String("remote", r.RemoteAddr),
	}
	if id := RequestIDFromContext(r.Context()); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	orDefault(logger).LogAttrs(r.Context(), slog.LevelWarn, "httpserver: host not allowed", attrs...)
}
