// Package maskedtest finds a secret a printer or an answer could show.
// [Unmasked] checks the types that hold secrets (configuration, client,
// connection): it reports every field that can carry text and is neither a
// masked type nor a reviewed non-secret field. [SecretNamed] checks wire
// types by name, where every title and path is text: it reports a text field
// named like a credential. [Leak] checks a printer's output for a secret's
// text and its HMAC pad forms as a listed set of standard-library encoders
// writes them. Import it only from _test.go files.
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
	// Fields are reviewed non-secret fields.
	Fields []Field
}

// Field names one struct field by its declaring type and its Go field name,
// for an embedded field the name Go gives it: the embedded type's name, or
// the alias's for a type embedded through an alias. An allow entry matches
// by type identity and name, so it silences no other field, however alike
// two types print, and a renamed field leaves it stale:
//
//	maskedtest.Field{In: reflect.TypeFor[app.Config](), Name: "Email"}
type Field struct {
	In   reflect.Type
	Name string
}

var secretType = reflect.TypeFor[masked.Secret]()

// Unmasked walks each root's type graph through struct fields (unexported
// included), pointers, slices, arrays and maps, and returns, sorted, one
// readable line per field that can carry text: a string, an integer slice or
// array (a []byte, a key schedule), or an interface. Masked types, allowed
// types and allowed fields are not entered; an allow entry no root reached
// is reported. Funcs, chans and unsafe pointers pass: every printer shows an
// address. Nil roots are skipped. A test fails when the result is not empty.
func Unmasked(a Allow, roots ...reflect.Type) []string {
	w := walker{
		allow:      a,
		usedTypes:  make(map[reflect.Type]bool),
		usedFields: make(map[Field]bool),
		seen:       make(map[item]bool),
		found:      make(map[finding]bool),
	}
	for _, r := range roots {
		if r != nil {
			w.queue = append(w.queue, item{t: r, owner: Field{In: r}})
		}
	}
	w.run()
	for _, f := range a.Fields {
		if !w.usedFields[f] {
			w.found[finding{f, "allow entry " + fieldLabel(f) + ": reached by no root"}] = true
		}
	}
	for _, t := range slices.Concat(a.Masked, a.Types) {
		if !w.usedTypes[t] {
			w.found[finding{t, "allow entry " + typeName(t) + ": reached by no root"}] = true
		}
	}
	return lines(w.found)
}

// finding is one report line about subject, a [Field] or a reflect.Type, so
// two subjects whose lines read alike are both reported.
type finding struct {
	subject any
	line    string
}

func lines(found map[finding]bool) []string {
	out := make([]string, 0, len(found))
	for f := range found {
		out = append(out, f.line)
	}
	slices.Sort(out)
	return out
}

// fieldLabel names f in a report line; a root's owner has no Name.
func fieldLabel(f Field) string {
	if f.Name == "" {
		return typeName(f.In)
	}
	return typeName(f.In) + "." + f.Name
}

// item is one type to check on behalf of owner, the struct field (or root)
// whose type it is or lies inside. A violation is reported against owner,
// so the verdict on a field does not depend on the route that reached it.
type item struct {
	t     reflect.Type
	owner Field
}

type walker struct {
	usedTypes  map[reflect.Type]bool
	usedFields map[Field]bool
	seen       map[item]bool
	found      map[finding]bool
	queue      []item
	allow      Allow
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
			it.owner = Field{}
		}
		if w.seen[it] {
			continue
		}
		w.seen[it] = true
		if slices.Contains(w.allow.Masked, it.t) || slices.Contains(w.allow.Types, it.t) {
			w.usedTypes[it.t] = true
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
		field := Field{In: t, Name: f.Name}
		if slices.Contains(w.allow.Fields, field) {
			w.usedFields[field] = true
			continue
		}
		w.queue = append(w.queue, item{t: f.Type, owner: field})
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
	w.found[finding{it.owner, fieldLabel(it.owner) + ": holds " + what + " (" + it.t.String() + ")"}] = true
}

// typeName names t for a report line, which a person reads: as
// reflect.Type.String writes it, with each named type, each unexported field
// that is not embedded and each unexported method qualified by its import
// path, since two packages can share a short name. It owes no uniqueness (an
// embedded predeclared type or alias, and two function-local types of one
// name, read alike): an allow entry matches by
// identity. The recursion ends because an unnamed type is never its own
// element.
func typeName(t reflect.Type) string {
	if t == nil {
		return "<nil>"
	}
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
		return structName(t)
	case reflect.Chan:
		return chanName(t)
	case reflect.Func:
		return "func" + signature(t)
	case reflect.Interface:
		return interfaceName(t)
	default:
		return t.String()
	}
}

func structName(t reflect.Type) string {
	fields := make([]string, 0, t.NumField())
	for f := range t.Fields() {
		field := typeName(f.Type)
		switch {
		case f.Anonymous:
		case !f.IsExported():
			field = f.PkgPath + "." + f.Name + " " + field
		default:
			field = f.Name + " " + field
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
}

func interfaceName(t reflect.Type) string {
	methods := make([]string, 0, t.NumMethod())
	for m := range t.Methods() {
		name := m.Name
		if m.PkgPath != "" {
			name = m.PkgPath + "." + name
		}
		methods = append(methods, name+signature(m.Type))
	}
	if len(methods) == 0 {
		return "interface {}"
	}
	return "interface { " + strings.Join(methods, "; ") + " }"
}

func chanName(t reflect.Type) string {
	elem := typeName(t.Elem())
	switch t.ChanDir() {
	case reflect.RecvDir:
		return "<-chan " + elem
	case reflect.SendDir:
		return "chan<- " + elem
	default:
		// reflect brackets a receive-only element: chan <-chan T would parse
		// as chan<- (chan T).
		if e := t.Elem(); e.Name() == "" && e.Kind() == reflect.Chan && e.ChanDir() == reflect.RecvDir {
			elem = "(" + elem + ")"
		}
		return "chan " + elem
	}
}

// signature writes a func type's parameters and results as reflect does,
// after the func keyword or a method name.
func signature(t reflect.Type) string {
	in := make([]string, 0, t.NumIn())
	for i := range t.NumIn() {
		if t.IsVariadic() && i == t.NumIn()-1 {
			in = append(in, "..."+typeName(t.In(i).Elem()))
			continue
		}
		in = append(in, typeName(t.In(i)))
	}
	out := make([]string, 0, t.NumOut())
	for o := range t.Outs() {
		out = append(out, typeName(o))
	}
	sig := "(" + strings.Join(in, ", ") + ")"
	switch len(out) {
	case 0:
		return sig
	case 1:
		return sig + " " + out[0]
	default:
		return sig + " (" + strings.Join(out, ", ") + ")"
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
		url.User(text).String(), (&url.URL{Fragment: text}).EscapedFragment(), html.EscapeString(text))
	for _, page := range htmlPages {
		out = append(out, page.render(text))
	}
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

// htmlPage writes a text into one html/template context between prefix and
// suffix.
type htmlPage struct {
	tmpl           *template.Template
	prefix, suffix string
}

func newHTMLPage(prefix, suffix string) htmlPage {
	return htmlPage{template.Must(template.New("page").Parse(prefix + "{{.}}" + suffix)), prefix, suffix}
}

// htmlPages are html/template's HTML text, which escapes + and NUL unlike
// html.EscapeString, and its URL attribute before and after a ? or #, which
// write %xx in lower case. The URL contexts follow a / or a ?, since a URL's
// first characters are also filtered for a scheme.
var htmlPages = []htmlPage{
	newHTMLPage("", ""),
	newHTMLPage(`<a href="/`, `">`),
	newHTMLPage(`<a href="?`, `">`),
}

// render returns text as p's context writes it.
func (p htmlPage) render(text string) string {
	var b strings.Builder
	// A string always executes, and a strings.Builder never fails a write.
	_ = p.tmpl.Execute(&b, text)
	out := strings.TrimPrefix(b.String(), p.prefix)
	return strings.TrimSuffix(out, p.suffix)
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

// Leak reports whether got holds text or a [KeyForms] pad form as fmt's string
// and []byte verbs, strconv quoting, encoding/json with and without HTML
// escaping, hex, base64 (std, URL, raw std, raw URL) at any byte offset, net/url
// query, path, userinfo and fragment escaping, html.EscapeString, or
// html/template in HTML text or a URL attribute writes it, a quoted or
// bracketed form also bare. Leak checks these forms only. For any other
// encoding, call Leak with the encoded text, such as base32's. text must not be
// empty, and a text of a few bytes can match by chance.
func Leak(got, text string) bool {
	for _, f := range KeyForms(text) {
		if strings.Contains(got, f) {
			return true
		}
	}
	return false
}
