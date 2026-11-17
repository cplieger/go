# Client IP, hosts and credentials

This page is for a developer deciding what a request may claim about itself. It covers the client IP behind a proxy, the Host allowlist, cross-origin writes, loopback-only endpoints, forwarders in front of the host, classifying a listen address, checking configured credentials and the values they travel in, writing a `WWW-Authenticate` challenge and limiting failed authentication.

## Client IP

`ResolveClient(r, trusted...)` resolves the client address against the trusted proxy prefixes, a `[]netip.Prefix`, and says where the answer came from.

With no trusted prefixes, or when the direct peer is outside them, it ignores `X-Forwarded-For` and answers the host of `r.RemoteAddr`. That is the TCP peer, which a client cannot spoof at this layer.

Behind a trusted proxy it reads `X-Forwarded-For` from right to left, skipping each trusted hop, and answers the first untrusted entry. That is the correct reading when a proxy appends the peer it saw, as Caddy and most reverse proxies do. The leftmost entry is whatever the client sent.

`X-Forwarded-For` is read as an RFC 9110 list. Every field line is joined into one value, each entry is trimmed of spaces and tabs only, and empty entries are skipped. An entry that is not an address on its own, such as one with a port, quotes or a zone, stops the walk and the peer is answered.

Each forwarding header is read up to 64 entries, counted across its field lines, which is far more hops than any real proxy chain has. A header holding more entries, or a `Forwarded` element holding more than 16 parameters, is invalid as a whole and is never read in part. Parsing costs time in proportion to the header's length, however many entries it holds, so `http.Server`'s `MaxHeaderBytes` is what bounds it. From a trusted proxy, an invalid `X-Forwarded-For` answers the peer. From any other peer, an invalid header sets `UntrustedForwarding` and names no address.

`X-Real-IP` and `Forwarded` never set the address, because Caddy passes a client's copy through. The library hardcodes no trusted range. You supply them.

The trusted set must hold every proxy hop between the client and the server. If a hop is missing, the walk stops there and returns that hop's address.

Addresses compare unmapped, so an IPv4 proxy reaching a dual-stack listener as `::ffff:10.0.0.7` matches `10.0.0.0/8`.

The answer is a `ClientResolution`:

| Field | Meaning |
| --- | --- |
| `IP` | The client address |
| `Peer` | The socket peer, unmapped unless it carries a zone, or the zero address when `r.RemoteAddr` is not an IP |
| `PeerTrusted` | The peer is inside the trusted set, whether or not it forwarded a client. `Forwarded` implies it and `UntrustedForwarding` excludes it |
| `Forwarded` | `IP` came from `X-Forwarded-For` sent by a trusted peer. When false, `IP` is the peer |
| `UntrustedForwarding` | A peer outside the trusted set sent a `Forwarded`, `X-Forwarded-For` or `X-Real-IP` holding at least one non-empty entry, which was ignored |
| `IgnoredPublic` | One of those ignored headers named a public address, meaning global unicast and outside the private ranges |
| `ForwardedHTTPS` | A trusted peer sent exactly one `X-Forwarded-Proto` field holding the one token `https`, any case. A list such as `https, http`, a repeated field, an empty value or an untrusted peer reads as plain, and `Forwarded: proto=` is not read. A listed proxy must overwrite the client's field, as Caddy does, not append to it |

Use it to decide whether a request is local. A trusted proxy that forwarded nothing answers its own address with `Forwarded` false, so a check on `IP` alone would take the proxy for a local client. `PeerTrusted` tells that proxy apart from a direct client, so read another header a proxy sets, such as `X-Forwarded-Proto`, only when it is true. `UntrustedForwarding` points to a proxy you have not listed, and `IgnoredPublic` to a public client behind it.

For `IgnoredPublic`, an `X-Forwarded-For` or `X-Real-IP` entry names an address even when quoted, bracketed or followed by a port. A `Forwarded` element counts only when it follows RFC 7239 exactly. Its `name=value` pairs are joined by `;` with no spaces, and each name appears once. A `for` value with a port or an IPv6 address must be in quotes.

`ClientIP(r, trusted...)` returns `ResolveClient`'s `IP` as text, for a log line or a rate-limit key. An IPv4-mapped peer reads as its IPv4 address unless it carries a zone, which it keeps, and a `RemoteAddr` that is not an IP reads as `invalid IP`.

`ParseCIDRs(entries)` turns an operator list of CIDRs or bare IPs into that trusted set. A bare IP becomes a `/32` or `/128` prefix, a CIDR is masked to its network, and an IPv4-mapped entry becomes its IPv4 prefix. Blank entries are skipped. Malformed entries, zoned addresses included, come back in a separate list in input order, so a strict caller can reject them and a lenient one can log them and use the rest.

### Moving from v3

- `ParseCIDRs` returns `[]netip.Prefix`, and `ClientIP` and `WithClientIP` take `netip.Prefix` values.
- `ClientIP` falls back to the unmapped peer address: `::ffff:10.0.0.1` reads `10.0.0.1`, a zoned `::ffff:10.0.0.1%eth0` keeps its form, and an unparseable `RemoteAddr` reads `invalid IP` instead of coming back as written.
- `ClientIP` skips empty `X-Forwarded-For` entries, so `203.0.113.9,` and `203.0.113.9,,10.0.0.2` answer `203.0.113.9` where v3 answered the peer. It trims only spaces and tabs, so an entry padded with other whitespace now answers the peer.
- `ClientIP` reads at most 64 `X-Forwarded-For` entries. A longer header answers the peer, where v3 walked it whole.

## Host allowlist

A DNS rebinding attack starts on a page the attacker controls. The page's own hostname is made to resolve to your service's address, so the victim's browser sends requests to your service with the attacker's name in `Host`. The allowlist refuses any request whose `Host` you did not list, which breaks the attack. It is listed as CWE-346.

- `ParseHostList(entries, opts...)` parses an operator allowlist, such as a config array or a comma-split environment variable, into an immutable `*HostPolicy`. Malformed entries come back separately, as with `ParseCIDRs`.
- `(*HostPolicy).Middleware()` answers 403 with the code `host_not_allowed` for a request whose `Host` is not listed, and logs one Warn line, `httpserver: host not allowed`. An inactive policy returns the handler unwrapped.
- `(*HostPolicy).Allows(r)` is the same decision for one request. `Active()` and `Size()` describe the policy, for startup logs.
- `(*HostPolicy).LogRefusal(r)` writes the same Warn line for a caller that admits by its own rule and writes its own refusal, so the refusal is logged once in one shape. It uses the policy's logger. A nil or zero-value policy logs through `slog.Default`.
- `WithHostResponder(fn)` writes the refusal through your `ErrorResponder` instead of `WriteError`, so your app's catalog can add its own remediation. It receives 403, `host_not_allowed` and `host not allowed`, and the status and code are the same on every listener.
- `WithLoopbackExempt(true)` admits a request from this host's own network stack, described below.
- `WithHostAllowlistLogger(logger)` sends the refusal line to `logger`. Nil, the default, means `slog.Default()` as it is when `ParseHostList` runs. Pass a logger with a discarding handler to silence it.

The refusal line carries `reason` (`missing host`, `malformed host` or `host not in allowlist`), `host` cut at 128 bytes, `remote` (the socket peer, which behind a reverse proxy is the proxy) and `request_id` when `Logging` set one. It carries no path, because a path can hold a token. The access line with the same `request_id` has the routed path, and with `WithClientIP` the client's address.

Place the middleware before any cross-origin or CSRF check. A rebinding request makes `Origin` and `Host` agree, so a same-origin check alone admits it.

### How matching works

`CanonicalHost(hostport)` is the strict canonicalizer that both the entries and each request's `Host` go through. It lowercases ASCII only, drops the port and the brackets, allows at most one trailing FQDN dot and normalizes IP literals so different spellings of one address compare equal. It returns `""` for anything malformed.

The lowercasing stops at ASCII on purpose. `strings.ToLower` maps U+212A and U+0130 to `k` and `i`, which would let a non-ASCII name canonicalize onto an allowlisted ASCII one. Configure a non-ASCII name as its Punycode A-label, because matching is byte-exact.

Matching is exact and purely textual. There is no name resolution, because resolving would reopen the race the check closes. `X-Forwarded-Host` is ignored, because a client controls it. A malformed `Host` is rejected rather than repaired, because a repair would map distinct wire values onto allowlisted entries.

Activation fails closed. A nil or all-blank entry list leaves the policy inactive, so requests pass through. Any non-blank entry turns the check on, so even a list where every entry is invalid denies every request rather than silently turning protection off.

### The loopback exemption

`WithLoopbackExempt(true)` keeps a container healthcheck or an in-container client working under an allowlist meant for browsers. It admits a request in two cases:

- The socket peer and the `Host` are both loopback.
- The socket peer is the connection's own local address, and the `Host` is that address as an IP literal. A service bound to one non-loopback address, such as `192.0.2.10:9190`, is probed this way, so its own probe passes without the address in the allowlist. A zoned IPv6 link-local bind and a listener that is not TCP are not covered, so list such an address in the allowlist.

A rebinding attack cannot reach it, because the attack's `Host` is the attacker's name. A remote client cannot forge it: a remote peer is not loopback, and it cannot connect from the server's own address, because the reply to that address never leaves the host. That holds for a listener that receives only traffic addressed to this host. A transparent (`IP_TRANSPARENT`) listener, or a PROXY-protocol wrapper that reports the header's addresses, makes the local address a value the client or proxy chose, so do not use the exemption or `LoopbackOnly` behind one. `false` means the same as leaving the option out, and options resolve last-wins, so you can pass a computed flag without branching.

## Cross-origin writes

`CrossOrigin(next, errs)` serves `next` behind the standard library's `http.CrossOriginProtection`, and answers a refused request 403 with the code `cross_origin_refused` through `errs`, nil being `WriteError`. The standard library's own refusal is a plain-text 403, so a client would get no envelope.

It refuses a request with an unsafe method, such as `POST`, `PUT`, `PATCH` or `DELETE`, when its `Sec-Fetch-Site` is anything but `same-origin` or `none`, so `same-site` is refused as well as `cross-site`. A request with no `Sec-Fetch-Site` is refused when its `Origin` names another host than the request's. `GET`, `HEAD` and `OPTIONS` pass, and so does a request with neither header, such as one from a script or a command-line client.

Wrap every route that changes state in it. An `SPA` handler answers only `GET` and `HEAD`, and refuses any other method itself, so it needs no guard. Wrapping a whole app root that includes `SPA` would answer a cross-site `POST` there with no Content-Security-Policy, which `httpservertest.CheckSPA` fails. Put the Host allowlist outside it.

## Loopback-only endpoints

`LoopbackOnly(opts...)` admits a request only when `LoopbackRequest` passes and `ProxiedRequest` finds no forwarding header. Every other request is answered 403 with the standard error envelope and the code `loopback_only`. `WithLoopbackError(code, msg)` sets your own code and message, and `WithLoopbackResponder(fn)` renders the refusal yourself, for an envelope your clients already depend on or a refusal you want to log, as the matching options do for `RateLimiter`.

`LoopbackRequest(r)` reports whether a request is local, by the same two cases as the exemption above. Either check failing refuses. It reads `r.RemoteAddr`, `r.Host` and the connection's local address that `http.Server` records, and nothing else, so forwarded headers never admit or refuse a request. A handler called without a server has no local address, so only the loopback case can pass there. The `Host` check accepts every spelling `CanonicalHost` collapses: `localhost` in any case, `127.0.0.0/8` and `::1`, with an optional port or trailing dot.

`ProxiedRequest(h)` reports whether the headers show that a proxy forwarded the request or a browser sent it. The set is `Forwarded`, `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Real-Ip`, `Sec-Fetch-Site` and `Origin`. A missing header proves nothing. Use it directly when your app has its own admission chain and cannot take the middleware whole.

### Why the middleware also checks forwarding headers

A normal in-container command-line client sends none of those headers. Their presence points to a browser or a proxy, so `LoopbackOnly` refuses the request. Host networking or a shared network namespace puts a reverse proxy on the server's own network stack. Both nginx and Apache then rewrite `Host` to their upstream address by default, whether that is loopback or the server's bound address, which passes the `Host` check, and the proxy itself passes the peer check. Apache also adds `X-Forwarded-For` by default, so the header check refuses it. nginx adds no forwarding header by default, so only a browser's `Origin` or `Sec-Fetch-Site` tells its requests apart.

The check has a limit. It admits a same-host proxy that sends none of these headers, which is nginx's default for any client that is not a browser, because nothing then tells it apart from an in-container caller. Only authentication closes that gap.

This is a different question from `HostPolicy`. The allowlist asks whether the `Host` is one the operator named, so it admits a remote client sending `Host: localhost` when the operator lists that name. `LoopbackOnly` asks whether the request came from inside.

## Forwarders in front of the host

A container runtime, a tunnel or a router can relay a remote client under its own address, which is often private. Docker's bridge gateway, rootless Podman's slirp4netns address and the Swarm routing mesh's ingress addresses all do this. So a direct private peer is not proof of a local client. Ask `ForwarderCheck` before you trust one more, for example before a first-run setup asks no token of a local client.

`NewForwarderCheck(opts...)` returns a check, and `IsForwarder(peer)` reports that the peer is one of these forwarders when it is:

- a gateway of any route
- an address of a non-loopback interface
- inside the connected subnet of an interface that carries no default route of the peer's family.

The check reads Linux's `/proc/net/route` and `/proc/net/ipv6_route` and the host's interfaces on every call, so a network connected after start counts. A missing IPv6 route file reads as no IPv6 routes. Any other failed read, as on a host without these files, answers true, so the caller fails closed and logs one Warn line per source, `httpserver: forwarder check could not read the host's network`. An invalid address and a nil check also answer true. The peer's zone is dropped, and an IPv4-mapped address compares as IPv4.

- `WithRouteFiles(ipv4, ipv6)` reads the route tables from other paths, in the same formats.
- `WithInterfaceAddrs(read)` replaces the read of the interfaces, each an `InterfaceAddrs` of name, prefixes and loopback flag.
- `WithForwarderLogger(logger)` sends the Warn line to `logger`. Nil, the default, means `slog.Default()` as it is when `NewForwarderCheck` runs.

## Bind classification

`ClassifyBind(addr)` classifies a configured listen address, given as `host:port`, by exposure. Use it at startup to warn when an unauthenticated service listens beyond loopback.

| Class | Covers |
| --- | --- |
| `BindLoopback` | Loopback IP literals, and `localhost` under an ASCII case fold. `LOCALHOST` matches, a non-ASCII lookalike does not |
| `BindExposed` | Wildcard binds, routable IPs and every other hostname. No name is resolved, so an unresolvable name is exposed |
| `BindInvalid` | Not `host:port`. It is the zero value, so an uninitialized class never reads as safe |

`ClassifyBindHost(host)` classifies a bare host with no port and never returns `BindInvalid`. It is the fallback for a bind value that has no port. `BindClass.String()` returns `invalid`, `loopback` or `exposed` for a log attribute.

What to do with an invalid address is your app's decision. The godoc shows three ways to handle it.

## One static credential

`NewStaticTokenVerifier(configured)` builds a verifier once, at startup, from the single operator-configured secret that guards an endpoint. That can be an API key, a bearer token, or a basic-auth user or password. `Verify(presented)` reports a match in constant time and is safe for concurrent use.

The configured secret is hashed with SHA-256 once, at construction. `Verify` hashes the presented value and compares the two fixed-length digests with `subtle.ConstantTimeCompare`. So no call's timing varies with the secret's length or content.

An empty configured secret fails closed. `Verify` then returns false for every value, the empty string included. Otherwise `sha256("")` would equal `sha256("")`, and an unset secret would admit a client presenting nothing. Treat an empty configured value as "auth not configured", never as "no credential required".

This checks one shared secret, not user identities. Per-user credentials, password hashing and sessions belong to the [access](https://github.com/cplieger/access) library.

## Several static credentials

`NewTokenSet(configured)` builds a `TokenSet` from secrets your operator configures, one per caller, for an endpoint where each caller holds its own. `Match(presented)` reports the position of the secret that matched, so your app can tell which caller it is, and is safe for concurrent use.

Each secret is hashed with SHA-256 once, at construction. `Match` hashes the presented value once and compares it with every configured digest, with no early exit, so the time taken depends only on how many secrets the set holds. A presented value longer than `MaxTokenBytes` (512) answers no match without being hashed. The zero `TokenSet` matches nothing.

`NewTokenSet` refuses an empty list, an empty secret, one longer than `MaxTokenBytes` and one given twice, because a repeated secret would make the position ambiguous. The refusal is a `*TokenSetError`. Its `Err` is `ErrNoTokens`, `ErrTokenEmpty`, `ErrTokenTooLong` or `ErrTokenRepeated`, which `errors.Is` matches. `Index` is the refused secret's position and `First` the earlier position a repeated secret equals. The error names positions only and never quotes a secret, so you can log it as it is.

## A value that must travel in a header

`CheckFieldValue(v)` reports whether `v` can be sent as an HTTP field value (RFC 9110 section 5.5) and arrive unchanged and unambiguous. Use it at startup on a configured credential that a client sends in a header, so a value that could never match is refused there rather than on every request. It refuses:

- an ASCII control byte other than HTAB, or DEL, which the grammar forbids and `net/http` refuses to send
- a space or tab at either edge, which a recipient strips, so the value read back is not the one sent
- invalid UTF-8
- an invisible rune: a C1 control, a Unicode format character (bidi controls, zero-width characters, the byte order mark) or a line or paragraph separator. Each is legal on the wire but invisible where a person reads or pastes the value.

The refusal is a `*FieldValueError`. Its `Err` is `ErrFieldControl`, `ErrFieldEdgeSpace`, `ErrFieldInvalidUTF8` or `ErrFieldInvisible`, which `errors.Is` matches, and `Offset` is the byte offset of the first refused byte. The error never quotes the value, so it can name a secret's problem without leaking it. Length limits, blank values and the message the operator reads stay your app's policy. The empty string passes.

## The 401 challenge

A `401` must carry at least one `WWW-Authenticate` challenge (RFC 9110 section 11.6.1). `SetChallenge(h, c, more...)` replaces `h`'s `WWW-Authenticate` field with one line per challenge, in order, and sets nothing else, so the status and body stay yours:

```go
httpserver.SetChallenge(w.Header(), httpserver.Challenge{
    Scheme: "Bearer",
    Params: []httpserver.AuthParam{{Name: "realm", Value: "myapp"}},
})
```

That writes `Bearer realm="myapp"`. Each parameter's value is written as a quoted-string, with `"` and `\` escaped. A scheme or parameter name that is not an HTTP token, or a value no quoted-string can carry, panics, since those come from your program. Check a value taken from configuration with `CheckFieldValue` first.

## Failed-authentication limits

`FailureLimiter` throttles clients that keep failing to authenticate. Use it in a handler that must check a credential before it can answer anything, such as a webhook endpoint. `RateLimiter`'s `FailedAuthRateLimit` preset is a single bucket for the whole process. `FailureLimiter` counts per client and across clients, and a request it throttles never moves its counts.

`NewFailureLimiter(limits)` checks a `FailureLimits` and returns a limiter, or an error naming the first field out of range:

| Field | Meaning |
| --- | --- |
| `PerKey`, `PerKeyWindow` | Failures one client key may make per window before it is throttled. Both must be positive |
| `Global`, `GlobalWindow` | Failures every key together may make per window before keys that are not exempt are throttled. Both zero turns the global window off |
| `MaxKeys` | How many keys' failures are held. Charging a new key past it forgets the least recently charged one |
| `TrustPerGroup`, `TrustWindow` | How many keys one group trusts at once, and how long a mark lasts |
| `IPv6Prefix` | Leading bits of an IPv6 address that make its key, 1 to 128. Zero means 64, RFC 6177's end-site `/64` |
| `Now` | The clock. Nil means `time.Now`, and a test passes a fake one |

Call `Decide(client, failure)` once per request, before you write anything. It answers whether the client is throttled, and for how long, from the state before this request. A throttled answer changes nothing, so a flood of throttled requests can neither fill a window nor push a key out of the held set. Answer a throttled request the same way whatever it presented. When the answer is not throttled and `failure` is true, the failure is charged to the client's key and to the global window. Asking once per request under the limiter's lock is what keeps concurrent requests from passing a full window together.

A key is a client address: an IPv4 address whole, and an IPv6 address masked to `IPv6Prefix` bits. Every invalid address shares one key.

After a request that was not throttled succeeds, call `MarkGood(group, client)` with a group name taken from your configuration, never from the request. While its mark lasts and its own window holds no failure, a marked key is exempt from the global window. So a spray of failures across many addresses cannot lock out a caller that recently succeeded. Each group holds at most `TrustPerGroup` keys and replaces its own oldest mark, never another group's.

The limiter keeps its state in memory, so it resets at a restart and replicas do not share it. It logs nothing and returns no error after construction, so a group name never leaves it.
