package httpwire

import (
	"cmp"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// parseText sets v, of a type inBinderSet admits, from text in its one
// canonical spelling, answering the field code of a refusal or "".
func parseText(v reflect.Value, text string) (code, message string) {
	t := v.Type()
	if m, ok := methodOf[maxBytes](t); ok && len(text) > m.MaxBytes() {
		return codeRange, "at most " + strconv.Itoa(m.MaxBytes()) + " bytes"
	}
	if !utf8.ValidString(text) {
		return codeMalformed, "not UTF-8 text"
	}
	if u, ok := reflect.TypeAssert[encoding.TextUnmarshaler](v.Addr()); ok {
		if u.UnmarshalText([]byte(text)) != nil {
			return codeMalformed, "not a value this field takes"
		}
		return "", ""
	}
	switch t.Kind() {
	case reflect.String:
		v.SetString(text)
	case reflect.Uint64:
		n, err := strconv.ParseUint(text, 10, 64)
		if code, message := numberProblem(err, text, strconv.FormatUint(n, 10)); code != "" {
			return code, message
		}
		v.SetUint(n)
	case reflect.Bool:
		if text != "true" && text != "false" {
			return codeMalformed, "true or false"
		}
		v.SetBool(text == "true")
	default:
		return setInt(v, text)
	}
	return "", ""
}

func setInt(v reflect.Value, text string) (code, message string) {
	n, err := strconv.ParseInt(text, 10, 64)
	if code, message := numberProblem(err, text, strconv.FormatInt(n, 10)); code != "" {
		return code, message
	}
	if lo, hi := rangeOf(v.Type()); v.OverflowInt(n) || n < lo || n > hi {
		return codeRange, fmt.Sprintf("an integer from %d to %d", lo, hi)
	}
	v.SetInt(n)
	return "", ""
}

// numberProblem judges a decimal parse: past its type is range, any other
// spelling than the canonical one malformed.
func numberProblem(err error, text, canonical string) (code, message string) {
	switch {
	case errors.Is(err, strconv.ErrRange):
		return codeRange, "past the integer type's range"
	case err != nil || text != canonical:
		return codeMalformed, "a decimal integer in its one spelling: no plus sign, leading zero or -0"
	}
	return "", ""
}

// rangeOf is the Range a signed integer type declares, else its own.
func rangeOf(t reflect.Type) (lo, hi int64) {
	if r, ok := methodOf[ranged](t); ok {
		return r.Range()
	}
	return math.MinInt64, math.MaxInt64
}

// paramValue is v as CheckParam takes it: the text for a TextUnmarshaler or
// a string, else the parsed number or boolean.
func paramValue(v reflect.Value, text string) any {
	if isTextType(v.Type()) {
		return text
	}
	switch v.Kind() {
	case reflect.Uint64:
		return v.Uint()
	case reflect.Bool:
		return v.Bool()
	case reflect.String:
		return text
	}
	return v.Int()
}

// refusalCode is the field code t names for its refused values, else code.
func refusalCode(t reflect.Type, code string) string {
	if r, ok := methodOf[refuser](t); ok {
		return r.Refusal()
	}
	return code
}

// decodePath parses ep's Params from r's path values; any value that does
// not parse or fails its schema names no resource.
func (a *API) decodePath(r *http.Request, ep *Endpoint) (any, bool) {
	pv := reflect.New(ep.Params).Elem()
	fields, _ := textFields(ep.Params)
	for _, f := range fields {
		text := r.PathValue(f.name)
		fv := pv.FieldByIndex(f.index)
		if code, _ := parseText(fv, text); code != "" {
			return nil, false
		}
		if a.schema.CheckParam(ep.Name, inPath, f.name, paramValue(fv, text)) != nil {
			return nil, false
		}
	}
	return pv.Interface(), true
}

// decodeQuery parses ep's Query from r's query string, answering one entry
// per refused key: a key missing when required, given twice, unknown, or
// whose value does not parse or fails its schema.
func (a *API) decodeQuery(r *http.Request, ep *Endpoint) (any, []FieldError) {
	vals, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, []FieldError{{Path: "", Code: codeMalformed, Message: "the query string does not parse"}}
	}
	qv := reflect.New(ep.Query).Elem()
	fields, _ := textFields(ep.Query)
	var c checker
	for _, f := range fields {
		if f.list {
			c.out = append(c.out, a.queryList(qv, ep, &f, vals[f.name])...)
			continue
		}
		code, message := a.queryField(qv, ep, &f, vals[f.name])
		if code != "" {
			c.add(f.name, refusalCode(f.typ, code), message)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(vals)) {
		if !slices.ContainsFunc(fields, func(f textField) bool { return f.name == k }) {
			c.add(k, codeUnknown, "not a parameter of this request")
		}
	}
	return qv.Interface(), c.out
}

func (a *API) queryField(qv reflect.Value, ep *Endpoint, f *textField, given []string) (code, message string) {
	switch {
	case len(given) == 0 && f.optional:
		return "", ""
	case len(given) == 0:
		return codeRequired, "required"
	case len(given) > 1:
		return codeMalformed, "given more than once"
	}
	fv := qv.FieldByIndex(f.index)
	if f.cursor {
		fv.SetString(given[0])
		return "", ""
	}
	if f.pointer {
		fv.Set(reflect.New(f.typ))
		fv = fv.Elem()
	}
	if code, message := parseText(fv, given[0]); code != "" {
		return code, message
	}
	if e := a.schema.CheckParam(ep.Name, inQuery, f.name, paramValue(fv, given[0])); e != nil {
		return e.Code, e.Message
	}
	return "", ""
}

// queryList parses every occurrence of a list field, in order, naming each
// refused element by its index.
func (a *API) queryList(qv reflect.Value, ep *Endpoint, f *textField, given []string) []FieldError {
	var c checker
	m, _ := methodOf[listed](f.typ)
	switch {
	case len(given) == 0 && f.optional:
		return nil
	case len(given) == 0:
		c.add(f.name, codeRequired, "required")
		return c.out
	case len(given) > m.MaxItems():
		// The envelope keeps the first maxFields entries.
		for i := m.MaxItems(); i < len(given) && len(c.out) < maxFields; i++ {
			c.add(f.name+"["+strconv.Itoa(i)+"]", codeRange, "at most "+strconv.Itoa(m.MaxItems())+" values")
		}
		return c.out
	}
	list := reflect.MakeSlice(f.typ, len(given), len(given))
	for i, text := range given {
		ev := list.Index(i)
		at := f.name + "[" + strconv.Itoa(i) + "]"
		if code, message := parseText(ev, text); code != "" {
			c.add(at, refusalCode(f.typ.Elem(), code), message)
			continue
		}
		c.out = append(c.out, a.schema.checkParamItem(ep.Name, f.name, at, paramValue(ev, text))...)
	}
	if len(c.out) == 0 {
		qv.FieldByIndex(f.index).Set(list)
	}
	return c.out
}

// jsonMediaType refuses a body r does not send as application/json in
// UTF-8, the one encoding RFC 8259 section 8.1 allows between systems.
func jsonMediaType(r *http.Request) *Error {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return &Error{Code: codeMediaType, Message: "the body is not sent as application/json"}
	}
	if cs, ok := params["charset"]; ok && !strings.EqualFold(cs, "utf-8") {
		return &Error{Code: codeMediaType, Message: "the body's charset is not utf-8"}
	}
	return nil
}

// readBody reads r's body up to limit bytes.
func readBody(r *http.Request, limit int) ([]byte, *Error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	switch {
	case err != nil:
		return nil, &Error{Code: codeBadBody, Message: "the body could not be read"}
	case len(b) > limit:
		return nil, tooLarge(limit)
	}
	return b, nil
}

// decodeBody reads ep's Request from b, the request's body: one JSON value
// with no unknown field, held to the bound methods of its types and to the
// row's request schema. The decoder's own error stays in a DEBUG line,
// since its text names Go types.
func (a *API) decodeBody(ctx context.Context, b []byte, ep *Endpoint) (any, *Error) {
	v := reflect.New(ep.Request)
	err := json.Unmarshal(b, v.Interface())
	w := boundWalk{refusals: map[string]string{}}
	if raw, ok := decodeJSON(b); ok {
		w.value(ep.Request, raw, "")
	}
	for _, e := range a.schema.CheckBody(ep.Name, 0, b) {
		w.add(e.Path, e.Code, e.Message)
	}
	if err != nil && len(w.out) == 0 {
		w.add(jsonErrorPath(err), codeMalformed, "does not decode as this request's body")
	}
	if len(w.out) > 0 {
		if err != nil {
			a.log().DebugContext(ctx, "httpwire: a request body did not decode", "row", ep.Name, "error", err)
		}
		for i := range w.out {
			if code, ok := w.refusals[w.out[i].Path]; ok {
				w.out[i].Code = code
			}
		}
		return nil, &Error{Code: codeBadBody, Message: "the body does not match this request", Fields: w.out}
	}
	return v.Elem().Interface(), nil
}

// jsonErrorPath is where encoding/json's refusal points, in the field-path
// form: items.1.key is items[1].key.
func jsonErrorPath(err error) string {
	te, ok := errors.AsType[*json.UnmarshalTypeError](err)
	if !ok || te.Field == "" {
		return ""
	}
	var out strings.Builder
	for i, seg := range strings.Split(te.Field, ".") {
		_, isIndex := strconv.Atoi(seg)
		switch {
		case isIndex == nil && i > 0:
			out.WriteString("[" + seg + "]")
		case i > 0:
			out.WriteString("." + seg)
		default:
			out.WriteString(seg)
		}
	}
	return out.String()
}

// boundWalk holds a body's JSON value, decoded with UseNumber, to the Go
// types it decodes into: each member a struct declares, by its exact name as
// the schema has it, each own decoder's verdict, and the bound methods. It
// notes the Refusal code each path's type names. It reads the JSON text, so
// a TextUnmarshaler's bound is held on the text it parses.
type boundWalk struct {
	refusals map[string]string
	checker
}

func (w *boundWalk) value(t reflect.Type, v any, path string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if r, ok := methodOf[refuser](t); ok {
		w.refusals[path] = r.Refusal()
	}
	switch {
	case reflect.PointerTo(t).Implements(jsonUnmarshalerType):
		w.ownJSON(t, v, path)
		return
	case v == nil:
		w.null(t, path)
		return
	case isTextType(t):
		w.text(t, v, path)
		return
	}
	switch t.Kind() {
	case reflect.String:
		w.text(t, v, path)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64:
		w.integer(t, v, path)
	case reflect.Slice, reflect.Array:
		w.list(t, v, path)
	case reflect.Map, reflect.Struct:
		if m, ok := v.(map[string]any); ok {
			w.object(t, m, path)
		}
	}
}

// ownJSON runs t's own JSON decoder on v, which encoding/json reports
// without a path.
func (w *boundWalk) ownJSON(t reflect.Type, v any, path string) {
	b, err := json.Marshal(v)
	u, _ := reflect.TypeAssert[json.Unmarshaler](reflect.New(t))
	if err == nil && u.UnmarshalJSON(b) != nil {
		w.add(path, codeMalformed, "not a value this field takes")
	}
}

// null refuses a null under a Count of at least one item.
func (w *boundWalk) null(t reflect.Type, path string) {
	if c, ok := methodOf[counted](t); ok && t.Kind() == reflect.Slice {
		if lo, _ := c.Count(); lo >= 1 {
			w.add(path, codeRequired, "at least "+strconv.Itoa(lo)+" items, never null")
		}
	}
}

func (w *boundWalk) text(t reflect.Type, v any, path string) {
	s, ok := v.(string)
	if !ok {
		return
	}
	if m, ok := methodOf[maxBytes](t); ok && len(s) > m.MaxBytes() {
		w.add(path, codeRange, "at most "+strconv.Itoa(m.MaxBytes())+" bytes of UTF-8")
		return
	}
	if u, ok := reflect.TypeAssert[encoding.TextUnmarshaler](reflect.New(t)); ok && u.UnmarshalText([]byte(s)) != nil {
		w.add(path, codeMalformed, "not a value this field takes")
	}
}

func (w *boundWalk) integer(t reflect.Type, v any, path string) {
	r, declared := methodOf[ranged](t)
	num, isNum := v.(json.Number)
	if !declared || !isNum || !isIntegerText(num) {
		return
	}
	lo, hi := r.Range()
	n, err := strconv.ParseInt(string(num), 10, 64)
	if err != nil || n < lo || n > hi {
		w.add(path, codeRange, fmt.Sprintf("an integer from %d to %d", lo, hi))
	}
}

func (w *boundWalk) list(t reflect.Type, v any, path string) {
	items, ok := v.([]any)
	if !ok {
		return
	}
	if c, ok := methodOf[counted](t); ok && t.Kind() == reflect.Slice {
		if lo, hi := c.Count(); len(items) < lo || len(items) > hi {
			w.add(path, codeRange, fmt.Sprintf("%d to %d items", lo, hi))
		}
	}
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
		return
	}
	for i, item := range items {
		w.value(t.Elem(), item, path+"["+strconv.Itoa(i)+"]")
	}
}

// object walks a map's members, or struct t's by their exact JSON name, an
// embedded struct's as t's own; a member the type lacks is the schema's to
// refuse.
func (w *boundWalk) object(t reflect.Type, m map[string]any, path string) {
	if t.Kind() == reflect.Map {
		for _, k := range sortedKeys(m) {
			w.value(t.Elem(), m[k], memberPath(path, k))
		}
		return
	}
	members := jsonMembers(t)
	for _, k := range sortedKeys(m) {
		if _, known := members[k]; !known {
			w.add(memberPath(path, k), codeUnknown, "unknown field")
		}
	}
	for _, name := range slices.Sorted(maps.Keys(members)) {
		ft, at := members[name], memberPath(path, name)
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if r, ok := methodOf[refuser](ft); ok {
			w.refusals[at] = r.Refusal()
		}
		if v, present := m[name]; present {
			w.value(ft, v, at)
		}
	}
}

// jsonMembers is struct t's fields by JSON name, as encoding/json reads
// them: a field of t itself wins over one an embedded struct brings.
func jsonMembers(t reflect.Type) map[string]reflect.Type {
	out, embedded := ownMembers(t)
	for _, et := range embedded {
		for name, ft := range jsonMembers(et) {
			if _, taken := out[name]; !taken {
				out[name] = ft
			}
		}
	}
	return out
}

// ownMembers is struct t's own fields by JSON name, and the structs it
// embeds untagged.
func ownMembers(t reflect.Type) (map[string]reflect.Type, []reflect.Type) {
	out := map[string]reflect.Type{}
	var embedded []reflect.Type
	for f := range t.Fields() {
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		ft := f.Type
		if f.Anonymous && name == "" && ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case tag == "-":
		case f.Anonymous && name == "" && ft.Kind() == reflect.Struct:
			embedded = append(embedded, ft)
		case f.IsExported():
			out[cmp.Or(name, f.Name)] = f.Type
		}
	}
	return out, embedded
}
