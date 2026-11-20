package httpwire

import (
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"reflect"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
)

// ownedHeaders are the request headers no Headers field may name, in
// canonical form: the connection-specific ones (RFC 9110 section 7.6.1,
// and net/http/httputil's hop-by-hop list), the body's framing and type,
// the credentials, and the ones the binder reads itself.
var ownedHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
	"Transfer-Encoding", "Upgrade", "Content-Type", "Content-Length", "Host", "Authorization", "Cookie",
	"Idempotency-Key", "If-Match", headerWireVersion, textproto.CanonicalMIMEHeaderKey(httpserver.HeaderRequestID),
}

// CheckHeaders answers every reason Derive refuses t as a table row's
// Headers under a table whose ClientHeader is clientHeader, joined with
// errors.Join, or nil.
func CheckHeaders(t reflect.Type, clientHeader string) error {
	if t == nil {
		return errors.New("httpwire: CheckHeaders of a nil type")
	}
	var errs []error
	for _, p := range headerProblems(t, clientHeader, true, map[reflect.Type]bool{}) {
		errs = append(errs, errors.New(p))
	}
	return errors.Join(errs...)
}

// headerProblems is every reason t cannot be a row's Headers: its names,
// and on a table row (types) its value types and their Refusals, seen
// shared with the row's other parts so a type is named once.
func headerProblems(t reflect.Type, clientHeader string, types bool, seen map[reflect.Type]bool) []string {
	_, problems := headerFields(t, clientHeader, types)
	if types {
		problems = append(problems, refusalProblems(t, seen)...)
	}
	return problems
}

// headerFields lists struct t's fields, each named by its header tag in
// canonical form, with every reason one cannot be decoded. On a mount row
// (types false) only the names are checked, since its handler parses the
// values itself.
func headerFields(t reflect.Type, clientHeader string, types bool) (fields []textField, problems []string) {
	if t.Kind() != reflect.Struct {
		return nil, []string{fmt.Sprintf("Headers %s is not a struct", t)}
	}
	seen := map[string]bool{}
	for f := range t.Fields() {
		tf, problem := headerFieldOf(&f, clientHeader)
		switch {
		case problem != "":
			problems = append(problems, problem)
			continue
		case seen[tf.name]:
			problems = append(problems, fmt.Sprintf("Headers field %q is declared twice", tf.name))
			continue
		}
		seen[tf.name] = true
		if p := headerTypeProblem(tf, f.Type); types && p != "" {
			problems = append(problems, p)
		}
		fields = append(fields, *tf)
	}
	return fields, problems
}

func headerFieldOf(f *reflect.StructField, clientHeader string) (tf *textField, problem string) {
	tag := f.Tag.Get("header")
	name, opts, _ := strings.Cut(tag, ",")
	canonical := textproto.CanonicalMIMEHeaderKey(name)
	switch {
	case !f.IsExported():
		return nil, fmt.Sprintf("Headers field %s is unexported, so the binder cannot set it", f.Name)
	case name == "":
		return nil, fmt.Sprintf("Headers field %s names no header", f.Name)
	case !isToken(name):
		return nil, fmt.Sprintf("Headers field %s names %q, which is not a header name", f.Name, name)
	case slices.Contains(ownedHeaders, canonical):
		return nil, fmt.Sprintf("Headers field %q names a header httpwire or the platform owns", canonical)
	case clientHeader != "" && canonical == textproto.CanonicalMIMEHeaderKey(clientHeader):
		return nil, fmt.Sprintf("Headers field %q names the table's ClientHeader", canonical)
	}
	tf = &textField{name: canonical, index: f.Index, typ: f.Type}
	if tf.typ.Kind() == reflect.Pointer {
		tf.typ, tf.pointer = tf.typ.Elem(), true
	}
	tf.optional = tf.pointer || hasOption(opts, "omitempty") || hasOption(opts, "omitzero")
	return tf, ""
}

// headerTypeProblem says why tf, declared as t, cannot be parsed from one
// header value.
func headerTypeProblem(tf *textField, t reflect.Type) string {
	switch {
	case tf.typ == cursorType:
		return fmt.Sprintf("Headers field %q is an httpwire.Cursor, which only an embedded httpwire.Page carries", tf.name)
	case isList(tf.typ):
		return fmt.Sprintf("Headers field %q is a list; a header takes one value", tf.name)
	case !inBinderSet(tf.typ):
		return fmt.Sprintf("Headers field %q is %s, outside the types a header value parses into: "+
			"a TextUnmarshaler, a string type with MaxBytes, an integer type with Range, a uint64 or a bool", tf.name, t)
	}
	return ""
}

// decodeHeaders parses ep's Headers from r's header, answering one entry
// per refused header: missing when required, given twice, or whose value
// does not parse or fails its schema. A header no field names is ignored.
func (a *API) decodeHeaders(r *http.Request, ep *Endpoint) (any, []FieldError) {
	hv := reflect.New(ep.Headers).Elem()
	fields, _ := headerFields(ep.Headers, a.table.ClientHeader, true)
	var c checker
	for _, f := range fields {
		if code, message := a.textValue(hv, ep, inHeader, &f, r.Header.Values(f.name)); code != "" {
			c.add(f.name, refusalCode(f.typ, code), message)
		}
	}
	return hv.Interface(), c.out
}
