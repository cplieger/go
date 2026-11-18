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
	// refuser names the field code a refused value of the type answers, in
	// place of the field-code map's.
	refuser interface{ Refusal() string }
)

// The parameter places the binder decodes, and the Endpoint fields they fill.
const (
	inPath     = "path"
	inQuery    = "query"
	inHeader   = "header"
	partParams = "Params"
	partQuery  = "Query"
)

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

// methodOf answers t's method of interface I when t declares it on its value
// receiver. t must not be a pointer: a value method called through a nil
// pointer panics.
func methodOf[I any](t reflect.Type) (I, bool) {
	if t.Kind() == reflect.Pointer {
		var zero I
		return zero, false
	}
	return reflect.TypeAssert[I](reflect.Zero(t))
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
}

// textFields lists struct t's fields by wire name, an embedded struct's
// fields as t's own, with every reason a field cannot be decoded.
func textFields(t reflect.Type) (fields []textField, problems []string) {
	if t.Kind() != reflect.Struct {
		return nil, []string{fmt.Sprintf("%s is not a struct", t)}
	}
	seen := map[string]bool{}
	var walk func(t reflect.Type, at []int)
	walk = func(t reflect.Type, at []int) {
		for f := range t.Fields() {
			index := append(append([]int(nil), at...), f.Index...)
			tf, embedded, problem := textFieldOf(&f, index)
			switch {
			case problem != "":
				problems = append(problems, problem)
			case embedded:
				walk(f.Type, index)
			case tf == nil:
			case seen[tf.name]:
				problems = append(problems, fmt.Sprintf("field %q is declared twice", tf.name))
			default:
				if !inBinderSet(tf.typ) {
					problems = append(problems, fmt.Sprintf("field %q is %s, outside the types a path or query value parses into: "+
						"a TextUnmarshaler, a string type with MaxBytes, an integer type with Range, a uint64 or a bool", tf.name, f.Type))
				}
				seen[tf.name] = true
				fields = append(fields, *tf)
			}
		}
	}
	walk(t, nil)
	return fields, problems
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
		return fmt.Sprintf("type %s declares Refusal on its pointer receiver; declare it on the value", t)
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
