# YAML config files with yamlenv

This page covers the yamlenv module, for a developer whose app reads a YAML config file and keeps its secrets in the environment. yamlenv is its own Go module with its own version tags. It is the only part of envx that needs a YAML library, [go-yaml](https://github.com/yaml/go-yaml) v3. Importing plain envx never links it.

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
- A variable that is set but empty does replace its reference. Here set or unset is what counts, unlike the getters, so an operator can blank a field on purpose.

Your allow function decides which names may expand. It is usually a one-line prefix check.

## Load

`Load(data, &cfg, allow, opts...)` runs the whole pipeline in a fixed order and sanitizes every error by default:

1. It rejects input with more than one YAML document, so nothing below a stray `---` is dropped silently.
2. It parses the raw file into a YAML document.
3. It checks the raw file for keys your config type does not declare.
4. It expands the allowed `${VAR}` references and decodes the result into `cfg`. `cfg` must be a non-nil pointer you fill with your defaults first. An empty file keeps them.
5. It rebuilds parse, unknown-key and decode errors so no expanded secret can appear in the message. `WithErrorPassthrough`, described below, is the one exception.

It also returns the allowed names the file references that were never set, in document order without repeats, so you can warn about them.

The key check in step 3 keeps only unknown-key findings. A custom `UnmarshalYAML` that rejects a `${VAR}` not yet expanded does not fail a valid config, and neither does a value of the wrong type before expansion. The decode in step 4 reports value errors once the references are expanded. When such a custom `UnmarshalYAML` stops the check, unknown keys in that document go undetected.

Three options set its policy:

- `WithLookupEnv(lookup)` reads every variable through your function instead of the process environment, for a test or for a policy such as a name pattern. Unresolved names are the ones `lookup` did not find.
- `WithSanitizeOptions(...)` passes sanitizer options, such as `WithUnknownKeyEcho(true)`, to every error `Load` rebuilds.
- `WithErrorPassthrough(pred)` returns a decode error unchanged when `pred` reports true. An error from your config type's own `UnmarshalYAML` reaches `pred` wrapped in `*UnmarshalerError`, so the recommended predicate is an `errors.As` check for that type. The YAML library never wraps its own errors in that type, so this predicate cannot let one of them through. Parse errors and unknown-key findings never reach `pred`.

The passthrough has one limit. The YAML library relays an `encoding.TextUnmarshaler` error as written. A standard field type whose error repeats its input, such as `netip.Addr` or `time.Time`, therefore counts as your own error and passes through unchanged.

`UnmarshalerError` returns your unmarshaler's message unchanged, and `Unwrap` returns your original error. A custom `UnmarshalYAML` error that itself starts with `yaml:` cannot be told apart from yaml.v3's own and is not wrapped, so do not start your error messages that way.

## The steps on their own

The steps `Load` runs stay exported for a pipeline with a different policy, such as a probe that reads one setting and ignores the rest.

- `ExpandWith(root, allow, lookup)` expands references in place in a parsed `*yaml.Node`, reading each variable through `lookup`, and returns the allowed names `lookup` did not find. It changes values only, never their quoting style, so decode the result rather than writing it back out.
- `Expand(root, allow)` is `ExpandWith` reading the process environment.
- `SanitizeDecodeError(err, opts...)` rebuilds a yaml.v3 parse or decode error from parts that hold no value, such as line numbers, tags and type names. An error it does not recognize becomes a fixed message that withholds the details. The result never wraps the original. The unknown key's name is redacted unless you pass `WithUnknownKeyEcho(true)`, and when the option repeats, the last one wins.
- `CheckUnknownKeys(data, probe)` decodes the raw file into `probe`, a pointer to a fresh value of your config type, with yaml.v3's `KnownFields(true)` turned on. Its error can contain text from the file, so pass it through `SanitizeDecodeError` before you log it.
- `CheckSingleDocument(data)` rejects input with more than one YAML document. Its only error is the fixed `ErrMultipleDocuments`, which is safe to log as it is.

Run both checks on the raw bytes before expansion. Expansion changes string values only, so it cannot change which keys exist or how many documents there are, and the line numbers then match the file the operator wrote.
