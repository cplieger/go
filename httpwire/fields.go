package httpwire

import (
	"cmp"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// The bound methods wiregen reads from source, each on the value receiver;
// the decode calls the same ones, so one method is both the schema keyword
// and the runtime check.
type (
	ranged   interface{ Range() (lo, hi int64) }
	maxBytes interface{ MaxBytes() int }
	counted  interface{ Count() (lo, hi int) }
	listed   interface{ MaxItems() int }
	// refuser names the field code a refused value of the type answers, in
	// place of the field-code map's.
	refuser interface{ Refusal() string }
)

// The parameter places the binder decodes, and the Endpoint fields they fill.
const (
	inPath      = "path"
	inQuery     = "query"
	inHeader    = "header"
	partParams  = "Params"
	partQuery   = "Query"
	partHeaders = "Headers"
)

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

// methodOf answers t's methods of interface I, every one taking no
// argument, when the zero value of t answers them: never for a pointer t,
// nor for a method promoted through a nil embedded pointer, either of which
// panics when called.
func methodOf[I any](t reflect.Type) (I, bool) {
	var zero I
	if t.Kind() == reflect.Pointer {
		return zero, false
	}
	v := reflect.Zero(t)
	m, ok := reflect.TypeAssert[I](v)
	if ok && embedsPointer(t) && !answers(v, reflect.TypeFor[I]()) {
		return zero, false
	}
	return m, ok
}

func embedsPointer(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for f := range t.Fields() {
		if f.Anonymous && (f.Type.Kind() == reflect.Pointer || embedsPointer(f.Type)) {
			return true
		}
	}
	return false
}

// answers reports whether v answers every method of i; a panic leaves ok
// false.
func answers(v reflect.Value, i reflect.Type) (ok bool) {
	defer func() { _ = recover() }()
	for m := range i.Methods() {
		v.MethodByName(m.Name).Call(nil)
	}
	return true
}

func isTextType(t reflect.Type) bool { return reflect.PointerTo(t).Implements(textUnmarshalerType) }

// inBinderSet reports whether a path or query value of type t parses from
// text: a TextUnmarshaler, a string type with MaxBytes, a signed integer
// type with Range, a uint64 or a bool.
func inBinderSet(t reflect.Type) bool {
	if isTextType(t) {
		return true
	}
	switch t.Kind() {
	case reflect.String:
		_, ok := methodOf[maxBytes](t)
		return ok
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		_, ok := methodOf[ranged](t)
		return ok
	case reflect.Uint64, reflect.Bool:
		return true
	}
	return false
}

// textField is one path or query field: its wire name (the JSON name, as
// wiregen writes the parameter) and where it sits in its struct.
type textField struct {
	typ      reflect.Type // the value type, under the one pointer a query field may have
	name     string
	index    []int
	pointer  bool
	optional bool // a pointer, omitempty or omitzero: a query may leave it out
	cursor   bool // a Page's Cursor, which only opening it checks
	list     bool // a query value repeated, one element per occurrence
}

// textFields lists struct t's fields by wire name, an embedded struct's
// fields as t's own, with every reason a field cannot be decoded.
func textFields(t reflect.Type) (fields []textField, problems []string) {
	if t.Kind() != reflect.Struct {
		return nil, []string{fmt.Sprintf("%s is not a struct", t)}
	}
	seen := map[string]bool{}
	var walk func(t reflect.Type, at []int, inPage bool)
	walk = func(t reflect.Type, at []int, inPage bool) {
		for f := range t.Fields() {
			index := append(append([]int(nil), at...), f.Index...)
			tf, embedded, problem := textFieldOf(&f, index)
			switch {
			case problem != "":
				problems = append(problems, problem)
			case embedded:
				walk(f.Type, index, f.Type == pageType)
			case tf == nil:
			case seen[tf.name]:
				problems = append(problems, fmt.Sprintf("field %q is declared twice", tf.name))
			default:
				tf.list = isList(tf.typ)
				if p := fieldTypeProblem(tf, f.Type); p != "" {
					problems = append(problems, p)
				}
				if tf.typ == cursorType && !inPage {
					problems = append(problems, fmt.Sprintf("field %q is an httpwire.Cursor, which only an embedded httpwire.Page carries", tf.name))
				}
				seen[tf.name] = true
				tf.cursor = tf.typ == cursorType
				fields = append(fields, *tf)
			}
		}
	}
	walk(t, nil, false)
	return fields, problems
}

// isList reports whether t is a slice type with MaxItems, which a query
// sends as one occurrence per element.
func isList(t reflect.Type) bool {
	_, ok := methodOf[listed](t)
	return ok && t.Kind() == reflect.Slice && !isTextType(t)
}

// fieldTypeProblem says why tf, declared as t, cannot be parsed from text.
func fieldTypeProblem(tf *textField, t reflect.Type) string {
	if !tf.list {
		if inBinderSet(tf.typ) {
			return ""
		}
		return fmt.Sprintf("field %q is %s, outside the types a path or query value parses into: "+
			"a TextUnmarshaler, a string type with MaxBytes, an integer type with Range, a uint64 or a bool, "+
			"or in a query a slice of one of them with MaxItems", tf.name, t)
	}
	m, _ := methodOf[listed](tf.typ)
	switch elem := tf.typ.Elem(); {
	case tf.pointer:
		return fmt.Sprintf("field %q is a query list behind a pointer; omitempty makes it optional", tf.name)
	case !inBinderSet(elem) || elem == cursorType:
		return fmt.Sprintf("field %q is a list of %s, outside the types a query value parses into", tf.name, elem)
	case m.MaxItems() < 1:
		return fmt.Sprintf("field %q's MaxItems is %d; a query list takes at least 1", tf.name, m.MaxItems())
	}
	return ""
}

// textFieldOf reads one struct field as encoding/json names it: nil for a
// field it skips, embedded for an untagged struct whose fields are its own.
func textFieldOf(f *reflect.StructField, index []int) (tf *textField, embedded bool, problem string) {
	tag := f.Tag.Get("json")
	name, opts, _ := strings.Cut(tag, ",")
	switch {
	case tag == "-":
		return nil, false, ""
	case f.Anonymous && name == "" && f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct:
		return nil, false, fmt.Sprintf("embeds %s; embed the struct itself", f.Type)
	case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
		return nil, true, ""
	case !f.IsExported():
		return nil, false, ""
	}
	tf = &textField{name: cmp.Or(name, f.Name), index: index, typ: f.Type}
	if tf.typ.Kind() == reflect.Pointer {
		tf.typ, tf.pointer = tf.typ.Elem(), true
	}
	tf.optional = tf.pointer || hasOption(opts, "omitempty") || hasOption(opts, "omitzero")
	return tf, false, ""
}

func hasOption(opts, want string) bool {
	for o := range strings.SplitSeq(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

// refusalProblems names each type reachable from t whose Refusal is not a
// field code the envelope can carry, or sits on the pointer receiver, where
// the decode never finds it.
func refusalProblem(t reflect.Type) string {
	if r, ok := methodOf[refuser](t); ok {
		if code := r.Refusal(); !isCode(code) {
			return fmt.Sprintf("type %s answers Refusal %q, which is not in the code grammar", t, code)
		}
	} else if reflect.PointerTo(t).Implements(reflect.TypeFor[refuser]()) {
		return fmt.Sprintf("type %s has Refusal only through a pointer; declare it on the value", t)
	}
	return ""
}

func refusalProblems(t reflect.Type, seen map[reflect.Type]bool) []string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || seen[t] {
		return nil
	}
	seen[t] = true
	var out []string
	if p := refusalProblem(t); p != "" {
		out = append(out, p)
	}
	switch t.Kind() {
	case reflect.Struct:
		for f := range t.Fields() {
			if f.IsExported() || f.Anonymous {
				out = append(out, refusalProblems(f.Type, seen)...)
			}
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		out = append(out, refusalProblems(t.Elem(), seen)...)
	}
	return out
}
