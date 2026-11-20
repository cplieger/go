package maskedtest

import (
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// credentialName is the one pattern every app's wire check uses.
var credentialName = regexp.MustCompile(`key|token|secret|password|passkey`)

// NameAllow is what [SecretNamed] accepts.
type NameAllow struct {
	// Fields are reviewed credential-named text fields, each mapped to the
	// reason it is allowed: it carries no credential, or one shown once on
	// purpose. An entry is stale when it names no credential-named text field
	// a root reaches.
	Fields map[Field]string
	// Enums are string types whose values are a fixed set, not free text.
	// An enum is stale when it excludes no credential-named field a root
	// reaches.
	Enums []reflect.Type
}

// SecretNamed follows encoding/json's field names, tags and embedding
// through each root's types and returns, sorted, one line per field whose
// JSON name (its Go name untagged), lowercased, contains key, token, secret,
// password or passkey, and that can hold text: a string not in a.Enums, a
// []byte or an interface, directly or in a container. A struct field is
// judged by its own fields, so a type with its own MarshalJSON or MarshalText
// needs its own test. Nil roots are skipped. A stale [NameAllow] entry or one
// with no reason is reported. A test fails when the result is not empty.
func SecretNamed(a NameAllow, roots ...reflect.Type) []string {
	n := namer{
		allow:  a,
		walked: make(map[reflect.Type]bool),
		fields: make(map[Field]bool),
		enums:  make(map[reflect.Type]bool),
		found:  make(map[finding]bool),
	}
	for _, r := range roots {
		if r != nil {
			n.walk(r)
		}
	}
	for f, reason := range a.Fields {
		if strings.TrimSpace(reason) == "" {
			n.found[finding{f, "allow entry " + fieldLabel(f) + ": has no reason"}] = true
		}
		if !n.fields[f] {
			n.found[finding{f, "allow entry " + fieldLabel(f) + ": names no credential-named text field a root reaches"}] = true
		}
	}
	for _, e := range a.Enums {
		if !n.enums[e] {
			n.found[finding{e, "allow entry " + typeName(e) + ": excludes no credential-named field a root reaches"}] = true
		}
	}
	return lines(n.found)
}

type namer struct {
	walked map[reflect.Type]bool
	fields map[Field]bool
	enums  map[reflect.Type]bool
	found  map[finding]bool
	allow  NameAllow
}

// walk visits each type once per call, which ends every type cycle, through
// a struct or through containers alone (type loop map[*loop]int).
func (n *namer) walk(t reflect.Type) {
	if n.walked[t] {
		return
	}
	n.walked[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		n.walk(t.Elem())
	case reflect.Map:
		n.walk(t.Key())
		n.walk(t.Elem())
	case reflect.Struct:
		for f := range t.Fields() {
			n.field(t, &f)
		}
	default:
	}
}

func (n *namer) field(t reflect.Type, f *reflect.StructField) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return
	}
	name, _, _ := strings.Cut(tag, ",")
	if f.Anonymous && name == "" && indirect(f.Type).Kind() == reflect.Struct {
		// encoding/json promotes an untagged embedded struct's fields,
		// exported or not, into the outer object.
		n.walk(f.Type)
		return
	}
	if !f.IsExported() {
		return
	}
	if name == "" {
		name = f.Name
	}
	// The name goes first: holdsText marks an enum reached, and only a
	// credential-named field's exclusion keeps an enum entry from going stale.
	if credentialName.MatchString(strings.ToLower(name)) && n.holdsText(f.Type, make(map[reflect.Type]bool)) {
		field := Field{In: t, Name: f.Name}
		if _, ok := n.allow.Fields[field]; ok {
			n.fields[field] = true
		} else {
			n.found[finding{field, fieldLabel(field) + ": json name " + strconv.Quote(name) + " holds text (" + f.Type.String() + ")"}] = true
		}
	}
	n.walk(f.Type)
}

// holdsText visits a map's key and value both, so every enum it passes is
// marked reached; through ends a cycle of containers.
func (n *namer) holdsText(t reflect.Type, through map[reflect.Type]bool) bool {
	if through[t] {
		return false
	}
	through[t] = true
	switch t.Kind() {
	case reflect.String:
		if slices.Contains(n.allow.Enums, t) {
			n.enums[t] = true
			return false
		}
		return true
	case reflect.Interface:
		// encoding/json writes whatever value the interface holds.
		return true
	case reflect.Slice:
		// encoding/json writes a []byte as a base64 string.
		return t.Elem().Kind() == reflect.Uint8 || n.holdsText(t.Elem(), through)
	case reflect.Pointer, reflect.Array:
		return n.holdsText(t.Elem(), through)
	case reflect.Map:
		key := n.holdsText(t.Key(), through)
		return n.holdsText(t.Elem(), through) || key
	default:
		return false
	}
}

func indirect(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}
