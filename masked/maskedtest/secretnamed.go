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
	// Fields are reviewed credential-named text fields, written as in
	// [Allow.Fields] ("app.Copy.ReleaseKey"), each mapped to the reason it
	// is allowed: it carries no credential, or one shown once on purpose.
	Fields map[string]string
	// Enums are string types whose values are a fixed set, not free text.
	Enums []reflect.Type
}

// SecretNamed walks each root's type graph as encoding/json does and
// returns, sorted, one line per field whose JSON name (its Go name when
// untagged), lowercased, contains key, token, secret, password or passkey,
// and whose type can hold text: a string that is none of a.Enums, or a
// []byte, directly or through a pointer, slice, array or map. A struct field
// is judged by its own fields. Nil roots are skipped. An allow entry with
// no reason, naming no such field a root reaches, or an enum no field
// reaches is reported. A test fails when the result is not empty.
func SecretNamed(a NameAllow, roots ...reflect.Type) []string {
	n := namer{
		allow:  a,
		seen:   make(map[reflect.Type]bool),
		fields: make(map[string]bool),
		enums:  make(map[reflect.Type]bool),
		found:  make(map[string]bool),
	}
	for _, r := range roots {
		if r != nil {
			n.walk(r)
		}
	}
	for id, reason := range a.Fields {
		if strings.TrimSpace(reason) == "" {
			n.found["allow entry "+id+": has no reason"] = true
		}
		if !n.fields[id] {
			n.found["allow entry "+id+": names no credential-named text field a root reaches"] = true
		}
	}
	for _, e := range a.Enums {
		if !n.enums[e] {
			n.found["allow entry "+e.String()+": reached by no root"] = true
		}
	}
	out := make([]string, 0, len(n.found))
	for line := range n.found {
		out = append(out, line)
	}
	slices.Sort(out)
	return out
}

type namer struct {
	seen   map[reflect.Type]bool
	fields map[string]bool
	enums  map[reflect.Type]bool
	found  map[string]bool
	allow  NameAllow
}

// walk judges each struct once, which ends every type cycle through a
// struct; through records the containers on the way to one, which ends a
// cycle of containers (type chain []map[string]chain).
func (n *namer) walk(t reflect.Type) {
	through := make(map[reflect.Type]bool)
	for !through[t] {
		through[t] = true
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			t = t.Elem()
		case reflect.Map:
			n.walk(t.Key())
			t = t.Elem()
		case reflect.Struct:
			n.fieldsOf(t)
			return
		default:
			return
		}
	}
}

func (n *namer) fieldsOf(t reflect.Type) {
	if n.seen[t] {
		return
	}
	n.seen[t] = true
	for f := range t.Fields() {
		n.field(t, &f)
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
	if n.holdsText(f.Type, make(map[reflect.Type]bool)) && credentialName.MatchString(strings.ToLower(name)) {
		id := t.String() + "." + f.Name
		if _, ok := n.allow.Fields[id]; ok {
			n.fields[id] = true
		} else {
			n.found[id+": json name "+strconv.Quote(name)+" holds text ("+f.Type.String()+")"] = true
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
