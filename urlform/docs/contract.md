# How urlform reads a URL

This page gives the full contract of each class, fact and helper in urlform. Read it when your code relies on an edge case, such as which strings carry a host or when a path reads empty.

urlform names the places where Go's `net/url` and a browser read a string differently. It reports what the browser would see and leaves the decision to you. A browser follows the [WHATWG URL Standard](https://url.spec.whatwg.org/), the URL spec browsers implement. urlform covers a bounded, listed set of those differences. It is a classifier built on `net/url` with the browser readings added, not a full WHATWG parser.

## Input preprocessing

A browser cleans a URL string before it parses it, and `Classify` does the same first. It removes every embedded tab, line feed and carriage return wherever they appear. `https://anime\tbytes.tv/x` therefore navigates to `animebytes.tv`, and urlform reports that host. It also trims C0 control characters and spaces from both ends. CPython adopted the same hardening for `urllib.parse` in [CVE-2022-0391](https://nvd.nist.gov/vuln/detail/CVE-2022-0391).

`HasTabOrNewline` records that a tab or newline was removed. `Trimmed` holds the cleaned string, free of the bytes a browser drops. Once your policy accepts the form, you can emit `Trimmed` without cleaning it again. Classifying `Trimmed` again gives the same facts, with the flag cleared.

Edge trimming is wider than the spec on purpose. It also removes Unicode whitespace, so a link wrapped in non-breaking spaces still classifies with its facts. Trimming too much fails safe for a check that needs host evidence, where a strict trim would return no facts at all.

## Classes

`Classify` never returns an error. Every input lands in exactly one class, and a string that does not parse is `ClassMalformed`.

| Class | Example | What it means |
| --- | --- | --- |
| `ClassEmpty` | `""`, `"  "` | Nothing is left after preprocessing |
| `ClassMalformed` | `http://[::1` | The parse failed. No facts and no host evidence |
| `ClassAbsolute` | `https://host/x` | A scheme and a host. `Host` holds the host |
| `ClassHiddenHost` | `https:/host/x`, `javascript:alert(1)` | A scheme with no host for `net/url`, where a browser may still open one |
| `ClassProtocolRelative` | `//host/x`, `///x` | A leading `//`, which a browser resolves against the current scheme |
| `ClassSchemelessHost` | `host/x` | No scheme and no leading `/`. A browser address bar opens `host` |
| `ClassRelative` | `/x` | A rooted path with no host |

`ClassHiddenHost` recovers the browser's reading for the `http`, `https`, `ws`, `wss` and `ftp` schemes. After one of those schemes a browser skips any number of slashes, even none, and reads the host. So `https:/host/x` and `https:host/x` both report `host`. `https://:443/x` reports no host, like the browser, which refuses an empty host. A `file` URL keeps its path but reports no host, so `file:/example.com/` reads the path `/example.com/`. Every other scheme reads as an opaque path with no host. `host:443/x` parses `host` as a scheme, and `javascript:alert(1)` and `mailto:x` carry no host either.

`ClassProtocolRelative` has two forms. `//host/x` reports `host`. A form with no host evidence, such as `///x`, `//` or `//?q`, reports an empty `Host`. Go reads `///x` as a rooted path while a browser reads an authority, so treat an empty `Host` here as ambiguous.

`ClassSchemelessHost` takes its host from a second parse of the authority. A query-only or fragment-only form, such as `?x:y`, reports an empty `Host`.

## Facts on a form

| Field | Meaning |
| --- | --- |
| `Class` | The structural form, from the table above |
| `Trimmed` | The preprocessed string. Backslashes are kept as written |
| `Host` | The host a browser would open, lowercased with an ASCII-only fold. Empty when the string carries none |
| `Scheme` | The scheme, already lowercase. Empty when there is none or the string did not parse |
| `Port` | The port as written. `net/url` accepts only digits, and urlform does not check the range |
| `HasBackslash` | The trimmed string holds a backslash |
| `HasTabOrNewline` | Preprocessing removed an embedded tab or newline |
| `HasUserInfo` | The authority carries `user@`, the `https://trusted@evil/` spoofing form. A browser shows the host after the `@` |
| `HostUnrecoverable` | A `ClassSchemelessHost` or recoverable `ClassHiddenHost` whose authority parse failed, for example with a space before an `@` |

A host with non-ASCII bytes stays unfolded in `Host`, so `IsASCIIHost` can refuse it. A host carrying evidence a browser could open but urlform cannot extract sets `HostUnrecoverable`. A check that needs host evidence treats it like a parse failure.

## Backslashes

The facts describe the browser's reading of a backslash. A browser reads a backslash as a slash before the query in two cases. One is the `http`, `https`, `ws`, `wss`, `ftp` and `file` schemes, and the other is a string with no scheme. So `/\host/x` classifies as protocol-relative with host `host`, not as a rooted path. For any other scheme a backslash is an ordinary character. Rewriting it there would report a host a browser never sees.

The raw string is never rewritten. `HasBackslash` lets a publisher that must emit the original string refuse it outright.

## The normalized path

`(*Form).NormalizedPath` returns the path a browser resolves for the classified string, with dot segments removed. `/view/1/../2` reads `/view/2`, and `/beat/api/../../ghost` reads `/ghost`. Use it to compare or display two spellings of one destination.

Every non-empty result is rooted. It is resolved by `net/url`'s own [RFC 3986 section 5.2.4](https://www.rfc-editor.org/rfc/rfc3986#section-5.2.4) resolution, so repeated slashes stay exactly as the WHATWG parser keeps them. `path.Clean`, which `net/http`'s `ServeMux` uses, collapses them and answers a question about Go's router, not about the browser.

The path is read after percent-decoding, so `/a/%2e%2e/b` resolves to `/b` like the literal `..`. The WHATWG parser treats the `%2e` spellings as dot segments too. The same decoding reads `%2F` as a separator, which the parser does not. If your comparison must keep `%2F` distinct, compare the escaped path itself.

A form with a host but no path reads `/`, which is how a browser resolves `https://nyaa.si`. `NormalizedPath` returns an empty string when there is no path a browser resolves:

- `ClassEmpty` and `ClassMalformed`, which carry no facts.
- A failed authority parse, flagged by `HostUnrecoverable`.
- A hidden-host form a browser reads as an opaque path, such as `javascript:alert(1)` or `mailto:x`.
- A `ClassProtocolRelative` form with no host, such as `///a/../b`.
- A form with neither host nor path, such as `?x:y`, because its path depends on a base urlform never saw.

`https://:443/x` has no host evidence but still reads `/x`, because its path is separated from the authority. The query and the fragment are never part of the result.

## Host and token comparisons

`Form.Host`, `FoldHostASCII`, `EqualASCIIFold` and `HostMatchesDomain` all fold only the bytes A to Z. A caller folding a host, a path and a query name therefore works from one idea of ASCII case. The four helpers do these jobs:

- `FoldHostASCII(host)` returns that fold, for host evidence you hold from elsewhere, such as a configured domain, a request header or a host you parsed yourself.
- `EqualASCIIFold(a, b)` compares under that fold, for ASCII tokens that are not hosts, such as a path token or a query parameter name.
- `IsASCIIHost(host)` reports whether every byte is ASCII. Run it first, so a Cyrillic lookalike or a U+0130 or U+212A spelling never matches a real domain. To accept international hosts, convert punycode yourself instead of relaxing this check.
- `HostMatchesDomain(host, domain)` reports whether the host equals the domain or is a real subdomain of it.

The standard library's case functions are unsafe here. `strings.ToLower` turns U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE into `i` and U+212A KELVIN SIGN into `k`. `strings.EqualFold` reads U+212A as `k` and U+017F LATIN SMALL LETTER LONG S as `s`. The two accept different inputs, so neither can stand in for the other.

`strings.ToLower` accepts the U+0130 spelling that `strings.EqualFold` refuses, and `strings.EqualFold` accepts the U+017F spelling that `strings.ToLower` refuses. `strings.ToLower` turns a lookalike into an ASCII string before `IsASCIIHost` or a comparison sees it. `strings.EqualFold` leaves the string as it is and matches the lookalike to an ASCII host or token anyway.

For tokens, `strings.EqualFold` accepts a KELVIN SIGN spelling of `apikey` and a LONG S spelling of `/torrents.php`. A `strings.ToLower` comparison accepts a DOTTED CAPITAL I spelling of `torrentid`. Each would hand a check a match on bytes that differ from the configured ASCII token. `EqualASCIIFold` cannot match them, because a string that is not ASCII never equals an ASCII token.

The fold works on bytes, not runes. Mapping runes would turn an invalid UTF-8 byte into U+FFFD and merge distinct non-ASCII evidence into one spelling. The rule reads no Unicode table, so no Unicode release can change what it folds. The one Unicode table urlform reads is the whitespace set the edge trim uses.

`HostMatchesDomain` refuses three readings a plain suffix test accepts:

- Suffix confusion, such as `evilnyaa.si` for `nyaa.si`.
- Parent-domain spoofing, such as `nyaa.si.evil.example`.
- Empty DNS labels, such as `.nyaa.si` and `a..nyaa.si`.

An empty host or domain matches nothing, and so does a domain with an empty label. It compares with the ASCII-only fold. Trimming surrounding spaces and a trailing root dot is yours, so `nyaa.si.` does not match `nyaa.si`.

## Query names and values

`RawQueryNames(rawQuery)` yields the percent-decoded parameter names of a raw query, in order. Pass `u.RawQuery`, without the leading `?`. It splits on both `&` and `;`. `url.ParseQuery` drops a malformed pair whole. An unescaped semicolon makes `apikey=SECRET;foo=x` disappear from the parsed map, while the bytes still travel in every request and log line. The raw walk sees every name the parsed view sees and more, so a check built on it cannot be evaded that way.

- Empty fields are skipped.
- The name is the text before the first `=`. A field with no `=` yields its whole text as the name.
- A name whose escapes do not decode is yielded raw, so it still reaches your check.

`RawQueryPairs(rawQuery)` is the same walk with each value, for a check that reads it. The value is everything after the first `=`. The name and the value are decoded separately, and each falls back to its raw text, so one malformed half never hides the other. A field with no `=` yields an empty value, so `x` and `x=` read the same.

```go
for name, value := range urlform.RawQueryPairs(u.RawQuery) {
	if isCredentialParam(name) && value != "" {
		return redact(name) // a parameter carrying nothing is not a leak
	}
}
```

Both walks report names and values and take no view of them. One caller warns on a credential parameter, another redacts it, and both read the same walk.

## No policy, no errors, bounded cost

urlform reports facts, and the policy stays with you. A link publisher drops what it cannot vouch for. A check that gathers host evidence hides what it cannot classify. Both branch on the same classes, so they never disagree on what the string is.

Allocation is bounded and linear in the input. The number of allocations `Classify` makes does not grow with the size of the input, and unparseable input is a class, not an error.

## Conformance tests

`testdata/whatwg-fixtures.json` holds 29 test cases, which name the browser readings urlform covers. Of those, 17 take their input and their browser result from the [web-platform-tests](https://github.com/web-platform-tests/wpt) URL test data. The other 12 are written by hand. The hand-written cases cover address-bar forms outside the URL spec, backslash scoping and the non-ASCII host boundary. Every expected value is checked by hand against the WHATWG reading, never generated from urlform itself. A change in urlform that breaks a case fails the build.
