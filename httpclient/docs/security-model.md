# Redirects, TLS and redaction

This page is for a developer whose client carries a credential. It covers the redirect policies that decide which hosts receive your custom auth headers, the transport that trusts only your own CA, and the helpers that keep secrets out of logs and errors.

## Redirect policies

Go strips `Authorization`, `Cookie` and `WWW-Authenticate` on a cross-domain redirect, but it forwards custom headers such as `X-Plex-Token` or `X-Api-Key`. A hostile 302 could send that credential to another origin, so pick the policy for each client.

| Policy | Follows |
| --- | --- |
| `DefaultRedirectPolicy` | same-host redirects only, on any port, including an `http` to `https` upgrade on that host. `New` installs it |
| `RefuseAllRedirects` | nothing. It returns `http.ErrUseLastResponse`, so the client hands back the 3xx with a nil error |
| `RedirectPolicyFunc(opts...)` | the hosts its options allow |

`RefuseAllRedirects` is the policy for a client that carries a token to an API that issues no redirects. The 3xx it surfaces is an error under `CheckStatus`.

Every shipped policy refuses an `https` to `http` downgrade, even to an allowed or same-host target, so an auth header never travels over cleartext. The downgrade is judged against the original request's scheme, and no option allows one. An `http` to `https` upgrade still has to pass the same-host or allowlist check.

`CheckRedirect` is a type alias for the `http.Client.CheckRedirect` function shape, and every shipped policy is one.

### RedirectPolicyFunc options

| Option | Default | Effect |
| --- | --- | --- |
| `WithAllowedHosts(hosts...)` | none | Exact host names allowed as targets |
| `WithAllowedSuffixes(suffixes...)` | none | Domain suffixes allowed, such as `.cdn.example.com` |
| `WithSameHost(bool)` | false | Also allows the original request's own host, on any port and scheme |
| `WithSameOrigin(bool)` | false | Also allows the original request's own origin: its scheme, host and port |
| `WithMaxHops(n)` | 5 | Redirects followed in one request. The next one is refused |
| `WithPreserveMethod(bool)` | false | Refuses a hop that would change the request method |

With no allowed host, no suffix, no `WithSameHost(true)` and no `WithSameOrigin(true)`, the policy refuses every redirect. Host names compare without regard to ASCII case. An IP address compares by value, so `[2001:db8::a]` and `[2001:db8:0::a]` are one host, and its IPv6 zone compares exactly, so `[fe80::1%25eth0]` and `[fe80::1%25ETH0]` are two hosts. A suffix matches a host name only, never an IP address. A later option overrides an earlier one, so appending `WithSameHost(false)` turns the permission back off.

### Same origin

`WithSameHost(true)` follows a hop to another port on the same host, and an `http` to `https` upgrade. Another port can be another service, which then receives your custom auth header. `WithSameOrigin(true)` follows a hop to the original host only at its own scheme and port, as RFC 6454 defines an origin. A missing port counts as 80 for `http` and 443 for `https`.

With `WithSameOrigin(true)`, the origin alone decides a hop to the original host, even when `WithSameHost(true)` or `WithAllowedHosts` also names it. Hops to other hosts are still judged by the allowlist and the downgrade rule.

### Refusals

Every hard refusal from a shipped policy, by the hop cap, the target permission or the downgrade rule, is a `*RedirectRefusedError`, inside the `*url.Error` that `http.Client` returns. Its `Reason` names the rule: `RedirectNotAllowed`, `RedirectOtherOrigin`, `RedirectDowngrade` or `RedirectTooMany`. Use `errors.As` and `Reason` to tell them apart, not the error text. Its `Origin` field holds the target's scheme, host and port, never its userinfo, path or query, and the error text names only that origin. The origin is escaped as in a URL, so an IPv6 zone reads `%25` and `url.Parse` reads it back. This error is never transient, so `Do` does not retry it. `RefuseAllRedirects` and a `WithPreserveMethod(true)` refusal return `http.ErrUseLastResponse` instead, so you get the 3xx itself with a nil error.

### Keeping the request method

net/http turns a POST, PUT, PATCH or DELETE into a GET across a 301, 302 or 303 and drops the body, as RFC 9110 section 15.4 and Go issue 18570 describe. Only 307 and 308 carry the method forward. For an API call whose meaning is its method, that turns a write into a read of a URL you never named.

`WithPreserveMethod(true)` refuses such a hop instead. It returns `http.ErrUseLastResponse`, so you get the 3xx with a nil error, and `CheckStatus` reports it as an error, the same pairing `RefuseAllRedirects` uses. The request is never re-sent with the original method.

The comparison is with the original request. A POST kept by a 307 and then turned into a GET by a 302 is refused at the second hop. A hop chain with no original request fails closed. The hop cap, the allowlist and the downgrade refusal are checked first and return hard errors. The option only narrows a policy, so with no allowlist, no `WithSameHost(true)` and no `WithSameOrigin(true)` it still refuses everything.

## Trusting only your own CA

`CATransport(pem)` builds an `*http.Transport` that trusts only the CA certificates in `pem`. Use it for a known self-hosted endpoint with a private or self-signed certificate. Hosts that chain to a public CA are rejected too.

- It is cloned from `http.DefaultTransport`, so it keeps connection pooling, dial and keep-alive timeouts, HTTP/2 and the proxy settings from the environment.
- It installs a fresh TLS configuration with TLS 1.2 as the minimum. Verification stays on, and `InsecureSkipVerify` is never set. TLS settings a program changed on `http.DefaultTransport` are not carried over.
- It returns `ErrNoCertsInPEM` when `pem` holds no certificate, so an empty or broken CA file fails at once.
- You read the PEM bytes yourself, from a file, a secret or an environment variable, so the function does no I/O.
- The result is a concrete `*http.Transport` you can tune or pass to `NewRetryRoundTripper`.

```go
tr, err := httpclient.CATransport(pemBytes)
if err != nil {
    return err
}
client := httpclient.NewRetry(tr, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{MaxAttempts: 3})
```

`CloneDefaultTransport()` returns a private clone of `http.DefaultTransport` for you to change, for example to set a per-attempt `ResponseHeaderTimeout` or `MaxIdleConnsPerHost`, without changing every other client in the process. Both functions return an error when `http.DefaultTransport` has been replaced by a type other than `*http.Transport`.

### Test certificates

The `github.com/cplieger/go/httpclient/certtest` package makes throwaway self-signed CA material for tests of code that uses `CATransport`. Only `_test.go` files import it, so its certificate code never reaches a production binary.

- `certtest.SelfSignedCA(tb)` returns a new self-signed CA certificate, PEM-encoded. Each call makes a new key, so two certificates never trust each other, which is useful to show a pin is enforced.
- `certtest.WriteSelfSignedCA(tb)` writes the same kind of certificate to `ca.pem` under `tb.TempDir()` and returns the path.

## URLs in logs and errors

Credentials in logs are [CWE-532](https://cwe.mitre.org/data/definitions/532.html). `GetBytes` removes userinfo and query values from every URL in its log lines and error messages. The error value it returns still carries the original URL in one exported field, described below.

- Every logged `url` attribute replaces the whole userinfo with `REDACTED`. That is stronger than `url.URL.Redacted`, which masks only the password and would leave a token passed as the username in the clear.
- Every query value becomes `REDACTED`, because query values often carry API keys. Query keys, the scheme, the host and the path are kept for debugging, and the fragment is dropped. A URL that does not parse is logged as a fixed placeholder.
- `StatusError.Error()` shows the same redacted URL. The exported `StatusError.URL` field holds the original URL for your code, so do not log or serialize that field.
- A transport `*url.Error` embeds the full URL, so it is reduced to its cause before it is logged or returned. The reduction repeats until no `*url.Error` is left in the chain, so a `*url.Error` a RoundTripper or redirect policy returned under net/http's own is reduced too. A chain nested more than eight deep becomes the fixed text `request failed`. `DoConditional` returns its transport errors reduced the same way.

A secret in the URL path is kept as written, and the URL is re-encoded on the way out. A space comes out as `%20`, a non-ASCII byte percent-encoded, and a `#` cuts the value at the fragment. So do not put a credential in a URL path you pass to `GetBytes`. If an API requires one, pass `WithLogger` a logger whose handler redacts both the raw and the percent-encoded form, and redact the returned error before you log it.

`LogSafeError(err)` is the reduction httpclient applies to every transport error it logs. Use it when you wrap transport errors into your own messages. It returns any other error unchanged, keeps `errors.Is` and `errors.As` working, and never turns a non-nil error into nil. It equals `RedactTransportError(err, "", "")`.

### Why drain errors are not logged

The URL reduction strips an envelope httpclient added, so it cannot clean text the far end wrote. net/http reports a malformed chunked trailer as `malformed MIME header: missing colon: "<remote bytes>"`. For a URL that carries its credential in the path, such as a webhook token, a server that echoes the request URI puts the credential in those bytes.

`Drain` and `DrainClose` log on `slog.Default()`, which no option can reroute, so they drop the read error instead of logging it. Nothing you can act on is lost, because a drain runs only where the body is already being thrown away and the outcome is reported by the code that threw it away.

## Redacting a secret you hold

- `RedactSecretString(s, secret)` replaces every copy of `secret` in `s` with `REDACTED`.
- `RedactSecret(err, secret)` does the same to an error's message.
- `RedactTransportError(err, prefix, secret)` reduces a `*url.Error` to its cause, adds the prefix, then redacts.

`RedactSecretString` and `RedactTransportError` take the secret as the `Secret` type, so the secret and the text to scan cannot be swapped. A swapped call would return the secret untouched, and it does not compile. An untyped string constant converts by itself, so only a `string` variable needs `httpclient.Secret(token)`. `RedactSecret` takes a plain string, because its two parameters already differ in type.

Convert to `Secret` at the call and never store one. Printing or logging a `Secret` shows the value. An empty secret turns redaction off, so treat an empty credential as "nothing redacted".

The match is exact, byte for byte, which puts three rules on you:

1. Hold the secret in the same encoding the text uses. A decoded token does not match JSON-escaped or percent-encoded text, and nothing tells you.
2. Redact on both sides of any transform that rewrites text, such as a sanitizer, Unicode normalization, unquoting or unescaping. Redacting only before it misses a secret the transform builds. Redacting only after it misses a secret the transform changes.
3. Apply any length cap last. A cut through the secret leaves a prefix the full value no longer matches.

Together, the order is redact, normalize, redact, cap.
