package chartdata

import (
	"reflect"

	"github.com/cplieger/go/measure/v2"
)

// declarationOf reads d's metadata, refusing a nil declaration, a typed nil
// one, whose Declaration method would panic, and one with no name.
func declarationOf(d measure.Declared, field string) (measure.Declaration, error) {
	if d == nil {
		return measure.Declaration{}, refuse(RuleDeclaration, field, "is nil")
	}
	if v := reflect.ValueOf(d); v.Kind() == reflect.Pointer && v.IsNil() {
		return measure.Declaration{}, refuse(RuleDeclaration, field, "is a nil pointer")
	}
	decl := d.Declaration()
	if decl.Name == "" {
		return measure.Declaration{}, refuse(RuleDeclaration, field, "has no name")
	}
	return decl, nil
}
