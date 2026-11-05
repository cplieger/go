package yamlenv

import (
	"reflect"
	"strings"

	"go.yaml.in/yaml/v4"
)

// provenance is what Load knows that a message cannot forge: the Go types its
// config type reaches and where the document's mapping keys sit. A nil
// *provenance (SanitizeDecodeError called directly) vouches for everything.
type provenance struct {
	types    map[string]bool
	keys     map[position]string
	keyLines map[int]bool
}

// position is a node's 1-based line and column.
type position struct{ line, column int }

// newProvenance indexes cfgType and doc. Expansion moves no node, but the
// key text is what the operator wrote only while doc is unexpanded.
func newProvenance(cfgType reflect.Type, doc *yaml.Node) *provenance {
	p := &provenance{types: map[string]bool{}, keys: map[position]string{}, keyLines: map[int]bool{}}
	p.addType(cfgType, map[reflect.Type]bool{})
	p.addKeys(doc)
	return p
}

// addType records t and every type a decode into t can construct into, under
// the names go-yaml's wrong-type message prints (reflect.Type.String).
func (p *provenance) addType(t reflect.Type, seen map[reflect.Type]bool) {
	if seen[t] {
		return
	}
	seen[t] = true
	p.types[t.String()] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		p.addType(t.Elem(), seen)
	case reflect.Map:
		p.addType(t.Key(), seen)
		p.addType(t.Elem(), seen)
	case reflect.Struct:
		for f := range t.Fields() {
			p.addType(f.Type, seen)
		}
	}
}

// addKeys records every mapping key under n by position, with the text
// go-yaml names it by: an alias key is named by its anchor's scalar.
func (p *provenance) addKeys(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			text := k.Value
			if k.Kind == yaml.AliasNode && k.Alias != nil {
				text = k.Alias.Value
			}
			p.keys[position{k.Line, k.Column}] = text
			p.keyLines[k.Line] = true
		}
	}
	for _, c := range n.Content {
		p.addKeys(c)
	}
}

// vouchesType reports whether dest, a wrong-type entry's destination, is a
// type the config type reaches.
func (p *provenance) vouchesType(dest string) bool {
	return p == nil || p.types[strings.TrimSuffix(dest, textUnmarshaler)]
}

// vouchesKey reports whether a mapping key named key sits at the entry's
// position, as it does for go-yaml's own unknown-field entry.
func (p *provenance) vouchesKey(at yaml.Mark, key string) bool {
	if p == nil {
		return true
	}
	text, ok := p.keys[position{at.Line, at.Column}]
	return ok && text == key
}

// vouchesDuplicate reports whether a key sits at the entry's position and
// another starts on its first-definition line.
func (p *provenance) vouchesDuplicate(at yaml.Mark, first int) bool {
	if p == nil {
		return true
	}
	_, ok := p.keys[position{at.Line, at.Column}]
	return ok && p.keyLines[first]
}
