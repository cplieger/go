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

// begin reads d's metadata, then charges the frame as charged does.
func begin(d measure.Declared, opts []Option, allowed optionKind, charge int, checks ...func() error) (measure.Declaration, options, error) {
	decl, err := declarationOf(d, "declaration")
	if err != nil {
		return decl, options{}, err
	}
	o, err := charged(opts, allowed, charge, checks...)
	return decl, o, err
}

// charged refuses a frame whose charge values, with its refs and notes,
// pass MaxValues, before any check reads the data; then it applies opts,
// refusing an option outside allowed, and runs checks in order.
func charged(opts []Option, allowed optionKind, charge int, checks ...func() error) (options, error) {
	var values count
	if err := values.add(charge, "frame"); err != nil {
		return options{}, err
	}
	o, err := applyOptions(opts, allowed, &values)
	if err != nil {
		return o, err
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return o, err
		}
	}
	return o, nil
}
