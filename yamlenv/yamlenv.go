// Package yamlenv expands allowlisted ${VAR} environment-variable references
// inside the string values of a parsed YAML document, so secrets can stay in
// the environment while the file holds structure.
//
// Expansion runs after parsing, on string scalars only, so an environment
// value cannot change the document's structure. Only the braced form is
// recognized; an unbraced $VAR, a rejected name and an unset variable stay
// literal, and expansion is a single pass. ExpandWith reads variables through
// a caller-supplied lookup; Expand and Load read the process environment
// unless WithLookupEnv says otherwise. SanitizeDecodeError strips value
// excerpts (possibly expanded secrets) from yaml.v3 decode errors.
// CheckUnknownKeys and CheckSingleDocument are the strict-load checks.
//
// A reference expands only into a string-typed (!!str) node, and the node
// keeps that tag after expansion: `port: ${APP_PORT}` decoded into an int
// field fails even when the value is "8080". A number, bool or duration that
// comes from the environment needs a string field and the caller's own parse.
//
// Load composes the checks, parse, expansion, decode and sanitization in the
// safe order and is the default path. The package is its own Go module so
// that importing envx never links the YAML dependency.
package yamlenv

import (
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// refRe matches a ${VAR} reference, the only supported expansion form.
var refRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand is ExpandWith reading the process environment through os.LookupEnv.
func Expand(root *yaml.Node, allow func(name string) bool) (unresolved []string) {
	return ExpandWith(root, allow, os.LookupEnv)
}

// ExpandWith expands each ${VAR} in the string scalar values under root, in
// place, with lookup's value when allow(name) is true and lookup finds it; a
// found empty value substitutes. Every other reference stays literal, and the
// process environment is read only if lookup reads it. It returns the allowed
// names lookup did not find, once each in document order. Only references
// written in the document count: a ${VAR} inside a substituted value is
// neither expanded nor reported. A nil root, allow or lookup expands nothing.
// Decode the result: it keeps each node's Style, so re-encoding can mis-render.
func ExpandWith(root *yaml.Node, allow func(name string) bool, lookup func(name string) (string, bool)) (unresolved []string) {
	if root == nil || allow == nil || lookup == nil {
		return nil
	}
	seen := map[string]bool{}
	walkStringValues(root, func(node *yaml.Node) {
		node.Value = refRe.ReplaceAllStringFunc(node.Value, func(m string) string {
			name := m[2 : len(m)-1]
			if !allow(name) {
				return m
			}
			if v, ok := lookup(name); ok {
				return v
			}
			if !seen[name] {
				seen[name] = true
				unresolved = append(unresolved, name)
			}
			return m
		})
	})
	return unresolved
}

// walkStringValues invokes fn on every !!str scalar VALUE in the document.
// Mapping keys and non-string scalars are deliberately skipped; alias nodes
// carry no content of their own, so an anchored value is visited exactly once
// at its anchor.
func walkStringValues(node *yaml.Node, fn func(*yaml.Node)) {
	if node.Kind == yaml.MappingNode {
		for i := 1; i < len(node.Content); i += 2 {
			walkStringValues(node.Content[i], fn)
		}
		return
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		fn(node)
	}
	for _, child := range node.Content {
		walkStringValues(child, fn)
	}
}
