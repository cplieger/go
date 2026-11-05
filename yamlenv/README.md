# yamlenv

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/yamlenv.svg)](https://pkg.go.dev/github.com/cplieger/go/yamlenv) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=yamlenv%2Fgo.mod)](https://github.com/cplieger/go/blob/main/yamlenv/go.mod)

yamlenv loads a YAML config file and fills `${VAR}` references from the environment after parsing, so a value cannot change the file's structure. It is for an app that reads a YAML config file and keeps its secrets in the environment.

Its one dependency is [go-yaml](https://github.com/yaml/go-yaml) v4. It is a module of its own, so a program that only needs envx links no YAML library. It needs Go 1.27.3 or later and is licensed under Apache-2.0.

```sh
go get github.com/cplieger/go/yamlenv@latest
```

The package documentation on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/yamlenv) has the full text of each symbol.

## How expansion works

A config file refers to an environment variable as `${VAR}`. yamlenv replaces the reference after the file is parsed, inside string values only, so the file holds the structure and the environment holds the secrets.

- Expansion runs on the parsed document. A value holding YAML syntax, such as a quote, a newline or `#`, cannot change the document's structure or cut the value short.
- Only the braced `${VAR}` form is expanded. An unbraced `$VAR` stays exactly as written, and so does a name your allow function rejects or a variable that is unset. None of them is blanked.
- Mapping keys and values that are not strings stay as written.
- Expansion runs once. A `${VAR}` that an expanded value brings in is neither expanded again nor reported as unresolved, so a password may contain `${`.
- An expanded value stays a string. `port: ${APP_PORT}` cannot fill an `int` field, even when the variable holds `8080`. Read a number, a bool or a duration from the environment into a string field and parse it yourself.
- A variable that is set but empty does replace its reference. Here set or unset is what counts, so an operator can blank a field on purpose.

Your allow function decides which names may expand. It is usually a one-line prefix check.

## Load

`Load(data, &cfg, allow, opts...)` runs the whole pipeline in a fixed order and sanitizes every error by default:

1. It parses the raw file into a YAML document and rejects input with more than one, so nothing below a stray `---` is dropped silently.
2. It checks the raw file for keys your config type does not declare.
3. It expands the allowed `${VAR}` references and decodes the result into `cfg`. `cfg` must be a non-nil pointer you fill with your defaults first. An empty file keeps them.
4. It rebuilds parse, unknown-key and decode errors so no expanded secret can appear in the message. `WithErrorPassthrough`, described below, is the one exception.

It also returns the allowed names the file references that were never set, in document order without repeats, so you can warn about them.

The key check in step 2 keeps only unknown-key findings. A custom `UnmarshalYAML` that rejects a `${VAR}` not yet expanded does not fail a valid config, and neither does a value of the wrong type before expansion. The decode in step 3 reports value errors once the references are expanded.

A rebuilt error starts with the position go-yaml reports, as `line N, column C:`. A parse error keeps only that position, which is where the scanner stopped: an unterminated quote opened on line 2 of a three-line file reports line 3, column 1, and an unknown anchor reports the alias's position. A decode error lists each problem as `decode errors: line 2, column 7: cannot construct !!str <redacted> into int`, with problems separated by semicolons. A duplicate key keeps the line of its first definition, an unknown key is `unknown configuration key <redacted>`, and any other problem is a fixed message after its position.

Your config type's own `UnmarshalYAML` error can be worded like one of go-yaml's. `Load` keeps a part of such a message only when your config type or the file vouches for it: the type after the word `into` must be a Go type your config type reaches, an echoed key must be a key the file has at that position, and a duplicate key's first line must hold a key. Anything else in that part becomes `<redacted>` or the fixed message.

Three options set its policy:

- `WithLookupEnv(lookup)` reads every variable through your function instead of the process environment, for a test or for a policy such as a name pattern. Unresolved names are the ones `lookup` did not find.
- `WithSanitizeOptions(...)` passes sanitizer options to every error `Load` rebuilds. `WithUnknownKeyEcho(true)` applies to the key check in step 2 only. A key reported by the decode in step 3, such as one your `UnmarshalYAML` found with go-yaml's `WithKnownFields`, can hold an expanded value, so it is always redacted.
- `WithErrorPassthrough(pred)` returns a decode error unchanged when `pred` reports true. Parse errors and unknown-key findings never reach `pred`.

Make `pred` claim your app's own error type, for example `errors.AsType[*myconfig.Error]`, whose text your app controls. `Load` hands `pred` an error from your config type's own `UnmarshalYAML` wrapped in `*UnmarshalerError`, and your type is reachable only through that wrap, so this predicate never claims an error the YAML library built. A predicate that claims every `*UnmarshalerError` passes more than your own text, in two cases:

- The YAML library relays an `encoding.TextUnmarshaler` error as written. A standard field type whose error repeats its input, such as `netip.Addr` or `time.Time`, therefore counts as your config type's error.
- An `UnmarshalYAML` that copies a YAML library error into its own error can carry a fragment of the value. `Load` withholds such a copy when it recognises the library's wording, described below, but it cannot vouch for text it does not recognise.

A config type's `UnmarshalYAML(*yaml.Node) error` must take go-yaml v4's `Node`. go-yaml v4 does not call a hook written against v3's `Node`; it reports it as a decode error, which `Load` sanitizes.

`UnmarshalerError` returns your unmarshaler's message unchanged, and `Unwrap` returns your original error. go-yaml keeps decoding after your `UnmarshalYAML` fails, so one load can report several problems. `Load` wraps the error only when every problem came from your own code; when several did, `Err` joins them in document order. When go-yaml found a problem too, the whole error is sanitized. An error your `UnmarshalYAML` returns counts as go-yaml's and is not wrapped when it wraps a go-yaml error, or when it carries go-yaml's error text anywhere, for example after `fmt.Errorf("...: %v", err)` or `errors.New(err.Error())`. Such a copy can hold go-yaml's excerpt of the value. go-yaml's text here means a message that starts with `yaml:` or has `: yaml:` inside it, or that contains any of `go-yaml load error`, `cannot construct`, `already set in type`, `not found in type`, `as a map key`, `anchor '`, `invalid array: want`, `cannot invoke v3 Node hooks`, `already defined at line`, `merge-key:`, `alias-data`, `event but got`, `that is not allowed in a YAML tag`, `(value:` or `input error:`. These are the fixed words of every go-yaml v4.0.0 message that quotes its input, and the module's tests fail on any other go-yaml version until the list is checked against it. Avoid those words in your own messages if you want them claimable.

## The steps on their own

The steps `Load` runs stay exported for a pipeline with a different policy, such as a probe that reads one setting and ignores the rest.

- `ExpandWith(root, allow, lookup)` expands references in place in a parsed go-yaml v4 `*yaml.Node`, reading each variable through `lookup`, and returns the allowed names `lookup` did not find. It changes values only, never their quoting style, so decode the result rather than writing it back out.
- `Expand(root, allow)` is `ExpandWith` reading the process environment.
- `SanitizeDecodeError(err, opts...)` rebuilds a go-yaml v4 `*yaml.LoadErrors` or `*yaml.LoadError` from parts that hold no value, such as positions, core YAML tags and type names. An error it does not recognize becomes a fixed message that withholds the details. The result never wraps the original. The unknown key's name is redacted unless you pass `WithUnknownKeyEcho(true)`, and when the option repeats, the last one wins. Called on its own it has no config type or file to check against, so it trusts go-yaml's wording: an error from your code worded like go-yaml's keeps its text after the last word `into`, and under the echo its key. Pass it go-yaml's errors only, or use `Load`.
- `CheckUnknownKeys(data, probe)` decodes the raw file into `probe`, a pointer to a fresh value of your config type, with go-yaml's `WithKnownFields` turned on. Its error is a `*yaml.LoadErrors`, or a single `*yaml.LoadError` when parsing fails or the load stops early. Either can contain text from the file, so pass it through `SanitizeDecodeError` before you log it.
- `CheckSingleDocument(data)` rejects input with more than one YAML document. Its only error is the fixed `ErrMultipleDocuments`, which is safe to log as it is.

Run both checks on the raw bytes before expansion. Expansion changes string values only, so it cannot change which keys exist or how many documents there are, and the positions then match the file the operator wrote. An alias used as a key also names the `${VAR}` as written, never its expanded value.
