package httpwire

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Schema is a parsed OpenAPI 3.1 document: the request and response schemas
// of each operation, by operationId. A nil *Schema declares no operation.
type Schema struct {
	ops map[string]*operation
}

type operation struct {
	request   *requestBody
	responses map[int]*response
	params    map[paramKey]*param
	method    string
	path      string
}

type paramKey struct{ in, name string }

type param struct {
	schema   *node
	required bool
}

// requestBody's schema is nil when the body has no application/json media.
type requestBody struct {
	schema   *node
	required bool
}

// response's schema is nil when it has no application/json media; content
// reports whether it declares any media at all.
type response struct {
	schema  *node
	content bool
}

const mediaJSON = "application/json"

// ParseSchema parses the OpenAPI 3.1 document wiregen writes. It refuses a
// schema keyword outside the closed set the validator reads (the README lists
// it), a pattern [IsWirePattern] rejects, an unresolved $ref, a $ref cycle
// with no object between, and an OpenAPI member wiregen never writes, naming
// each in the joined error. An object schema with properties and no
// additionalProperties admits no other member, as the decode refuses unknown
// fields.
func ParseSchema(doc []byte) (*Schema, error) {
	root, ok := decodeJSON(doc)
	if !ok {
		return nil, errors.New("httpwire: ParseSchema: the document is not one JSON value")
	}
	p := &parser{components: map[string]*node{}, ops: map[string]*operation{}}
	p.document(root)
	if len(p.errs) == 0 {
		p.resolve()
	}
	if len(p.errs) == 0 {
		p.refuseCycles()
	}
	if err := errors.Join(p.errs...); err != nil {
		return nil, err
	}
	return &Schema{ops: p.ops}, nil
}

// decodeJSON reads b as exactly one JSON value, numbers kept as their text.
// empty reports a body holding only whitespace.
func decodeJSON(b []byte) (v any, ok bool) {
	v, empty, ok := decodeBody(b)
	return v, ok && !empty
}

func decodeBody(b []byte) (v any, empty, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	switch err := dec.Decode(&v); {
	case errors.Is(err, io.EOF):
		return nil, true, true
	case err != nil:
		return nil, false, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false, false
	}
	return v, false, true
}

type parser struct {
	components map[string]*node
	ops        map[string]*operation
	refs       []*node
	unions     []*union
	errs       []error
}

func (p *parser) fail(at, format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf("httpwire: ParseSchema: %s: %s", cmp.Or(at, "/"), fmt.Sprintf(format, args...)))
}

// pointer appends key to the JSON pointer at (RFC 6901 section 3).
func pointer(at, key string) string {
	return at + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(key)
}

func sortedKeys(m map[string]any) []string {
	return slices.Sorted(maps.Keys(m))
}

// object reads v at at as a JSON object whose members are all in allowed,
// or start with "x-" when extensions is set.
func (p *parser) object(v any, at string, extensions bool, allowed ...string) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(at, "not an object")
		return nil, false
	}
	for _, k := range sortedKeys(m) {
		if !slices.Contains(allowed, k) && (!extensions || !strings.HasPrefix(k, "x-")) {
			p.fail(at, "member %q is not one wiregen writes", k)
		}
	}
	return m, true
}

func (p *parser) document(root any) {
	m, ok := p.object(root, "", true, "openapi", "info", "paths", "components", "servers", "security", "tags", "externalDocs")
	if !ok {
		return
	}
	if v, _ := m["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
		p.fail("/openapi", "openapi %q is not 3.1", m["openapi"])
	}
	if c, has := m["components"]; has {
		p.componentSchemas(c)
	}
	paths, ok := m["paths"].(map[string]any)
	if !ok {
		p.fail("/paths", "not an object")
		return
	}
	for _, path := range sortedKeys(paths) {
		p.pathItem(path, paths[path], pointer("/paths", path))
	}
}

func (p *parser) componentSchemas(v any) {
	m, ok := p.object(v, "/components", false, "schemas", "securitySchemes")
	if !ok {
		return
	}
	raw, ok := m["schemas"]
	if !ok {
		return
	}
	schemas, ok := raw.(map[string]any)
	if !ok {
		p.fail("/components/schemas", "not an object")
		return
	}
	for _, name := range sortedKeys(schemas) {
		p.components[name] = p.schema(schemas[name], pointer("/components/schemas", name))
	}
}

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func (p *parser) pathItem(path string, v any, at string) {
	m, ok := p.object(v, at, false, methods...)
	if !ok {
		return
	}
	for _, method := range sortedKeys(m) {
		p.operation(path, method, m[method], pointer(at, method))
	}
}

func (p *parser) operation(path, method string, v any, at string) {
	m, ok := p.object(v, at, true, "operationId", "summary", "description", "parameters", "requestBody",
		"responses", "security", "servers", "tags", "deprecated", "externalDocs")
	if !ok {
		return
	}
	op := &operation{method: strings.ToUpper(method), path: path, responses: map[int]*response{}, params: map[paramKey]*param{}}
	id, _ := m["operationId"].(string)
	switch {
	case id == "":
		p.fail(at, "has no operationId")
	case p.ops[id] != nil:
		p.fail(at, "operationId %q names two operations", id)
	default:
		p.ops[id] = op
	}
	if v, ok := m["parameters"]; ok {
		p.parameters(op, v, pointer(at, "parameters"))
	}
	if v, ok := m["requestBody"]; ok {
		op.request = p.requestBody(v, pointer(at, "requestBody"))
	}
	p.responses(op, m["responses"], pointer(at, "responses"))
}

var paramPlaces = []string{inPath, inQuery, "header", "cookie"}

func (p *parser) parameters(op *operation, v any, at string) {
	list, ok := v.([]any)
	if !ok {
		p.fail(at, "not an array")
		return
	}
	for i, e := range list {
		pat := pointer(at, strconv.Itoa(i))
		m, ok := p.object(e, pat, true, "name", "in", "required", "schema", "description", "deprecated")
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		in, _ := m["in"].(string)
		switch {
		case name == "":
			p.fail(pat, "parameter has no name")
		case !slices.Contains(paramPlaces, in):
			p.fail(pat, "parameter in %q is not path, query, header or cookie", m["in"])
		case m["schema"] == nil:
			p.fail(pat, "parameter has no schema")
		case op.params[paramKey{in, name}] != nil:
			p.fail(pat, "parameter %s %q declared twice", in, name)
		default:
			required, _ := m["required"].(bool)
			op.params[paramKey{in, name}] = &param{schema: p.schema(m["schema"], pointer(pat, "schema")), required: required}
		}
	}
}

func (p *parser) requestBody(v any, at string) *requestBody {
	m, ok := p.object(v, at, true, "required", "content", "description")
	if !ok {
		return nil
	}
	required, _ := m["required"].(bool)
	rb := &requestBody{required: required}
	rb.schema, _ = p.content(m["content"], pointer(at, "content"))
	return rb
}

func (p *parser) responses(op *operation, v any, at string) {
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(at, "not an object")
		return
	}
	for _, key := range sortedKeys(m) {
		rat := pointer(at, key)
		status, err := strconv.Atoi(key)
		if err != nil || len(key) != 3 || status < 100 || status > 599 {
			p.fail(rat, "response key %q is not a status from 100 to 599", key)
			continue
		}
		r, ok := p.object(m[key], rat, true, "description", "content", "headers")
		if !ok {
			continue
		}
		resp := &response{}
		if c, ok := r["content"]; ok {
			resp.schema, resp.content = p.content(c, pointer(rat, "content"))
		}
		if h, ok := r["headers"]; ok {
			p.headers(h, pointer(rat, "headers"))
		}
		op.responses[status] = resp
	}
}

// content parses a media-type map, answering its application/json schema
// and whether it declares any media type.
func (p *parser) content(v any, at string) (*node, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(at, "not an object")
		return nil, false
	}
	var jsonSchema *node
	for _, media := range sortedKeys(m) {
		mat := pointer(at, media)
		mm, ok := p.object(m[media], mat, false, "schema", "x-events")
		if !ok {
			continue
		}
		if s, ok := mm["schema"]; ok {
			n := p.schema(s, pointer(mat, "schema"))
			if media == mediaJSON {
				jsonSchema = n
			}
		}
		if ev, ok := mm["x-events"]; ok {
			p.schemaMap(ev, pointer(mat, "x-events"))
		}
	}
	return jsonSchema, len(m) > 0
}

func (p *parser) headers(v any, at string) {
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(at, "not an object")
		return
	}
	for _, name := range sortedKeys(m) {
		hat := pointer(at, name)
		h, ok := p.object(m[name], hat, true, "required", "schema", "description", "deprecated")
		if ok && h["schema"] != nil {
			p.schema(h["schema"], pointer(hat, "schema"))
		}
	}
}

func (p *parser) schemaMap(v any, at string) {
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(at, "not an object")
		return
	}
	for _, name := range sortedKeys(m) {
		p.schema(m[name], pointer(at, name))
	}
}

const componentsRef = "#/components/schemas/"

// resolve points every $ref at its component and fills each union's member
// names, which need the cases resolved.
func (p *parser) resolve() {
	for _, n := range p.refs {
		name, ok := strings.CutPrefix(n.ref, componentsRef)
		if ok {
			name = strings.NewReplacer("~1", "/", "~0", "~").Replace(name)
		}
		target := p.components[name]
		if !ok || target == nil {
			p.fail(n.at, "$ref %q names no schema under %s", n.ref, componentsRef)
			continue
		}
		n.target = target
	}
	if len(p.errs) > 0 {
		return
	}
	for _, u := range p.unions {
		u.names = map[string]bool{}
		for _, c := range u.cases {
			for k := range c.final().props {
				u.names[k] = true
			}
		}
	}
}

// refuseCycles refuses a component reached from itself through $ref, anyOf
// and oneOf alone: validating it would never reach a member or an element.
func (p *parser) refuseCycles() {
	const (
		unseen = iota
		visiting
		done
	)
	state := map[*node]int{}
	var visit func(n *node, trail []string) bool
	visit = func(n *node, trail []string) bool {
		switch state[n] {
		case visiting:
			p.fail(n.at, "$ref cycle with no object between: %s", strings.Join(trail, " -> "))
			return false
		case done:
			return true
		}
		state[n] = visiting
		for _, next := range n.immediate() {
			if !visit(next.target, append(trail, next.ref)) {
				return false
			}
		}
		state[n] = done
		return true
	}
	names := slices.Sorted(maps.Keys(p.components))
	for _, name := range names {
		if !visit(p.components[name], []string{componentsRef + name}) {
			return
		}
	}
}
