package wiregen

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Union declares Type, a registered struct, a union tagged by its Tag field,
// named by wire name. Each Cases key is a tag value and lists the wire names
// of the other fields that case allows; a listed field keeps its own presence
// rule. The TS type is a discriminated union, the decoder refuses a field
// another case owns, and OpenAPI gets a oneOf with a discriminator whose cases
// admit no other property.
//
// Payload, when set, names a json.RawMessage field of Type whose type the tag
// selects: PayloadTypes maps every Cases key to the type that field decodes as
// in that case, or to the zero Type for a case that carries no payload. The
// payload field is listed in no case; it belongs to each case with a payload
// type, keeps its own omit options there, and is refused in the others. A
// json.RawMessage can always be null on the wire, so the typed payload's
// TypeScript member is optional, its schemas are nullable, and a decoder
// reads null as absent. Each payload type is a struct, as every registered
// type is, and joins the registry's types.
type Union struct {
	Cases        map[string][]string
	PayloadTypes map[string]Type
	Type         Type
	Tag          string
	Payload      string
}

// unionInfo is one resolved Union: its tag's and payload's wire names and
// its cases in emission order.
type unionInfo struct {
	tag     string
	payload string // "" when the union types no payload
	cases   []unionCase
}

// unionCase is one tag value and the non-tag fields it allows, in field
// order. payload is the payload field as this case types it, nil when the
// case carries none.
type unionCase struct {
	payload *fieldInfo
	value   string
	fields  []string
}

// resolveUnions attaches each registered Union to its type, reporting every
// union that does not fit its type.
func (e *astEngine) resolveUnions() error {
	var errs []error
	for _, u := range e.gen.Unions {
		ti := e.byName[u.Type.Name]
		if ti.Map != nil {
			errs = append(errs, fmt.Errorf("wiregen: union %s.%s: a union type is a struct, not a map", u.Type.PkgPath, u.Type.Name))
			continue
		}
		if ti.Union != nil {
			errs = append(errs, fmt.Errorf("wiregen: union %s.%s is declared twice", u.Type.PkgPath, u.Type.Name))
			continue
		}
		ui, err := e.resolveUnion(&u, ti)
		if err != nil {
			errs = append(errs, fmt.Errorf("wiregen: union %s.%s: %w", u.Type.PkgPath, u.Type.Name, err))
			continue
		}
		ti.Union = ui
	}
	return errors.Join(errs...)
}

// resolveUnion checks u against ti's fields and orders its cases: by the tag
// enum's declaration order, else by value.
func (e *astEngine) resolveUnion(u *Union, ti *typeInfo) (*unionInfo, error) {
	values, err := e.caseValues(u, ti)
	if err != nil {
		return nil, err
	}
	raw, err := payloadField(u, ti, values)
	if err != nil {
		return nil, err
	}
	ui := &unionInfo{tag: u.Tag, payload: u.Payload}
	covered, err := e.resolveCases(u, ti, values, raw, ui)
	if err != nil {
		return nil, err
	}
	for i := range ti.Fields {
		f := &ti.Fields[i]
		if !covered[f.WireName] {
			return nil, fmt.Errorf("field %q is in no case, so it can never be sent", f.WireName)
		}
		if f.WireName != u.Tag && !f.Omittable && !ui.inEveryCase(f.WireName) {
			return nil, fmt.Errorf("field %q is not in every case, but encoding/json writes it in every case; tag it omitempty or omitzero", f.WireName)
		}
	}
	return ui, nil
}

// resolveCases resolves each case of u into ui, typing its payload, and
// returns the wire names some case covers.
func (e *astEngine) resolveCases(u *Union, ti *typeInfo, values []string, raw *fieldInfo, ui *unionInfo) (map[string]bool, error) {
	covered := map[string]bool{u.Tag: true}
	for _, v := range values {
		uc, err := resolveCase(u, ti, v)
		if err != nil {
			return nil, err
		}
		if pt := u.PayloadTypes[v]; pt != (Type{}) {
			uc.payload = e.typedPayload(raw, pt)
			uc.fields = withField(ti, uc.fields, u.Payload)
		}
		for _, name := range uc.fields {
			covered[name] = true
		}
		ui.cases = append(ui.cases, uc)
	}
	return covered, nil
}

// payloadField checks u's payload declaration against ti and the case
// values, returning the raw payload field, or nil when u types no payload.
func payloadField(u *Union, ti *typeInfo, values []string) (*fieldInfo, error) {
	if u.Payload == "" {
		if len(u.PayloadTypes) > 0 {
			return nil, errors.New("sets PayloadTypes without Payload")
		}
		return nil, nil
	}
	raw := ti.field(u.Payload)
	switch {
	case raw == nil:
		return nil, fmt.Errorf("payload %q is no field of the type", u.Payload)
	case raw.Kind != kindRaw:
		return nil, fmt.Errorf("payload %q must be a json.RawMessage field", u.Payload)
	}
	if keys := slices.Sorted(maps.Keys(u.PayloadTypes)); !slices.Equal(keys, slices.Sorted(slices.Values(values))) {
		return nil, fmt.Errorf("PayloadTypes keys %v must be exactly the Cases keys %v", keys, slices.Sorted(slices.Values(values)))
	}
	typed := false
	for _, v := range values {
		if slices.Contains(u.Cases[v], u.Payload) {
			return nil, fmt.Errorf("case %q lists the payload %q, which PayloadTypes places", v, u.Payload)
		}
		typed = typed || u.PayloadTypes[v] != (Type{})
	}
	if !typed {
		return nil, fmt.Errorf("no case has a payload type, so payload %q can never be sent", u.Payload)
	}
	return raw, nil
}

// typedPayload is raw as a field of registered type pt: the same wire name
// and omit options, with pt's shape, one more pointer level and RawNull.
func (e *astEngine) typedPayload(raw *fieldInfo, pt Type) *fieldInfo {
	f := *raw
	f.Kind, f.TSType, f.GoTypeName = kindStruct, e.gen.tsName(pt.Name), pt.Name
	f.Elem, f.Bounds, f.Mapping, f.Refusals, f.InRefusals = nil, nil, nil, nil, nil
	// A json.RawMessage can be null twice over: encoding/json writes its nil
	// as null, which an omit option drops, and writes bytes holding null, such
	// as a request null decoded into it, which no omit option sees. The nil is
	// one more pointer level and the bytes are RawNull, so presence.go derives
	// null and absence as for any pointer, and null out whatever the options.
	f.Ptrs++
	f.RawNull = true
	return &f
}

// withField adds name to fields, a case's field list, keeping ti's field
// order.
func withField(ti *typeInfo, fields []string, name string) []string {
	out := make([]string, 0, len(fields)+1)
	for i := range ti.Fields {
		if n := ti.Fields[i].WireName; n == name || slices.Contains(fields, n) {
			out = append(out, n)
		}
	}
	return out
}

func (ui *unionInfo) inEveryCase(name string) bool {
	for i := range ui.cases {
		if !slices.Contains(ui.cases[i].fields, name) {
			return false
		}
	}
	return true
}

// caseValues checks u's tag field and returns its case values in emission
// order.
func (e *astEngine) caseValues(u *Union, ti *typeInfo) ([]string, error) {
	tag := ti.field(u.Tag)
	if tag == nil {
		return nil, fmt.Errorf("has no field %q to tag it", u.Tag)
	}
	if tag.optional() || (tag.Kind != kindString && tag.Kind != kindEnum) {
		return nil, fmt.Errorf("tag %q must be a required string or enum field", u.Tag)
	}
	if len(u.Cases) == 0 {
		return nil, errors.New("declares no case")
	}
	values := slices.Sorted(maps.Keys(u.Cases))
	if tag.Kind != kindEnum {
		return values, nil
	}
	members := e.gen.enums[tag.GoTypeName].values
	if !slices.Equal(slices.Sorted(slices.Values(members)), values) {
		return nil, fmt.Errorf("cases %v must be exactly the members of tag enum %s, %v", values, tag.GoTypeName, members)
	}
	return members, nil
}

// resolveCase checks case v of u against ti and lists its fields in field
// order.
func resolveCase(u *Union, ti *typeInfo, v string) (unionCase, error) {
	if !isComponentNamePart(v) {
		return unionCase{}, fmt.Errorf("case %q must be letters, digits, '.', '-' or '_', since it names an OpenAPI component", v)
	}
	allowed := map[string]bool{}
	for _, name := range u.Cases[v] {
		switch {
		case name == u.Tag:
			return unionCase{}, fmt.Errorf("case %q lists the tag %q", v, name)
		case ti.field(name) == nil:
			return unionCase{}, fmt.Errorf("case %q lists %q, which is no field of the type", v, name)
		case allowed[name]:
			return unionCase{}, fmt.Errorf("case %q lists %q twice", v, name)
		}
		allowed[name] = true
	}
	uc := unionCase{value: v}
	for i := range ti.Fields {
		if allowed[ti.Fields[i].WireName] {
			uc.fields = append(uc.fields, ti.Fields[i].WireName)
		}
	}
	return uc, nil
}

// field returns ti's field of wire name name, or nil.
func (ti *typeInfo) field(name string) *fieldInfo {
	for i := range ti.Fields {
		if ti.Fields[i].WireName == name {
			return &ti.Fields[i]
		}
	}
	return nil
}

// caseFields returns ti's fields uc allows, the tag first, the payload as uc
// types it.
func (ti *typeInfo) caseFields(uc *unionCase) []*fieldInfo {
	out := make([]*fieldInfo, 0, 1+len(uc.fields))
	out = append(out, ti.field(ti.Union.tag))
	for _, name := range uc.fields {
		if name == ti.Union.payload {
			out = append(out, uc.payload)
			continue
		}
		out = append(out, ti.field(name))
	}
	return out
}

// graphFields returns every field a walk of the type graph follows from ti:
// its own, then each case's typed payload, whose type its raw field hides.
func (ti *typeInfo) graphFields() []*fieldInfo {
	out := make([]*fieldInfo, 0, len(ti.Fields)+1)
	for i := range ti.Fields {
		out = append(out, &ti.Fields[i])
	}
	if ti.Map != nil {
		out = append(out, ti.Map)
	}
	if ti.Union != nil {
		for i := range ti.Union.cases {
			if p := ti.Union.cases[i].payload; p != nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// otherFields returns the wire names of ti's non-tag fields uc does not
// allow.
func (ti *typeInfo) otherFields(uc *unionCase) []string {
	var out []string
	for i := range ti.Fields {
		name := ti.Fields[i].WireName
		if name != ti.Union.tag && !slices.Contains(uc.fields, name) {
			out = append(out, name)
		}
	}
	return out
}

// isComponentNamePart reports whether s may appear in an OpenAPI component
// name (https://spec.openapis.org/oas/v3.1.0#components-object).
func isComponentNamePart(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if c := s[i]; !isAlnum(c) && c != '.' && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func (g *generator) emitUnionType(w *strings.Builder, ti *typeInfo) {
	w.WriteString(ti.Doc)
	w.WriteString("export type " + g.tsName(ti.Name) + " =")
	for i := range ti.Union.cases {
		uc := &ti.Union.cases[i]
		members := []string{tsPropName(ti.Union.tag) + ": \"" + tsStringLiteral(uc.value) + "\""}
		for _, f := range ti.caseFields(uc)[1:] {
			q := ""
			if f.optional() {
				q = "?"
			}
			members = append(members, tsPropName(f.WireName)+q+": "+f.TSType)
		}
		w.WriteString("\n  | { " + strings.Join(members, "; ") + " }")
	}
	w.WriteString(";\n\n")
}

func (g *generator) emitUnionDecoder(w *strings.Builder, ti *typeInfo, used *usedIdents) {
	tn := g.tsName(ti.Name)
	path := "$." + g.pathName(ti.Name, tn)
	used.helper("asObject")
	used.helper("reqOneOf")
	used.typeRef(tn)
	tagLit := "\"" + tsStringLiteral(ti.Union.tag) + "\""
	values := make([]string, 0, len(ti.Union.cases))
	for _, uc := range ti.Union.cases {
		values = append(values, "\""+tsStringLiteral(uc.value)+"\"")
	}
	w.WriteString("export const " + g.decoderName(ti.Name) + ": Decoder<" + tn + "> = (v) => {\n")
	w.WriteString("  const o = asObject(v, \"" + path + "\");\n")
	w.WriteString("  const tag = reqOneOf(o, " + tagLit + ", [" + strings.Join(values, ", ") + "] as const, \"" + path + "\");\n")
	w.WriteString("  switch (tag) {\n")
	for i := range ti.Union.cases {
		uc := &ti.Union.cases[i]
		lit := "\"" + tsStringLiteral(uc.value) + "\""
		var body strings.Builder
		if others := ti.otherFields(uc); len(others) > 0 {
			used.helper("refuseKeys")
			quoted := make([]string, 0, len(others))
			for _, o := range others {
				quoted = append(quoted, "\""+tsStringLiteral(o)+"\"")
			}
			body.WriteString("  refuseKeys(o, [" + strings.Join(quoted, ", ") + "], \"" + path + "\");\n")
		}
		outType := "globalThis.Extract<" + tn + ", { " + tsPropName(ti.Union.tag) + ": " + lit + " }>"
		g.emitFieldDecodes(&body, outType, tsPropName(ti.Union.tag)+": tag", ti.caseFields(uc)[1:], path, used)
		w.WriteString("    case " + lit + ": {\n")
		for line := range strings.SplitSeq(strings.TrimSuffix(body.String(), "\n"), "\n") {
			w.WriteString("    " + line + "\n")
		}
		w.WriteString("    }\n")
	}
	w.WriteString("    default:\n")
	// A double-quoted literal, not a template: path and the tag are escaped
	// for double quotes only, so a backtick or "${" in an override stays data.
	w.WriteString("      throw new globalThis.TypeError(\"" + path + ": unknown " + tsStringLiteral(ti.Union.tag) + " \" + globalThis.String(tag as unknown));\n")
	w.WriteString("  }\n")
	w.WriteString("};\n\n")
}

// arbForUnion renders a union's arbitrary: a draw from any case, the tag
// fixed to the case's value. A union that can reach itself is depth-capped
// like a recursive struct.
func (g *generator) arbForUnion(ti *typeInfo, recursive func(*fieldInfo) bool) string {
	render := func(indent string, rec func(*fieldInfo) bool) []string {
		out := make([]string, 0, len(ti.Union.cases))
		for i := range ti.Union.cases {
			uc := &ti.Union.cases[i]
			fixed := map[string]string{ti.Union.tag: "fc.constant(\"" + tsStringLiteral(uc.value) + "\" as const)"}
			out = append(out, g.arbRecord(ti.caseFields(uc), indent, rec, fixed))
		}
		return out
	}
	if recursive == nil {
		return "fc.oneof(\n    " + strings.Join(render("    ", nil), ",\n    ") + ",\n  )"
	}
	leaves := render("    ", recursive)
	full := render("    ", nil)
	return "fc.oneof(\n    " + arbRecursionCap + ",\n    " + strings.Join(append(leaves, full...), ",\n    ") + ",\n  )"
}
