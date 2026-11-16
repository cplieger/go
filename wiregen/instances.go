package wiregen

import (
	"fmt"
	"go/token"
	"go/types"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// typeSpelling is a type an instantiation's name is derived from: a builtin,
// a declared type, or an instantiation of a generic one with its arguments.
type typeSpelling struct {
	pkg  string // "" for a builtin
	name string // the builtin's name, or the declaration's
	args []typeSpelling
}

// text spells s as importpath.Name[args], the Go spelling an error names a type by.
func (s *typeSpelling) text() string {
	if s.pkg == "" {
		return s.name
	}
	t := s.pkg + "." + s.name
	if len(s.args) > 0 {
		parts := make([]string, len(s.args))
		for i := range s.args {
			parts[i] = s.args[i].text()
		}
		t += "[" + strings.Join(parts, ",") + "]"
	}
	return t
}

// wireName is the bare name wiregen keys s by: its name, then each type
// argument's wire name, joined by '_' (docs/type-mapping.md "Generic types").
func (s *typeSpelling) wireName() string {
	parts := make([]string, 0, 1+len(s.args))
	parts = append(parts, s.name)
	for i := range s.args {
		parts = append(parts, s.args[i].wireName())
	}
	return strings.Join(parts, "_")
}

// builtinArg reports whether name is a predeclared boolean, integer, float or
// string type a type argument may be, and the name reflect and go/types spell
// it by: byte and rune alias uint8 and int32.
func builtinArg(name string) (string, bool) {
	tn, ok := types.Universe.Lookup(name).(*types.TypeName)
	if !ok {
		return "", false
	}
	b, ok := tn.Type().(*types.Basic)
	if !ok || b.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsString) == 0 {
		return "", false
	}
	return types.Typ[b.Kind()].Name(), true
}

// parseInstance reads wt as an instantiation, reporting false for a type
// with no type arguments. A type with no package is no instantiation:
// TypeOf names an unnamed type, brackets and all, with no package.
func parseInstance(wt Type) (sp typeSpelling, generic bool, err error) {
	base, rest, generic := strings.Cut(wt.Name, "[")
	if !generic || wt.PkgPath == "" {
		return typeSpelling{}, false, nil
	}
	full := wt.PkgPath + "." + wt.Name
	inner, closed := strings.CutSuffix(rest, "]")
	if !closed || !token.IsIdentifier(base) {
		return typeSpelling{}, true, fmt.Errorf("wiregen: %s names no instantiation of a generic type", full)
	}
	sp = typeSpelling{pkg: wt.PkgPath, name: base}
	for _, raw := range splitTypeArgs(inner) {
		arg, ok := parseTypeArg(raw)
		if !ok {
			return typeSpelling{}, true, fmt.Errorf("wiregen: type argument %s of %s is neither a builtin nor a registered type or enum; register a named type for it", strings.TrimSpace(raw), full)
		}
		sp.args = append(sp.args, arg)
	}
	return sp, true, nil
}

func splitTypeArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := range len(s) {
		switch s[i] {
		case '[', '(', '{':
			depth++
		case ']', ')', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// parseTypeArg reads one type argument as reflect spells it: a builtin, or
// an import path, '.', a name and, for an instantiation, its arguments in
// brackets. reflect escapes a '.' in the path's last element as %2e. Any
// other spelling (a pointer, a slice, a map, an unnamed type) is false.
func parseTypeArg(raw string) (typeSpelling, bool) {
	s := strings.TrimSpace(raw)
	if name, ok := builtinArg(s); ok {
		return typeSpelling{name: name}, true
	}
	base, rest, generic := strings.Cut(s, "[")
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || strings.ContainsAny(base, " *(){}]") {
		return typeSpelling{}, false
	}
	pkg, err := url.PathUnescape(base[:dot])
	if err != nil || !token.IsIdentifier(base[dot+1:]) {
		return typeSpelling{}, false
	}
	sp := typeSpelling{pkg: pkg, name: base[dot+1:]}
	if !generic {
		return sp, true
	}
	inner, closed := strings.CutSuffix(rest, "]")
	if !closed {
		return typeSpelling{}, false
	}
	for _, a := range splitTypeArgs(inner) {
		arg, ok := parseTypeArg(a)
		if !ok {
			return typeSpelling{}, false
		}
		sp.args = append(sp.args, arg)
	}
	return sp, true
}

// spellingOf names t as parseTypeArg would read reflect's spelling of it,
// or reports false for a type no name reaches.
func spellingOf(t types.Type) (typeSpelling, bool) {
	switch u := types.Unalias(t).(type) {
	case *types.Basic:
		name, ok := builtinArg(u.Name())
		return typeSpelling{name: name}, ok
	case *types.Named:
		obj := u.Obj()
		if obj.Pkg() == nil {
			return typeSpelling{}, false
		}
		sp := typeSpelling{pkg: obj.Pkg().Path(), name: obj.Name()}
		for a := range u.TypeArgs().Types() {
			arg, ok := spellingOf(a)
			if !ok {
				return typeSpelling{}, false
			}
			sp.args = append(sp.args, arg)
		}
		return sp, true
	}
	return typeSpelling{}, false
}

// eachType calls fn on every Type the registry names outside Enums, union
// payloads last in case order, so fn can rewrite it.
func (g *generator) eachType(fn func(*Type)) {
	slots := make([]*Type, 0, 1+len(g.Types)+4*len(g.Endpoints)+len(g.Events)+len(g.Unions))
	slots = append(slots, &g.ErrorEnvelope)
	for i := range g.Types {
		slots = append(slots, &g.Types[i])
	}
	for i := range g.Endpoints {
		e := &g.Endpoints[i]
		slots = append(slots, &e.Params, &e.Query, &e.Request, &e.Response)
	}
	for i := range g.Events {
		slots = append(slots, &g.Events[i].Data)
	}
	for i := range g.Unions {
		slots = append(slots, &g.Unions[i].Type)
	}
	for _, s := range slots {
		fn(s)
	}
	for i := range g.Unions {
		payloads := g.Unions[i].PayloadTypes
		for _, c := range slices.Sorted(maps.Keys(payloads)) {
			wt := payloads[c]
			fn(&wt)
			payloads[c] = wt
		}
	}
}

// nameInstances renames every instantiation the registry names to its wire
// name, which every table then keys it by, and refuses two types taking one
// name: two instantiations, or an instantiation and a registered type or
// enum declared under that name in any package.
func (g *generator) nameInstances() error {
	g.instances = map[string]typeSpelling{}
	declared := map[string]Type{}
	for _, ref := range g.Enums {
		declared[ref.Type.Name] = ref.Type
	}
	g.eachType(func(wt *Type) {
		if !strings.Contains(wt.Name, "[") {
			declared[wt.Name] = *wt
		}
	})
	var err error
	g.eachType(func(wt *Type) {
		if err == nil {
			err = g.nameInstance(wt, declared)
		}
	})
	return err
}

// nameInstance renames instantiation wt to its wire name, refusing a name a
// declared type or an earlier instantiation already holds.
func (g *generator) nameInstance(wt *Type, declared map[string]Type) error {
	sp, generic, err := parseInstance(*wt)
	if err != nil || !generic {
		return err
	}
	name := sp.wireName()
	other := ""
	if prev, dup := g.instances[name]; dup && prev.text() != sp.text() {
		other = prev.text()
	} else if d, taken := declared[name]; taken {
		other = d.PkgPath + "." + d.Name
	}
	if other != "" {
		return fmt.Errorf("wiregen: %s and %s both take the name %s, which wiregen keys types by; rename the declared type, or register a named type for the instantiation", other, sp.text(), name)
	}
	g.instances[name] = sp
	wt.Name = name
	return nil
}

// checkTypeArguments refuses an instantiation whose type argument is
// neither a builtin nor a registered type or enum, which would leave the
// argument's own TS type ungenerated.
func (g *generator) checkTypeArguments() error {
	for _, name := range slices.Sorted(maps.Keys(g.instances)) {
		sp := g.instances[name]
		for i := range sp.args {
			arg := &sp.args[i]
			def, enum := g.enums[arg.name]
			if arg.pkg == "" || g.isRegisteredType(arg) || enum && def.pkgPath == arg.pkg {
				continue
			}
			return fmt.Errorf("wiregen: type argument %s of %s is neither a builtin nor a registered type or enum; register it", arg.text(), sp.text())
		}
	}
	return nil
}

// isRegisteredType reports whether s is the registered type its wire name
// keys: a declared type is never an instantiation sharing its name.
func (g *generator) isRegisteredType(s *typeSpelling) bool {
	name := s.wireName()
	inst, generic := g.instances[name]
	if generic != (len(s.args) > 0) || generic && inst.text() != s.text() {
		return false
	}
	pkg, ok := g.typeNames[name]
	return ok && pkg == s.pkg
}

// namedRef is the name a value of named type t is keyed by, its Go spelling
// for an error, and whether it is a registered type.
func (g *generator) namedRef(t *types.Named) (name, full string, registered bool) {
	sp, ok := spellingOf(t)
	if !ok {
		return "", types.TypeString(t, nil), false
	}
	return sp.wireName(), sp.text(), g.isRegisteredType(&sp)
}

// goName spells registered type wt in Go, for an error: an instantiation
// with its type arguments, else importpath.Name.
func (g *generator) goName(wt Type) string {
	if sp, ok := g.instances[wt.Name]; ok {
		return sp.text()
	}
	return wt.PkgPath + "." + wt.Name
}

func (g *generator) typeLabel(name string) string {
	return "type " + g.goName(Type{PkgPath: g.typeNames[name], Name: name})
}

func (g *generator) enumLabel(name string) string {
	return "enum " + g.enums[name].pkgPath + "." + name
}

// declaration is the Go type declaration registered type wt reads its
// fields from: an instantiation's origin, else wt.
func (g *generator) declaration(wt Type) Type {
	if sp, ok := g.instances[wt.Name]; ok {
		return Type{PkgPath: sp.pkg, Name: sp.name}
	}
	return wt
}

// instantiate builds the instantiation sp spells from the loaded packages,
// checking each type argument against its constraint.
func (e *astEngine) instantiate(sp *typeSpelling) (types.Type, error) {
	if sp.pkg == "" {
		return types.Universe.Lookup(sp.name).Type(), nil
	}
	pkg := e.pkgs[sp.pkg]
	tn, ok := pkg.Types.Scope().Lookup(sp.name).(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("%s.%s is not a type in its package", sp.pkg, sp.name)
	}
	if len(sp.args) == 0 {
		return tn.Type(), nil
	}
	if tn.IsAlias() {
		return nil, fmt.Errorf("%s.%s is an alias; register the type it stands for, spelled as TypeRef spells it", sp.pkg, sp.name)
	}
	var params *types.TypeParamList // a nil list has length 0
	if origin, named := tn.Type().(*types.Named); named {
		params = origin.TypeParams()
	}
	if n := params.Len(); n != len(sp.args) {
		return nil, fmt.Errorf("%s.%s has %d type parameters, not %d", sp.pkg, sp.name, n, len(sp.args))
	}
	args := make([]types.Type, len(sp.args))
	for i := range sp.args {
		arg, err := e.instantiate(&sp.args[i])
		if err != nil {
			return nil, err
		}
		args[i] = arg
	}
	t, err := types.Instantiate(nil, tn.Type(), args, true)
	if err != nil {
		return nil, fmt.Errorf("instantiate %s: %w", sp.text(), err)
	}
	return t, nil
}
