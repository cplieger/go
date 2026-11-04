# urlform

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/urlform.svg)](https://pkg.go.dev/github.com/cplieger/go/urlform) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=urlform%2Fgo.mod)](https://github.com/cplieger/go/blob/main/urlform/go.mod)

urlform tells your Go code which host a browser opens for an untrusted URL, or that it cannot vouch for one, before you publish or match the link.

Go's `net/url` and a browser can read the same URL differently. For example, a browser drops embedded tabs and newlines, reads a backslash as a slash and opens a schemeless `host/x` at `host`. Code that trusts Go can then link to a host it never checked. urlform uses only the standard library, needs Go 1.27.3 or later and is licensed under Apache-2.0.

## Why use it

urlform is built for Go programs that show untrusted URLs to people as links, or that decide which site a link points to.

- `Classify` puts every string in exactly one of seven classes and never returns an error.
- It reports the host a browser opens for `https:/host/x`, `/\host/x` and the Go string `"https://anime\tbytes.tv"`.
- `HostMatchesDomain` refuses `evilnyaa.si` and `nyaa.si.evil.example` for the domain `nyaa.si`, and `IsASCIIHost` refuses a lookalike host.
- `RawQueryNames` finds `apikey` in `apikey=SECRET;foo=x`, a pair `url.ParseQuery` drops.
- It never accepts or refuses a URL. Your code sets the policy.
- 29 test cases pin the browser readings, 17 of them from the browsers' shared [web-platform-tests](https://github.com/web-platform-tests/wpt).

Consider [whatwg-url](https://github.com/nlnwa/whatwg-url) if you need a complete [WHATWG URL](https://url.spec.whatwg.org/) parser in Go, with canonicalization profiles. urlform does not check a URL your own program fetches. For that, consider [ssrf](https://github.com/cplieger/ssrf) by the same author, which refuses private addresses and checks every address at connect time.

## Install

```sh
go get github.com/cplieger/go/urlform@latest
```

## Usage

Decide whether to publish a link:

```go
f := urlform.Classify(raw)
switch f.Class {
case urlform.ClassAbsolute:
	if f.Scheme != "https" && f.Scheme != "http" {
		return "", false // publish web links only
	}
	if f.HasUserInfo || f.HasBackslash || !validPort(f.Port) {
		// visual-spoofing vectors, and a port a browser refuses
		return "", false
	}
	return f.Trimmed, true
case urlform.ClassRelative:
	return base + f.Trimmed, true // rooted path, no host of its own
default:
	// ClassEmpty, ClassMalformed, ClassHiddenHost,
	// ClassProtocolRelative and ClassSchemelessHost
	return "", false
}
```

`validPort` is your own check. It accepts an empty `Port` or a number up to 65535, the largest port a browser accepts.

Match the host against known domains, and refuse a host you cannot vouch for:

```go
f := urlform.Classify(raw)
if f.Host == "" || !urlform.IsASCIIHost(f.Host) {
	// no host evidence, or homograph territory: fail closed
	return nil, false
}
for domain, tracker := range knownDomains {
	if urlform.HostMatchesDomain(f.Host, domain) {
		return tracker, true
	}
}
return nil, false
```

Read the parameter names of a raw query, including the ones the parsed view drops:

```go
for name := range urlform.RawQueryNames(u.RawQuery) {
	if isCredentialParam(name) { // the caller's predicate, and its fail direction
		return true
	}
}
```

Compare two spellings of one destination:

```go
f := urlform.Classify(raw)
if p := f.NormalizedPath(); p == "" || !strings.HasPrefix(p, "/beat/") {
	return errOutsideNamespace // "/beat/api/../../ghost" resolves out of the namespace
}
```

`RawQueryPairs` walks the same query with each value. [How urlform reads a URL](docs/contract.md#query-names-and-values) shows it.

## API

- `Classify` returns a `Form`, which holds the `Class` and the facts a browser sees: `Trimmed`, `Host`, `Scheme`, `Port`, `HasBackslash`, `HasTabOrNewline`, `HasUserInfo` and `HostUnrecoverable`.
- `Class` is one of `ClassEmpty`, `ClassMalformed`, `ClassAbsolute`, `ClassHiddenHost`, `ClassProtocolRelative`, `ClassSchemelessHost` and `ClassRelative`.
- `(*Form).NormalizedPath` returns the path a browser resolves, with dot segments removed.
- `IsASCIIHost`, `FoldHostASCII` and `HostMatchesDomain` check host evidence. `EqualASCIIFold` compares ASCII tokens that are not hosts, such as a query parameter name.
- `RawQueryNames` and `RawQueryPairs` walk a raw query without the parsed map.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/urlform).

## It reads the string as the browser does

Before it parses, `Classify` cleans the string as a browser does. It removes every embedded tab and newline and trims control characters and whitespace from both ends. `Trimmed` holds that clean string, and `HasTabOrNewline` records that something was removed. For the parse, it then reads a backslash as a slash in the `http`, `https`, `ws`, `wss`, `ftp` and `file` schemes and in strings with no scheme, as the URL standard does. After any other scheme a backslash stays an ordinary character, because a browser keeps it. `Trimmed` keeps every backslash as written, and `HasBackslash` lets a publisher refuse it.

Host and token comparisons fold only the letters A to Z. `strings.ToLower` turns U+212A KELVIN SIGN into `k`, so a lookalike host becomes ASCII before `IsASCIIHost` checks it. `strings.EqualFold` matches U+017F LONG S to `s`, so a lookalike token compares equal to an ASCII one.

[How urlform reads a URL](docs/contract.md) gives the full contract of each class, fact and helper.

## Unsupported by design

urlform parses with `net/url` and then applies the browser readings described above. It is not a full WHATWG parser, and it leaves these out:

- IDNA and punycode mapping. A non-ASCII host stays raw so `IsASCIIHost` can refuse it.
- Percent-encoding normalization.
- Port range checks. `Port` reports `99999` for `https://host:99999/x`, and the caller checks the range.
- Host validation beyond what `net/url` accepts.
- Control characters inside the string other than tab and newline, which `net/url` rejects.
- The drive-letter rules of the `file` scheme.

## Documentation

- [How urlform reads a URL](docs/contract.md) gives each class, fact and helper in full, for code that relies on an edge case.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).

Third-party attributions are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
