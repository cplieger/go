// Package maskedtest finds a secret a printer or an answer could show.
// [Unmasked] checks the types that hold secrets (configuration, client,
// connection): it reports every field that can carry text and is neither a
// masked type nor a reviewed non-secret field. [SecretNamed] checks wire
// types by name, where every title and path is text: it reports a text field
// named like a credential. [Leak] checks a printer's output for a secret's
// text and its HMAC pad forms as the standard library's encoders write them
// into a log line, an error or a response. Import it only from _test.go
// files.
package maskedtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/go/masked"
)

// Allow is what [Unmasked] accepts without looking inside it.
type Allow struct {
	// Masked are the caller's own masked types, whose own tests pin that
	// they hold no text in clear. masked.Secret is always masked and need
	// not be listed.
	Masked []reflect.Type
	// Types are reviewed non-secret types, such as time.Time.
	Types []reflect.Type
	// Fields are reviewed non-secret fields, each written as its declaring
	// type's name, a dot and the field name: "example.com/app.User.Email". A
	// type is named as reflect.Type.String writes it, with each named type and
	// unexported field qualified by its import path instead of its package
	// name: "struct { T example.com/app.Tok }". Two function-local types of
	// one name in one package share a name.
	Fields []string
}

var secretType = reflect.TypeFor[masked.Secret]()

// Unmasked walks each root's type graph through struct fields (unexported
// included), pointers, slices, arrays and maps, and returns, sorted, one line
// per field that can carry text: a string, an integer slice or array (a
// []byte, an expanded key schedule), or an interface, whose dynamic value the
// type does not fix. Masked types, allowed types and allowed fields are not
// entered; an allow entry no root reached is reported, so none goes stale.
// Funcs, chans and unsafe pointers pass: every printer shows an address.
// Nil roots are skipped. A test fails when the result is not empty.
func Unmasked(a Allow, roots ...reflect.Type) []string {
	w := walker{
		allow: a,
		used:  make(map[string]bool),
		seen:  make(map[item]bool),
		found: make(map[string]bool),
	}
	for _, r := range roots {
		if r != nil {
			w.queue = append(w.queue, item{t: r, owner: typeName(r)})
		}
	}
	w.run()
	names := slices.Clone(a.Fields)
	for _, t := range slices.Concat(a.Masked, a.Types) {
		names = append(names, typeName(t))
	}
	for _, name := range names {
		if !w.used[name] {
			w.found["allow entry "+name+": reached by no root"] = true
		}
	}
	out := make([]string, 0, len(w.found))
	for line := range w.found {
		out = append(out, line)
	}
	slices.Sort(out)
	return out
}

// item is one type to check on behalf of owner, the struct field (or root)
// whose type it is or lies inside. A violation is reported against owner,
// so the verdict on a field does not depend on the route that reached it.
type item struct {
	t     reflect.Type
	owner string
}

type walker struct {
	used  map[string]bool
	seen  map[item]bool
	found map[string]bool
	queue []item
	allow Allow
}

// run drains the queue, checking each item once, which ends every type
// cycle. A struct is one item whatever its owner, because its fields'
// verdicts are its own; any other type is one item per owner, so two fields
// of one named slice type are both reported.
func (w *walker) run() {
	for len(w.queue) > 0 {
		it := w.queue[0]
		w.queue = w.queue[1:]
		if it.t.Kind() == reflect.Struct {
			it.owner = ""
		}
		if w.seen[it] {
			continue
		}
		w.seen[it] = true
		if slices.Contains(w.allow.Masked, it.t) || slices.Contains(w.allow.Types, it.t) {
			w.used[typeName(it.t)] = true
			continue
		}
		if it.t == secretType {
			continue
		}
		w.check(it)
	}
}

func (w *walker) fields(t reflect.Type) {
	for f := range t.Fields() {
		name := typeName(t) + "." + f.Name
		if slices.Contains(w.allow.Fields, name) {
			w.used[name] = true
			continue
		}
		w.queue = append(w.queue, item{t: f.Type, owner: name})
	}
}

func (w *walker) check(it item) {
	t := it.t
	switch t.Kind() {
	case reflect.Struct:
		w.fields(t)
	case reflect.String:
		w.report(it, "a string")
	case reflect.Interface:
		w.report(it, "an interface")
	case reflect.Slice, reflect.Array:
		if isInteger(t.Elem().Kind()) {
			w.report(it, "an integer sequence")
			return
		}
		w.queue = append(w.queue, item{t: t.Elem(), owner: it.owner})
	case reflect.Pointer:
		w.queue = append(w.queue, item{t: t.Elem(), owner: it.owner})
	case reflect.Map:
		w.queue = append(w.queue, item{t: t.Key(), owner: it.owner}, item{t: t.Elem(), owner: it.owner})
	default:
	}
}

func (w *walker) report(it item, what string) {
	w.found[it.owner+": holds "+what+" ("+it.t.String()+")"] = true
}

// typeName names t as [Allow.Fields] documents. reflect.Type.String uses the
// short package name, which two packages can share, and omits the package of
// an unnamed struct's unexported fields, which makes two such types distinct.
// Func, chan and interface types keep String: no field is reported inside
// one. The recursion ends because an unnamed type is never its own element.
func typeName(t reflect.Type) string {
	if t.Name() != "" {
		if t.PkgPath() == "" {
			return t.Name()
		}
		return t.PkgPath() + "." + t.Name()
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + typeName(t.Elem())
	case reflect.Slice:
		return "[]" + typeName(t.Elem())
	case reflect.Array:
		return "[" + strconv.Itoa(t.Len()) + "]" + typeName(t.Elem())
	case reflect.Map:
		return "map[" + typeName(t.Key()) + "]" + typeName(t.Elem())
	case reflect.Struct:
		fields := make([]string, 0, t.NumField())
		for f := range t.Fields() {
			field := typeName(f.Type)
			switch {
			case !f.IsExported():
				field = f.PkgPath + "." + f.Name + " " + field
			case !f.Anonymous:
				field = f.Name + " " + field
			default:
			}
			if f.Tag != "" {
				field += " " + strconv.Quote(string(f.Tag))
			}
			fields = append(fields, field)
		}
		if len(fields) == 0 {
			return "struct {}"
		}
		return "struct { " + strings.Join(fields, "; ") + " }"
	default:
		return t.String()
	}
}

func isInteger(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

// forms returns text as [Leak] lists it. Each quoted or bracketed form is
// kept without its quotes or brackets: that interior is also inside a
// longer value printed the same way.
func forms(text string) []string {
	b := []byte(text)
	h := hex.EncodeToString(b)
	out := []string{text, h, strings.ToUpper(h)}
	for _, verb := range []string{"%s", "%v", "%x", "%X"} {
		out = append(out, fmt.Sprintf(verb, text))
	}
	for _, verb := range []string{"%x", "%X"} {
		out = append(out, fmt.Sprintf(verb, b))
	}
	for _, w := range []string{
		strconv.Quote(text), strconv.QuoteToASCII(text), strconv.QuoteToGraphic(text),
		jsonString(text, true), jsonString(text, false),
	} {
		out = append(out, unwrap(w))
	}
	for _, verb := range []string{"%q", "%+q", "%#q", "%#v"} {
		out = append(out, unwrap(fmt.Sprintf(verb, text)))
	}
	for _, verb := range []string{"%v", "%d", "%#v"} {
		out = append(out, unwrap(fmt.Sprintf(verb, b)))
	}
	out = append(out, url.QueryEscape(text), url.PathEscape(text), (&url.URL{Path: text}).EscapedPath(),
		html.EscapeString(text), htmlText(text))
	// The padded encodings' output contains these runs.
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for offset := range 3 {
			encoded := enc.EncodeToString(append(make([]byte, offset), text...))
			// A character covers 6 bits: skip those that hold a prefix bit and
			// stop before the one that would hold a bit of what follows text.
			start, end := (8*offset+5)/6, 8*(offset+len(text))/6
			if start < end {
				out = append(out, encoded[start:end])
			}
		}
	}
	return out
}

// unwrap strips the quotes, backquotes or brackets a printer puts around a
// value: "x", `x`, [1 2] and []byte{0x1, 0x2}.
func unwrap(s string) string {
	for _, w := range [][2]string{{`"`, `"`}, {"`", "`"}, {"[]byte{", "}"}, {"[", "]"}} {
		if rest, ok := strings.CutPrefix(s, w[0]); ok {
			if inner, ok := strings.CutSuffix(rest, w[1]); ok {
				return inner
			}
		}
	}
	return s
}

// jsonString returns text as encoding/json writes it, quoted, with HTML
// escaping on (json.Marshal) or off (log/slog's JSON handler).
func jsonString(text string, escapeHTML bool) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(escapeHTML)
	// A string always encodes, and a strings.Builder never fails a write.
	_ = enc.Encode(text)
	return strings.TrimSuffix(b.String(), "\n")
}

var htmlPage = template.Must(template.New("text").Parse("{{.}}"))

// htmlText returns text as html/template writes it in HTML text, which also
// escapes + and NUL, unlike html.EscapeString.
func htmlText(text string) string {
	var b strings.Builder
	// A string always executes, and a strings.Builder never fails a write.
	_ = htmlPage.Execute(&b, text)
	return b.String()
}

// KeyForms returns the forms of key a printer can show: key itself, and
// key XOR 0x36 and XOR 0x5c, the inner and outer pads a crypto/hmac value
// caches (RFC 2104 section 2), each as [Leak] lists. A key longer than
// sha256.BlockSize also gets the pads of its SHA-256 hash, which
// HMAC-SHA-256 uses in its place.
func KeyForms(key string) []string {
	out := forms(key)
	padded := [][]byte{[]byte(key)}
	if len(key) > sha256.BlockSize {
		sum := sha256.Sum256([]byte(key))
		padded = append(padded, sum[:])
	}
	for _, k := range padded {
		for _, pad := range []byte{0x36, 0x5c} {
			b := make([]byte, len(k))
			for i := range k {
				b[i] = k[i] ^ pad
			}
			out = append(out, forms(string(b))...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Leak reports whether got holds text or a [KeyForms] pad form as each
// standard-library encoder whose output reaches a log, an error or a response
// writes a string or []byte holding it: fmt's string and []byte verbs, strconv
// quoting, encoding/json with HTML escaping on and off, hex in both cases, std,
// URL, raw std and raw URL base64 at each byte offset, net/url query and path
// escaping, html.EscapeString and html/template. A quoted or bracketed form is
// also searched bare. Any other encoder is the caller's to check. text must not
// be empty, and a text of a few bytes can match by chance.
func Leak(got, text string) bool {
	for _, f := range KeyForms(text) {
		if strings.Contains(got, f) {
			return true
		}
	}
	return false
}
