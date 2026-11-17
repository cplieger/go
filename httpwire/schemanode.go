package httpwire

import (
	"bytes"
	"encoding/json"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// jsonType is a set of JSON Schema type names.
type jsonType uint8

const (
	typeNull jsonType = 1 << iota
	typeBoolean
	typeObject
	typeArray
	typeNumber
	typeString
	typeInteger
)

const typeNameNull = "null"

var typeNames = map[string]jsonType{
	typeNameNull: typeNull, "boolean": typeBoolean, "object": typeObject, "array": typeArray,
	"number": typeNumber, "string": typeString, "integer": typeInteger,
}

// node is one parsed schema. Exactly one of ref, inner and union is set on a
// node built from $ref, anyOf or oneOf; every other node is a plain schema.
type node struct {
	target   *node // ref's component, set by resolve
	inner    *node // anyOf's non-null schema
	union    *union
	props    map[string]*node
	addl     *node
	keys     *node
	items    *node
	pattern  *regexp.Regexp
	min, max *big.Rat
	constant any
	ref      string
	at       string
	enum     []any
	required []string
	// minLength, maxLength, x-max-bytes, minItems and maxItems; -1 is absent.
	minLength, maxLength, maxBytes, minItems, maxItems int
	types                                              jsonType
	hasEnum, hasConst, closed                          bool
}

// union is a oneOf with a discriminator: each tag value's case, a $ref node,
// and every case's member names, so a member of another case is told apart
// from one no case declares.
type union struct {
	cases map[string]*node
	names map[string]bool
	tag   string
}

// final follows n's $ref chain to the schema it names.
func (n *node) final() *node {
	for n.target != nil {
		n = n.target
	}
	return n
}

// immediate lists the $ref nodes n validates the same value through.
func (n *node) immediate() []*node {
	switch {
	case n.ref != "":
		return []*node{n}
	case n.inner != nil:
		return n.inner.immediate()
	case n.union != nil:
		var out []*node
		for _, tag := range slices.Sorted(maps.Keys(n.union.cases)) {
			out = append(out, n.union.cases[tag])
		}
		return out
	}
	return nil
}

const kwItems = "items"

var annotations = []string{"description", "format", "contentEncoding", "default"}

var schemaKeywords = append([]string{
	"type", "properties", "required", "additionalProperties", "propertyNames", kwItems, "enum", "const",
	"minimum", "maximum", "minLength", "maxLength", "x-max-bytes", "pattern", "minItems", "maxItems",
	"$ref", "anyOf", "oneOf", "discriminator",
}, annotations...)

// wireRegexps holds the compiled IsWirePattern patterns.
var wireRegexps = func() map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp, len(wirePatterns))
	for _, p := range wirePatterns {
		out[p] = regexp.MustCompile(p)
	}
	return out
}()

// schema parses the schema object v at JSON pointer at.
func (p *parser) schema(v any, at string) *node {
	n := &node{at: at, minLength: -1, maxLength: -1, maxBytes: -1, minItems: -1, maxItems: -1}
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(at, "schema is not an object")
		return n
	}
	for _, k := range sortedKeys(m) {
		if !slices.Contains(schemaKeywords, k) {
			p.fail(at, "keyword %q is outside the set ParseSchema reads", k)
		}
	}
	switch {
	case m["$ref"] != nil:
		p.ref(n, m, at)
	case m["anyOf"] != nil:
		p.anyOf(n, m, at)
	case m["oneOf"] != nil || m["discriminator"] != nil:
		p.oneOf(n, m, at)
	default:
		p.plain(n, m, at)
	}
	return n
}

// alone refuses any keyword of m beside kw that is not an annotation.
func (p *parser) alone(m map[string]any, at string, kw ...string) {
	for _, k := range sortedKeys(m) {
		if !slices.Contains(kw, k) && !slices.Contains(annotations, k) && slices.Contains(schemaKeywords, k) {
			p.fail(at, "%s beside %q", kw[0], k)
		}
	}
}

func (p *parser) ref(n *node, m map[string]any, at string) {
	p.alone(m, at, "$ref")
	ref, ok := m["$ref"].(string)
	if !ok {
		p.fail(at, "$ref is not a string")
		return
	}
	n.ref = ref
	p.refs = append(p.refs, n)
}

func (p *parser) anyOf(n *node, m map[string]any, at string) {
	p.alone(m, at, "anyOf")
	list, _ := m["anyOf"].([]any)
	if len(list) != 2 || !isNullSchema(list[1]) {
		p.fail(at, `anyOf is not the null wrapper [schema, {"type": "null"}]`)
		return
	}
	n.inner = p.schema(list[0], pointer(pointer(at, "anyOf"), "0"))
}

func isNullSchema(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) == 1 && m["type"] == typeNameNull
}

func (p *parser) oneOf(n *node, m map[string]any, at string) {
	p.alone(m, at, "oneOf", "discriminator")
	list, ok := m["oneOf"].([]any)
	if !ok {
		p.fail(at, "discriminator with no oneOf list")
		return
	}
	disc, ok := m["discriminator"].(map[string]any)
	if !ok {
		p.fail(at, "oneOf with no discriminator")
		return
	}
	refs := map[string]bool{}
	for i, e := range list {
		c, isObject := e.(map[string]any)
		ref, isString := c["$ref"].(string)
		if !isObject || len(c) != 1 || !isString {
			p.fail(pointer(pointer(at, "oneOf"), strconv.Itoa(i)), "oneOf case is not a $ref")
			continue
		}
		refs[ref] = true
	}
	dat := pointer(at, "discriminator")
	p.object(disc, dat, false, "propertyName", "mapping")
	tag, _ := disc["propertyName"].(string)
	mapping, ok := disc["mapping"].(map[string]any)
	if tag == "" || !ok {
		p.fail(dat, "discriminator needs a propertyName and a mapping")
		return
	}
	u := &union{tag: tag, cases: map[string]*node{}}
	mapped := map[string]bool{}
	for _, value := range sortedKeys(mapping) {
		ref, _ := mapping[value].(string)
		if !refs[ref] {
			p.fail(pointer(pointer(dat, "mapping"), value), "mapping target %q is off the oneOf list", mapping[value])
			continue
		}
		mapped[ref] = true
		c := &node{at: pointer(pointer(dat, "mapping"), value), ref: ref}
		p.refs = append(p.refs, c)
		u.cases[value] = c
	}
	for _, ref := range slices.Sorted(maps.Keys(refs)) {
		if !mapped[ref] {
			p.fail(dat, "mapping names no tag value for oneOf case %q", ref)
		}
	}
	n.union = u
	p.unions = append(p.unions, u)
}

func (p *parser) plain(n *node, m map[string]any, at string) {
	n.types = p.types(m, at)
	if v, ok := m["enum"]; ok {
		list, isList := v.([]any)
		if !isList {
			p.fail(at, "enum is not an array")
		}
		n.enum, n.hasEnum = list, true
	}
	n.constant, n.hasConst = m["const"]
	p.bounds(n, m, at)
	p.members(n, m, at)
	if v, ok := m[kwItems]; ok {
		n.items = p.schema(v, pointer(at, kwItems))
	}
}

func (p *parser) types(m map[string]any, at string) jsonType {
	v, ok := m["type"]
	if !ok {
		return 0
	}
	names, isList := v.([]any)
	if s, isString := v.(string); isString {
		names = []any{s}
	} else if !isList || len(names) == 0 {
		p.fail(at, "type is neither a type name nor a list of them")
		return 0
	}
	var set jsonType
	for _, e := range names {
		name, _ := e.(string)
		t, known := typeNames[name]
		switch {
		case !known:
			p.fail(at, "type %v is not a JSON Schema type", e)
		case set&t != 0:
			p.fail(at, "type %q listed twice", name)
		}
		set |= t
	}
	return set
}

func (p *parser) bounds(n *node, m map[string]any, at string) {
	for kw, dst := range map[string]*int{
		"minLength": &n.minLength, "maxLength": &n.maxLength, "x-max-bytes": &n.maxBytes,
		"minItems": &n.minItems, "maxItems": &n.maxItems,
	} {
		v, ok := m[kw]
		if !ok {
			continue
		}
		num, _ := v.(json.Number)
		c, err := strconv.Atoi(string(num))
		if err != nil || c < 0 {
			p.fail(at, "%s %v is not a count", kw, v)
			continue
		}
		*dst = c
	}
	for kw, dst := range map[string]**big.Rat{"minimum": &n.min, "maximum": &n.max} {
		v, ok := m[kw]
		if !ok {
			continue
		}
		num, _ := v.(json.Number)
		i, isInt := new(big.Int).SetString(string(num), 10)
		if !isInt {
			p.fail(at, "%s %v is not an integer", kw, v)
			continue
		}
		*dst = new(big.Rat).SetInt(i)
	}
	if v, ok := m["pattern"]; ok {
		s, isString := v.(string)
		switch {
		case !isString:
			p.fail(at, "pattern is not a string")
		case !IsWirePattern(s):
			p.fail(at, "pattern %q is not one of the fixed wire patterns", s)
		default:
			n.pattern = wireRegexps[s]
		}
	}
}

func (p *parser) members(n *node, m map[string]any, at string) {
	if v, ok := m["properties"]; ok {
		props, isObject := v.(map[string]any)
		if !isObject {
			p.fail(at, "properties is not an object")
		}
		n.props = make(map[string]*node, len(props))
		for _, name := range sortedKeys(props) {
			n.props[name] = p.schema(props[name], pointer(pointer(at, "properties"), name))
		}
		n.closed = true
	}
	if v, ok := m["required"]; ok {
		list, isList := v.([]any)
		for _, e := range list {
			name, isString := e.(string)
			if !isString {
				isList = false
			}
			n.required = append(n.required, name)
		}
		if !isList {
			p.fail(at, "required is not a list of member names")
		}
	}
	switch v := m["additionalProperties"].(type) {
	case nil:
	case bool:
		n.closed = !v
	case map[string]any:
		n.addl = p.schema(v, pointer(at, "additionalProperties"))
		n.closed = false
	default:
		p.fail(at, "additionalProperties is neither a boolean nor a schema")
	}
	if v, ok := m["propertyNames"]; ok {
		n.keys = p.schema(v, pointer(at, "propertyNames"))
	}
}

// The field codes of the one field-code map, shared by the decode, the
// schema check and an answer's contract check.
const (
	codeMalformed = "malformed"
	codeRequired  = "required"
	codeUnknown   = "unknown"
	codeEnum      = "enum"
	codeUnion     = "union"
	codeRange     = "range"
	codePattern   = "pattern"
)

// checker collects one FieldError per failing path, the first rule met.
type checker struct {
	seen map[string]bool
	out  []FieldError
}

func (c *checker) add(path, code, message string) {
	if c.seen[path] {
		return
	}
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[path] = true
	c.out = append(c.out, FieldError{Path: path, Code: code, Message: message})
}

func memberPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// check validates v, decoded with UseNumber, against n at path. others holds
// the member names of a union's other cases when n is one case.
func (c *checker) check(n *node, v any, path string, others map[string]bool) {
	switch {
	case n.target != nil:
		c.check(n.target, v, path, others)
		return
	case v == nil:
		if !n.admitsNull() {
			c.add(path, codeRequired, "null is not allowed here")
		}
		return
	case n.inner != nil:
		c.check(n.inner, v, path, nil)
		return
	case n.union != nil:
		c.checkUnion(n.union, v, path)
		return
	}
	if !c.checkType(n, v, path) {
		return
	}
	if n.hasConst && !jsonEqual(v, n.constant) || n.hasEnum && !slices.ContainsFunc(n.enum, func(e any) bool { return jsonEqual(v, e) }) {
		c.add(path, codeEnum, "not one of the allowed values")
		return
	}
	switch v := v.(type) {
	case string:
		c.checkString(n, v, path)
	case json.Number:
		c.checkNumber(n, v, path)
	case []any:
		c.checkArray(n, v, path)
	case map[string]any:
		c.checkObject(n, v, path, others)
	}
}

func (n *node) admitsNull() bool {
	switch {
	case n.target != nil:
		return n.target.admitsNull()
	case n.inner != nil:
		return true
	case n.union != nil:
		return false
	case n.types != 0 && n.types&typeNull == 0:
		return false
	case n.hasConst && n.constant != nil:
		return false
	case n.hasEnum && !slices.Contains(n.enum, nil):
		return false
	}
	return true
}

func isIntegerText(num json.Number) bool {
	return !strings.ContainsAny(string(num), ".eE")
}

func valueType(v any) jsonType {
	switch v := v.(type) {
	case bool:
		return typeBoolean
	case string:
		return typeString
	case json.Number:
		if isIntegerText(v) {
			return typeNumber | typeInteger
		}
		return typeNumber
	case []any:
		return typeArray
	case map[string]any:
		return typeObject
	}
	return typeNull
}

func (c *checker) checkType(n *node, v any, path string) bool {
	if n.types == 0 || n.types&valueType(v) != 0 {
		return true
	}
	var names []string
	for _, name := range []string{"string", "integer", "number", "boolean", "object", "array", typeNameNull} {
		if n.types&typeNames[name] != 0 {
			names = append(names, name)
		}
	}
	c.add(path, codeMalformed, "expected "+strings.Join(names, " or "))
	return false
}

func (c *checker) checkString(n *node, s, path string) {
	runes := len([]rune(s))
	switch {
	case n.minLength >= 0 && runes < n.minLength:
		c.add(path, codeRange, "at least "+strconv.Itoa(n.minLength)+" characters")
	case n.maxLength >= 0 && runes > n.maxLength:
		c.add(path, codeRange, "at most "+strconv.Itoa(n.maxLength)+" characters")
	case n.maxBytes >= 0 && len(s) > n.maxBytes:
		c.add(path, codeRange, "at most "+strconv.Itoa(n.maxBytes)+" bytes of UTF-8")
	case n.pattern != nil && !n.pattern.MatchString(s):
		c.add(path, codePattern, "does not match its wire pattern")
	}
}

// exactNumber is num's exact value: an integer through int64 or uint64, and
// anything else through float64, the type the decode reads it into. ok is
// false for an integer outside both 64-bit types, or a number outside
// float64's range.
func exactNumber(num json.Number) (r *big.Rat, isInt, ok bool) {
	text := string(num)
	if isIntegerText(num) {
		if i, err := strconv.ParseInt(text, 10, 64); err == nil {
			return new(big.Rat).SetInt64(i), true, true
		}
		if u, err := strconv.ParseUint(text, 10, 64); err == nil {
			return new(big.Rat).SetUint64(u), true, true
		}
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, false, false
	}
	return new(big.Rat).SetFloat64(f), false, true
}

func (c *checker) checkNumber(n *node, num json.Number, path string) {
	integerOnly := n.types&typeInteger != 0 && n.types&typeNumber == 0
	if n.types&(typeInteger|typeNumber) == 0 && n.min == nil && n.max == nil {
		return
	}
	r, isInt, ok := exactNumber(num)
	switch {
	case integerOnly && !isInt:
		c.add(path, codeRange, "outside the 64-bit integer range")
	case !ok:
		c.add(path, codeRange, "outside the float64 range")
	case n.min != nil && r.Cmp(n.min) < 0:
		c.add(path, codeRange, "at least "+n.min.RatString())
	case n.max != nil && r.Cmp(n.max) > 0:
		c.add(path, codeRange, "at most "+n.max.RatString())
	}
}

func (c *checker) checkArray(n *node, list []any, path string) {
	switch {
	case n.minItems >= 0 && len(list) < n.minItems:
		c.add(path, codeRange, "at least "+strconv.Itoa(n.minItems)+" items")
	case n.maxItems >= 0 && len(list) > n.maxItems:
		c.add(path, codeRange, "at most "+strconv.Itoa(n.maxItems)+" items")
	}
	if n.items == nil {
		return
	}
	for i, e := range list {
		c.check(n.items, e, path+"["+strconv.Itoa(i)+"]", nil)
	}
}

func (c *checker) checkObject(n *node, m map[string]any, path string, others map[string]bool) {
	for _, name := range n.required {
		if _, ok := m[name]; !ok {
			c.add(memberPath(path, name), codeRequired, "required")
		}
	}
	for _, k := range sortedKeys(m) {
		at := memberPath(path, k)
		switch s, declared := n.props[k]; {
		case n.keys != nil && !keyMatches(n.keys, k):
			c.add(at, codePattern, "the key does not match its wire pattern")
		case declared:
			c.check(s, m[k], at, nil)
		case n.addl != nil:
			c.check(n.addl, m[k], at, nil)
		case n.closed && others[k]:
			c.add(at, codeUnion, "belongs to another case of the union")
		case n.closed:
			c.add(at, codeUnknown, "unknown field")
		}
	}
}

func keyMatches(keys *node, k string) bool {
	var c checker
	c.check(keys, k, "", nil)
	return len(c.out) == 0
}

func (c *checker) checkUnion(u *union, v any, path string) {
	m, ok := v.(map[string]any)
	if !ok {
		c.add(path, codeMalformed, "expected object")
		return
	}
	at := memberPath(path, u.tag)
	tag, isString := m[u.tag].(string)
	switch cs := u.cases[tag]; {
	case !isString:
		c.add(at, codeUnion, "needs the union tag as a string")
	case cs == nil:
		c.add(at, codeUnion, "names no case of the union")
	default:
		c.check(cs, v, path, u.names)
	}
}

// jsonEqual compares two values decoded with UseNumber, numbers by value.
func jsonEqual(a, b any) bool {
	switch a := a.(type) {
	case json.Number:
		bn, ok := b.(json.Number)
		return ok && numberEqual(a, bn)
	case []any:
		bl, ok := b.([]any)
		return ok && slices.EqualFunc(a, bl, jsonEqual)
	case map[string]any:
		bm, ok := b.(map[string]any)
		return ok && maps.EqualFunc(a, bm, jsonEqual)
	}
	return a == b
}

func numberEqual(a, b json.Number) bool {
	ra, _, okA := exactNumber(a)
	rb, _, okB := exactNumber(b)
	if !okA || !okB {
		return a == b
	}
	return ra.Cmp(rb) == 0
}

// CheckBody validates one JSON value against an operation's request body
// (status 0) or the response for status, numbers read exactly (an integer as
// int64 or uint64, never through float64). It answers one entry per failing
// path ("" for the value itself), with the field code of the one map. An
// operation, status or body the document does not declare answers one entry
// at "" with code unknown.
func (s *Schema) CheckBody(operation string, status int, body []byte) []FieldError {
	n, required, refusal := s.bodySchema(operation, status, body)
	if refusal != "" {
		return []FieldError{{Path: "", Code: codeUnknown, Message: refusal}}
	}
	if n == nil {
		return nil
	}
	v, empty, ok := decodeBody(body)
	var c checker
	switch {
	case empty && required:
		c.add("", codeRequired, "a JSON body is required")
	case empty:
	case !ok:
		c.add("", codeMalformed, "not one JSON value")
	default:
		c.check(n, v, "", nil)
	}
	return c.out
}

// bodySchema finds the schema CheckBody checks body with; a nil node and no
// refusal is a status that declares no body, which body leaves empty.
func (s *Schema) bodySchema(opID string, status int, body []byte) (n *node, required bool, refusal string) {
	var op *operation
	if s != nil {
		op = s.ops[opID]
	}
	switch {
	case op == nil:
		return nil, false, "the document has no operation " + strconv.Quote(opID)
	case status == 0 && (op.request == nil || op.request.schema == nil):
		return nil, false, "the operation declares no JSON request body"
	case status == 0:
		return op.request.schema, op.request.required, ""
	}
	r := op.responses[status]
	switch {
	case r == nil:
		return nil, false, "the operation declares no status " + strconv.Itoa(status)
	case r.schema != nil:
		return r.schema, true, ""
	case !r.content && len(bytes.TrimSpace(body)) == 0:
		return nil, false, ""
	}
	return nil, false, "status " + strconv.Itoa(status) + " declares no JSON body"
}

// CheckParam validates one parameter value against its schema: in is
// "query", "path" or "header", value a string, int64, uint64 or bool (the
// number or boolean a query value parsed to). The entry's Path is name.
func (s *Schema) CheckParam(operation, in, name string, value any) *FieldError {
	var n *node
	if s != nil && s.ops[operation] != nil {
		n = s.ops[operation].params[paramKey{in, name}]
	}
	if n == nil {
		return &FieldError{Path: name, Code: codeUnknown, Message: "not a declared " + in + " parameter"}
	}
	var v any
	switch x := value.(type) {
	case string, bool:
		v = x
	case int64:
		v = json.Number(strconv.FormatInt(x, 10))
	case uint64:
		v = json.Number(strconv.FormatUint(x, 10))
	default:
		return &FieldError{Path: name, Code: codeMalformed, Message: "a parameter value is a string, int64, uint64 or bool"}
	}
	var c checker
	c.check(n, v, name, nil)
	if len(c.out) == 0 {
		return nil
	}
	return &c.out[0]
}
