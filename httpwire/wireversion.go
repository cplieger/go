package httpwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// WireVersion answers the wire version of an OpenAPI 3.1 document: the first
// 16 lowercase hex characters of the SHA-256 of its normal form. The normal
// form drops what changes no wire: summary and description where each is a
// keyword of an OpenAPI object (never a member name inside a map such as
// properties), the top-level info object, and each x-error-codes entry's
// remediation in a response; the rest is written with sorted keys and no
// whitespace. It answers "" when doc is not one JSON object.
func WireVersion(doc []byte) string {
	root, ok := decodeJSON(doc)
	m, isObject := root.(map[string]any)
	if !ok || !isObject {
		return ""
	}
	delete(m, "info")
	normDocument(m)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// The tree holds only what decodeJSON built (maps, slices, strings,
	// bools, nil and json.Number), which always encodes.
	_ = enc.Encode(m)
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])[:16]
}

type normalizer func(map[string]any)

func dropDocs(m map[string]any) {
	delete(m, "summary")
	delete(m, "description")
}

// member applies norm to the object m[key].
func member(m map[string]any, key string, norm normalizer) {
	if v, ok := m[key].(map[string]any); ok {
		norm(v)
	}
}

// eachMember applies norm to every object value of the map m[key].
func eachMember(m map[string]any, key string, norm normalizer) {
	if mm, ok := m[key].(map[string]any); ok {
		for k := range mm {
			member(mm, k, norm)
		}
	}
}

// eachItem applies norm to every object element of the array m[key].
func eachItem(m map[string]any, key string, norm normalizer) {
	if list, ok := m[key].([]any); ok {
		for _, e := range list {
			if v, ok := e.(map[string]any); ok {
				norm(v)
			}
		}
	}
}

func normDocument(m map[string]any) {
	eachItem(m, "servers", normServer)
	eachMember(m, "paths", normPathItem)
	eachMember(m, "webhooks", normPathItem)
	member(m, "components", normComponents)
}

func normComponents(m map[string]any) {
	eachMember(m, "schemas", normSchema)
	eachMember(m, "responses", normResponse)
	eachMember(m, "parameters", normParameter)
	eachMember(m, "requestBodies", normRequestBody)
	eachMember(m, "headers", normHeader)
	eachMember(m, "securitySchemes", dropDocs)
	eachMember(m, "callbacks", normCallback)
	eachMember(m, "pathItems", normPathItem)
}

func normServer(m map[string]any) {
	dropDocs(m)
	eachMember(m, "variables", dropDocs)
}

func normPathItem(m map[string]any) {
	dropDocs(m)
	for _, method := range methods {
		member(m, method, normOperation)
	}
	eachItem(m, "servers", normServer)
	eachItem(m, "parameters", normParameter)
}

func normOperation(m map[string]any) {
	dropDocs(m)
	eachItem(m, "parameters", normParameter)
	member(m, "requestBody", normRequestBody)
	eachMember(m, "responses", normResponse)
	eachMember(m, "callbacks", normCallback)
	eachItem(m, "servers", normServer)
}

// normCallback normalizes a Callback object, a map of expressions to path
// items.
func normCallback(m map[string]any) {
	for k := range m {
		member(m, k, normPathItem)
	}
}

func normParameter(m map[string]any) {
	dropDocs(m)
	member(m, "schema", normSchema)
	eachMember(m, "content", normMedia)
}

func normRequestBody(m map[string]any) {
	dropDocs(m)
	eachMember(m, "content", normMedia)
}

func normResponse(m map[string]any) {
	dropDocs(m)
	eachMember(m, "headers", normHeader)
	eachMember(m, "content", normMedia)
	eachMember(m, "x-error-codes", func(code map[string]any) { delete(code, "remediation") })
}

func normHeader(m map[string]any) {
	dropDocs(m)
	member(m, "schema", normSchema)
	eachMember(m, "content", normMedia)
}

func normMedia(m map[string]any) {
	member(m, "schema", normSchema)
	eachMember(m, "encoding", normEncoding)
}

func normEncoding(m map[string]any) {
	eachMember(m, "headers", normHeader)
}

// The JSON Schema 2020-12 keywords whose values are subschemas, by shape;
// definitions is the draft 7 name of $defs.
var (
	subschemaKeywords     = []string{kwItems, "additionalProperties", "propertyNames", "contains", "not", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties", "contentSchema"}
	subschemaListKeywords = []string{"allOf", "anyOf", "oneOf", "prefixItems", kwItems}
	subschemaMapKeywords  = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}
)

func normSchema(m map[string]any) {
	dropDocs(m)
	for _, k := range subschemaKeywords {
		member(m, k, normSchema)
	}
	for _, k := range subschemaListKeywords {
		eachItem(m, k, normSchema)
	}
	for _, k := range subschemaMapKeywords {
		eachMember(m, k, normSchema)
	}
}
