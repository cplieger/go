package wiregen

// These derive every null and absence rule from what encoding/json writes and
// reads for a field, or from a union case's Required, which overrides both;
// the TS types, decoders, arbitraries and OpenAPI schemas all read them.

// optional reports whether encoding/json can leave the key out. The TS
// member is `key?:`, and the decoder reads an absent key or null as absent.
func (f *fieldInfo) optional() bool { return !f.CaseRequired && (f.Omittable || f.ViaEmbedPtr) }

// optionalIn reports whether a request may leave the key out: encoding/json
// also leaves an absent pointer nil.
func (f *fieldInfo) optionalIn() bool { return !f.CaseRequired && (f.Ptrs > 0 || f.optional()) }

// nullOut reports whether encoding/json can write null for the key: a nil
// pointer or collection no omit option drops, anything nil under a non-nil
// outer pointer, which the option does not look through, or raw bytes that
// hold null.
func (f *fieldInfo) nullOut() bool {
	switch {
	case f.CaseRequired:
		return false
	case f.RawNull:
		return true
	case f.Kind == kindRaw:
		return false
	case f.Ptrs > 1:
		return true
	case f.Ptrs == 1:
		return !f.Omittable || f.nilValue()
	}
	return f.nilValue() && !f.Omittable
}

// nullIn reports whether a request may send null, which encoding/json decodes
// to a nil pointer or collection.
func (f *fieldInfo) nullIn() bool {
	return !f.CaseRequired && f.Kind != kindRaw && (f.Ptrs > 0 || f.nilValue())
}

// nilValue reports whether the value under the pointers can be nil.
func (f *fieldInfo) nilValue() bool {
	return f.Kind == kindSlice || f.Kind == kindMap || f.Kind == kindBytes
}

// nullable reports whether f decodes null as null: it has a pointer level.
// A required one's TS type admits null; a nil collection with none instead
// reads as empty, and an optional field reads null as absent.
func (f *fieldInfo) nullable() bool { return f.Ptrs > 0 && f.Kind != kindRaw && !f.CaseRequired }

// memberTS is f's TS type as a required member or a collection element.
func (f *fieldInfo) memberTS() string {
	if f.nullable() && !f.optional() {
		return f.TSType + " | null"
	}
	return f.TSType
}

// refusesNull reports whether f's decoder refuses null: f is CaseRequired
// and neither a json.RawMessage nor mapped, whose Decoder judges null, or
// its Count bound refuses null, which would decode to an empty collection
// below the bound, while a pointer level instead takes it as nil.
func (f *fieldInfo) refusesNull() bool {
	return (f.CaseRequired && f.Kind != kindRaw && f.Kind != kindMapped) || (f.Ptrs == 0 && f.Bounds.refusesNull())
}
