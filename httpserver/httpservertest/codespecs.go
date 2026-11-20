// Package httpservertest holds the test helpers for code built on httpserver.
// It sits apart from httpserver so that package imports no testing.
package httpservertest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

const (
	httpserverPath = "github.com/cplieger/go/httpserver/v2"
	errorCodeType  = "ErrorCode"
)

// rootWriters are the httpserver functions that write the codes they are
// given.
var rootWriters = map[string]bool{"WriteError": true, "NewErrorResponse": true}

// CheckCodeSpecs fails t unless specs holds exactly the codes the package in
// dir writes, naming each missing from specs and each key nothing writes. It
// type-checks dir's non-test files as go build selects them, so the package
// must compile. A code is a string constant expression passed as an ErrorCode
// argument to WriteError, NewErrorResponse, a function the package declares or
// a func value, or set in an ErrorCode field of any struct literal, or
// listed in an ErrorCode slice or array a struct field holds; another
// package's functions do not write, and neither does a map key.
func CheckCodeSpecs(t testing.TB, dir string, specs map[httpserver.ErrorCode]httpserver.CodeSpec) {
	t.Helper()
	written, err := writtenCodes(t.Context(), dir)
	if err != nil {
		t.Fatalf("CheckCodeSpecs: %v", err)
	}
	for _, code := range slices.Sorted(maps.Keys(written)) {
		if _, ok := specs[code]; !ok {
			t.Errorf("%s writes code %q, which CodeSpecs does not hold", written[code], code)
		}
	}
	for _, code := range slices.Sorted(maps.Keys(specs)) {
		if _, ok := written[code]; !ok {
			t.Errorf("CodeSpecs holds %q, which no writer in %s answers", code, dir)
		}
	}
}

// codeScan holds one type-checked package and the codes found in it so far.
type codeScan struct {
	fset    *token.FileSet
	info    *types.Info
	pkg     *types.Package
	written map[httpserver.ErrorCode]string
}

// writtenCodes type-checks the package in dir and returns each code its
// non-test files write with the position of its first occurrence.
func writtenCodes(ctx context.Context, dir string) (map[httpserver.ErrorCode]string, error) {
	listed, err := listPackage(ctx, dir)
	if err != nil {
		return nil, err
	}
	s := &codeScan{
		fset:    token.NewFileSet(),
		info:    &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}},
		written: make(map[httpserver.ErrorCode]string),
	}
	files := make([]*ast.File, 0, len(listed.root.GoFiles))
	for _, name := range listed.root.GoFiles {
		f, perr := parser.ParseFile(s.fset, filepath.Join(listed.root.Dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil, perr
		}
		files = append(files, f)
	}
	conf := types.Config{
		Importer: importer.ForCompiler(s.fset, "gc", listed.lookup),
		Sizes:    types.SizesFor("gc", runtime.GOARCH),
	}
	if s.pkg, err = conf.Check(listed.root.ImportPath, s.fset, files, s.info); err != nil {
		return nil, fmt.Errorf("type-check %s: %w", dir, err)
	}
	for _, f := range files {
		ast.Inspect(f, s.visit)
	}
	return s.written, nil
}

// listedPackage is what go list reports of one package.
type listedPackage struct {
	Error      *struct{ Err string }
	ImportMap  map[string]string
	ImportPath string
	Dir        string
	Export     string
	GoFiles    []string
	CgoFiles   []string
	DepOnly    bool
}

// listing is the package in a directory and the export data of everything it
// imports.
type listing struct {
	exports map[string]string // import path: export data file
	root    listedPackage
}

// listPackage runs go list in dir for its package and the export data of its
// dependencies, which go list builds through the build cache.
func listPackage(ctx context.Context, dir string) (*listing, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-export", "-e", "-json=ImportPath,Dir,GoFiles,CgoFiles,Export,ImportMap,Error,DepOnly", ".")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list in %s: %w: %s", dir, err, bytes.TrimSpace(stderr.Bytes()))
	}
	l := &listing{exports: make(map[string]string)}
	found := false
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list in %s: %w", dir, err)
		}
		if p.DepOnly {
			l.exports[p.ImportPath] = p.Export
			continue
		}
		l.root, found = p, true
	}
	switch {
	case !found:
		return nil, fmt.Errorf("go list names no package in %s", dir)
	case l.root.Error != nil:
		return nil, fmt.Errorf("go list in %s: %s", dir, l.root.Error.Err)
	case len(l.root.CgoFiles) > 0:
		return nil, fmt.Errorf("%s uses cgo, which CheckCodeSpecs cannot type-check", dir)
	case len(l.root.GoFiles) == 0:
		return nil, fmt.Errorf("no non-test Go files in %s", dir)
	}
	return l, nil
}

// lookup opens the export data of the package path names, path as the
// package's source writes it.
func (l *listing) lookup(path string) (io.ReadCloser, error) {
	if mapped, ok := l.root.ImportMap[path]; ok {
		path = mapped
	}
	file := l.exports[path]
	if file == "" {
		return nil, fmt.Errorf("go list built no export data for %s", path)
	}
	return os.Open(file)
}

// visit records the codes a call or a composite literal writes.
func (s *codeScan) visit(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.CallExpr:
		s.collectCall(n)
	case *ast.CompositeLit:
		s.collectFields(n)
	}
	return true
}

// collectCall records each constant passed as an ErrorCode argument of call
// when its callee writes codes.
func (s *codeScan) collectCall(call *ast.CallExpr) {
	if tv := s.info.Types[call.Fun]; tv.IsType() || tv.IsBuiltin() || !s.writer(call.Fun) {
		return
	}
	sig, ok := s.info.TypeOf(call.Fun).Underlying().(*types.Signature)
	if !ok {
		return
	}
	params := sig.Params()
	for i, arg := range call.Args {
		var param types.Type
		switch last := params.Len() - 1; {
		case sig.Variadic() && i >= last && !call.Ellipsis.IsValid():
			slice, ok := params.At(last).Type().(*types.Slice)
			if !ok {
				continue
			}
			param = slice.Elem()
		case i <= last:
			param = params.At(i).Type()
		default:
			continue
		}
		if isErrorCode(param) {
			s.record(arg)
		}
	}
}

// writer reports whether a call through fun writes the codes it is given: a
// root writer, a function or method the package declares, or a func value.
func (s *codeScan) writer(fun ast.Expr) bool {
	fun = ast.Unparen(fun)
	switch e := fun.(type) {
	case *ast.IndexExpr:
		fun = e.X
	case *ast.IndexListExpr:
		fun = e.X
	}
	var obj types.Object
	switch e := ast.Unparen(fun).(type) {
	case *ast.Ident:
		obj = s.info.Uses[e]
	case *ast.SelectorExpr:
		if sel, ok := s.info.Selections[e]; ok {
			obj = sel.Obj()
		} else {
			obj = s.info.Uses[e.Sel]
		}
	}
	f, ok := obj.(*types.Func)
	if !ok {
		return true
	}
	if f.Pkg() == s.pkg {
		return true
	}
	sig, _ := f.Type().(*types.Signature)
	return f.Pkg() != nil && f.Pkg().Path() == httpserverPath && rootWriters[f.Name()] && sig != nil && sig.Recv() == nil
}

// collectFields records the constants lit gives its ErrorCode fields, and
// the elements of an ErrorCode slice or array literal given to a field, in a
// struct of any package: a struct carries a code to the writer that answers
// it. A map literal's keys and a slice outside a struct are never codes
// written, since an app's spec map or code list would then hold itself.
func (s *codeScan) collectFields(lit *ast.CompositeLit) {
	st, ok := types.Unalias(s.info.TypeOf(lit)).Underlying().(*types.Struct)
	if !ok {
		return
	}
	for i, el := range lit.Elts {
		field, value := (*types.Var)(nil), el
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			field, value = fieldNamed(st, key.Name), kv.Value
		} else if i < st.NumFields() {
			field = st.Field(i)
		}
		switch {
		case field == nil:
		case isErrorCode(field.Type()):
			s.record(value)
		case isErrorCode(elemOf(field.Type())):
			s.recordElements(value)
		}
	}
}

// recordElements records each element of e when e is a slice or array
// composite literal.
func (s *codeScan) recordElements(e ast.Expr) {
	lit, ok := ast.Unparen(e).(*ast.CompositeLit)
	if !ok {
		return
	}
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			el = kv.Value
		}
		s.record(el)
	}
}

// elemOf is the element type of a slice or array type, else nil.
func elemOf(t types.Type) types.Type {
	switch u := types.Unalias(t).Underlying().(type) {
	case *types.Slice:
		return u.Elem()
	case *types.Array:
		return u.Elem()
	}
	return nil
}

func fieldNamed(st *types.Struct, name string) *types.Var {
	for f := range st.Fields() {
		if f.Name() == name {
			return f
		}
	}
	return nil
}

// isErrorCode reports whether t is httpserver's ErrorCode.
func isErrorCode(t types.Type) bool {
	if t == nil {
		return false
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && isHTTPServerType(named.Obj(), errorCodeType)
}

func isHTTPServerType(obj *types.TypeName, name string) bool {
	return obj.Name() == name && obj.Pkg() != nil && obj.Pkg().Path() == httpserverPath
}

// record adds the code e names when e is a constant of string value.
func (s *codeScan) record(e ast.Expr) {
	v := s.info.Types[e].Value
	if v == nil || v.Kind() != constant.String {
		return
	}
	c := httpserver.ErrorCode(constant.StringVal(v))
	if _, seen := s.written[c]; !seen {
		s.written[c] = s.fset.Position(e.Pos()).String()
	}
}
